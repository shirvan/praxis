package ecrpolicy

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsarn "github.com/aws/aws-sdk-go-v2/aws/arn"
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
	apiFactory func(aws.Config) LifecyclePolicyAPI
}

// NewGenericECRLifecyclePolicyDriver binds ECR lifecycle policy behavior to the generic kernel.
func NewGenericECRLifecyclePolicyDriver(auth authservice.AuthClient) *kernel.Driver[ECRLifecyclePolicySpec, ECRLifecyclePolicyOutputs, ObservedState] {
	return newGenericECRLifecyclePolicyDriverWithFactory(auth, nil)
}

func newGenericECRLifecyclePolicyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LifecyclePolicyAPI) *kernel.Driver[ECRLifecyclePolicySpec, ECRLifecyclePolicyOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) LifecyclePolicyAPI { return NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[ECRLifecyclePolicySpec, ECRLifecyclePolicyOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec ECRLifecyclePolicySpec) (ECRLifecyclePolicySpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return ECRLifecyclePolicySpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.Region = strings.TrimSpace(spec.Region)
			spec.RepositoryName = strings.TrimSpace(spec.RepositoryName)
			spec.LifecyclePolicyText = strings.TrimSpace(spec.LifecyclePolicyText)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return ECRLifecyclePolicySpec{}, restate.TerminalError(fmt.Errorf(
					"region %q does not match account region %q", spec.Region, region,
				), 400)
			}
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateProvisionSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) ECRLifecyclePolicySpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ ECRLifecyclePolicyOutputs) ECRLifecyclePolicyOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired ECRLifecyclePolicySpec, outputs ECRLifecyclePolicyOutputs) (kernel.Observation[ObservedState], error) {
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
	return observeLifecyclePolicy(ctx, api, name)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired ECRLifecyclePolicySpec) (kernel.CreateResult[ECRLifecyclePolicyOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[ECRLifecyclePolicyOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.PutLifecyclePolicy(rc, desired)
	}, classifyLifecyclePolicyMutation)
	return kernel.CreateResult[ECRLifecyclePolicyOutputs]{SeedOutputs: ECRLifecyclePolicyOutputs{
		RepositoryName: desired.RepositoryName,
	}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired ECRLifecyclePolicySpec, observed ObservedState) error {
	if err := validateImmutableIdentity(desired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	if normalizePolicy(desired.LifecyclePolicyText) == normalizePolicy(observed.LifecyclePolicyText) {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.PutLifecyclePolicy(rc, desired)
	}, classifyLifecyclePolicyMutation)
	return err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired ECRLifecyclePolicySpec, outputs ECRLifecyclePolicyOutputs) error {
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
		runErr := api.DeleteLifecyclePolicy(rc, name)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyLifecyclePolicyMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	name := strings.TrimSpace(ref.ResourceID)
	if strings.HasPrefix(name, "arn:") {
		parsed, parseErr := awsarn.Parse(name)
		if parseErr != nil || parsed.Service != "ecr" || !strings.HasPrefix(parsed.Resource, "repository/") {
			return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("invalid ECR repository ARN %q", name), 400)
		}
		if region != "" && parsed.Region != region {
			return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
				"repository ARN region %q does not match account region %q", parsed.Region, region,
			), 400)
		}
		name = strings.TrimPrefix(parsed.Resource, "repository/")
	}
	return observeLifecyclePolicy(ctx, api, name)
}

func observeLifecyclePolicy(ctx restate.ObjectContext, api LifecyclePolicyAPI, repositoryName string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.GetLifecyclePolicy(rc, repositoryName)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyLifecyclePolicyObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.ObjectContext, account string) (LifecyclePolicyAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("ECRLifecyclePolicy driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ECR lifecycle policy account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyLifecyclePolicyObserve(err error) error {
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

func classifyLifecyclePolicyMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsRepositoryNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func validateProvisionSpec(spec ECRLifecyclePolicySpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.RepositoryName) == "" {
		return fmt.Errorf("repositoryName is required")
	}
	return validatePolicyJSON(spec.LifecyclePolicyText)
}

func validateImmutableIdentity(desired ECRLifecyclePolicySpec, observed ObservedState) error {
	if observed.RepositoryName != "" && desired.RepositoryName != observed.RepositoryName {
		return fmt.Errorf("repositoryName is immutable: current=%q desired=%q", observed.RepositoryName, desired.RepositoryName)
	}
	if observedRegion := regionFromRepositoryARN(observed.RepositoryArn); observedRegion != "" && observedRegion != desired.Region {
		return fmt.Errorf("region is immutable: current=%q desired=%q", observedRegion, desired.Region)
	}
	return nil
}
