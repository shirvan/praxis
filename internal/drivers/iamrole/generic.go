package iamrole

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
	apiFactory func(aws.Config) IAMRoleAPI
}

// NewGenericIAMRoleDriver binds IAM's composite role lifecycle to the shared kernel.
func NewGenericIAMRoleDriver(auth authservice.AuthClient) *kernel.Driver[IAMRoleSpec, IAMRoleOutputs, ObservedState] {
	return newGenericIAMRoleDriverWithFactory(auth, nil)
}

func newGenericIAMRoleDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMRoleAPI) *kernel.Driver[IAMRoleSpec, IAMRoleOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) IAMRoleAPI { return NewIAMRoleAPI(awsclient.NewIAMClient(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[IAMRoleSpec, IAMRoleOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec IAMRoleSpec) (IAMRoleSpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return IAMRoleSpec{}, drivers.ClassifyCredentialError(err)
			}
			return applyDefaults(spec), nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) IAMRoleSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed IAMRoleOutputs) IAMRoleOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.Arn == "" {
				outputs.Arn = seed.Arn
			}
			if outputs.RoleId == "" {
				outputs.RoleId = seed.RoleId
			}
			if outputs.RoleName == "" {
				outputs.RoleName = seed.RoleName
			}
			return outputs
		},
		HasDrift: HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired IAMRoleSpec, outputs IAMRoleOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.RoleName
	if name == "" {
		name = desired.RoleName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeRole(ctx, api, name)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired IAMRoleSpec) (kernel.CreateResult[IAMRoleOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[IAMRoleOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (IAMRoleOutputs, error) {
		arn, roleID, runErr := api.CreateRole(rc, desired)
		return IAMRoleOutputs{Arn: arn, RoleId: roleID, RoleName: desired.RoleName}, runErr
	}, classifyMutation)
	return kernel.CreateResult[IAMRoleOutputs]{SeedOutputs: outputs}, err
}

func (o *kernelOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next IAMRoleSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.RoleName != next.RoleName:
		return restate.TerminalError(fmt.Errorf("roleName is immutable; delete and reprovision to change it"), 409)
	case previous.Path != next.Path:
		return restate.TerminalError(fmt.Errorf("path is immutable; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired IAMRoleSpec, observed ObservedState) error {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	return o.convergeRole(ctx, api, desired, observed)
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired IAMRoleSpec, outputs IAMRoleOutputs) error {
	name := outputs.RoleName
	if name == "" {
		name = desired.RoleName
	}
	if name == "" {
		return nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	observation, err := o.observeRole(ctx, api, name)
	if err != nil || !observation.Exists {
		return err
	}
	observed := observation.Value

	// Inline policies, managed-policy attachments, and the boundary are owned by
	// this composite resource. Instance-profile associations are not: callers
	// must remove those external dependencies before deleting the role.
	for _, policyName := range sortedMapKeys(observed.InlinePolicies) {
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
	for _, policyARN := range sortedStrings(observed.ManagedPolicyArns) {
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
	if observed.PermissionsBoundary != "" {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DeletePermissionsBoundary(rc, name)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("delete permissions boundary: %w", err)
		}
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteRole(rc, name)
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
	return o.observeRole(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func (o *kernelOperations) observeRole(ctx restate.ObjectContext, api IAMRoleAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribeRole(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) convergeRole(ctx restate.ObjectContext, api IAMRoleAPI, desired IAMRoleSpec, observed ObservedState) error {
	if desired.Path != "" && observed.Path != "" && desired.Path != observed.Path {
		return restate.TerminalError(fmt.Errorf("path is immutable; delete and recreate the role to change the path"), 409)
	}
	if !policyDocumentsEqual(desired.AssumeRolePolicyDocument, observed.AssumeRolePolicyDocument) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateAssumeRolePolicy(rc, desired.RoleName, desired.AssumeRolePolicyDocument)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update assume role policy: %w", err)
		}
	}
	if desired.Description != observed.Description || desired.MaxSessionDuration != observed.MaxSessionDuration {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateRole(rc, desired.RoleName, desired.Description, desired.MaxSessionDuration)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update role settings: %w", err)
		}
	}
	if desired.PermissionsBoundary != observed.PermissionsBoundary {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if desired.PermissionsBoundary == "" {
				runErr := api.DeletePermissionsBoundary(rc, desired.RoleName)
				if IsNotFound(runErr) {
					runErr = nil
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, api.PutPermissionsBoundary(rc, desired.RoleName, desired.PermissionsBoundary)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update permissions boundary: %w", err)
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
			return restate.Void{}, api.PutInlinePolicy(rc, desired.RoleName, policyName, document)
		}, classifyMutation); err != nil {
			return fmt.Errorf("put inline policy %s: %w", policyName, err)
		}
	}
	for _, policyName := range sortedMapKeys(observedInline) {
		if _, ok := desiredInline[policyName]; ok {
			continue
		}
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DeleteInlinePolicy(rc, desired.RoleName, policyName)
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
			return restate.Void{}, api.AttachManagedPolicy(rc, desired.RoleName, policyARN)
		}, classifyMutation); err != nil {
			return fmt.Errorf("attach managed policy %s: %w", policyARN, err)
		}
	}
	for _, policyARN := range managedToRemove {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DetachManagedPolicy(rc, desired.RoleName, policyARN)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("detach managed policy %s: %w", policyARN, err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, desired.RoleName, desired.Tags)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update role tags: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (IAMRoleAPI, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("IAMRole driver is not configured with an auth registry")
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

func validateSpec(spec IAMRoleSpec) error {
	if spec.RoleName == "" {
		return fmt.Errorf("roleName is required")
	}
	if spec.AssumeRolePolicyDocument == "" {
		return fmt.Errorf("assumeRolePolicyDocument is required")
	}
	return nil
}

func applyDefaults(spec IAMRoleSpec) IAMRoleSpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.RoleName = strings.TrimSpace(spec.RoleName)
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.MaxSessionDuration == 0 {
		spec.MaxSessionDuration = 3600
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func specFromObserved(obs ObservedState) IAMRoleSpec {
	inlinePolicies := make(map[string]string, len(obs.InlinePolicies))
	for key, value := range obs.InlinePolicies {
		inlinePolicies[key] = normalizePolicyDocument(value)
	}
	return IAMRoleSpec{
		Path: obs.Path, RoleName: obs.RoleName,
		AssumeRolePolicyDocument: normalizePolicyDocument(obs.AssumeRolePolicyDocument),
		Description:              obs.Description, MaxSessionDuration: obs.MaxSessionDuration,
		PermissionsBoundary: obs.PermissionsBoundary, InlinePolicies: inlinePolicies,
		ManagedPolicyArns: sortedStrings(obs.ManagedPolicyArns), Tags: drivers.FilterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) IAMRoleOutputs {
	return IAMRoleOutputs{Arn: obs.Arn, RoleId: obs.RoleId, RoleName: obs.RoleName}
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
