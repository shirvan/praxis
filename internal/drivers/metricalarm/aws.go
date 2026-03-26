package metricalarm

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cloudwatch "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type MetricAlarmAPI interface {
	PutMetricAlarm(ctx context.Context, spec MetricAlarmSpec) error
	DescribeAlarm(ctx context.Context, alarmName string) (ObservedState, bool, error)
	DeleteAlarm(ctx context.Context, alarmName string) error
	TagResource(ctx context.Context, alarmArn string, tags map[string]string) error
	UntagResource(ctx context.Context, alarmArn string, tagKeys []string) error
	ListTagsForResource(ctx context.Context, alarmArn string) (map[string]string, error)
}

type realMetricAlarmAPI struct {
	client  *cloudwatch.Client
	limiter *ratelimit.Limiter
}

func NewMetricAlarmAPI(client *cloudwatch.Client) MetricAlarmAPI {
	return &realMetricAlarmAPI{
		client:  client,
		limiter: ratelimit.New("cloudwatch-metric-alarm", 20, 10),
	}
}

func (r *realMetricAlarmAPI) PutMetricAlarm(ctx context.Context, spec MetricAlarmSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(spec.AlarmName),
		Namespace:          aws.String(spec.Namespace),
		MetricName:         aws.String(spec.MetricName),
		Period:             aws.Int32(spec.Period),
		EvaluationPeriods:  aws.Int32(spec.EvaluationPeriods),
		Threshold:          aws.Float64(spec.Threshold),
		ComparisonOperator: cwtypes.ComparisonOperator(spec.ComparisonOperator),
		ActionsEnabled:     aws.Bool(spec.ActionsEnabled),
	}
	if spec.Statistic != "" {
		input.Statistic = cwtypes.Statistic(spec.Statistic)
	}
	if spec.ExtendedStatistic != "" {
		input.ExtendedStatistic = aws.String(spec.ExtendedStatistic)
	}
	if len(spec.Dimensions) > 0 {
		input.Dimensions = toDimensionList(spec.Dimensions)
	}
	if spec.DatapointsToAlarm != nil {
		input.DatapointsToAlarm = spec.DatapointsToAlarm
	}
	if spec.TreatMissingData != "" {
		input.TreatMissingData = aws.String(spec.TreatMissingData)
	}
	if spec.AlarmDescription != "" {
		input.AlarmDescription = aws.String(spec.AlarmDescription)
	}
	if len(spec.AlarmActions) > 0 {
		input.AlarmActions = append([]string(nil), spec.AlarmActions...)
	}
	if len(spec.OKActions) > 0 {
		input.OKActions = append([]string(nil), spec.OKActions...)
	}
	if len(spec.InsufficientDataActions) > 0 {
		input.InsufficientDataActions = append([]string(nil), spec.InsufficientDataActions...)
	}
	if spec.Unit != "" {
		input.Unit = cwtypes.StandardUnit(spec.Unit)
	}
	_, err := r.client.PutMetricAlarm(ctx, input)
	return err
}

