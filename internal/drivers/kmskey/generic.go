package kmskey

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type kernelOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) KMSKeyAPI
}

const (
	defaultKeyUsage   = "ENCRYPT_DECRYPT"
	defaultKeySpec    = "SYMMETRIC_DEFAULT"
	defaultDeleteWait = int32(30)
	aliasPrefix       = "alias/"
)

// NewGenericKMSKeyDriver binds the composite key-plus-alias resource to the
// shared lifecycle while retaining KMS-specific convergence in typed ops.
func NewGenericKMSKeyDriver(auth authservice.AuthClient) *kernel.Driver[KMSKeySpec, KMSKeyOutputs, ObservedState] {
	return NewGenericKMSKeyDriverWithFactory(auth, nil)
}

func NewGenericKMSKeyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) KMSKeyAPI) *kernel.Driver[KMSKeySpec, KMSKeyOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) KMSKeyAPI { return NewKMSKeyAPI(awsclient.NewKMSClient(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[KMSKeySpec, KMSKeyOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec KMSKeySpec) (KMSKeySpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return KMSKeySpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) KMSKeySpec {
			name := strings.TrimPrefix(strings.TrimSpace(ref.ResourceID), aliasPrefix)
			spec := specFromObserved(name, observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ KMSKeyOutputs) KMSKeyOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired KMSKeySpec, outputs KMSKeyOutputs) (kernel.Observation[ObservedState], error) {
	alias := outputs.AliasName
	if alias == "" && strings.TrimSpace(desired.Name) != "" {
		alias = aliasFor(desired.Name)
	}
	if alias == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observed, found, err := o.observeKey(ctx, api, alias)
	return kernel.Observation[ObservedState]{Exists: found, Value: observed}, err
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired KMSKeySpec) (kernel.CreateResult[KMSKeyOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[KMSKeyOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	keyID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		id, _, runErr := api.CreateKey(rc, desired)
		return id, runErr
	}, classifyMutation)
	if err != nil {
		return kernel.CreateResult[KMSKeyOutputs]{}, err
	}
	alias := aliasFor(desired.Name)
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.CreateAlias(rc, alias, keyID)
		if IsConflict(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return kernel.CreateResult[KMSKeyOutputs]{SeedOutputs: KMSKeyOutputs{KeyID: keyID, AliasName: alias}}, err
}

func (o *kernelOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next KMSKeySpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.Name != next.Name:
		return restate.TerminalError(fmt.Errorf("name is immutable; delete and reprovision to change it"), 409)
	case previous.KeyUsage != next.KeyUsage:
		return restate.TerminalError(fmt.Errorf("keyUsage is immutable; delete and reprovision to change it"), 409)
	case previous.KeySpec != next.KeySpec:
		return restate.TerminalError(fmt.Errorf("keySpec is immutable; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired KMSKeySpec, observed ObservedState) error {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	return o.convergeMutableFields(ctx, api, desired, observed)
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired KMSKeySpec, outputs KMSKeyOutputs) error {
	if outputs.KeyID == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	window := desired.DeletionWindowInDays
	if window == 0 {
		window = defaultDeleteWait
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if outputs.AliasName != "" {
			if runErr := api.DeleteAlias(rc, outputs.AliasName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		if runErr := api.ScheduleKeyDeletion(rc, outputs.KeyID, window); runErr != nil && !IsNotFound(runErr) {
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	name := strings.TrimPrefix(strings.TrimSpace(ref.ResourceID), aliasPrefix)
	if name == "" {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("KMS alias is required"), 400)
	}
	observed, found, err := o.observeKey(ctx, api, aliasFor(name))
	return kernel.Observation[ObservedState]{Exists: found, Value: observed}, err
}

func (o *kernelOperations) convergeMutableFields(ctx restate.ObjectContext, api KMSKeyAPI, spec KMSKeySpec, observed ObservedState) error {
	keyID := observed.KeyID
	if spec.Description != observed.Description {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateDescription(rc, keyID, spec.Description)
		}, classifyMutation); err != nil {
			return err
		}
	}
	if spec.EnableKeyRotation != observed.EnableKeyRotation {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if spec.EnableKeyRotation {
				return restate.Void{}, api.EnableKeyRotation(rc, keyID)
			}
			return restate.Void{}, api.DisableKeyRotation(rc, keyID)
		}, classifyMutation); err != nil {
			return err
		}
	}
	toAdd, toRemove := tagDiff(spec.Tags, observed.Tags, spec.ManagedKey)
	if len(toRemove) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, keyID, toRemove)
		}, classifyMutation); err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, keyID, toAdd)
		}, classifyMutation); err != nil {
			return err
		}
	}
	return nil
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (KMSKeyAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("KMSKey driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve KMSKey account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func (o *kernelOperations) observeKey(ctx restate.ObjectContext, api KMSKeyAPI, alias string) (ObservedState, bool, error) {
	result, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, runErr := api.DescribeKey(rc, alias)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: found, Value: observed}, runErr
	}, classifyMutation)
	return result.Value, result.Exists, err
}

func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	if IsInvalidParam(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func specFromObserved(name string, observed ObservedState) KMSKeySpec {
	return KMSKeySpec{
		Name: name, Description: observed.Description, KeyUsage: observed.KeyUsage,
		KeySpec: observed.KeySpec, EnableKeyRotation: observed.EnableKeyRotation,
		DeletionWindowInDays: defaultDeleteWait, Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) KMSKeyOutputs {
	return KMSKeyOutputs{ARN: observed.ARN, KeyID: observed.KeyID, AliasName: observed.AliasName}
}

func aliasFor(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, aliasPrefix) {
		return name
	}
	return aliasPrefix + name
}

func applyDefaults(spec KMSKeySpec) KMSKeySpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimPrefix(strings.TrimSpace(spec.Name), aliasPrefix)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.KeyUsage = strings.TrimSpace(spec.KeyUsage)
	if spec.KeyUsage == "" {
		spec.KeyUsage = defaultKeyUsage
	}
	spec.KeySpec = strings.TrimSpace(spec.KeySpec)
	if spec.KeySpec == "" {
		spec.KeySpec = defaultKeySpec
	}
	if spec.DeletionWindowInDays == 0 {
		spec.DeletionWindowInDays = defaultDeleteWait
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec KMSKeySpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch spec.KeyUsage {
	case "ENCRYPT_DECRYPT", "SIGN_VERIFY", "GENERATE_VERIFY_MAC":
	default:
		return fmt.Errorf("keyUsage must be ENCRYPT_DECRYPT, SIGN_VERIFY, or GENERATE_VERIFY_MAC")
	}
	if spec.KeySpec == "" {
		return fmt.Errorf("keySpec is required")
	}
	if spec.DeletionWindowInDays < 7 || spec.DeletionWindowInDays > 30 {
		return fmt.Errorf("deletionWindowInDays must be between 7 and 30")
	}
	return nil
}
