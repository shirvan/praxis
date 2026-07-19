package dbsubnetgroup

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
	apiFactory func(aws.Config) DBSubnetGroupAPI
}

// NewGenericDBSubnetGroupDriver binds DB subnet-group provider behavior to the
// shared lifecycle kernel.
func NewGenericDBSubnetGroupDriver(auth authservice.AuthClient) *kernel.Driver[DBSubnetGroupSpec, DBSubnetGroupOutputs, ObservedState] {
	return newGenericDBSubnetGroupDriverWithFactory(auth, nil)
}

func newGenericDBSubnetGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) DBSubnetGroupAPI) *kernel.Driver[DBSubnetGroupSpec, DBSubnetGroupOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) DBSubnetGroupAPI { return NewDBSubnetGroupAPI(awsclient.NewRDSClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[DBSubnetGroupSpec, DBSubnetGroupOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec DBSubnetGroupSpec) (DBSubnetGroupSpec, error) {
			if _, _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return DBSubnetGroupSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) DBSubnetGroupSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ DBSubnetGroupOutputs) DBSubnetGroupOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired DBSubnetGroupSpec, outputs DBSubnetGroupOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	groupName := strings.TrimSpace(outputs.GroupName)
	resolvedByName := groupName == ""
	if groupName == "" {
		groupName = strings.TrimSpace(desired.GroupName)
	}
	observation, err := observeDBSubnetGroup(ctx, api, groupName)
	if err != nil || !observation.Exists {
		return observation, err
	}
	if resolvedByName && observation.Value.ManagedKey != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"db subnet group %q already exists without exact Praxis ownership (managed key %q, expected %q)",
			groupName, observation.Value.ManagedKey, desired.ManagedKey,
		), 409)
	}
	if observation.Value.ManagedKey != "" && observation.Value.ManagedKey != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"db subnet group %q is owned by Praxis object %q, not %q",
			groupName, observation.Value.ManagedKey, desired.ManagedKey,
		), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired DBSubnetGroupSpec) (kernel.CreateResult[DBSubnetGroupOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[DBSubnetGroupOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	arn, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// RDS has no idempotency token for this create API. The ownership tag is
		// atomic with creation, so an exact match recovers an ambiguous response.
		observed, describeErr := api.DescribeDBSubnetGroup(rc, desired.GroupName)
		if describeErr == nil {
			if observed.ManagedKey == desired.ManagedKey {
				return observed.ARN, nil
			}
			return "", restate.TerminalError(fmt.Errorf(
				"db subnet group %q already exists without exact Praxis ownership", desired.GroupName,
			), 409)
		}
		if !IsNotFound(describeErr) {
			return "", describeErr
		}
		return api.CreateDBSubnetGroup(rc, desired)
	}, classifyDBSubnetGroupCreate)
	return kernel.CreateResult[DBSubnetGroupOutputs]{SeedOutputs: DBSubnetGroupOutputs{GroupName: desired.GroupName, ARN: arn}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired DBSubnetGroupSpec, observed ObservedState, currentOutputs DBSubnetGroupOutputs) (DBSubnetGroupOutputs, error) {
	if desired.GroupName != observed.GroupName {
		return currentOutputs, restate.TerminalError(fmt.Errorf(
			"groupName is immutable: observed %q, requested %q; delete and reprovision",
			observed.GroupName, desired.GroupName,
		), 409)
	}
	if observed.ManagedKey != "" && observed.ManagedKey != desired.ManagedKey {
		return currentOutputs, restate.TerminalError(fmt.Errorf(
			"db subnet group %q is owned by Praxis object %q, not %q",
			observed.GroupName, observed.ManagedKey, desired.ManagedKey,
		), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if desired.Description != observed.Description || !stringSliceEqual(desired.SubnetIds, observed.SubnetIds) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyDBSubnetGroup(rc, desired)
		}, classifyDBSubnetGroupMutation); err != nil {
			return currentOutputs, fmt.Errorf("modify db subnet group: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) && observed.ARN != "" {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.ARN, desired.Tags)
		}, classifyDBSubnetGroupMutation); err != nil {
			return currentOutputs, fmt.Errorf("update db subnet group tags: %w", err)
		}
	}
	return currentOutputs, nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired DBSubnetGroupSpec, outputs DBSubnetGroupOutputs) error {
	groupName := strings.TrimSpace(outputs.GroupName)
	if groupName == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeDBSubnetGroup(ctx, api, groupName)
	if err != nil || !observation.Exists {
		return err
	}
	if observation.Value.ManagedKey != "" && observation.Value.ManagedKey != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf(
			"refusing to delete db subnet group %q owned by Praxis object %q, not %q",
			groupName, observation.Value.ManagedKey, desired.ManagedKey,
		), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteDBSubnetGroup(rc, groupName)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyDBSubnetGroupMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observation, err := observeDBSubnetGroup(ctx, api, strings.TrimSpace(ref.ResourceID))
	if observation.Exists {
		observation.Value.Region = region
	}
	return observation, err
}

func observeDBSubnetGroup(ctx restate.ObjectContext, api DBSubnetGroupAPI, groupName string) (kernel.Observation[ObservedState], error) {
	if groupName == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeDBSubnetGroup(rc, groupName)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyDBSubnetGroupObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (DBSubnetGroupAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("db subnet group driver is not configured")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RDS account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyDBSubnetGroupObserve(err error) error {
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

func classifyDBSubnetGroupCreate(err error) error {
	if err != nil && IsAlreadyExists(err) {
		return restate.TerminalError(err, 409)
	}
	if err != nil && IsQuotaExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return classifyDBSubnetGroupObserve(err)
}

func classifyDBSubnetGroupMutation(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if IsInvalidState(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyDBSubnetGroupObserve(err)
}

func validateSpec(spec DBSubnetGroupSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.GroupName) == "" {
		return fmt.Errorf("groupName is required")
	}
	if strings.TrimSpace(spec.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if len(spec.SubnetIds) < 2 {
		return fmt.Errorf("subnetIds must contain at least 2 subnets")
	}
	return nil
}

func specFromObserved(observed ObservedState) DBSubnetGroupSpec {
	return DBSubnetGroupSpec{
		GroupName: observed.GroupName, Description: observed.Description,
		SubnetIds: normalizeStrings(observed.SubnetIds), Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) DBSubnetGroupOutputs {
	return DBSubnetGroupOutputs{
		GroupName: observed.GroupName, ARN: observed.ARN, VpcId: observed.VpcId,
		SubnetIds: normalizeStrings(observed.SubnetIds), AvailabilityZones: normalizeStrings(observed.AvailabilityZones),
		Status: observed.Status,
	}
}
