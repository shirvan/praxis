package eip

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	stssdk "github.com/aws/aws-sdk-go-v2/service/sts"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

const managedKeyTag = "praxis:managed-key"

type accountIDResolver func(ctx restate.ObjectContext, cfg aws.Config) (string, error)

type genericOperations struct {
	auth             authservice.AuthClient
	apiFactory       func(aws.Config) EIPAPI
	resolveAccountID accountIDResolver
}

// NewGenericElasticIPDriver binds EC2 Elastic IP allocation semantics to the
// shared lifecycle kernel.
func NewGenericElasticIPDriver(auth authservice.AuthClient) *kernel.Driver[ElasticIPSpec, ElasticIPOutputs, ObservedState] {
	return newGenericElasticIPDriverWithFactories(auth, nil, nil)
}

func newGenericElasticIPDriverWithFactories(
	auth authservice.AuthClient,
	apiFactory func(aws.Config) EIPAPI,
	resolveAccountID accountIDResolver,
) *kernel.Driver[ElasticIPSpec, ElasticIPOutputs, ObservedState] {
	if apiFactory == nil {
		apiFactory = func(cfg aws.Config) EIPAPI { return NewEIPAPI(awsclient.NewEC2Client(cfg)) }
	}
	if resolveAccountID == nil {
		resolveAccountID = resolveAWSAccountID
	}
	ops := &genericOperations{auth: auth, apiFactory: apiFactory, resolveAccountID: resolveAccountID}
	return kernel.MustNew(kernel.Descriptor[ElasticIPSpec, ElasticIPOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec ElasticIPSpec) (ElasticIPSpec, error) {
			spec = applyDefaults(spec)
			if _, _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return ElasticIPSpec{}, drivers.ClassifyCredentialError(err)
			}
			// Ownership is derived from the Restate object identity. User tags cannot
			// replace the marker that AllocateAddress applies atomically.
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) ElasticIPSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: outputsFromObserved,
		HasDrift:            HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired ElasticIPSpec, outputs ElasticIPOutputs) (kernel.Observation[ObservedState], error) {
	api, cfg, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	allocationID := strings.TrimSpace(outputs.AllocationId)
	recoveredByManagedKey := false
	if allocationID == "" && desired.ManagedKey != "" {
		allocationID, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyEIPFind)
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		recoveredByManagedKey = allocationID != ""
	}
	if allocationID == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	observation, err := o.observeAddress(ctx, api, cfg, allocationID)
	if err != nil || !observation.Exists {
		return observation, err
	}
	if recoveredByManagedKey && observation.Value.Tags[managedKeyTag] != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"refusing to adopt elastic IP %s without exact Praxis ownership tag %q",
			allocationID, desired.ManagedKey,
		), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired ElasticIPSpec) (kernel.CreateResult[ElasticIPOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[ElasticIPOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	seed, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (ElasticIPOutputs, error) {
		// AllocateAddress has no client token. Recheck the atomically-applied
		// ownership marker inside the durable callback so a retry after an
		// ambiguous response adopts the first allocation instead of duplicating it.
		if desired.ManagedKey != "" {
			existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil || existing != "" {
				return ElasticIPOutputs{AllocationId: existing}, findErr
			}
		}
		allocationID, publicIP, allocateErr := api.AllocateAddress(rc, desired)
		return ElasticIPOutputs{AllocationId: allocationID, PublicIp: publicIP}, allocateErr
	}, classifyEIPCreate)
	return kernel.CreateResult[ElasticIPOutputs]{SeedOutputs: seed}, err
}

func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next ElasticIPSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired ElasticIPSpec, observed ObservedState) error {
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		api, _, err := o.apiForAccount(ctx, desired.Account)
		if err != nil {
			return drivers.ClassifyCredentialError(err)
		}
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.AllocationId, desired.Tags)
		}, classifyEIPMutation)
		if err != nil {
			return fmt.Errorf("update elastic IP tags: %w", err)
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired ElasticIPSpec, outputs ElasticIPOutputs) error {
	allocationID := strings.TrimSpace(outputs.AllocationId)
	if allocationID == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		releaseErr := api.ReleaseAddress(rc, allocationID)
		if IsNotFound(releaseErr) {
			releaseErr = nil
		}
		if IsAssociationExists(releaseErr) {
			releaseErr = restate.TerminalError(fmt.Errorf(
				"elastic IP %s is still associated with an instance or network interface; disassociate it before releasing",
				allocationID,
			), 409)
		}
		return restate.Void{}, releaseErr
	}, classifyEIPMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, cfg, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeAddress(ctx, api, cfg, strings.TrimSpace(ref.ResourceID))
}

func (o *genericOperations) observeAddress(ctx restate.ObjectContext, api EIPAPI, cfg aws.Config, allocationID string) (kernel.Observation[ObservedState], error) {
	observation, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, describeErr := api.DescribeAddress(rc, allocationID)
		if IsNotFound(describeErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if describeErr != nil {
			return kernel.Observation[ObservedState]{}, describeErr
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyEIPObserve)
	if err != nil || !observation.Exists {
		return observation, err
	}
	accountID, err := o.resolveAccountID(ctx, cfg)
	if err != nil {
		return kernel.Observation[ObservedState]{}, err
	}
	observation.Value.Region = cfg.Region
	observation.Value.AccountId = accountID
	return observation, nil
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (EIPAPI, aws.Config, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, aws.Config{}, fmt.Errorf("ElasticIP driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, aws.Config{}, fmt.Errorf("resolve Elastic IP account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg, nil
}

func resolveAWSAccountID(ctx restate.ObjectContext, cfg aws.Config) (string, error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		out, err := stssdk.NewFromConfig(cfg).GetCallerIdentity(rc, &stssdk.GetCallerIdentityInput{})
		if err != nil {
			return "", err
		}
		return aws.ToString(out.Account), nil
	}, classifyEIPObserve)
}

func applyDefaults(spec ElasticIPSpec) ElasticIPSpec {
	if spec.Domain == "" {
		spec.Domain = "vpc"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec ElasticIPSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Domain != "vpc" {
		return fmt.Errorf("domain must be \"vpc\"")
	}
	return nil
}

func specFromObserved(observed ObservedState) ElasticIPSpec {
	return ElasticIPSpec{
		Domain: observed.Domain, NetworkBorderGroup: observed.NetworkBorderGroup,
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState, seed ElasticIPOutputs) ElasticIPOutputs {
	allocationID := observed.AllocationId
	if allocationID == "" {
		allocationID = seed.AllocationId
	}
	publicIP := observed.PublicIp
	if publicIP == "" {
		publicIP = seed.PublicIp
	}
	arn := seed.ARN
	if observed.Region != "" && observed.AccountId != "" && allocationID != "" {
		arn = fmt.Sprintf("arn:aws:ec2:%s:%s:elastic-ip/%s", observed.Region, observed.AccountId, allocationID)
	}
	return ElasticIPOutputs{
		AllocationId: allocationID, PublicIp: publicIP, ARN: arn,
		Domain: observed.Domain, NetworkBorderGroup: observed.NetworkBorderGroup,
	}
}

func classifyEIPObserve(err error) error {
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

func classifyEIPFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyEIPObserve(err)
}

func classifyEIPCreate(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	if IsAddressLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return classifyEIPMutation(err)
}

func classifyEIPMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsAssociationExists(err) {
		return restate.TerminalError(err, 409)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}
