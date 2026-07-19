package ekscluster

import (
	"fmt"
	"slices"
	"sort"
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

const managedKeyTag = "praxis:managed-key"

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) EKSClusterAPI
}

func NewGenericEKSClusterDriver(auth authservice.AuthClient) *kernel.Driver[EKSClusterSpec, EKSClusterOutputs, ObservedState] {
	return newGenericEKSClusterDriverWithFactory(auth, nil)
}

func newGenericEKSClusterDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) EKSClusterAPI) *kernel.Driver[EKSClusterSpec, EKSClusterOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) EKSClusterAPI { return NewEKSClusterAPI(awsclient.NewEKSClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[EKSClusterSpec, EKSClusterOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			ManagedDriftCorrection: true, Readiness: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec EKSClusterSpec) (EKSClusterSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return EKSClusterSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return EKSClusterSpec{}, restate.TerminalError(fmt.Errorf("region %q does not match account region %q", spec.Region, region), 400)
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) EKSClusterSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ EKSClusterOutputs) EKSClusterOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
		CheckReadiness: func(observed ObservedState) kernel.ReadinessResult {
			switch strings.ToUpper(strings.TrimSpace(observed.Status)) {
			case "ACTIVE":
				return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
			case "FAILED":
				return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: fmt.Sprintf("EKS cluster %s entered FAILED state", observed.Name)}
			case "DELETING":
				return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: fmt.Sprintf("EKS cluster %s is being deleted", observed.Name)}
			default:
				return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: fmt.Sprintf("EKS cluster status is %s", observed.Status)}
			}
		},
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired EKSClusterSpec, outputs EKSClusterOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	name := strings.TrimSpace(outputs.Name)
	recovering := name == ""
	if name == "" {
		name = strings.TrimSpace(desired.Name)
	}
	observation, err := observeEKSCluster(ctx, api, name)
	if err != nil || !observation.Exists {
		return observation, err
	}
	owner := observation.Value.Tags[managedKeyTag]
	if owner != "" && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("EKS cluster %q is owned by Praxis object %q, not %q", name, owner, desired.ManagedKey), 409)
	}
	if recovering && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt EKS cluster %q without exact Praxis ownership tag %q", name, desired.ManagedKey), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired EKSClusterSpec) (kernel.CreateResult[EKSClusterOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[EKSClusterOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	observed, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (ObservedState, error) {
		existing, found, describeErr := api.DescribeCluster(rc, desired.Name)
		if describeErr != nil {
			return ObservedState{}, describeErr
		}
		if found {
			if existing.Tags[managedKeyTag] != desired.ManagedKey {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("refusing to adopt EKS cluster %q without exact Praxis ownership tag %q", desired.Name, desired.ManagedKey), 409)
			}
			return existing, nil
		}
		return api.CreateCluster(rc, desired)
	}, classifyEKSCreate)
	return kernel.CreateResult[EKSClusterOutputs]{SeedOutputs: outputsFromObserved(observed)}, err
}

