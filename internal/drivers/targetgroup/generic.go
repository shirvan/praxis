package targetgroup

import (
	"fmt"
	"maps"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) TargetGroupAPI
}

func NewGenericTargetGroupDriver(auth authservice.AuthClient) *kernel.Driver[TargetGroupSpec, TargetGroupOutputs, ObservedState] {
	return newGenericTargetGroupDriverWithFactory(auth, nil)
}
func newGenericTargetGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) TargetGroupAPI) *kernel.Driver[TargetGroupSpec, TargetGroupOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) TargetGroupAPI { return NewTargetGroupAPI(awsclient.NewELBv2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[TargetGroupSpec, TargetGroupOutputs, ObservedState]{
		ServiceName: ServiceName, Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true}, Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec TargetGroupSpec) (TargetGroupSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return TargetGroupSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, obs ObservedState) TargetGroupSpec {
			spec := specFromObserved(obs)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(obs ObservedState, _ TargetGroupOutputs) TargetGroupOutputs { return outputsFromObserved(obs) }, FieldDiffs: ComputeFieldDiffs,
		HasDrift: HasDrift,
	})
}
func (o *genericOperations) Observe(ctx restate.ObjectContext, d TargetGroupSpec, out TargetGroupOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, d.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	id := out.TargetGroupArn
	byName := false
	if id == "" {
		id = d.Name
		byName = id != ""
	}
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	obs, err := observeTG(ctx, api, id)
	if err != nil || !obs.Exists {
		return obs, err
	}
	owner := obs.Value.Tags["praxis:managed-key"]
	if owner != "" && owner != d.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("target group %s is owned by Praxis object %q, not %q", obs.Value.TargetGroupArn, owner, d.ManagedKey), 409)
	}
	if byName && owner != d.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt target group %s without exact Praxis ownership tag %q", d.Name, d.ManagedKey), 409)
	}
	return obs, nil
}
func (o *genericOperations) Create(ctx restate.ObjectContext, d TargetGroupSpec) (kernel.CreateResult[TargetGroupOutputs], error) {
	api, _, err := o.apiForAccount(ctx, d.Account)
	if err != nil {
		return kernel.CreateResult[TargetGroupOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	out, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (TargetGroupOutputs, error) {
		obs, descErr := api.DescribeTargetGroup(rc, d.Name)
		if descErr == nil {
			if obs.Tags["praxis:managed-key"] != d.ManagedKey {
				return TargetGroupOutputs{}, restate.TerminalError(fmt.Errorf("refusing to adopt target group %s without exact Praxis ownership tag %q", d.Name, d.ManagedKey), 409)
			}
			return outputsFromObserved(obs), nil
		}
		if !IsNotFound(descErr) {
			return TargetGroupOutputs{}, descErr
		}
		return api.CreateTargetGroup(rc, d)
	}, classifyTGMutation)
	return kernel.CreateResult[TargetGroupOutputs]{SeedOutputs: out}, err
}
func (o *genericOperations) Converge(ctx restate.ObjectContext, d TargetGroupSpec, obs ObservedState, currentOutputs TargetGroupOutputs) (TargetGroupOutputs, error) {
	if d.Name != obs.Name || hasImmutableChange(d, obs) {
		return currentOutputs, restate.TerminalError(fmt.Errorf("target group %q has immutable changes; delete and reprovision it", obs.Name), 409)
	}
	if owner := obs.Tags["praxis:managed-key"]; owner != "" && owner != d.ManagedKey {
		return currentOutputs, restate.TerminalError(fmt.Errorf("target group %s is owned by Praxis object %q", obs.TargetGroupArn, owner), 409)
	}
	api, _, err := o.apiForAccount(ctx, d.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if d.HealthCheck != obs.HealthCheck {
		if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyTargetGroup(rc, obs.TargetGroupArn, d)
		}, classifyTGMutation); err != nil {
			return currentOutputs, err
		}
	}
	if d.DeregistrationDelay != obs.DeregistrationDelay || !stickinessEqual(d.Stickiness, obs.Stickiness) {
		if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateAttributes(rc, obs.TargetGroupArn, d)
		}, classifyTGMutation); err != nil {
			return currentOutputs, err
		}
	}
	if !targetsEqual(d.Targets, obs.Targets) {
		if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTargets(rc, obs.TargetGroupArn, d.Targets, obs.Targets)
		}, classifyTGMutation); err != nil {
			return currentOutputs, err
		}
	}
	if !drivers.TagsMatch(d.Tags, obs.Tags) || obs.Tags["praxis:managed-key"] != d.ManagedKey {
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, obs.TargetGroupArn, targetGroupManagedTags(d.Tags, d.ManagedKey))
		}, classifyTGMutation)
	}
	return currentOutputs, err
}
func (o *genericOperations) Delete(ctx restate.ObjectContext, d TargetGroupSpec, out TargetGroupOutputs) error {
	if out.TargetGroupArn == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, d.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	obs, err := observeTG(ctx, api, out.TargetGroupArn)
	if err != nil || !obs.Exists {
		return err
	}
	if owner := obs.Value.Tags["praxis:managed-key"]; owner != "" && owner != d.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete target group owned by %q", owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if len(obs.Value.Targets) > 0 {
			if e := api.UpdateTargets(rc, out.TargetGroupArn, nil, obs.Value.Targets); e != nil {
				return restate.Void{}, e
			}
		}
		e := api.DeleteTargetGroup(rc, out.TargetGroupArn)
		if IsNotFound(e) {
			e = nil
		}
		return restate.Void{}, e
	}, classifyTGMutation)
	return err
}
func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeTG(ctx, api, ref.ResourceID)
}
func observeTG(ctx restate.ObjectContext, api TargetGroupAPI, id string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		obs, err := api.DescribeTargetGroup(rc, id)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: obs}, nil
	}, classifyTGObserve)
}
func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (TargetGroupAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("TargetGroup driver is not configured with an auth client")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve target group account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}
func classifyTGObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidConfiguration(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}
func classifyTGMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsDuplicate(err) || IsResourceInUse(err) {
		return restate.TerminalError(err, 409)
	}
	if IsInvalidConfiguration(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsTooMany(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}
func targetGroupManagedTags(tags map[string]string, key string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, drivers.FilterPraxisTags(tags))
	if key != "" {
		out["praxis:managed-key"] = key
	}
	return out
}

