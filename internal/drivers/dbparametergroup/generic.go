package dbparametergroup

import (
	"fmt"
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
	apiFactory func(aws.Config) DBParameterGroupAPI
}

// NewGenericDBParameterGroupDriver binds DB and cluster parameter-group
// provider behavior to the shared lifecycle kernel.
func NewGenericDBParameterGroupDriver(auth authservice.AuthClient) *kernel.Driver[DBParameterGroupSpec, DBParameterGroupOutputs, ObservedState] {
	return newGenericDBParameterGroupDriverWithFactory(auth, nil)
}

func newGenericDBParameterGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) DBParameterGroupAPI) *kernel.Driver[DBParameterGroupSpec, DBParameterGroupOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) DBParameterGroupAPI { return NewDBParameterGroupAPI(awsclient.NewRDSClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[DBParameterGroupSpec, DBParameterGroupOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec DBParameterGroupSpec) (DBParameterGroupSpec, error) {
			if _, _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return DBParameterGroupSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) DBParameterGroupSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ DBParameterGroupOutputs) DBParameterGroupOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired DBParameterGroupSpec, outputs DBParameterGroupOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	groupName := strings.TrimSpace(outputs.GroupName)
	groupType := strings.TrimSpace(outputs.Type)
	resolvedByName := groupName == ""
	if groupName == "" {
		groupName = strings.TrimSpace(desired.GroupName)
	}
	if groupType == "" {
		groupType = desired.Type
	}
	observation, err := observeDBParameterGroup(ctx, api, groupName, groupType)
	if err != nil || !observation.Exists {
		return observation, err
	}
	if resolvedByName && observation.Value.ManagedKey != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"db parameter group %q already exists without exact Praxis ownership (managed key %q, expected %q)",
			groupName, observation.Value.ManagedKey, desired.ManagedKey,
		), 409)
	}
	if observation.Value.ManagedKey != "" && observation.Value.ManagedKey != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"db parameter group %q is owned by Praxis object %q, not %q",
			groupName, observation.Value.ManagedKey, desired.ManagedKey,
		), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired DBParameterGroupSpec) (kernel.CreateResult[DBParameterGroupOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[DBParameterGroupOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	arn, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// RDS has no idempotency token for these create APIs. The ownership tag
		// is atomic with creation, so an exact match recovers an ambiguous reply.
		observed, describeErr := api.DescribeParameterGroup(rc, desired.GroupName, desired.Type)
		if describeErr == nil {
			if observed.ManagedKey == desired.ManagedKey {
				return observed.ARN, nil
			}
			return "", restate.TerminalError(fmt.Errorf(
				"db parameter group %q already exists without exact Praxis ownership", desired.GroupName,
			), 409)
		}
		if !IsNotFound(describeErr) {
			return "", describeErr
		}
		return api.CreateParameterGroup(rc, desired)
	}, classifyDBParameterGroupCreate)
	return kernel.CreateResult[DBParameterGroupOutputs]{SeedOutputs: DBParameterGroupOutputs{
		GroupName: desired.GroupName, ARN: arn, Family: desired.Family, Type: desired.Type,
	}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired DBParameterGroupSpec, observed ObservedState) error {
	if err := validateImmutableIdentity(desired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	if observed.ManagedKey != "" && observed.ManagedKey != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf(
			"db parameter group %q is owned by Praxis object %q, not %q",
			observed.GroupName, observed.ManagedKey, desired.ManagedKey,
		), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if !parametersEqual(desired.Parameters, observed.Parameters) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateParameters(rc, desired, observed)
		}, classifyDBParameterGroupMutation); err != nil {
			return fmt.Errorf("update db parameter group parameters: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) && observed.ARN != "" {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.ARN, desired.Tags)
		}, classifyDBParameterGroupMutation); err != nil {
			return fmt.Errorf("update db parameter group tags: %w", err)
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired DBParameterGroupSpec, outputs DBParameterGroupOutputs) error {
	groupName := strings.TrimSpace(outputs.GroupName)
	if groupName == "" {
		return nil
	}
	groupType := outputs.Type
	if groupType == "" {
		groupType = desired.Type
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeDBParameterGroup(ctx, api, groupName, groupType)
	if err != nil || !observation.Exists {
		return err
	}
	if observation.Value.ManagedKey != "" && observation.Value.ManagedKey != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf(
			"refusing to delete db parameter group %q owned by Praxis object %q, not %q",
			groupName, observation.Value.ManagedKey, desired.ManagedKey,
		), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteParameterGroup(rc, groupName, groupType)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyDBParameterGroupMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observation, err := observeDBParameterGroup(ctx, api, strings.TrimSpace(ref.ResourceID), parameterGroupTypeFromKey(restate.Key(ctx)))
	if observation.Exists {
		observation.Value.Region = region
	}
	return observation, err
}

func observeDBParameterGroup(ctx restate.ObjectContext, api DBParameterGroupAPI, groupName, groupType string) (kernel.Observation[ObservedState], error) {
	if groupName == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeParameterGroup(rc, groupName, groupType)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyDBParameterGroupObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (DBParameterGroupAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("db parameter group driver is not configured")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RDS account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyDBParameterGroupObserve(err error) error {
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

func classifyDBParameterGroupCreate(err error) error {
	if err != nil && IsAlreadyExists(err) {
		return restate.TerminalError(err, 409)
	}
	if err != nil && IsQuotaExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return classifyDBParameterGroupObserve(err)
}

func classifyDBParameterGroupMutation(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if IsInvalidState(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyDBParameterGroupObserve(err)
}

func validateImmutableIdentity(desired DBParameterGroupSpec, observed ObservedState) error {
	switch {
	case desired.GroupName != observed.GroupName:
		return fmt.Errorf("groupName is immutable: observed %q, requested %q; delete and reprovision", observed.GroupName, desired.GroupName)
	case desired.Type != observed.Type:
		return fmt.Errorf("type is immutable: observed %q, requested %q; delete and reprovision", observed.Type, desired.Type)
	case desired.Family != observed.Family:
		return fmt.Errorf("family is immutable: observed %q, requested %q; delete and reprovision", observed.Family, desired.Family)
	case desired.Description != observed.Description:
		return fmt.Errorf("description is immutable: observed %q, requested %q; delete and reprovision", observed.Description, desired.Description)
	default:
		return nil
	}
}

func validateSpec(spec DBParameterGroupSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.GroupName) == "" {
		return fmt.Errorf("groupName is required")
	}
	if spec.Type != TypeDB && spec.Type != TypeCluster {
		return fmt.Errorf("type must be %q or %q", TypeDB, TypeCluster)
	}
	if strings.TrimSpace(spec.Family) == "" {
		return fmt.Errorf("family is required")
	}
	return nil
}

func specFromObserved(observed ObservedState) DBParameterGroupSpec {
	return DBParameterGroupSpec{
		GroupName: observed.GroupName, Type: observed.Type, Family: observed.Family,
		Description: observed.Description, Parameters: observed.Parameters,
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) DBParameterGroupOutputs {
	return DBParameterGroupOutputs{
		GroupName: observed.GroupName, ARN: observed.ARN, Family: observed.Family, Type: observed.Type,
	}
}

func parameterGroupTypeFromKey(key string) string {
	if strings.Contains(strings.ToLower(key), "cluster") {
		return TypeCluster
	}
	return TypeDB
}
