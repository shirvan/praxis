package nlb

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

const managedKeyTag = "praxis:managed-key"

const (
	nlbReadyPollInterval = 10 * time.Second
	nlbReadyMaxAttempts  = 60
)

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) NLBAPI
}

func NewGenericNLBDriver(auth authservice.AuthClient) *kernel.Driver[NLBSpec, NLBOutputs, ObservedState] {
	return newGenericNLBDriverWithFactory(auth, nil)
}

func newGenericNLBDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) NLBAPI) *kernel.Driver[NLBSpec, NLBOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) NLBAPI { return NewNLBAPI(awsclient.NewELBv2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[NLBSpec, NLBOutputs, ObservedState]{
		ServiceName:  ServiceName,
		Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true},
		Operations:   ops,
		Prepare: func(ctx restate.ObjectContext, spec NLBSpec) (NLBSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return NLBSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return NLBSpec{}, restate.TerminalError(fmt.Errorf("region %q does not match account region %q", spec.Region, region), 400)
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) NLBSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ NLBOutputs) NLBOutputs { return outputsFromObserved(observed) },
		FieldDiffs:          ComputeFieldDiffs,
		HasDrift:            HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired NLBSpec, outputs NLBOutputs) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	id := strings.TrimSpace(outputs.LoadBalancerArn)
	byName := false
	if id == "" {
		id = desired.Name
		byName = id != ""
	}
	observation, err := observeNLB(ctx, api, id)
	if err != nil || !observation.Exists {
		return observation, err
	}
	observation.Value.Region = region
	owner := observation.Value.Tags[managedKeyTag]
	if owner != "" && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("NLB %s is owned by Praxis object %q, not %q", observation.Value.LoadBalancerArn, owner, desired.ManagedKey), 409)
	}
	if byName && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt NLB %s without exact Praxis ownership tag %q", desired.Name, desired.ManagedKey), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired NLBSpec) (kernel.CreateResult[NLBOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[NLBOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (NLBOutputs, error) {
		observed, describeErr := api.DescribeNLB(rc, desired.Name)
		if describeErr == nil {
			if observed.Tags[managedKeyTag] != desired.ManagedKey {
				return NLBOutputs{}, restate.TerminalError(fmt.Errorf("refusing to adopt NLB %s without exact Praxis ownership tag %q", desired.Name, desired.ManagedKey), 409)
			}
			return outputsFromObserved(observed), nil
		}
		if !IsNotFound(describeErr) {
			return NLBOutputs{}, describeErr
		}
		arn, dns, zone, vpc, createErr := api.CreateNLB(rc, desired)
		return NLBOutputs{LoadBalancerArn: arn, DnsName: dns, HostedZoneId: zone, CanonicalHostedZoneId: zone, VpcId: vpc}, createErr
	}, classifyNLBMutation)
	if err != nil {
		return kernel.CreateResult[NLBOutputs]{}, err
	}
	_, err = waitForActive(ctx, api, outputs.LoadBalancerArn)
	return kernel.CreateResult[NLBOutputs]{SeedOutputs: outputs}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired NLBSpec, observed ObservedState) error {
	if hasImmutableChange(desired, observed) {
		return restate.TerminalError(fmt.Errorf("NLB %q has immutable changes (name or scheme); delete and reprovision it", observed.Name), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	arn := observed.LoadBalancerArn
	if desired.IpAddressType != observed.IpAddressType {
		if err = runNLBMutation(ctx, func(rc restate.RunContext) error { return api.SetIpAddressType(rc, arn, desired.IpAddressType) }); err != nil {
			return fmt.Errorf("set IP address type: %w", err)
		}
	}
	if !sortedStringsEqual(resolveSubnets(desired), observed.Subnets) {
		if err = runNLBMutation(ctx, func(rc restate.RunContext) error { return api.SetSubnets(rc, arn, resolveSubnetMappings(desired)) }); err != nil {
			return fmt.Errorf("set subnets: %w", err)
		}
	}
	attrs := buildAttributeMap(desired)
	observedAttrs := map[string]string{"deletion_protection.enabled": fmt.Sprintf("%t", observed.DeletionProtection), "load_balancing.cross_zone.enabled": fmt.Sprintf("%t", observed.CrossZoneLoadBalancing)}
	if !maps.Equal(attrs, observedAttrs) {
		if err = runNLBMutation(ctx, func(rc restate.RunContext) error { return api.ModifyAttributes(rc, arn, attrs) }); err != nil {
			return fmt.Errorf("modify attributes: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) || observed.Tags[managedKeyTag] != desired.ManagedKey {
		if err = runNLBMutation(ctx, func(rc restate.RunContext) error {
			return api.UpdateTags(rc, arn, nlbManagedTags(desired.Tags, desired.ManagedKey))
		}); err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired NLBSpec, outputs NLBOutputs) error {
	if outputs.LoadBalancerArn == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeNLB(ctx, api, outputs.LoadBalancerArn)
	if err != nil || !observation.Exists {
		return err
	}
	if owner := observation.Value.Tags[managedKeyTag]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete NLB owned by Praxis object %q", owner), 409)
	}
	if observation.Value.DeletionProtection {
		if mutationErr := runNLBMutation(ctx, func(rc restate.RunContext) error {
			return api.ModifyAttributes(rc, outputs.LoadBalancerArn, map[string]string{"deletion_protection.enabled": "false"})
		}); mutationErr != nil {
			return mutationErr
		}
	}
	return runNLBMutation(ctx, func(rc restate.RunContext) error {
		err := api.DeleteNLB(rc, outputs.LoadBalancerArn)
		if IsNotFound(err) {
			return nil
		}
		return err
	})
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observation, err := observeNLB(ctx, api, strings.TrimSpace(ref.ResourceID))
	observation.Value.Region = region
	return observation, err
}

func observeNLB(ctx restate.ObjectContext, api NLBAPI, id string) (kernel.Observation[ObservedState], error) {
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeNLB(rc, id)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyNLBObserve)
}

func waitForActive(ctx restate.ObjectContext, api NLBAPI, arn string) (ObservedState, error) {
	for range nlbReadyMaxAttempts {
		observed, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (ObservedState, error) { return api.DescribeNLB(rc, arn) }, classifyNLBObserve)
		if err != nil {
			return ObservedState{}, err
		}
		switch observed.State {
		case "active", "":
			return observed, nil
		case "failed":
			return ObservedState{}, restate.TerminalError(fmt.Errorf("NLB entered failed state"), 500)
		}
		if err := restate.Sleep(ctx, nlbReadyPollInterval); err != nil {
			return ObservedState{}, err
		}
	}
	return ObservedState{}, restate.TerminalError(fmt.Errorf("NLB %s not active after %s", arn, time.Duration(nlbReadyMaxAttempts)*nlbReadyPollInterval), 500)
}

func runNLBMutation(ctx restate.ObjectContext, operation func(restate.RunContext) error) error {
	_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) { return restate.Void{}, operation(rc) }, classifyNLBMutation)
	return err
}
func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (NLBAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("NLB driver is not configured with an auth client")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve NLB account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}
func classifyNLBObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}
func classifyNLBMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsDuplicate(err) || IsResourceInUse(err) {
		return restate.TerminalError(err, 409)
	}
	if awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsTooMany(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}
