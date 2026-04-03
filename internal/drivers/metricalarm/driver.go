// Package metricalarm – driver.go
//
// This file implements the Restate Virtual Object handler for AWS CloudWatch Metric Alarm.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package metricalarm

import (
	"fmt"
	"slices"
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

// MetricAlarmDriver is the Restate Virtual Object handler for AWS CloudWatch Metric Alarm.
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type MetricAlarmDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) MetricAlarmAPI
}

// NewMetricAlarmDriver creates a MetricAlarm driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewMetricAlarmDriver(auth authservice.AuthClient) *MetricAlarmDriver {
	return NewMetricAlarmDriverWithFactory(auth, func(cfg aws.Config) MetricAlarmAPI {
		return NewMetricAlarmAPI(awsclient.NewCloudWatchClient(cfg))
	})
}

// NewMetricAlarmDriverWithFactory creates a MetricAlarm driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewMetricAlarmDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) MetricAlarmAPI) *MetricAlarmDriver {
	if factory == nil {
		factory = func(cfg aws.Config) MetricAlarmAPI {
			return NewMetricAlarmAPI(awsclient.NewCloudWatchClient(cfg))
		}
	}
	return &MetricAlarmDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *MetricAlarmDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or updates a AWS CloudWatch Metric Alarm. It validates the spec,
