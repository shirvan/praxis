package iamuser

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
	apiFactory func(aws.Config) IAMUserAPI
}

// NewGenericIAMUserDriver binds IAM's composite user lifecycle to the shared kernel.
func NewGenericIAMUserDriver(auth authservice.AuthClient) *kernel.Driver[IAMUserSpec, IAMUserOutputs, ObservedState] {
	return newGenericIAMUserDriverWithFactory(auth, nil)
}

func newGenericIAMUserDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMUserAPI) *kernel.Driver[IAMUserSpec, IAMUserOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) IAMUserAPI { return NewIAMUserAPI(awsclient.NewIAMClient(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[IAMUserSpec, IAMUserOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec IAMUserSpec) (IAMUserSpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return IAMUserSpec{}, drivers.ClassifyCredentialError(err)
			}
			return applyDefaults(spec), nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) IAMUserSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed IAMUserOutputs) IAMUserOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.Arn == "" {
				outputs.Arn = seed.Arn
			}
			if outputs.UserId == "" {
				outputs.UserId = seed.UserId
			}
			if outputs.UserName == "" {
				outputs.UserName = seed.UserName
			}
			return outputs
		},
		HasDrift: HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired IAMUserSpec, outputs IAMUserOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.UserName
	if name == "" {
		name = desired.UserName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeUser(ctx, api, name)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired IAMUserSpec) (kernel.CreateResult[IAMUserOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[IAMUserOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (IAMUserOutputs, error) {
		arn, userID, runErr := api.CreateUser(rc, desired)
		return IAMUserOutputs{Arn: arn, UserId: userID, UserName: desired.UserName}, runErr
	}, classifyMutation)
	return kernel.CreateResult[IAMUserOutputs]{SeedOutputs: outputs}, err
}

func (o *kernelOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next IAMUserSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.UserName != next.UserName:
		return restate.TerminalError(fmt.Errorf("userName is immutable; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired IAMUserSpec, observed ObservedState) error {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	return o.convergeUser(ctx, api, desired, observed)
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired IAMUserSpec, outputs IAMUserOutputs) error {
	name := outputs.UserName
	if name == "" {
		name = desired.UserName
	}
	if name == "" {
		return nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	observation, err := o.observeUser(ctx, api, name)
	if err != nil || !observation.Exists {
		return err
	}
	observed := observation.Value

	// These composite settings are authoritative parts of IAMUserSpec, so they
	// are removed before the user. Access keys, login profiles, MFA devices, and
	// other credentials are outside this resource contract and deliberately
	// remain provider-reported deletion conflicts.
	for _, groupName := range sortedStrings(observed.Groups) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.RemoveUserFromGroup(rc, name, groupName)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("remove user from group %s: %w", groupName, err)
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
	if observed.PermissionsBoundary != "" {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DeleteUserPermissionsBoundary(rc, name)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("delete permissions boundary: %w", err)
		}
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteUser(rc, name)
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
	return o.observeUser(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func (o *kernelOperations) observeUser(ctx restate.ObjectContext, api IAMUserAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribeUser(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) convergeUser(ctx restate.ObjectContext, api IAMUserAPI, desired IAMUserSpec, observed ObservedState) error {
	if desired.Path != observed.Path {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateUserPath(rc, desired.UserName, desired.Path)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update user path: %w", err)
		}
	}
	if desired.PermissionsBoundary != observed.PermissionsBoundary {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if desired.PermissionsBoundary == "" {
				runErr := api.DeleteUserPermissionsBoundary(rc, desired.UserName)
				if IsNotFound(runErr) {
					runErr = nil
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, api.PutUserPermissionsBoundary(rc, desired.UserName, desired.PermissionsBoundary)
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
			return restate.Void{}, api.PutInlinePolicy(rc, desired.UserName, policyName, document)
		}, classifyMutation); err != nil {
			return fmt.Errorf("put inline policy %s: %w", policyName, err)
		}
	}
	for _, policyName := range sortedMapKeys(observedInline) {
		if _, ok := desiredInline[policyName]; ok {
			continue
		}
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DeleteInlinePolicy(rc, desired.UserName, policyName)
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
			return restate.Void{}, api.AttachManagedPolicy(rc, desired.UserName, policyARN)
		}, classifyMutation); err != nil {
			return fmt.Errorf("attach managed policy %s: %w", policyARN, err)
		}
	}
	for _, policyARN := range managedToRemove {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DetachManagedPolicy(rc, desired.UserName, policyARN)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("detach managed policy %s: %w", policyARN, err)
		}
	}

	groupsToAdd, groupsToRemove := diffStringSets(desired.Groups, observed.Groups)
	for _, groupName := range groupsToAdd {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AddUserToGroup(rc, desired.UserName, groupName)
		}, classifyMutation); err != nil {
			return fmt.Errorf("add user to group %s: %w", groupName, err)
		}
	}
	for _, groupName := range groupsToRemove {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.RemoveUserFromGroup(rc, desired.UserName, groupName)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("remove user from group %s: %w", groupName, err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, desired.UserName, desired.Tags)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update user tags: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (IAMUserAPI, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("IAMUser driver is not configured with an auth registry")
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

func validateSpec(spec IAMUserSpec) error {
	if spec.UserName == "" {
		return fmt.Errorf("userName is required")
	}
	return nil
}

func applyDefaults(spec IAMUserSpec) IAMUserSpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.UserName = strings.TrimSpace(spec.UserName)
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	if spec.Groups == nil {
		spec.Groups = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func specFromObserved(obs ObservedState) IAMUserSpec {
	inlinePolicies := make(map[string]string, len(obs.InlinePolicies))
	for key, value := range obs.InlinePolicies {
		inlinePolicies[key] = normalizePolicyDocument(value)
	}
	return IAMUserSpec{
		Path: obs.Path, UserName: obs.UserName, PermissionsBoundary: obs.PermissionsBoundary,
		InlinePolicies: inlinePolicies, ManagedPolicyArns: sortedStrings(obs.ManagedPolicyArns),
		Groups: sortedStrings(obs.Groups), Tags: drivers.FilterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) IAMUserOutputs {
	return IAMUserOutputs{Arn: obs.Arn, UserId: obs.UserId, UserName: obs.UserName}
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

func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
