package ecscluster

import (
	"fmt"
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

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) ECSClusterAPI
}

func NewGenericECSClusterDriver(auth authservice.AuthClient) *kernel.Driver[ECSClusterSpec, ECSClusterOutputs, ObservedState] {
	return newGenericECSClusterDriverWithFactory(auth, nil)
}

func newGenericECSClusterDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) ECSClusterAPI) *kernel.Driver[ECSClusterSpec, ECSClusterOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) ECSClusterAPI { return NewECSClusterAPI(awsclient.NewECSClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[ECSClusterSpec, ECSClusterOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec ECSClusterSpec) (ECSClusterSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return ECSClusterSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) ECSClusterSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ ECSClusterOutputs) ECSClusterOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired ECSClusterSpec, outputs ECSClusterOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	name := strings.TrimSpace(outputs.Name)
	recoveredByName := false
	if name == "" {
		name = strings.TrimSpace(desired.Name)
		recoveredByName = name != ""
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	observation, err := observeCluster(ctx, api, name)
	if err != nil || !observation.Exists {
		return observation, err
	}
	owner := strings.TrimSpace(observation.Value.Tags["praxis:managed-key"])
	if owner != "" && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"ECS cluster %s is owned by Praxis object %q, not %q", name, owner, desired.ManagedKey,
		), 409)
	}
	if recoveredByName && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"refusing to adopt ECS cluster %s without exact Praxis ownership tag %q", name, desired.ManagedKey,
		), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired ECSClusterSpec) (kernel.CreateResult[ECSClusterOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[ECSClusterOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	observed, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (ObservedState, error) {
		current, found, describeErr := api.DescribeCluster(rc, desired.Name)
		if describeErr != nil {
			return ObservedState{}, describeErr
		}
		if found {
			if current.Tags["praxis:managed-key"] != desired.ManagedKey {
				return ObservedState{}, restate.TerminalError(fmt.Errorf(
					"refusing to adopt ECS cluster %s without exact Praxis ownership tag %q", desired.Name, desired.ManagedKey,
				), 409)
			}
			return current, nil
		}
		return api.CreateCluster(rc, desired)
	}, classifyECSMutation)
	return kernel.CreateResult[ECSClusterOutputs]{SeedOutputs: outputsFromObserved(observed)}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired ECSClusterSpec, observed ObservedState) error {
	if desired.Name != observed.Name {
		return restate.TerminalError(fmt.Errorf("name is immutable; delete and reprovision the ECS cluster to change it from %s to %s", observed.Name, desired.Name), 409)
	}
	if owner := strings.TrimSpace(observed.Tags["praxis:managed-key"]); owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("ECS cluster %s is owned by Praxis object %q, not %q", observed.Name, owner, desired.ManagedKey), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if containerInsightsDrift(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateCluster(rc, observed.Name, normalizeContainerInsights(desired.ContainerInsights))
		}, classifyECSMutation); err != nil {
			return err
		}
	}
	if capacityProvidersDrift(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.PutCapacityProviders(rc, observed.Name, desired.CapacityProviders)
		}, classifyECSMutation); err != nil {
			return err
		}
	}
	toAdd, toRemove := tagDiff(desired.Tags, observed.Tags, desired.ManagedKey)
	if len(toRemove) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, observed.ARN, toRemove)
		}, classifyECSMutation); err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, observed.ARN, toAdd)
		}, classifyECSMutation)
	}
	return err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired ECSClusterSpec, outputs ECSClusterOutputs) error {
	if outputs.Name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeCluster(ctx, api, outputs.Name)
	if err != nil || !observation.Exists {
		return err
	}
	if owner := strings.TrimSpace(observation.Value.Tags["praxis:managed-key"]); owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete ECS cluster %s owned by Praxis object %q", outputs.Name, owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteCluster(rc, outputs.Name)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyECSMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeCluster(ctx, api, ref.ResourceID)
}

func observeCluster(ctx restate.ObjectContext, api ECSClusterAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, err := api.DescribeCluster(rc, name)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil || !found {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyECSObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (ECSClusterAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("ECSCluster driver is not configured with an auth client")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ECSCluster account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyECSObserve(err error) error {
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

func classifyECSMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
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

func specFromObserved(observed ObservedState) ECSClusterSpec {
	return ECSClusterSpec{
		Name: observed.Name, ContainerInsights: normalizeContainerInsights(observed.ContainerInsights),
		CapacityProviders: append([]string{}, observed.CapacityProviders...), Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) ECSClusterOutputs {
	return ECSClusterOutputs{ARN: observed.ARN, Name: observed.Name, Status: observed.Status}
}

func applyDefaults(spec ECSClusterSpec) ECSClusterSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.ContainerInsights = strings.TrimSpace(spec.ContainerInsights)
	if spec.ContainerInsights == "" {
		spec.ContainerInsights = defaultContainerInsights
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec ECSClusterSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if spec.ContainerInsights != "enabled" && spec.ContainerInsights != "disabled" {
		return fmt.Errorf("containerInsights must be %q or %q", "enabled", "disabled")
	}
	return nil
}
