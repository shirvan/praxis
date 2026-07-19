package vpcpeering

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
	apiFactory func(aws.Config) VPCPeeringAPI
}

func NewGenericVPCPeeringDriver(auth authservice.AuthClient) *kernel.Driver[VPCPeeringSpec, VPCPeeringOutputs, ObservedState] {
	return newGenericVPCPeeringDriverWithFactory(auth, nil)
}

func newGenericVPCPeeringDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) VPCPeeringAPI) *kernel.Driver[VPCPeeringSpec, VPCPeeringOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) VPCPeeringAPI { return NewVPCPeeringAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[VPCPeeringSpec, VPCPeeringOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			Readiness: true, ConvergeWhilePending: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec VPCPeeringSpec) (VPCPeeringSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return VPCPeeringSpec{}, drivers.ClassifyCredentialError(err)
			}
			if spec.Region == "" {
				spec.Region = region
			}
			if err := validateSpec(spec, region); err != nil {
				return VPCPeeringSpec{}, err
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: func(spec VPCPeeringSpec) error { return validateSpec(spec, spec.Region) },
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) VPCPeeringSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed VPCPeeringOutputs) VPCPeeringOutputs {
			out := outputsFromObserved(observed)
			if out.VpcPeeringConnectionId == "" {
				out.VpcPeeringConnectionId = seed.VpcPeeringConnectionId
			}
			return out
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
		CheckReadiness: func(observed ObservedState) kernel.ReadinessResult {
			switch strings.ToLower(strings.TrimSpace(observed.Status)) {
			case "active":
				return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
			case "rejected", "expired", "deleted", "deleting", "failed":
				return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: fmt.Sprintf("VPC peering connection %s is in terminal provider state %q", observed.VpcPeeringConnectionId, observed.Status)}
			default:
				return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: fmt.Sprintf("VPC peering connection status is %s", observed.Status)}
			}
		},
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired VPCPeeringSpec, outputs VPCPeeringOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	id := strings.TrimSpace(outputs.VpcPeeringConnectionId)
	recovered := false
	if id == "" && desired.ManagedKey != "" {
		id, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyVPCPeeringFind)
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		recovered = id != ""
	}
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	observation, err := observeVPCPeering(ctx, api, id)
	if err != nil || !observation.Exists {
		return observation, err
	}
	owner := observation.Value.Tags[managedKeyTag]
	if owner != "" && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("VPC peering connection %s is owned by Praxis object %q, not %q", id, owner, desired.ManagedKey), 409)
	}
	if recovered && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt VPC peering connection %s without exact Praxis ownership tag %q", id, desired.ManagedKey), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired VPCPeeringSpec) (kernel.CreateResult[VPCPeeringOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[VPCPeeringOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	id, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
		if findErr != nil || existing != "" {
			return existing, findErr
		}
		return api.CreateVPCPeeringConnection(rc, desired)
	}, classifyVPCPeeringCreate)
	return kernel.CreateResult[VPCPeeringOutputs]{SeedOutputs: VPCPeeringOutputs{VpcPeeringConnectionId: id}}, err
}

