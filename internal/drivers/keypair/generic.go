package keypair

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

const managedKeyTag = "praxis:managed-key"

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) KeyPairAPI
}

func NewGenericKeyPairDriver(auth authservice.AuthClient) *kernel.Driver[KeyPairSpec, KeyPairOutputs, ObservedState] {
	return newGenericKeyPairDriverWithFactory(auth, nil)
}

func newGenericKeyPairDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) KeyPairAPI) *kernel.Driver[KeyPairSpec, KeyPairOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) KeyPairAPI { return NewKeyPairAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[KeyPairSpec, KeyPairOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec KeyPairSpec) (KeyPairSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return KeyPairSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return KeyPairSpec{}, restate.TerminalError(fmt.Errorf("region %q does not match account region %q", spec.Region, region), 400)
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateGenericSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) KeyPairSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ KeyPairOutputs) KeyPairOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired KeyPairSpec, outputs KeyPairOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	name := strings.TrimSpace(outputs.KeyName)
	recovering := name == ""
	if name == "" {
		name = strings.TrimSpace(desired.KeyName)
	}
	observation, err := observeKeyPair(ctx, api, name)
	if err != nil || !observation.Exists {
		return observation, err
	}
	owner := observation.Value.Tags[managedKeyTag]
	if owner != "" && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("key pair %q is owned by Praxis object %q, not %q", name, owner, desired.ManagedKey), 409)
	}
	if recovering && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt key pair %q without exact Praxis ownership tag %q", name, desired.ManagedKey), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired KeyPairSpec) (kernel.CreateResult[KeyPairOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[KeyPairOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	created, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (KeyPairOutputs, error) {
		observed, describeErr := api.DescribeKeyPair(rc, desired.KeyName)
		if describeErr == nil {
			if observed.Tags[managedKeyTag] != desired.ManagedKey {
				return KeyPairOutputs{}, restate.TerminalError(fmt.Errorf("refusing to adopt key pair %q without exact Praxis ownership tag %q", desired.KeyName, desired.ManagedKey), 409)
			}
			return outputsFromObserved(observed), nil
		}
		if !IsNotFound(describeErr) {
			return KeyPairOutputs{}, describeErr
		}
		tags := keyPairManagedTags(desired.Tags, desired.ManagedKey)
		if desired.PublicKeyMaterial != "" {
			id, fingerprint, importErr := api.ImportKeyPair(rc, desired.KeyName, desired.PublicKeyMaterial, tags)
			return KeyPairOutputs{KeyName: desired.KeyName, KeyPairId: id, KeyFingerprint: fingerprint, KeyType: desired.KeyType}, importErr
		}
		id, fingerprint, privateKey, createErr := api.CreateKeyPair(rc, desired.KeyName, desired.KeyType, tags)
		return KeyPairOutputs{
			KeyName: desired.KeyName, KeyPairId: id, KeyFingerprint: fingerprint,
			KeyType: desired.KeyType, PrivateKeyMaterial: privateKey,
		}, createErr
	}, classifyKeyPairCreate)
	if err != nil {
		return kernel.CreateResult[KeyPairOutputs]{}, err
	}
	seed := created
	seed.PrivateKeyMaterial = ""
	result := kernel.CreateResult[KeyPairOutputs]{SeedOutputs: seed}
	if created.PrivateKeyMaterial != "" {
		// This complete response is returned once and journaled by Restate, but
		// the kernel never copies it into durable State.Outputs.
		result.CreateOnlyResponse = &created
	}
	return result, nil
}

func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next KeyPairSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.KeyName != next.KeyName:
		return restate.TerminalError(fmt.Errorf("keyName is immutable; delete and reprovision to change it"), 409)
	case previous.KeyType != next.KeyType:
		return restate.TerminalError(fmt.Errorf("keyType is immutable; delete and reprovision to change it"), 409)
	case previous.PublicKeyMaterial != next.PublicKeyMaterial:
		return restate.TerminalError(fmt.Errorf("publicKeyMaterial is create-only; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired KeyPairSpec, observed ObservedState) error {
	if desired.KeyName != observed.KeyName {
		return restate.TerminalError(fmt.Errorf("keyName is immutable: observed %q, requested %q; delete and reprovision", observed.KeyName, desired.KeyName), 409)
	}
	if desired.KeyType != observed.KeyType {
		return restate.TerminalError(fmt.Errorf("keyType is immutable: observed %q, requested %q; delete and reprovision", observed.KeyType, desired.KeyType), 409)
	}
	if drivers.TagsMatch(desired.Tags, observed.Tags) && observed.Tags[managedKeyTag] == desired.ManagedKey {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.UpdateTags(rc, observed.KeyPairId, keyPairManagedTags(desired.Tags, desired.ManagedKey))
	}, classifyKeyPairMutation)
	return err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired KeyPairSpec, outputs KeyPairOutputs) error {
	name := strings.TrimSpace(outputs.KeyName)
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeKeyPair(ctx, api, name)
	if err != nil || !observation.Exists {
		return err
	}
	if owner := observation.Value.Tags[managedKeyTag]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete key pair %q owned by Praxis object %q", name, owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteKeyPair(rc, name)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyKeyPairMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeKeyPair(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeKeyPair(ctx restate.ObjectContext, api KeyPairAPI, name string) (kernel.Observation[ObservedState], error) {
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeKeyPair(rc, name)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyKeyPairObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (KeyPairAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("KeyPair driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve key pair account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func validateGenericSpec(spec KeyPairSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.KeyName == "" {
		return fmt.Errorf("keyName is required")
	}
	if spec.KeyType != "rsa" && spec.KeyType != "ed25519" {
		return fmt.Errorf("keyType must be \"rsa\" or \"ed25519\"")
	}
	return nil
}

func keyPairManagedTags(tags map[string]string, managedKey string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, drivers.FilterPraxisTags(tags))
	if managedKey != "" {
		out[managedKeyTag] = managedKey
	}
	return out
}

func classifyKeyPairObserve(err error) error {
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

func classifyKeyPairCreate(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if IsDuplicate(err) {
		return restate.TerminalError(err, 409)
	}
	if IsInvalidKeyFormat(err) {
		return restate.TerminalError(err, 400)
	}
	return classifyKeyPairObserve(err)
}

func classifyKeyPairMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	return classifyKeyPairObserve(err)
}

func applyDefaults(spec KeyPairSpec) KeyPairSpec {
	if spec.KeyType == "" {
		spec.KeyType = "ed25519"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func specFromObserved(observed ObservedState) KeyPairSpec {
	return KeyPairSpec{KeyName: observed.KeyName, KeyType: observed.KeyType, Tags: drivers.FilterPraxisTags(observed.Tags)}
}

func outputsFromObserved(observed ObservedState) KeyPairOutputs {
	return KeyPairOutputs{
		KeyName: observed.KeyName, KeyPairId: observed.KeyPairId,
		KeyFingerprint: observed.KeyFingerprint, KeyType: observed.KeyType,
	}
}
