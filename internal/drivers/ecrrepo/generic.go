package ecrrepo

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
	apiFactory func(aws.Config) RepositoryAPI
}

// NewGenericECRRepositoryDriver returns the ECR repository lifecycle
// implementation backed by the shared generic kernel.
func NewGenericECRRepositoryDriver(auth authservice.AuthClient) *kernel.Driver[ECRRepositorySpec, ECRRepositoryOutputs, ObservedState] {
	return newGenericECRRepositoryDriverWithFactory(auth, nil)
}

func newGenericECRRepositoryDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RepositoryAPI) *kernel.Driver[ECRRepositorySpec, ECRRepositoryOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) RepositoryAPI {
			return NewRepositoryAPI(awsclient.NewECRClient(cfg))
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[ECRRepositorySpec, ECRRepositoryOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec ECRRepositorySpec) (ECRRepositorySpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return ECRRepositorySpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateProvisionSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) ECRRepositorySpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ ECRRepositoryOutputs) ECRRepositoryOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired ECRRepositorySpec, outputs ECRRepositoryOutputs) (kernel.Observation[ObservedState], error) {
	name := strings.TrimSpace(outputs.RepositoryName)
	if name == "" {
		name = desired.RepositoryName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeRepository(ctx, api, name)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired ECRRepositorySpec) (kernel.CreateResult[ECRRepositoryOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[ECRRepositoryOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	observed, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (ObservedState, error) {
		created, createErr := api.CreateRepository(rc, desired)
		if IsConflict(createErr) {
			// CreateRepository is name-unique but not request-token idempotent. A
			// retry after an ambiguous response must recover the resource instead
			// of turning a completed create into a terminal conflict.
			return api.DescribeRepository(rc, desired.RepositoryName)
		}
		return created, createErr
	}, classifyRepositoryMutation)
	return kernel.CreateResult[ECRRepositoryOutputs]{SeedOutputs: outputsFromObserved(observed)}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired ECRRepositorySpec, observed ObservedState) error {
	if observed.RepositoryName != "" && desired.RepositoryName != observed.RepositoryName {
		return restate.TerminalError(fmt.Errorf(
			"repositoryName is immutable for %s: current=%s desired=%s",
			observed.RepositoryArn, observed.RepositoryName, desired.RepositoryName,
		), 409)
	}
	if !encryptionEqual(desired.EncryptionConfiguration, observed.EncryptionConfiguration) {
		return restate.TerminalError(fmt.Errorf(
			"encryptionConfiguration is immutable for %s; delete and recreate the repository to change it",
			observed.RepositoryArn,
		), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	if desired.ImageTagMutability != observed.ImageTagMutability {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateImageTagMutability(rc, desired.RepositoryName, desired.ImageTagMutability)
		}, classifyRepositoryMutation); err != nil {
			return err
		}
	}
	if !scanningEqual(desired.ImageScanningConfiguration, observed.ImageScanningConfiguration) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateScanningConfiguration(rc, desired.RepositoryName, desired.ImageScanningConfiguration)
		}, classifyRepositoryMutation); err != nil {
			return err
		}
	}
	if normalizeJSON(desired.RepositoryPolicy) != normalizeJSON(observed.RepositoryPolicy) {
		if strings.TrimSpace(desired.RepositoryPolicy) == "" {
			if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
				deleteErr := api.DeleteRepositoryPolicy(rc, desired.RepositoryName)
				if IsRepositoryPolicyNotFound(deleteErr) {
					deleteErr = nil
				}
				return restate.Void{}, deleteErr
			}, classifyRepositoryMutation); err != nil {
				return err
			}
		} else if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.PutRepositoryPolicy(rc, desired.RepositoryName, desired.RepositoryPolicy)
		}, classifyRepositoryMutation); err != nil {
			return err
		}
	}
	if !tagsEqual(desired.Tags, observed.Tags) && observed.RepositoryArn != "" {
		_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.RepositoryArn, tagsForApply(desired.Tags, desired.ManagedKey))
		}, classifyRepositoryMutation)
		return err
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired ECRRepositorySpec, outputs ECRRepositoryOutputs) error {
	name := strings.TrimSpace(outputs.RepositoryName)
	if name == "" {
		name = desired.RepositoryName
	}
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteRepository(rc, name, desired.ForceDelete)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyRepositoryMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeRepository(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeRepository(ctx restate.ObjectContext, api RepositoryAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeRepository(rc, name)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyRepositoryObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (RepositoryAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("ECRRepository driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ECR repository account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyRepositoryObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyRepositoryMutation(err error) error {
	if err == nil {
		return nil
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsConflict(err) || IsRepositoryNotEmpty(err) {
		return restate.TerminalError(err, 409)
	}
	return err
}

func applyDefaults(spec ECRRepositorySpec) ECRRepositorySpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.RepositoryName = strings.TrimSpace(spec.RepositoryName)
	spec.ImageTagMutability = strings.TrimSpace(spec.ImageTagMutability)
	if spec.ImageTagMutability == "" {
		spec.ImageTagMutability = "MUTABLE"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateProvisionSpec(spec ECRRepositorySpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.RepositoryName == "" {
		return fmt.Errorf("repositoryName is required")
	}
	if spec.EncryptionConfiguration != nil && spec.EncryptionConfiguration.EncryptionType == "KMS" && strings.TrimSpace(spec.EncryptionConfiguration.KmsKey) == "" {
		return fmt.Errorf("encryptionConfiguration.kmsKey is required when encryptionType is KMS")
	}
	return nil
}