func applyDefaults(spec TargetGroupSpec) TargetGroupSpec {
	if spec.TargetType == "" {
		spec.TargetType = "instance"
	}
	if spec.ProtocolVersion == "" && (strings.EqualFold(spec.Protocol, "HTTP") || strings.EqualFold(spec.Protocol, "HTTPS")) {
		spec.ProtocolVersion = "HTTP1"
	}
	spec.HealthCheck = healthCheckWithDefaults(spec.HealthCheck)
	if spec.DeregistrationDelay == 0 {
		spec.DeregistrationDelay = 300
	}
	if spec.Stickiness != nil {
		if spec.Stickiness.Type == "" {
			spec.Stickiness.Type = defaultStickinessType(spec.Protocol)
		}
		if spec.Stickiness.Duration == 0 {
			spec.Stickiness.Duration = 86400
		}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Targets == nil {
		spec.Targets = []Target{}
	}
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Region = strings.TrimSpace(spec.Region)
	spec.VpcId = strings.TrimSpace(spec.VpcId)
	return spec
}
func validateSpec(spec TargetGroupSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if spec.Protocol == "" {
		return fmt.Errorf("protocol is required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if spec.TargetType != "lambda" && spec.VpcId == "" {
		return fmt.Errorf("vpcId is required for non-lambda target groups")
	}
	for _, t := range spec.Targets {
		if strings.TrimSpace(t.ID) == "" {
			return fmt.Errorf("target id is required")
		}
	}
	return nil
}
func hasImmutableChange(d TargetGroupSpec, o ObservedState) bool {
	return d.Protocol != o.Protocol || d.Port != o.Port || d.VpcId != o.VpcId || d.TargetType != o.TargetType || d.ProtocolVersion != o.ProtocolVersion
}
func specFromObserved(o ObservedState) TargetGroupSpec {
	return applyDefaults(TargetGroupSpec{Name: o.Name, Protocol: o.Protocol, Port: o.Port, VpcId: o.VpcId, TargetType: o.TargetType, ProtocolVersion: o.ProtocolVersion, HealthCheck: o.HealthCheck, DeregistrationDelay: o.DeregistrationDelay, Stickiness: o.Stickiness, Targets: o.Targets, Tags: drivers.FilterPraxisTags(o.Tags)})
}
func outputsFromObserved(o ObservedState) TargetGroupOutputs {
	return TargetGroupOutputs{TargetGroupArn: o.TargetGroupArn, TargetGroupName: o.Name}
}