// checks for an existing resource (by ARN or name), detects immutable-field
// conflicts, and either creates a new resource or corrects drift on the
// existing one. State is persisted in Restate K/V after every step.
func (d *MetricAlarmDriver) Provision(ctx restate.ObjectContext, spec MetricAlarmSpec) (MetricAlarmOutputs, error) {
	ctx.Log().Info("provisioning CloudWatch metric alarm", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return MetricAlarmOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	if err := validateSpec(spec); err != nil {
		return MetricAlarmOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[MetricAlarmState](ctx, drivers.StateKey)
	if err != nil {
		return MetricAlarmOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.PutMetricAlarm(rc, spec)
		if runErr != nil {
			if IsInvalidParam(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			if IsLimitExceeded(runErr) {
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
		return MetricAlarmOutputs{}, err
	}

	observed, found, err := d.observeAlarm(ctx, api, spec.AlarmName)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return MetricAlarmOutputs{}, err
	}
	if !found {
		err := fmt.Errorf("alarm %s disappeared during provisioning", spec.AlarmName)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return MetricAlarmOutputs{}, err
	}
	if err := d.syncTags(ctx, api, spec, observed); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return MetricAlarmOutputs{}, err
	}
	observed, _, err = d.observeAlarm(ctx, api, spec.AlarmName)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return MetricAlarmOutputs{}, err
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import adopts an existing AWS CloudWatch Metric Alarm into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *MetricAlarmDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (MetricAlarmOutputs, error) {
	ctx.Log().Info("importing CloudWatch metric alarm", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return MetricAlarmOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[MetricAlarmState](ctx, drivers.StateKey)
	if err != nil {
		return MetricAlarmOutputs{}, err
	}
	state.Generation++
	observed, found, err := d.observeAlarm(ctx, api, ref.ResourceID)
	if err != nil {
		return MetricAlarmOutputs{}, err
	}
	if !found {
		return MetricAlarmOutputs{}, restate.TerminalError(fmt.Errorf("import failed: alarm %s does not exist", ref.ResourceID), 404)
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Delete removes the AWS CloudWatch Metric Alarm from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *MetricAlarmDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting CloudWatch metric alarm", "key", restate.Key(ctx))
	state, err := restate.Get[MetricAlarmState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete metric alarm %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.AlarmName), 409)
	}
	if state.Outputs.AlarmName == "" {
		restate.Set(ctx, drivers.StateKey, MetricAlarmState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteAlarm(rc, state.Outputs.AlarmName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidParam(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
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
	restate.Set(ctx, drivers.StateKey, MetricAlarmState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *MetricAlarmDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[MetricAlarmState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}
	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	if state.Outputs.AlarmName == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, found, err := d.observeAlarm(ctx, api, state.Outputs.AlarmName)
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if !found {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("metric alarm %s was deleted externally", state.Outputs.AlarmName)
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.correctDrift(ctx, api, state.Desired, observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		observed, found, err = d.observeAlarm(ctx, api, state.Outputs.AlarmName)
		if err == nil && found {
			state.Observed = observed
			state.Outputs = outputsFromObserved(observed)
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift}, nil
}

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *MetricAlarmDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[MetricAlarmState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *MetricAlarmDriver) GetOutputs(ctx restate.ObjectSharedContext) (MetricAlarmOutputs, error) {
	state, err := restate.Get[MetricAlarmState](ctx, drivers.StateKey)
	if err != nil {
		return MetricAlarmOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *MetricAlarmDriver) correctDrift(ctx restate.ObjectContext, api MetricAlarmAPI, desired MetricAlarmSpec, observed ObservedState) error {
	_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.PutMetricAlarm(rc, desired)
		if runErr != nil {
			if IsInvalidParam(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		return err
	}
	return d.syncTags(ctx, api, desired, observed)
}

func (d *MetricAlarmDriver) syncTags(ctx restate.ObjectContext, api MetricAlarmAPI, spec MetricAlarmSpec, observed ObservedState) error {
	toAdd, toRemove := syncTagDiff(spec.Tags, observed.Tags, spec.ManagedKey)
	if len(toRemove) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, observed.AlarmArn, toRemove)
		})
		if err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, observed.AlarmArn, toAdd)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *MetricAlarmDriver) observeAlarm(ctx restate.ObjectContext, api MetricAlarmAPI, alarmName string) (ObservedState, bool, error) {
	result, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, found, runErr := api.DescribeAlarm(rc, alarmName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return struct {
					Observed ObservedState
					Found    bool
				}{}, nil
			}
			return struct {
				Observed ObservedState
				Found    bool
			}{}, runErr
		}
		if !found {
			return struct {
				Observed ObservedState
				Found    bool
			}{}, nil
		}
		tags, tagErr := api.ListTagsForResource(rc, obs.AlarmArn)
		if tagErr != nil && !IsNotFound(tagErr) {
			return struct {
				Observed ObservedState
				Found    bool
			}{}, tagErr
		}
		obs.Tags = tags
		return struct {
			Observed ObservedState
			Found    bool
		}{Observed: normalizeObserved(obs), Found: true}, nil
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	return result.Observed, result.Found, nil
}

func (d *MetricAlarmDriver) scheduleReconcile(ctx restate.ObjectContext, state *MetricAlarmState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *MetricAlarmDriver) apiForAccount(ctx restate.ObjectContext, account string) (MetricAlarmAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("MetricAlarmDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve MetricAlarm account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func outputsFromObserved(observed ObservedState) MetricAlarmOutputs {
	return MetricAlarmOutputs{
		AlarmArn:    observed.AlarmArn,
		AlarmName:   observed.AlarmName,
		StateValue:  observed.StateValue,
		StateReason: observed.StateReason,
	}
}

func specFromObserved(observed ObservedState) MetricAlarmSpec {
	datapoints := observed.DatapointsToAlarm
	return MetricAlarmSpec{
		AlarmName:               observed.AlarmName,
		Namespace:               observed.Namespace,
		MetricName:              observed.MetricName,
		Dimensions:              observed.Dimensions,
		Statistic:               observed.Statistic,
		ExtendedStatistic:       observed.ExtendedStatistic,
		Period:                  observed.Period,
		EvaluationPeriods:       observed.EvaluationPeriods,
		DatapointsToAlarm:       &datapoints,
		Threshold:               observed.Threshold,
		ComparisonOperator:      observed.ComparisonOperator,
		TreatMissingData:        observed.TreatMissingData,
		AlarmDescription:        observed.AlarmDescription,
		ActionsEnabled:          observed.ActionsEnabled,
		AlarmActions:            append([]string(nil), observed.AlarmActions...),
		OKActions:               append([]string(nil), observed.OKActions...),
		InsufficientDataActions: append([]string(nil), observed.InsufficientDataActions...),
		Unit:                    observed.Unit,
		Tags:                    filterPraxisTags(observed.Tags),
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec MetricAlarmSpec) MetricAlarmSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.AlarmName = strings.TrimSpace(spec.AlarmName)
	spec.Namespace = strings.TrimSpace(spec.Namespace)
	spec.MetricName = strings.TrimSpace(spec.MetricName)
	spec.Statistic = strings.TrimSpace(spec.Statistic)
	spec.ExtendedStatistic = strings.TrimSpace(spec.ExtendedStatistic)
	spec.ComparisonOperator = strings.TrimSpace(spec.ComparisonOperator)
	spec.TreatMissingData = strings.TrimSpace(spec.TreatMissingData)
	spec.Unit = strings.TrimSpace(spec.Unit)
	if spec.TreatMissingData == "" {
		spec.TreatMissingData = "missing"
	}
	if spec.Dimensions == nil {
		spec.Dimensions = map[string]string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	statefulSort := func(items []string) []string {
		copyItems := append([]string(nil), items...)
		slices.Sort(copyItems)
		return copyItems
	}
	spec.AlarmActions = statefulSort(spec.AlarmActions)
	spec.OKActions = statefulSort(spec.OKActions)
	spec.InsufficientDataActions = statefulSort(spec.InsufficientDataActions)
	return spec
}

func validateSpec(spec MetricAlarmSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.AlarmName == "" {
		return fmt.Errorf("alarmName is required")
	}
	if spec.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if spec.MetricName == "" {
		return fmt.Errorf("metricName is required")
	}
	if spec.Statistic != "" && spec.ExtendedStatistic != "" {
		return fmt.Errorf("statistic and extendedStatistic are mutually exclusive")
	}
	if spec.Statistic == "" && spec.ExtendedStatistic == "" {
		return fmt.Errorf("one of statistic or extendedStatistic is required")
	}
	if spec.Period <= 0 {
		return fmt.Errorf("period must be > 0")
	}
	if spec.EvaluationPeriods <= 0 {
		return fmt.Errorf("evaluationPeriods must be > 0")
	}
	if spec.DatapointsToAlarm != nil && *spec.DatapointsToAlarm > spec.EvaluationPeriods {
		return fmt.Errorf("datapointsToAlarm cannot exceed evaluationPeriods")
	}
	if spec.ComparisonOperator == "" {
		return fmt.Errorf("comparisonOperator is required")
	}
	return nil
}