func nlbManagedTags(tags map[string]string, key string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, drivers.FilterPraxisTags(tags))
	if key != "" {
		out[managedKeyTag] = key
	}
	return out
}
func applyDefaults(spec NLBSpec) NLBSpec {
	if spec.Scheme == "" {
		spec.Scheme = "internet-facing"
	}
	if spec.IpAddressType == "" {
		spec.IpAddressType = "ipv4"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Region = strings.TrimSpace(spec.Region)
	return spec
}
func validateSpec(spec NLBSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(spec.Subnets) == 0 && len(spec.SubnetMappings) == 0 {
		return fmt.Errorf("at least 1 subnet is required")
	}
	return nil
}
func hasImmutableChange(desired NLBSpec, observed ObservedState) bool {
	return desired.Name != observed.Name || desired.Scheme != observed.Scheme
}
func specFromObserved(observed ObservedState) NLBSpec {
	return applyDefaults(NLBSpec{Name: observed.Name, Scheme: observed.Scheme, IpAddressType: observed.IpAddressType, Subnets: observed.Subnets, CrossZoneLoadBalancing: observed.CrossZoneLoadBalancing, DeletionProtection: observed.DeletionProtection, Tags: drivers.FilterPraxisTags(observed.Tags)})
}
func outputsFromObserved(observed ObservedState) NLBOutputs {
	return NLBOutputs{LoadBalancerArn: observed.LoadBalancerArn, DnsName: observed.DnsName, HostedZoneId: observed.HostedZoneId, VpcId: observed.VpcId, CanonicalHostedZoneId: observed.HostedZoneId}
}
func mapsEqual(a, b map[string]string) bool { return maps.Equal(a, b) }
