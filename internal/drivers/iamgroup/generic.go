package iamgroup

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

type kernelOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) IAMGroupAPI
}

// NewGenericIAMGroupDriver binds IAM's composite group lifecycle to the shared kernel.
func NewGenericIAMGroupDriver(auth authservice.AuthClient) *kernel.Driver[IAMGroupSpec, IAMGroupOutputs, ObservedState] {
	return newGenericIAMGroupDriverWithFactory(auth, nil)
}

func newGenericIAMGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMGroupAPI) *kernel.Driver[IAMGroupSpec, IAMGroupOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) IAMGroupAPI { return NewIAMGroupAPI(awsclient.NewIAMClient(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[IAMGroupSpec, IAMGroupOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec IAMGroupSpec) (IAMGroupSpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return IAMGroupSpec{}, drivers.ClassifyCredentialError(err)
			}
			return applyDefaults(spec), nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) IAMGroupSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed IAMGroupOutputs) IAMGroupOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.Arn == "" {
				outputs.Arn = seed.Arn
			}
			if outputs.GroupId == "" {
				outputs.GroupId = seed.GroupId
			}
			if outputs.GroupName == "" {
				outputs.GroupName = seed.GroupName
			}
			return outputs
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired IAMGroupSpec, outputs IAMGroupOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.GroupName
	if name == "" {
		name = desired.GroupName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeGroup(ctx, api, name)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired IAMGroupSpec) (kernel.CreateResult[IAMGroupOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[IAMGroupOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (IAMGroupOutputs, error) {
		arn, groupID, runErr := api.CreateGroup(rc, desired)
		return IAMGroupOutputs{Arn: arn, GroupId: groupID, GroupName: desired.GroupName}, runErr
	}, classifyMutation)
	return kernel.CreateResult[IAMGroupOutputs]{SeedOutputs: outputs}, err
}

func (o *kernelOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next IAMGroupSpec, _ ObservedState, currentOutputs IAMGroupOutputs) (IAMGroupOutputs, error) {
	switch {
	case previous.Account != next.Account:
		return currentOutputs, restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.GroupName != next.GroupName:
		return currentOutputs, restate.TerminalError(fmt.Errorf("groupName is immutable; delete and reprovision to change it"), 409)
	default:
		return currentOutputs, nil
	}
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired IAMGroupSpec, observed ObservedState, currentOutputs IAMGroupOutputs) (IAMGroupOutputs, error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	return currentOutputs, o.convergeGroup(ctx, api, desired, observed)
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired IAMGroupSpec, outputs IAMGroupOutputs) error {
	name := outputs.GroupName
	if name == "" {
		name = desired.GroupName
	}
	if name == "" {
		return nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	observation, err := o.observeGroup(ctx, api, name)
	if err != nil || !observation.Exists {
		return err
	}

	// Policies are components declared on the group. Memberships are external
	// relationships and must remain untouched; DeleteGroup reports a conflict
	// until callers remove them explicitly.
	for _, policyName := range sortedMapKeys(observation.Value.InlinePolicies) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DeleteInlinePolicy(rc, name, policyName)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("delete inline policy %s: %w", policyName, err)
		}
	}
	for _, policyARN := range sortedStrings(observation.Value.ManagedPolicyArns) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DetachManagedPolicy(rc, name, policyARN)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("detach managed policy %s: %w", policyARN, err)
		}
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteGroup(rc, name)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeGroup(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func (o *kernelOperations) observeGroup(ctx restate.ObjectContext, api IAMGroupAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribeGroup(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) convergeGroup(ctx restate.ObjectContext, api IAMGroupAPI, desired IAMGroupSpec, observed ObservedState) error {
	if desired.Path != observed.Path {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateGroupPath(rc, desired.GroupName, desired.Path)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update group path: %w", err)
		}
	}

	desiredInline := normalizePolicyMap(desired.InlinePolicies)
	observedInline := normalizePolicyMap(observed.InlinePolicies)
	for _, policyName := range sortedMapKeys(desiredInline) {
		document := desiredInline[policyName]
		if current, ok := observedInline[policyName]; ok && current == document {
			continue
		}
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.PutInlinePolicy(rc, desired.GroupName, policyName, document)
		}, classifyMutation); err != nil {
			return fmt.Errorf("put inline policy %s: %w", policyName, err)
		}
	}
	for _, policyName := range sortedMapKeys(observedInline) {
		if _, ok := desiredInline[policyName]; ok {
			continue
		}
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DeleteInlinePolicy(rc, desired.GroupName, policyName)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("delete inline policy %s: %w", policyName, err)
		}
	}

	managedToAdd, managedToRemove := diffStringSets(desired.ManagedPolicyArns, observed.ManagedPolicyArns)
	for _, policyARN := range managedToAdd {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AttachManagedPolicy(rc, desired.GroupName, policyARN)
		}, classifyMutation); err != nil {
			return fmt.Errorf("attach managed policy %s: %w", policyARN, err)
		}
	}
	for _, policyARN := range managedToRemove {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DetachManagedPolicy(rc, desired.GroupName, policyARN)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("detach managed policy %s: %w", policyARN, err)
		}
	}
	return nil
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (IAMGroupAPI, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("IAMGroup driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAM account %q: %w", account, err)
	}
	return o.apiFactory(cfg), nil
}

func classifyObserve(err error) error {
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

func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsMalformedPolicy(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsAlreadyExists(err) || IsDeleteConflict(err) {
		return restate.TerminalError(err, 409)
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	return err
}

func validateSpec(spec IAMGroupSpec) error {
	if spec.GroupName == "" {
		return fmt.Errorf("groupName is required")
	}
	return nil
}

func applyDefaults(spec IAMGroupSpec) IAMGroupSpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.GroupName = strings.TrimSpace(spec.GroupName)
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	return spec
}

func outputsFromObserved(observed ObservedState) IAMGroupOutputs {
	return IAMGroupOutputs{Arn: observed.Arn, GroupId: observed.GroupId, GroupName: observed.GroupName}
}

func specFromObserved(observed ObservedState) IAMGroupSpec {
	return IAMGroupSpec{
		Path:              observed.Path,
		GroupName:         observed.GroupName,
		InlinePolicies:    normalizePolicyMap(observed.InlinePolicies),
		ManagedPolicyArns: sortedStrings(observed.ManagedPolicyArns),
	}
}

func diffStringSets(desired, observed []string) ([]string, []string) {
	desiredSet := make(map[string]struct{}, len(desired))
	observedSet := make(map[string]struct{}, len(observed))
	for _, value := range desired {
		desiredSet[value] = struct{}{}
	}
	for _, value := range observed {
		observedSet[value] = struct{}{}
	}
	var add, remove []string
	for value := range desiredSet {
		if _, ok := observedSet[value]; !ok {
			add = append(add, value)
		}
	}
	for value := range observedSet {
		if _, ok := desiredSet[value]; !ok {
			remove = append(remove, value)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
