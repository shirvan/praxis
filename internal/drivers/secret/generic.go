package secret

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

type kernelOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SecretsManagerSecretAPI
}

// NewGenericSecretsManagerSecretDriver binds Secrets Manager semantics to the
// shared lifecycle kernel without exposing secret values in resource outputs.
func NewGenericSecretsManagerSecretDriver(auth authservice.AuthClient) *kernel.Driver[SecretsManagerSecretSpec, SecretsManagerSecretOutputs, ObservedState] {
	return newGenericSecretsManagerSecretDriverWithFactory(auth, nil)
}

func newGenericSecretsManagerSecretDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SecretsManagerSecretAPI) *kernel.Driver[SecretsManagerSecretSpec, SecretsManagerSecretOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) SecretsManagerSecretAPI {
			return NewSecretsManagerSecretAPI(awsclient.NewSecretsManagerClient(cfg))
		}
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[SecretsManagerSecretSpec, SecretsManagerSecretOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec SecretsManagerSecretSpec) (SecretsManagerSecretSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return SecretsManagerSecretSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) SecretsManagerSecretSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed SecretsManagerSecretOutputs) SecretsManagerSecretOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.ARN == "" {
				outputs.ARN = seed.ARN
			}
			if outputs.Name == "" {
				outputs.Name = seed.Name
			}
			if outputs.VersionID == "" {
				outputs.VersionID = seed.VersionID
			}
			return outputs
		},
		HasDrift: HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired SecretsManagerSecretSpec, outputs SecretsManagerSecretOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.Name
	if name == "" {
		name = desired.Name
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeSecret(ctx, api, name)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired SecretsManagerSecretSpec) (kernel.CreateResult[SecretsManagerSecretOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[SecretsManagerSecretOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	token := secretClientRequestToken(desired.ManagedKey, ctx.Request().ID)
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (SecretsManagerSecretOutputs, error) {
		return api.CreateSecret(rc, desired, token)
	}, classifyMutation)
	return kernel.CreateResult[SecretsManagerSecretOutputs]{SeedOutputs: outputs}, err
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired SecretsManagerSecretSpec, observed ObservedState) error {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	return o.convergeSecret(ctx, api, desired, observed)
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired SecretsManagerSecretSpec, outputs SecretsManagerSecretOutputs) error {
	name := outputs.Name
	if name == "" {
		name = desired.Name
	}
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteSecret(rc, name, desired.ForceDelete)
		if IsNotFound(runErr) || IsScheduledForDeletion(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observation, err := o.observeSecret(ctx, api, strings.TrimSpace(ref.ResourceID))
	if err == nil && observation.Exists && observation.Value.ScheduledForDeletion {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(
			fmt.Errorf("cannot import secret %s while it is scheduled for deletion; restore it first", ref.ResourceID), 409,
		)
	}
	return observation, err
}

func (o *kernelOperations) observeSecret(ctx restate.ObjectContext, api SecretsManagerSecretAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, runErr := api.DescribeSecret(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil && found, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) convergeSecret(ctx restate.ObjectContext, api SecretsManagerSecretAPI, desired SecretsManagerSecretSpec, observed ObservedState) error {
	if desired.Name != observed.Name {
		return restate.TerminalError(fmt.Errorf("name is immutable; delete and recreate the secret to change its name"), 409)
	}

	if observed.ScheduledForDeletion {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.RestoreSecret(rc, desired.Name)
		}, classifyMutation); err != nil {
			return fmt.Errorf("restore scheduled secret: %w", err)
		}
	}
	if metadataDrift(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateSecret(rc, desired.Name, desired.Description, desired.KmsKeyID)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update secret metadata: %w", err)
		}
	}
	if desired.SecretString != observed.SecretString {
		token := secretClientRequestToken(desired.ManagedKey, ctx.Request().ID)
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.PutSecretValue(rc, desired.Name, desired.SecretString, token)
		}, classifyMutation); err != nil {
			return fmt.Errorf("put secret value: %w", err)
		}
	}
	toAdd, toRemove := tagDiff(desired.Tags, observed.Tags, desired.ManagedKey)
	if len(toRemove) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.RemoveTags(rc, desired.Name, toRemove)
		}, classifyMutation); err != nil {
			return fmt.Errorf("remove secret tags: %w", err)
		}
	}
	if len(toAdd) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AddTags(rc, desired.Name, toAdd)
		}, classifyMutation); err != nil {
			return fmt.Errorf("add secret tags: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (SecretsManagerSecretAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("SecretsManagerSecret driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SecretsManagerSecret account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyObserve(err error) error {
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

func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsAlreadyExists(err) {
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

func metadataDrift(spec SecretsManagerSecretSpec, observed ObservedState) bool {
	if strings.TrimSpace(spec.Description) != strings.TrimSpace(observed.Description) {
		return true
	}
	return !kmsKeyMatch(spec.KmsKeyID, observed.KmsKeyID)
}

func specFromObserved(observed ObservedState) SecretsManagerSecretSpec {
	kmsKeyID := observed.KmsKeyID
	if kmsKeyID == "alias/aws/secretsmanager" {
		kmsKeyID = ""
	}
	return SecretsManagerSecretSpec{
		Name: observed.Name, Description: observed.Description, KmsKeyID: kmsKeyID,
		SecretString: observed.SecretString, Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) SecretsManagerSecretOutputs {
	return SecretsManagerSecretOutputs{ARN: observed.ARN, Name: observed.Name, VersionID: observed.VersionID}
}

func applyDefaults(spec SecretsManagerSecretSpec) SecretsManagerSecretSpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec SecretsManagerSecretSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if spec.SecretString == "" {
		return fmt.Errorf("secretString is required")
	}
	return nil
}
