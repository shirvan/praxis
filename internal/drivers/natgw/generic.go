package natgw

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

const managedKeyTag = "praxis:managed-key"

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) NATGatewayAPI
}

func NewGenericNATGatewayDriver(auth authservice.AuthClient) *kernel.Driver[NATGatewaySpec, NATGatewayOutputs, ObservedState] {
	return newGenericNATGatewayDriverWithFactory(auth, nil)
}

func newGenericNATGatewayDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) NATGatewayAPI) *kernel.Driver[NATGatewaySpec, NATGatewayOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) NATGatewayAPI { return NewNATGatewayAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[NATGatewaySpec, NATGatewayOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			Readiness: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec NATGatewaySpec) (NATGatewaySpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return NATGatewaySpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return NATGatewaySpec{}, restate.TerminalError(fmt.Errorf("region %q does not match account region %q", spec.Region, region), 400)
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) NATGatewaySpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed NATGatewayOutputs) NATGatewayOutputs {
			out := outputsFromObserved(observed)
			if out.NatGatewayId == "" {
				out.NatGatewayId = seed.NatGatewayId
			}
			return out
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
		CheckReadiness: func(observed ObservedState) kernel.ReadinessResult {
			switch strings.ToLower(strings.TrimSpace(observed.State)) {
			case "available":
				return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
			case "failed":
				return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: failedStateError(observed).Error()}
			case "deleting", "deleted":
				return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: fmt.Sprintf("NAT gateway %s is %s; delete and reprovision", observed.NatGatewayId, observed.State)}
			default:
				return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: fmt.Sprintf("NAT gateway status is %s", observed.State)}
			}
		},
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired NATGatewaySpec, outputs NATGatewayOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	id := strings.TrimSpace(outputs.NatGatewayId)
	recovered := false
	if id == "" && desired.ManagedKey != "" {
		id, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyNATFind)
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		recovered = id != ""
	}
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	observation, err := observeNATGateway(ctx, api, id)
	if err != nil || !observation.Exists {
		return observation, err
	}
	owner := observation.Value.Tags[managedKeyTag]
	if owner != "" && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("NAT gateway %s is owned by Praxis object %q, not %q", id, owner, desired.ManagedKey), 409)
	}
	if recovered && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt NAT gateway %s without exact Praxis ownership tag %q", id, desired.ManagedKey), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired NATGatewaySpec) (kernel.CreateResult[NATGatewayOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[NATGatewayOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	id, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// NAT gateway creation has no client token. The ownership tag is atomic,
		// so a retry after an ambiguous response adopts the first gateway.
		existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
		if findErr != nil || existing != "" {
			return existing, findErr
		}
		return api.CreateNATGateway(rc, desired)
	}, classifyNATCreate)
	return kernel.CreateResult[NATGatewayOutputs]{SeedOutputs: NATGatewayOutputs{NatGatewayId: id}}, err
}