func (r *realMetricAlarmAPI) DescribeAlarm(ctx context.Context, alarmName string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	out, err := r.client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{alarmName},
		AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeMetricAlarm},
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	if len(out.MetricAlarms) == 0 {
		return ObservedState{}, false, nil
	}
	alarm := out.MetricAlarms[0]
	observed := ObservedState{
		AlarmArn:                aws.ToString(alarm.AlarmArn),
		AlarmName:               aws.ToString(alarm.AlarmName),
		Namespace:               aws.ToString(alarm.Namespace),
		MetricName:              aws.ToString(alarm.MetricName),
		Dimensions:              fromDimensionList(alarm.Dimensions),
		Statistic:               string(alarm.Statistic),
		ExtendedStatistic:       aws.ToString(alarm.ExtendedStatistic),
		Period:                  aws.ToInt32(alarm.Period),
		EvaluationPeriods:       aws.ToInt32(alarm.EvaluationPeriods),
		DatapointsToAlarm:       aws.ToInt32(alarm.DatapointsToAlarm),
		Threshold:               aws.ToFloat64(alarm.Threshold),
		ComparisonOperator:      string(alarm.ComparisonOperator),
		TreatMissingData:        aws.ToString(alarm.TreatMissingData),
		AlarmDescription:        aws.ToString(alarm.AlarmDescription),
		ActionsEnabled:          aws.ToBool(alarm.ActionsEnabled),
		AlarmActions:            append([]string(nil), alarm.AlarmActions...),
		OKActions:               append([]string(nil), alarm.OKActions...),
		InsufficientDataActions: append([]string(nil), alarm.InsufficientDataActions...),
		Unit:                    string(alarm.Unit),
		StateValue:              string(alarm.StateValue),
		StateReason:             aws.ToString(alarm.StateReason),
		Tags:                    map[string]string{},
	}
	return normalizeObserved(observed), true, nil
}

func (r *realMetricAlarmAPI) DeleteAlarm(ctx context.Context, alarmName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarmName}})
	return err
}

func (r *realMetricAlarmAPI) TagResource(ctx context.Context, alarmArn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TagResource(ctx, &cloudwatch.TagResourceInput{
		ResourceARN: aws.String(alarmArn),
		Tags:        toTagList(tags),
	})
	return err
}

func (r *realMetricAlarmAPI) UntagResource(ctx context.Context, alarmArn string, tagKeys []string) error {
	if len(tagKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UntagResource(ctx, &cloudwatch.UntagResourceInput{
		ResourceARN: aws.String(alarmArn),
		TagKeys:     append([]string(nil), tagKeys...),
	})
	return err
}

func (r *realMetricAlarmAPI) ListTagsForResource(ctx context.Context, alarmArn string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: aws.String(alarmArn)})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string, len(out.Tags))
	for _, tag := range out.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags, nil
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFound")
}

func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination")
}

func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "LimitExceeded")
}

func toDimensionList(dimensions map[string]string) []cwtypes.Dimension {
	keys := make([]string, 0, len(dimensions))
	for key := range dimensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]cwtypes.Dimension, 0, len(keys))
	for _, key := range keys {
		out = append(out, cwtypes.Dimension{Name: aws.String(key), Value: aws.String(dimensions[key])})
	}
	return out
}

func fromDimensionList(dimensions []cwtypes.Dimension) map[string]string {
	if len(dimensions) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(dimensions))
	for _, dimension := range dimensions {
		out[aws.ToString(dimension.Name)] = aws.ToString(dimension.Value)
	}
	return out
}

func toTagList(tags map[string]string) []cwtypes.Tag {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]cwtypes.Tag, 0, len(keys))
	for _, key := range keys {
		out = append(out, cwtypes.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	return out
}

func syncTagDiff(desired, observed map[string]string, managedKey string) (map[string]string, []string) {
	want := managedTags(desired, managedKey)
	have := managedTags(observed, managedKey)
	toAdd := map[string]string{}
	for key, value := range want {
		if current, ok := have[key]; !ok || current != value {
			toAdd[key] = value
		}
	}
	var toRemove []string
	for key := range have {
		if _, ok := want[key]; !ok {
			toRemove = append(toRemove, key)
		}
	}
	sort.Strings(toRemove)
	return toAdd, toRemove
}

func managedTags(tags map[string]string, managedKey string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, filterPraxisTags(tags))
	if strings.TrimSpace(managedKey) != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

func normalizeObserved(observed ObservedState) ObservedState {
	if observed.Dimensions == nil {
		observed.Dimensions = map[string]string{}
	}
	if observed.Tags == nil {
		observed.Tags = map[string]string{}
	}
	slices.Sort(observed.AlarmActions)
	slices.Sort(observed.OKActions)
	slices.Sort(observed.InsufficientDataActions)
	return observed
}
