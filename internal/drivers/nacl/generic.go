package nacl

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
	apiFactory func(aws.Config) NetworkACLAPI
}

func NewGenericNetworkACLDriver(auth authservice.AuthClient) *kernel.Driver[NetworkACLSpec, NetworkACLOutputs, ObservedState] {
	return newGenericNetworkACLDriverWithFactory(auth, nil)
}

func newGenericNetworkACLDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) NetworkACLAPI) *kernel.Driver[NetworkACLSpec, NetworkACLOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) NetworkACLAPI { return NewNetworkACLAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[NetworkACLSpec, NetworkACLOutputs, ObservedState]{
		ServiceName:  ServiceName,
		Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true},
		Operations:   ops,
		Prepare: func(ctx restate.ObjectContext, spec NetworkACLSpec) (NetworkACLSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return NetworkACLSpec{}, drivers.ClassifyCredentialError(err)
			}
			if spec.Region == "" {
				spec.Region = region
			}
			spec.ManagedKey = restate.Key(ctx)
			normalized, err := normalizeSpec(spec)
			if err != nil {
				return NetworkACLSpec{}, restate.TerminalError(err, 400)
			}
			return normalized, nil
		},
		Validate: func(spec NetworkACLSpec) error { _, err := normalizeSpec(spec); return err },
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) NetworkACLSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ NetworkACLOutputs) NetworkACLOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired NetworkACLSpec, outputs NetworkACLOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	id := strings.TrimSpace(outputs.NetworkAclId)
	recovered := false
	if id == "" && desired.ManagedKey != "" {
		id, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) { return api.FindByManagedKey(rc, desired.ManagedKey) }, classifyFind)
		if err != nil || id == "" {
			return kernel.Observation[ObservedState]{}, err
		}
		recovered = true
	}
	observation, err := observeACL(ctx, api, id)
	if err == nil && recovered && !observation.Exists {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(formatManagedKeyConflict(desired.ManagedKey, id), 409)
	}
	return observation, err
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired NetworkACLSpec) (kernel.CreateResult[NetworkACLOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[NetworkACLOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	id, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		if desired.ManagedKey != "" {
			existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil || existing != "" {
				return existing, findErr
			}
		}
		return api.CreateNetworkACL(rc, desired)
	}, classifyCreate)
	return kernel.CreateResult[NetworkACLOutputs]{SeedOutputs: NetworkACLOutputs{NetworkAclId: id, VpcId: desired.VpcId}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired NetworkACLSpec, observed ObservedState) error {
	if desired.VpcId != observed.VpcId {
		return restate.TerminalError(fmt.Errorf("network ACL vpcId is immutable: observed %q, requested %q; delete and reprovision", observed.VpcId, desired.VpcId), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	// The convergence helpers contain only provider-specific rule and association
	// sequencing; lifecycle state remains owned by the generic kernel.
	return o.applyDesiredState(ctx, api, observed.NetworkAclId, desired, observed)
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired NetworkACLSpec, outputs NetworkACLOutputs) error {
	id := strings.TrimSpace(outputs.NetworkAclId)
	if id == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	obs, err := observeACL(ctx, api, id)
	if err != nil || !obs.Exists {
		return err
	}
	if obs.Value.IsDefault {
		return restate.TerminalError(fmt.Errorf("cannot delete default network ACL %s", id), 409)
	}
	if len(obs.Value.Associations) > 0 {
		defaultID, findErr := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindDefaultNetworkACL(rc, obs.Value.VpcId)
		}, classifyMutation)
		if findErr != nil {
			return fmt.Errorf("find default network ACL for VPC %s: %w", obs.Value.VpcId, findErr)
		}
		for _, assoc := range obs.Value.Associations {
			if _, replaceErr := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
				return api.ReplaceNetworkACLAssociation(rc, assoc.AssociationId, defaultID)
			}, classifyMutation); replaceErr != nil {
				return fmt.Errorf("reassociate subnet %s to default network ACL: %w", assoc.SubnetId, replaceErr)
			}
		}
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteNetworkACL(rc, id)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyDelete)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeACL(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeACL(ctx restate.ObjectContext, api NetworkACLAPI, id string) (kernel.Observation[ObservedState], error) {
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		obs, err := api.DescribeNetworkACL(rc, id)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: obs}, nil
	}, classifyObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (NetworkACLAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("NetworkACL driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve network ACL account %q: %w", account, err)
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
func classifyFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyObserve(err)
}
func classifyCreate(err error) error {
	if err == nil {
		return nil
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return classifyMutation(err)
}
func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	if IsNotFound(err) || IsRuleNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsDuplicateRule(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyObserve(err)
}
func classifyDelete(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if IsDefaultACL(err) || IsInUse(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyMutation(err)
}