func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next VPCPeeringSpec, _ ObservedState, currentOutputs VPCPeeringOutputs) (VPCPeeringOutputs, error) {
	switch {
	case previous.Account != next.Account:
		return currentOutputs, restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return currentOutputs, restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.RequesterVpcId != next.RequesterVpcId:
		return currentOutputs, restate.TerminalError(fmt.Errorf("requesterVpcId is immutable; delete and reprovision to change it"), 409)
	case previous.AccepterVpcId != next.AccepterVpcId:
		return currentOutputs, restate.TerminalError(fmt.Errorf("accepterVpcId is immutable; delete and reprovision to change it"), 409)
	case previous.PeerOwnerId != next.PeerOwnerId:
		return currentOutputs, restate.TerminalError(fmt.Errorf("peerOwnerId is immutable; delete and reprovision to change it"), 409)
	case previous.PeerRegion != next.PeerRegion:
		return currentOutputs, restate.TerminalError(fmt.Errorf("peerRegion is immutable; delete and reprovision to change it"), 409)
	default:
		return currentOutputs, nil
	}
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired VPCPeeringSpec, observed ObservedState, currentOutputs VPCPeeringOutputs) (VPCPeeringOutputs, error) {
	if err := validateVPCPeeringImmutableIdentity(desired, observed); err != nil {
		return currentOutputs, restate.TerminalError(err, 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if observed.Status == "pending-acceptance" && desired.AutoAccept {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AcceptVPCPeeringConnection(rc, observed.VpcPeeringConnectionId)
		}, classifyVPCPeeringMutation); err != nil {
			return currentOutputs, fmt.Errorf("accept VPC peering connection: %w", err)
		}
	}
	if observed.Status == "active" && (optionsDrift(desired.RequesterOptions, observed.RequesterOptions) || optionsDrift(desired.AccepterOptions, observed.AccepterOptions)) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyPeeringOptions(rc, observed.VpcPeeringConnectionId, desired.RequesterOptions, desired.AccepterOptions)
		}, classifyVPCPeeringMutation); err != nil {
			return currentOutputs, fmt.Errorf("modify VPC peering options: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.VpcPeeringConnectionId, desired.Tags)
		}, classifyVPCPeeringMutation); err != nil {
			return currentOutputs, fmt.Errorf("update VPC peering tags: %w", err)
		}
	}
	return currentOutputs, nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired VPCPeeringSpec, outputs VPCPeeringOutputs) error {
	id := strings.TrimSpace(outputs.VpcPeeringConnectionId)
	if id == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeVPCPeering(ctx, api, id)
	if err != nil || !observation.Exists {
		return err
	}
	if owner := observation.Value.Tags[managedKeyTag]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete VPC peering connection %s owned by Praxis object %q", id, owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteVPCPeeringConnection(rc, id)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyVPCPeeringMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeVPCPeering(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeVPCPeering(ctx restate.ObjectContext, api VPCPeeringAPI, id string) (kernel.Observation[ObservedState], error) {
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeVPCPeeringConnection(rc, id)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyVPCPeeringObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (VPCPeeringAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("VPCPeering driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve VPC peering account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func validateVPCPeeringImmutableIdentity(desired VPCPeeringSpec, observed ObservedState) error {
	switch {
	case desired.RequesterVpcId != observed.RequesterVpcId:
		return fmt.Errorf("requesterVpcId is immutable: observed %q, requested %q; delete and reprovision", observed.RequesterVpcId, desired.RequesterVpcId)
	case desired.AccepterVpcId != observed.AccepterVpcId:
		return fmt.Errorf("accepterVpcId is immutable: observed %q, requested %q; delete and reprovision", observed.AccepterVpcId, desired.AccepterVpcId)
	case desired.PeerOwnerId != "" && desired.PeerOwnerId != observed.AccepterOwnerId:
		return fmt.Errorf("peerOwnerId is immutable: observed %q, requested %q; delete and reprovision", observed.AccepterOwnerId, desired.PeerOwnerId)
	default:
		return nil
	}
}

func classifyVPCPeeringObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsVpcNotFound(err) || IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyVPCPeeringFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyVPCPeeringObserve(err)
}

func classifyVPCPeeringCreate(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if IsAlreadyExists(err) || IsCidrOverlap(err) {
		return restate.TerminalError(err, 409)
	}
	if IsPeeringLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return classifyVPCPeeringFind(err)
}

func classifyVPCPeeringMutation(err error) error {
	if err == nil || IsNotFound(err) || restate.IsTerminalError(err) {
		return err
	}
	return classifyVPCPeeringObserve(err)
}

func validateSpec(spec VPCPeeringSpec, resolvedRegion string) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.RequesterVpcId) == "" {
		return fmt.Errorf("requesterVpcId is required")
	}
	if strings.TrimSpace(spec.AccepterVpcId) == "" {
		return fmt.Errorf("accepterVpcId is required")
	}
	if spec.RequesterVpcId == spec.AccepterVpcId {
		return fmt.Errorf("requesterVpcId and accepterVpcId must be different")
	}
	if spec.Region != resolvedRegion {
		return fmt.Errorf("spec.region %q does not match resolved account region %q", spec.Region, resolvedRegion)
	}
	if spec.PeerOwnerId != "" {
		return fmt.Errorf("cross-account VPC peering is not supported yet")
	}
	if spec.PeerRegion != "" && spec.PeerRegion != spec.Region {
		return fmt.Errorf("cross-region VPC peering is not supported yet")
	}
	return nil
}

func specFromObserved(observed ObservedState) VPCPeeringSpec {
	return VPCPeeringSpec{
		RequesterVpcId: observed.RequesterVpcId, AccepterVpcId: observed.AccepterVpcId,
		AutoAccept:       observed.Status == "active" || observed.Status == "pending-acceptance",
		RequesterOptions: cloneOptions(observed.RequesterOptions), AccepterOptions: cloneOptions(observed.AccepterOptions),
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) VPCPeeringOutputs {
	return VPCPeeringOutputs{
		VpcPeeringConnectionId: observed.VpcPeeringConnectionId,
		RequesterVpcId:         observed.RequesterVpcId, AccepterVpcId: observed.AccepterVpcId,
		RequesterCidrBlock: observed.RequesterCidrBlock, AccepterCidrBlock: observed.AccepterCidrBlock,
		Status: observed.Status, RequesterOwnerId: observed.RequesterOwnerId, AccepterOwnerId: observed.AccepterOwnerId,
	}
}

func cloneOptions(options *PeeringOptions) *PeeringOptions {
	if options == nil {
		return nil
	}
	clone := *options
	return &clone
}