func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next EKSClusterSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.Name != next.Name:
		return restate.TerminalError(fmt.Errorf("name is immutable; delete and reprovision to change it"), 409)
	case previous.RoleArn != next.RoleArn:
		return restate.TerminalError(fmt.Errorf("roleArn is immutable; delete and reprovision to change it"), 409)
	case !stringSetEqual(previous.SubnetIds, next.SubnetIds):
		return restate.TerminalError(fmt.Errorf("subnetIds are immutable; delete and reprovision to change them"), 409)
	case !stringSetEqual(previous.SecurityGroupIds, next.SecurityGroupIds):
		return restate.TerminalError(fmt.Errorf("securityGroupIds are immutable; delete and reprovision to change them"), 409)
	default:
		return nil
	}
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired EKSClusterSpec, observed ObservedState) error {
	if err := validateEKSImmutableIdentity(desired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if versionDrift(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateClusterVersion(rc, desired.Name, desired.Version)
		}, classifyEKSMutation); err != nil {
			return fmt.Errorf("update cluster version: %w", err)
		}
	}
	if endpointAccessDrift(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateClusterConfig(rc, desired)
		}, classifyEKSMutation); err != nil {
			return fmt.Errorf("update cluster endpoint configuration: %w", err)
		}
	}
	if loggingDrift(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateClusterLogging(rc, desired.Name, desired.EnabledLoggingTypes)
		}, classifyEKSMutation); err != nil {
			return fmt.Errorf("update cluster logging: %w", err)
		}
	}
	toAdd, toRemove := tagDiff(desired.Tags, observed.Tags, desired.ManagedKey)
	if len(toRemove) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, observed.ARN, toRemove)
		}, classifyEKSMutation); err != nil {
			return fmt.Errorf("remove cluster tags: %w", err)
		}
	}
	if len(toAdd) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, observed.ARN, toAdd)
		}, classifyEKSMutation); err != nil {
			return fmt.Errorf("update cluster tags: %w", err)
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired EKSClusterSpec, outputs EKSClusterOutputs) error {
	name := strings.TrimSpace(outputs.Name)
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeEKSCluster(ctx, api, name)
	if err != nil || !observation.Exists {
		return err
	}
	if owner := observation.Value.Tags[managedKeyTag]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete EKS cluster %q owned by Praxis object %q", name, owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteCluster(rc, name)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyEKSDelete)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeEKSCluster(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeEKSCluster(ctx restate.ObjectContext, api EKSClusterAPI, name string) (kernel.Observation[ObservedState], error) {
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, err := api.DescribeCluster(rc, name)
		if IsNotFound(err) || !found {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyEKSObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (EKSClusterAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("EKSCluster driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve EKSCluster account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func validateEKSImmutableIdentity(desired EKSClusterSpec, observed ObservedState) error {
	switch {
	case desired.Name != observed.Name:
		return fmt.Errorf("name is immutable: observed %q, requested %q; delete and reprovision", observed.Name, desired.Name)
	case desired.RoleArn != observed.RoleArn:
		return fmt.Errorf("roleArn is immutable: observed %q, requested %q; delete and reprovision", observed.RoleArn, desired.RoleArn)
	case !stringSetEqual(desired.SubnetIds, observed.SubnetIds):
		return fmt.Errorf("subnetIds are immutable; delete and reprovision")
	case !stringSetEqual(desired.SecurityGroupIds, observed.SecurityGroupIds):
		return fmt.Errorf("securityGroupIds are immutable; delete and reprovision")
	default:
		return nil
	}
}

func classifyEKSObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyEKSCreate(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return classifyEKSObserve(err)
}

func classifyEKSMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	return classifyEKSObserve(err)
}

func classifyEKSDelete(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	return classifyEKSMutation(err)
}

func tagDiff(desired, observed map[string]string, managedKey string) (map[string]string, []string) {
	want := managedTags(drivers.FilterPraxisTags(desired), managedKey)
	have := managedTags(drivers.FilterPraxisTags(observed), managedKey)
	toAdd := map[string]string{}
	for key, value := range want {
		if current, ok := have[key]; !ok || current != value {
			toAdd[key] = value
		}
	}
	var toRemove []string
	for key := range have {
		if _, ok := want[key]; !ok {
			toRemove = append(toRemove, key)
		}
	}
	sort.Strings(toRemove)
	return toAdd, toRemove
}

func specFromObserved(observed ObservedState) EKSClusterSpec {
	return EKSClusterSpec{
		Name: observed.Name, RoleArn: observed.RoleArn,
		SubnetIds: append([]string{}, observed.SubnetIds...), SecurityGroupIds: append([]string{}, observed.SecurityGroupIds...),
		Version: observed.Version, EndpointPublicAccess: observed.EndpointPublicAccess,
		EndpointPrivateAccess: observed.EndpointPrivateAccess, PublicAccessCidrs: append([]string{}, observed.PublicAccessCidrs...),
		EnabledLoggingTypes: append([]string{}, observed.EnabledLoggingTypes...), Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) EKSClusterOutputs {
	return EKSClusterOutputs{
		ARN: observed.ARN, Name: observed.Name, Status: observed.Status,
		Version: observed.Version, PlatformVersion: observed.PlatformVersion, Endpoint: observed.Endpoint,
	}
}

func applyDefaults(spec EKSClusterSpec) EKSClusterSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.RoleArn = strings.TrimSpace(spec.RoleArn)
	spec.Version = strings.TrimSpace(spec.Version)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec EKSClusterSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if spec.RoleArn == "" {
		return fmt.Errorf("roleArn is required")
	}
	if len(spec.SubnetIds) < 2 {
		return fmt.Errorf("subnetIds requires at least two subnets in different availability zones")
	}
	for _, logType := range spec.EnabledLoggingTypes {
		if !slices.Contains(allLogTypes, logType) {
			return fmt.Errorf("enabledLoggingTypes contains invalid log type %q", logType)
		}
	}
	return nil
}