func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next NATGatewaySpec, _ ObservedState, currentOutputs NATGatewayOutputs) (NATGatewayOutputs, error) {
	switch {
	case previous.Account != next.Account:
		return currentOutputs, restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return currentOutputs, restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.SubnetId != next.SubnetId:
		return currentOutputs, restate.TerminalError(fmt.Errorf("subnetId is immutable; delete and reprovision to change it"), 409)
	case previous.ConnectivityType != next.ConnectivityType:
		return currentOutputs, restate.TerminalError(fmt.Errorf("connectivityType is immutable; delete and reprovision to change it"), 409)
	case previous.AllocationId != next.AllocationId:
		return currentOutputs, restate.TerminalError(fmt.Errorf("allocationId is immutable; delete and reprovision to change it"), 409)
	default:
		return currentOutputs, nil
	}
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired NATGatewaySpec, observed ObservedState, currentOutputs NATGatewayOutputs) (NATGatewayOutputs, error) {
	if err := validateNATImmutableIdentity(desired, observed); err != nil {
		return currentOutputs, restate.TerminalError(err, 409)
	}
	if drivers.TagsMatch(desired.Tags, observed.Tags) {
		return currentOutputs, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.UpdateTags(rc, observed.NatGatewayId, desired.Tags)
	}, classifyNATMutation)
	return currentOutputs, err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired NATGatewaySpec, outputs NATGatewayOutputs) error {
	id := strings.TrimSpace(outputs.NatGatewayId)
	if id == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeNATGateway(ctx, api, id)
	if err != nil || !observation.Exists {
		return err
	}
	if owner := observation.Value.Tags[managedKeyTag]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete NAT gateway %s owned by Praxis object %q", id, owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if deleteErr := api.DeleteNATGateway(rc, id); deleteErr != nil && !IsNotFound(deleteErr) {
			return restate.Void{}, deleteErr
		}
		waitErr := api.WaitUntilDeleted(rc, id)
		if IsNotFound(waitErr) {
			waitErr = nil
		}
		return restate.Void{}, waitErr
	}, classifyNATMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeNATGateway(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeNATGateway(ctx restate.ObjectContext, api NATGatewayAPI, id string) (kernel.Observation[ObservedState], error) {
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeNATGateway(rc, id)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyNATObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (NATGatewayAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("NATGateway driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve NAT gateway account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func validateNATImmutableIdentity(desired NATGatewaySpec, observed ObservedState) error {
	switch {
	case desired.SubnetId != observed.SubnetId:
		return fmt.Errorf("subnetId is immutable: observed %q, requested %q; delete and reprovision", observed.SubnetId, desired.SubnetId)
	case desired.ConnectivityType != observed.ConnectivityType:
		return fmt.Errorf("connectivityType is immutable: observed %q, requested %q; delete and reprovision", observed.ConnectivityType, desired.ConnectivityType)
	case desired.AllocationId != observed.AllocationId:
		return fmt.Errorf("allocationId is immutable: observed %q, requested %q; delete and reprovision", observed.AllocationId, desired.AllocationId)
	default:
		return nil
	}
}

func classifyNATObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParam(err) || IsSubnetNotFound(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyNATFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyNATObserve(err)
}

func classifyNATCreate(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if IsAllocationInUse(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyNATFind(err)
}

func classifyNATMutation(err error) error {
	if err == nil || IsNotFound(err) || restate.IsTerminalError(err) {
		return err
	}
	return classifyNATObserve(err)
}

func specFromObserved(observed ObservedState) NATGatewaySpec {
	return applyDefaults(NATGatewaySpec{
		SubnetId: observed.SubnetId, ConnectivityType: observed.ConnectivityType,
		AllocationId: observed.AllocationId, Tags: drivers.FilterPraxisTags(observed.Tags),
	})
}

func outputsFromObserved(observed ObservedState) NATGatewayOutputs {
	return NATGatewayOutputs{
		NatGatewayId: observed.NatGatewayId, SubnetId: observed.SubnetId, VpcId: observed.VpcId,
		ConnectivityType: observed.ConnectivityType, State: observed.State,
		PublicIp: observed.PublicIp, PrivateIp: observed.PrivateIp,
		AllocationId: observed.AllocationId, NetworkInterfaceId: observed.NetworkInterfaceId,
	}
}

func applyDefaults(spec NATGatewaySpec) NATGatewaySpec {
	if spec.ConnectivityType == "" {
		spec.ConnectivityType = "public"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec NATGatewaySpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.SubnetId == "" {
		return fmt.Errorf("subnetId is required")
	}
	if spec.ConnectivityType != "public" && spec.ConnectivityType != "private" {
		return fmt.Errorf("connectivityType must be \"public\" or \"private\"")
	}
	if spec.ConnectivityType == "public" && spec.AllocationId == "" {
		return fmt.Errorf("allocationId is required for public NAT gateways")
	}
	if spec.ConnectivityType == "private" && spec.AllocationId != "" {
		return fmt.Errorf("allocationId must be empty for private NAT gateways")
	}
	return nil
}

func failedStateError(observed ObservedState) error {
	message := fmt.Sprintf("NAT gateway %s is in failed state", observed.NatGatewayId)
	if observed.FailureCode != "" {
		message += fmt.Sprintf(" (%s)", observed.FailureCode)
	}
	if observed.FailureMessage != "" {
		message += ": " + observed.FailureMessage
	}
	return fmt.Errorf("%s", message)
}
