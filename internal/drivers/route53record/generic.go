package route53record

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
	apiFactory func(aws.Config) RecordAPI
}

// NewGenericDNSRecordDriver binds Route 53 record behavior to the shared lifecycle kernel.
func NewGenericDNSRecordDriver(auth authservice.AuthClient) *kernel.Driver[RecordSpec, RecordOutputs, ObservedState] {
	return newGenericDNSRecordDriverWithFactory(auth, nil)
}

func newGenericDNSRecordDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RecordAPI) *kernel.Driver[RecordSpec, RecordOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) RecordAPI { return NewRecordAPI(awsclient.NewRoute53Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[RecordSpec, RecordOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec RecordSpec) (RecordSpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return RecordSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.ManagedKey = restate.Key(ctx)
			normalized, err := normalizeRecordSpec(spec)
			if err != nil {
				return RecordSpec{}, restate.TerminalError(err, 400)
			}
			return normalized, nil
		},
		Validate: func(spec RecordSpec) error {
			_, err := normalizeRecordSpec(spec)
			return err
		},
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) RecordSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ RecordOutputs) RecordOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired RecordSpec, outputs RecordOutputs) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	identity := identityFromOutputs(outputs)
	if identity.HostedZoneId == "" || identity.Name == "" || identity.Type == "" {
		identity = identityFromSpec(desired)
	}
	return observeRecord(ctx, api, identity)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired RecordSpec) (kernel.CreateResult[RecordOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[RecordOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		// Route 53 UPSERT is a deterministic write to the record identity. Replaying
		// an ambiguous response cannot create a second record set.
		return restate.Void{}, api.UpsertRecord(rc, desired)
	}, classifyRecordMutation)
	if err != nil {
		return kernel.CreateResult[RecordOutputs]{}, err
	}
	// Preserve the identity even if a subsequent provider read fails.
	return kernel.CreateResult[RecordOutputs]{SeedOutputs: outputsFromSpec(desired)}, nil
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired RecordSpec, observed ObservedState, currentOutputs RecordOutputs) (RecordOutputs, error) {
	if err := validateRecordIdentity(desired, observed); err != nil {
		return currentOutputs, restate.TerminalError(err, 409)
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.UpsertRecord(rc, desired)
	}, classifyRecordMutation)
	return currentOutputs, err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired RecordSpec, outputs RecordOutputs) error {
	identity := identityFromOutputs(outputs)
	if identity.HostedZoneId == "" || identity.Name == "" || identity.Type == "" {
		return nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeRecord(ctx, api, identity)
	if err != nil || !observation.Exists {
		return err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteRecord(rc, observation.Value)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyRecordMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	identity, err := parseRecordIdentity(restate.Key(ctx))
	if err != nil {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(err, 400)
	}
	return observeRecord(ctx, api, identity)
}

func observeRecord(ctx restate.ObjectContext, api RecordAPI, identity RecordIdentity) (kernel.Observation[ObservedState], error) {
	if identity.HostedZoneId == "" || identity.Name == "" || identity.Type == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeRecord(rc, identity)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: normalizeObservedState(observed)}, nil
	}, classifyRecordObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (RecordAPI, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("Route53 record driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53 record account %q: %w", account, err)
	}
	return o.apiFactory(cfg), nil
}

func validateRecordIdentity(desired RecordSpec, observed ObservedState) error {
	desiredIdentity := identityFromSpec(desired)
	observedIdentity := RecordIdentity{HostedZoneId: observed.HostedZoneId, Name: observed.Name, Type: observed.Type, SetIdentifier: observed.SetIdentifier}
	if normalizeHostedZoneID(desiredIdentity.HostedZoneId) != normalizeHostedZoneID(observedIdentity.HostedZoneId) ||
		normalizeRecordName(desiredIdentity.Name) != normalizeRecordName(observedIdentity.Name) ||
		!strings.EqualFold(desiredIdentity.Type, observedIdentity.Type) ||
		strings.TrimSpace(desiredIdentity.SetIdentifier) != strings.TrimSpace(observedIdentity.SetIdentifier) {
		return fmt.Errorf("Route53 record identity is immutable (hostedZoneId, name, type, setIdentifier); delete and reprovision")
	}
	return nil
}

func classifyRecordObserve(err error) error {
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

func classifyRecordMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	// PriorRequestNotComplete is provider serialization and remains retryable.
	if IsConflict(err) {
		return err
	}
	return classifyRecordObserve(err)
}

func identityFromSpec(spec RecordSpec) RecordIdentity {
	return RecordIdentity{HostedZoneId: spec.HostedZoneId, Name: spec.Name, Type: spec.Type, SetIdentifier: spec.SetIdentifier}
}

func identityFromOutputs(out RecordOutputs) RecordIdentity {
	return RecordIdentity{HostedZoneId: out.HostedZoneId, Name: out.FQDN, Type: out.Type, SetIdentifier: out.SetIdentifier}
}

func parseRecordIdentity(key string) (RecordIdentity, error) {
	parts := strings.Split(key, "~")
	if len(parts) < 3 || len(parts) > 4 {
		return RecordIdentity{}, fmt.Errorf("invalid Route53 record key %q", key)
	}
	identity := RecordIdentity{HostedZoneId: normalizeHostedZoneID(parts[0]), Name: normalizeRecordName(parts[1]), Type: strings.ToUpper(strings.TrimSpace(parts[2]))}
	if len(parts) == 4 {
		identity.SetIdentifier = strings.TrimSpace(parts[3])
	}
	return identity, nil
}

func specFromObserved(observed ObservedState) RecordSpec {
	return RecordSpec{HostedZoneId: observed.HostedZoneId, Name: observed.Name, Type: observed.Type, TTL: observed.TTL, ResourceRecords: observed.ResourceRecords, AliasTarget: observed.AliasTarget, SetIdentifier: observed.SetIdentifier, Weight: observed.Weight, Region: observed.Region, Failover: observed.Failover, GeoLocation: observed.GeoLocation, MultiValueAnswer: observed.MultiValueAnswer, HealthCheckId: observed.HealthCheckId}
}

func outputsFromObserved(observed ObservedState) RecordOutputs {
	return RecordOutputs{HostedZoneId: observed.HostedZoneId, FQDN: observed.Name, Type: observed.Type, SetIdentifier: observed.SetIdentifier}
}

func outputsFromSpec(spec RecordSpec) RecordOutputs {
	return RecordOutputs{HostedZoneId: spec.HostedZoneId, FQDN: spec.Name, Type: spec.Type, SetIdentifier: spec.SetIdentifier}
}
