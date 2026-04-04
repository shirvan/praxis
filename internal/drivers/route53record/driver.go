package route53record

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// RecordDriver is the Restate virtual object that manages a single Route53 DNS record set.
type RecordDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) RecordAPI
}

func NewDNSRecordDriver(auth authservice.AuthClient) *RecordDriver {
	return NewDNSRecordDriverWithFactory(auth, func(cfg aws.Config) RecordAPI {
		return NewRecordAPI(awsclient.NewRoute53Client(cfg))
	})
}

func NewDNSRecordDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RecordAPI) *RecordDriver {
	if factory == nil {
		factory = func(cfg aws.Config) RecordAPI { return NewRecordAPI(awsclient.NewRoute53Client(cfg)) }
	}
	return &RecordDriver{auth: auth, apiFactory: factory}
}

func (d *RecordDriver) ServiceName() string {
	return ServiceName
}

// Provision implements the idempotent UPSERT-based provisioning pattern for DNS records.
// Uses the Route53 UPSERT action which creates or updates the record in a single call.
func (d *RecordDriver) Provision(ctx restate.ObjectContext, spec RecordSpec) (RecordOutputs, error) {
	ctx.Log().Info("provisioning route53 record", "key", restate.Key(ctx))
	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return RecordOutputs{}, restate.TerminalError(err, 400)
	}
	spec.ManagedKey = restate.Key(ctx)
	spec, err = normalizeRecordSpec(spec)
	if err != nil {
		return RecordOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[RecordState](ctx, drivers.StateKey)
	if err != nil {
		return RecordOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.UpsertRecord(rc, spec)
		if runErr != nil {
			if IsInvalidInput(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			if IsConflict(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return RecordOutputs{}, err
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeRecord(rc, identityFromSpec(spec))
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = outputsFromSpec(spec)
		restate.Set(ctx, drivers.StateKey, state)
		return RecordOutputs{}, err
	}
	outputs := outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *RecordDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (RecordOutputs, error) {
	ctx.Log().Info("importing route53 record", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return RecordOutputs{}, restate.TerminalError(err, 400)
	}
	identity, err := parseRecordIdentity(restate.Key(ctx))
	if err != nil {
		return RecordOutputs{}, restate.TerminalError(err, 400)
	}
	mode := defaultRecordImportMode(ref.Mode)
	state, err := restate.Get[RecordState](ctx, drivers.StateKey)
	if err != nil {
		return RecordOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRecord(rc, identity)
		if runErr != nil {
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return RecordOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.ManagedKey = restate.Key(ctx)
	outputs := outputsFromObserved(observed)
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes the DNS record from Route53 by fetching live state and issuing a DELETE action.
func (d *RecordDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting route53 record", "key", restate.Key(ctx))
	state, err := restate.Get[RecordState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete Route53 record %s %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.FQDN, state.Outputs.Type), 409)
	}
	identity, identityErr := parseRecordIdentity(restate.Key(ctx))
	if identityErr != nil {
		return restate.TerminalError(identityErr, 400)
	}
	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRecord(rc, identity)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, nil
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	if observed.Name == "" {
		restate.Set(ctx, drivers.StateKey, RecordState{Status: types.StatusDeleted})
		return nil
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteRecord(rc, observed)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidInput(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			if IsConflict(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, RecordState{Status: types.StatusDeleted})
	return nil
}

func (d *RecordDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[RecordState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}
	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	identity, err := parseRecordIdentity(restate.Key(ctx))
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	type describeResult struct {
		Observed ObservedState `json:"observed"`
		Deleted  bool          `json:"deleted"`
	}

	describe, err := restate.Run(ctx, func(rc restate.RunContext) (describeResult, error) {
		obs, runErr := api.DescribeRecord(rc, identity)
		if runErr != nil {
			if IsNotFound(runErr) {
				return describeResult{Deleted: true}, nil
			}
			return describeResult{}, runErr
		}
		return describeResult{Observed: obs}, nil
	})
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if describe.Deleted {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("record %s %s was deleted externally", identity.Name, identity.Type)
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	observed := describe.Observed
	state.Observed = observed
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		_, correctionErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpsertRecord(rc, state.Desired)
			if runErr != nil {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift, Correcting: false}, nil
}

func (d *RecordDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[RecordState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *RecordDriver) GetOutputs(ctx restate.ObjectSharedContext) (RecordOutputs, error) {
	state, err := restate.Get[RecordState](ctx, drivers.StateKey)
	if err != nil {
		return RecordOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *RecordDriver) GetInputs(ctx restate.ObjectSharedContext) (RecordSpec, error) {
	state, err := restate.Get[RecordState](ctx, drivers.StateKey)
	if err != nil {
		return RecordSpec{}, err
	}
	return state.Desired, nil
}

func (d *RecordDriver) scheduleReconcile(ctx restate.ObjectContext, state *RecordState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *RecordDriver) apiForAccount(ctx restate.ObjectContext, account string) (RecordAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("RecordDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53 record account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func identityFromSpec(spec RecordSpec) RecordIdentity {
	return RecordIdentity{HostedZoneId: spec.HostedZoneId, Name: spec.Name, Type: spec.Type, SetIdentifier: spec.SetIdentifier}
}

// parseRecordIdentity splits the Restate object key (hostedZoneId~name~type~setIdentifier)
// into its composite identity parts. The "~" delimiter separates 3 or 4 fields.
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

func defaultRecordImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
