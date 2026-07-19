package route53zone

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
	apiFactory func(aws.Config) HostedZoneAPI
}

// NewGenericHostedZoneDriver binds Route 53 hosted-zone behavior to the shared lifecycle kernel.
func NewGenericHostedZoneDriver(auth authservice.AuthClient) *kernel.Driver[HostedZoneSpec, HostedZoneOutputs, ObservedState] {
	return newGenericHostedZoneDriverWithFactory(auth, nil)
}

func newGenericHostedZoneDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) HostedZoneAPI) *kernel.Driver[HostedZoneSpec, HostedZoneOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) HostedZoneAPI { return NewHostedZoneAPI(awsclient.NewRoute53Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[HostedZoneSpec, HostedZoneOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec HostedZoneSpec) (HostedZoneSpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return HostedZoneSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.Name = restate.Key(ctx)
			spec.ManagedKey = restate.Key(ctx)
			normalized, err := normalizeHostedZoneSpec(spec)
			if err != nil {
				return HostedZoneSpec{}, restate.TerminalError(err, 400)
			}
			return normalized, nil
		},
		Validate: func(spec HostedZoneSpec) error {
			_, err := normalizeHostedZoneSpec(spec)
			return err
		},
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) HostedZoneSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ HostedZoneOutputs) HostedZoneOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired HostedZoneSpec, outputs HostedZoneOutputs) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	zoneID := normalizeHostedZoneID(outputs.HostedZoneId)
	if zoneID == "" && desired.Name != "" {
		zoneID, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindHostedZoneByName(rc, desired.Name)
		}, classifyHostedZoneFind)
		if err != nil || zoneID == "" {
			return kernel.Observation[ObservedState]{}, err
		}
	}
	observation, err := observeHostedZone(ctx, api, zoneID)
	if err != nil || !observation.Exists {
		return observation, err
	}
	if outputs.HostedZoneId == "" && observation.Value.CallerReference != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"hosted zone %q already exists without exact Praxis ownership (callerReference %q, expected %q)",
			desired.Name, observation.Value.CallerReference, desired.ManagedKey,
		), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired HostedZoneSpec) (kernel.CreateResult[HostedZoneOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[HostedZoneOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	zoneID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// CallerReference is Route 53's native idempotency token. A replay of this
		// durable callback therefore returns the original zone rather than creating
		// a second one.
		return api.CreateHostedZone(rc, desired)
	}, classifyHostedZoneCreate)
	return kernel.CreateResult[HostedZoneOutputs]{SeedOutputs: HostedZoneOutputs{HostedZoneId: normalizeHostedZoneID(zoneID)}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired HostedZoneSpec, observed ObservedState) error {
	if normalizeZoneName(desired.Name) != normalizeZoneName(observed.Name) {
		return restate.TerminalError(fmt.Errorf("hosted zone name is immutable: observed %q, requested %q; delete and reprovision", observed.Name, desired.Name), 409)
	}
	if desired.IsPrivate != observed.IsPrivate {
		return restate.TerminalError(fmt.Errorf("hosted zone isPrivate is immutable: observed %t, requested %t; delete and reprovision", observed.IsPrivate, desired.IsPrivate), 409)
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	zoneID := observed.HostedZoneId
	if normalizeZoneComment(desired.Comment) != normalizeZoneComment(observed.Comment) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateComment(rc, zoneID, desired.Comment)
		}, classifyHostedZoneMutation); err != nil {
			return fmt.Errorf("update hosted zone comment: %w", err)
		}
	}
	if desired.IsPrivate {
		desiredSet := make(map[string]HostedZoneVPC, len(desired.VPCs))
		for _, vpc := range normalizeHostedZoneVPCs(desired.VPCs) {
			desiredSet[hostedZoneVPCKey(vpc)] = vpc
		}
		observedSet := make(map[string]HostedZoneVPC, len(observed.VPCs))
		for _, vpc := range normalizeHostedZoneVPCs(observed.VPCs) {
			observedSet[hostedZoneVPCKey(vpc)] = vpc
		}
		for key, vpc := range desiredSet {
			if _, ok := observedSet[key]; ok {
				continue
			}
			if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.AssociateVPC(rc, zoneID, vpc)
			}, classifyHostedZoneMutation); err != nil {
				return fmt.Errorf("associate hosted zone VPC %s: %w", key, err)
			}
		}
		for key, vpc := range observedSet {
			if _, ok := desiredSet[key]; ok {
				continue
			}
			if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.DisassociateVPC(rc, zoneID, vpc)
			}, classifyHostedZoneMutation); err != nil {
				return fmt.Errorf("disassociate hosted zone VPC %s: %w", key, err)
			}
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, zoneID, desired.Tags)
		}, classifyHostedZoneMutation); err != nil {
			return fmt.Errorf("update hosted zone tags: %w", err)
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired HostedZoneSpec, outputs HostedZoneOutputs) error {
	zoneID := normalizeHostedZoneID(outputs.HostedZoneId)
	if zoneID == "" {
		return nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteHostedZone(rc, zoneID)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyHostedZoneDelete)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeHostedZone(ctx, api, normalizeHostedZoneID(ref.ResourceID))
}

func observeHostedZone(ctx restate.ObjectContext, api HostedZoneAPI, zoneID string) (kernel.Observation[ObservedState], error) {
	if zoneID == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeHostedZone(rc, zoneID)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: normalizeObservedState(observed)}, nil
	}, classifyHostedZoneObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (HostedZoneAPI, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("Route53 hosted-zone driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53 hosted-zone account %q: %w", account, err)
	}
	return o.apiFactory(cfg), nil
}

func classifyHostedZoneObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidInput(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyHostedZoneFind(err error) error {
	if err != nil && (strings.Contains(err.Error(), "multiple hosted zones") || strings.Contains(err.Error(), "ambiguous lookup")) {
		return restate.TerminalError(err, 409)
	}
	return classifyHostedZoneObserve(err)
}

func classifyHostedZoneCreate(err error) error {
	if err == nil {
		return nil
	}
	if IsAlreadyExists(err) || IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyHostedZoneObserve(err)
}

func classifyHostedZoneMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsConflict(err) {
		// PriorRequestNotComplete is provider serialization and must remain retryable.
		if awserr.HasCode(err, "PriorRequestNotComplete") {
			return err
		}
		return restate.TerminalError(err, 409)
	}
	return classifyHostedZoneObserve(err)
}

func classifyHostedZoneDelete(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if IsNotEmpty(err) {
		return restate.TerminalError(fmt.Errorf("hosted zone is not empty; delete DNS records before deleting the zone: %w", err), 409)
	}
	return classifyHostedZoneMutation(err)
}

func specFromObserved(observed ObservedState) HostedZoneSpec {
	return HostedZoneSpec{
		Name:      observed.Name,
		Comment:   observed.Comment,
		IsPrivate: observed.IsPrivate,
		VPCs:      observed.VPCs,
		Tags:      drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) HostedZoneOutputs {
	return HostedZoneOutputs{
		HostedZoneId: observed.HostedZoneId,
		Name:         observed.Name,
		NameServers:  observed.NameServers,
		IsPrivate:    observed.IsPrivate,
		RecordCount:  observed.RecordCount,
	}
}
