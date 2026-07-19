package metricalarm

import (
	"fmt"
	"slices"
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
	apiFactory func(aws.Config) MetricAlarmAPI
}

// NewGenericMetricAlarmDriver returns the CloudWatch metric alarm lifecycle
// implementation backed by the shared generic kernel.
func NewGenericMetricAlarmDriver(auth authservice.AuthClient) *kernel.Driver[MetricAlarmSpec, MetricAlarmOutputs, ObservedState] {
	return newGenericMetricAlarmDriverWithFactory(auth, nil)
}

func newGenericMetricAlarmDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) MetricAlarmAPI) *kernel.Driver[MetricAlarmSpec, MetricAlarmOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) MetricAlarmAPI {
			return NewMetricAlarmAPI(awsclient.NewCloudWatchClient(cfg))
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[MetricAlarmSpec, MetricAlarmOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec MetricAlarmSpec) (MetricAlarmSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return MetricAlarmSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) MetricAlarmSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ MetricAlarmOutputs) MetricAlarmOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired MetricAlarmSpec, outputs MetricAlarmOutputs) (kernel.Observation[ObservedState], error) {
	name := strings.TrimSpace(outputs.AlarmName)
	if name == "" {
		name = desired.AlarmName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeAlarm(ctx, api, name)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired MetricAlarmSpec) (kernel.CreateResult[MetricAlarmOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[MetricAlarmOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.PutMetricAlarm(rc, desired)
	}, classifyAlarmMutation)
	return kernel.CreateResult[MetricAlarmOutputs]{
		SeedOutputs: MetricAlarmOutputs{AlarmName: desired.AlarmName},
	}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired MetricAlarmSpec, observed ObservedState) error {
	if observed.AlarmName != "" && desired.AlarmName != observed.AlarmName {
		return restate.TerminalError(fmt.Errorf(
			"alarmName is immutable for %s: current=%s desired=%s",
			observed.AlarmArn, observed.AlarmName, desired.AlarmName,
		), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	if hasConfigurationDrift(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.PutMetricAlarm(rc, desired)
		}, classifyAlarmMutation); err != nil {
			return err
		}
	}

	toAdd, toRemove := syncTagDiff(desired.Tags, observed.Tags, desired.ManagedKey)
	if len(toRemove) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, observed.AlarmArn, toRemove)
		}, classifyAlarmMutation); err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, observed.AlarmArn, toAdd)
		}, classifyAlarmMutation); err != nil {
			return err
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired MetricAlarmSpec, outputs MetricAlarmOutputs) error {
	name := strings.TrimSpace(outputs.AlarmName)
	if name == "" {
		name = desired.AlarmName
	}
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteAlarm(rc, name)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyAlarmMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeAlarm(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeAlarm(ctx restate.ObjectContext, api MetricAlarmAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, err := api.DescribeAlarm(rc, name)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil || !found {
			return kernel.Observation[ObservedState]{}, err
		}
		if observed.AlarmArn != "" {
			tags, tagErr := api.ListTagsForResource(rc, observed.AlarmArn)
			if tagErr != nil && !IsNotFound(tagErr) {
				return kernel.Observation[ObservedState]{}, tagErr
			}
			observed.Tags = tags
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: normalizeObserved(observed)}, nil
	}, classifyAlarmObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (MetricAlarmAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("MetricAlarm driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve MetricAlarm account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyAlarmObserve(err error) error {
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

func classifyAlarmMutation(err error) error {
	if err == nil {
		return nil
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}

func hasConfigurationDrift(desired MetricAlarmSpec, observed ObservedState) bool {
	copyDesired := desired
	copyDesired.Tags = drivers.FilterPraxisTags(observed.Tags)
	return HasDrift(copyDesired, observed)
}

func outputsFromObserved(observed ObservedState) MetricAlarmOutputs {
	return MetricAlarmOutputs{
		AlarmArn: observed.AlarmArn, AlarmName: observed.AlarmName,
		StateValue: observed.StateValue, StateReason: observed.StateReason,
	}
}

func specFromObserved(observed ObservedState) MetricAlarmSpec {
	var datapoints *int32
	if observed.DatapointsToAlarm > 0 {
		value := observed.DatapointsToAlarm
		datapoints = &value
	}
	return MetricAlarmSpec{
		AlarmName: observed.AlarmName, Namespace: observed.Namespace, MetricName: observed.MetricName,
		Dimensions: observed.Dimensions, Statistic: observed.Statistic, ExtendedStatistic: observed.ExtendedStatistic,
		Period: observed.Period, EvaluationPeriods: observed.EvaluationPeriods, DatapointsToAlarm: datapoints,
		Threshold: observed.Threshold, ComparisonOperator: observed.ComparisonOperator,
		TreatMissingData: observed.TreatMissingData, AlarmDescription: observed.AlarmDescription,
		ActionsEnabled: observed.ActionsEnabled, AlarmActions: slices.Clone(observed.AlarmActions),
		OKActions: slices.Clone(observed.OKActions), InsufficientDataActions: slices.Clone(observed.InsufficientDataActions),
		Unit: observed.Unit, Tags: drivers.FilterPraxisTags(observed.Tags),
	}
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
	spec.AlarmActions = sortedCopy(spec.AlarmActions)
	spec.OKActions = sortedCopy(spec.OKActions)
	spec.InsufficientDataActions = sortedCopy(spec.InsufficientDataActions)
	return spec
}

func sortedCopy(items []string) []string {
	items = slices.Clone(items)
	slices.Sort(items)
	return items
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
	if spec.DatapointsToAlarm != nil && *spec.DatapointsToAlarm < 1 {
		return fmt.Errorf("datapointsToAlarm must be >= 1")
	}
	if spec.ComparisonOperator == "" {
		return fmt.Errorf("comparisonOperator is required")
	}
	return nil
}
