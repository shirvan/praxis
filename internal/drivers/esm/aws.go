package esm

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type ESMAPI interface {
	CreateEventSourceMapping(ctx context.Context, spec EventSourceMappingSpec) (EventSourceMappingOutputs, error)
	GetEventSourceMapping(ctx context.Context, uuid string) (ObservedState, error)
	FindEventSourceMapping(ctx context.Context, functionName, eventSourceArn string) (string, error)
	UpdateEventSourceMapping(ctx context.Context, uuid string, spec EventSourceMappingSpec) error
	DeleteEventSourceMapping(ctx context.Context, uuid string) error
	WaitForStableState(ctx context.Context, uuid string) (string, error)
}

type realESMAPI struct {
	client  *lambdasdk.Client
	limiter *ratelimit.Limiter
}

func NewESMAPI(client *lambdasdk.Client) ESMAPI {
	return &realESMAPI{client: client, limiter: ratelimit.New("lambda-esm", 15, 10)}
}

func (r *realESMAPI) CreateEventSourceMapping(ctx context.Context, spec EventSourceMappingSpec) (EventSourceMappingOutputs, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return EventSourceMappingOutputs{}, err
	}
	input := &lambdasdk.CreateEventSourceMappingInput{FunctionName: aws.String(spec.FunctionName), EventSourceArn: aws.String(spec.EventSourceArn), Enabled: aws.Bool(spec.Enabled)}
	applyCreateInput(input, spec)
	out, err := r.client.CreateEventSourceMapping(ctx, input)
	if err != nil {
		return EventSourceMappingOutputs{}, err
	}
	return EventSourceMappingOutputs{
		UUID:           aws.ToString(out.UUID),
		EventSourceArn: aws.ToString(out.EventSourceArn),
		FunctionArn:    aws.ToString(out.FunctionArn),
		State:          aws.ToString(out.State),
		LastModified:   timeString(out.LastModified),
		BatchSize:      aws.ToInt32(out.BatchSize),
	}, nil
}

func (r *realESMAPI) GetEventSourceMapping(ctx context.Context, uuid string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetEventSourceMapping(ctx, &lambdasdk.GetEventSourceMappingInput{UUID: aws.String(uuid)})
	if err != nil {
		return ObservedState{}, err
	}
	return observedFromMapping(out), nil
}

func (r *realESMAPI) FindEventSourceMapping(ctx context.Context, functionName, eventSourceArn string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.ListEventSourceMappings(ctx, &lambdasdk.ListEventSourceMappingsInput{FunctionName: aws.String(functionName), EventSourceArn: aws.String(eventSourceArn)})
	if err != nil {
		return "", err
	}
	for i := range out.EventSourceMappings {
		if aws.ToString(out.EventSourceMappings[i].EventSourceArn) == eventSourceArn {
			return aws.ToString(out.EventSourceMappings[i].UUID), nil
		}
	}
	return "", nil
}

func (r *realESMAPI) UpdateEventSourceMapping(ctx context.Context, uuid string, spec EventSourceMappingSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &lambdasdk.UpdateEventSourceMappingInput{UUID: aws.String(uuid), Enabled: aws.Bool(spec.Enabled), FunctionName: aws.String(spec.FunctionName)}
	applyUpdateInput(input, spec)
	_, err := r.client.UpdateEventSourceMapping(ctx, input)
	return err
}

func (r *realESMAPI) DeleteEventSourceMapping(ctx context.Context, uuid string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteEventSourceMapping(ctx, &lambdasdk.DeleteEventSourceMappingInput{UUID: aws.String(uuid)})
	return err
}

func (r *realESMAPI) WaitForStableState(ctx context.Context, uuid string) (string, error) {
	deadline := time.NewTimer(3 * time.Minute)
	ticker := time.NewTicker(5 * time.Second)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("timeout waiting for event source mapping %s to stabilize", uuid)
		case <-ticker.C:
			observed, err := r.GetEventSourceMapping(ctx, uuid)
			if err != nil {
				if IsNotFound(err) {
					return "Deleted", nil
				}
				return "", err
			}
			switch observed.State {
			case "Enabled", "Disabled", "Deleted":
				return observed.State, nil
			case "Creating", "Enabling", "Disabling", "Updating", "Deleting":
				continue
			default:
				return observed.State, nil
			}
		}
	}
}

func applyCreateInput(input *lambdasdk.CreateEventSourceMappingInput, spec EventSourceMappingSpec) {
	if spec.BatchSize != nil {
		input.BatchSize = spec.BatchSize
	}
	if spec.MaximumBatchingWindowInSeconds != nil {
		input.MaximumBatchingWindowInSeconds = spec.MaximumBatchingWindowInSeconds
	}
	if spec.StartingPosition != "" {
		input.StartingPosition = lambdatypes.EventSourcePosition(spec.StartingPosition)
	}
	if spec.StartingPositionTimestamp != nil {
		if parsed, err := time.Parse(time.RFC3339, *spec.StartingPositionTimestamp); err == nil {
			input.StartingPositionTimestamp = aws.Time(parsed)
		}
	}
	if spec.FilterCriteria != nil {
		input.FilterCriteria = buildFilterCriteria(spec.FilterCriteria)
	}
	if spec.BisectBatchOnFunctionError != nil {
		input.BisectBatchOnFunctionError = spec.BisectBatchOnFunctionError
	}
	if spec.MaximumRetryAttempts != nil {
		input.MaximumRetryAttempts = spec.MaximumRetryAttempts
	}
	if spec.MaximumRecordAgeInSeconds != nil {
		input.MaximumRecordAgeInSeconds = spec.MaximumRecordAgeInSeconds
	}
	if spec.ParallelizationFactor != nil {
		input.ParallelizationFactor = spec.ParallelizationFactor
	}
	if spec.TumblingWindowInSeconds != nil {
		input.TumblingWindowInSeconds = spec.TumblingWindowInSeconds
	}
	if spec.DestinationConfig != nil {
		input.DestinationConfig = buildDestinationConfig(spec.DestinationConfig)
	}
	if spec.ScalingConfig != nil {
		input.ScalingConfig = &lambdatypes.ScalingConfig{MaximumConcurrency: aws.Int32(spec.ScalingConfig.MaximumConcurrency)}
	}
	if len(spec.FunctionResponseTypes) > 0 {
		input.FunctionResponseTypes = toFunctionResponseTypes(spec.FunctionResponseTypes)
	}
}

func applyUpdateInput(input *lambdasdk.UpdateEventSourceMappingInput, spec EventSourceMappingSpec) {
	if spec.BatchSize != nil {
		input.BatchSize = spec.BatchSize
	}
	if spec.MaximumBatchingWindowInSeconds != nil {
		input.MaximumBatchingWindowInSeconds = spec.MaximumBatchingWindowInSeconds
	}
	if spec.FilterCriteria != nil {
		input.FilterCriteria = buildFilterCriteria(spec.FilterCriteria)
	}
	if spec.BisectBatchOnFunctionError != nil {
		input.BisectBatchOnFunctionError = spec.BisectBatchOnFunctionError
	}
	if spec.MaximumRetryAttempts != nil {
		input.MaximumRetryAttempts = spec.MaximumRetryAttempts
	}
	if spec.MaximumRecordAgeInSeconds != nil {
		input.MaximumRecordAgeInSeconds = spec.MaximumRecordAgeInSeconds
	}
	if spec.ParallelizationFactor != nil {
		input.ParallelizationFactor = spec.ParallelizationFactor
	}
	if spec.TumblingWindowInSeconds != nil {
		input.TumblingWindowInSeconds = spec.TumblingWindowInSeconds
	}
	if spec.DestinationConfig != nil {
		input.DestinationConfig = buildDestinationConfig(spec.DestinationConfig)
	}
	if spec.ScalingConfig != nil {
		input.ScalingConfig = &lambdatypes.ScalingConfig{MaximumConcurrency: aws.Int32(spec.ScalingConfig.MaximumConcurrency)}
	}
	if len(spec.FunctionResponseTypes) > 0 {
		input.FunctionResponseTypes = toFunctionResponseTypes(spec.FunctionResponseTypes)
	}
}

func buildFilterCriteria(spec *FilterCriteriaSpec) *lambdatypes.FilterCriteria {
	if spec == nil {
		return nil
	}
	filters := make([]lambdatypes.Filter, 0, len(spec.Filters))
	for _, filter := range spec.Filters {
		filters = append(filters, lambdatypes.Filter{Pattern: aws.String(filter.Pattern)})
	}
	return &lambdatypes.FilterCriteria{Filters: filters}
}

func buildDestinationConfig(spec *DestinationSpec) *lambdatypes.DestinationConfig {
	if spec == nil {
		return nil
	}
	return &lambdatypes.DestinationConfig{OnFailure: &lambdatypes.OnFailure{Destination: aws.String(spec.OnFailure.DestinationArn)}}
}

func toFunctionResponseTypes(values []string) []lambdatypes.FunctionResponseType {
	result := make([]lambdatypes.FunctionResponseType, 0, len(values))
	for _, value := range values {
		result = append(result, lambdatypes.FunctionResponseType(value))
	}
	return result
}

func fromFunctionResponseTypes(values []lambdatypes.FunctionResponseType) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	slices.Sort(result)
	return result
}

func observedFromMapping(out *lambdasdk.GetEventSourceMappingOutput) ObservedState {
	return ObservedState{
		UUID:                           aws.ToString(out.UUID),
		EventSourceArn:                 aws.ToString(out.EventSourceArn),
		FunctionArn:                    aws.ToString(out.FunctionArn),
		State:                          aws.ToString(out.State),
		BatchSize:                      aws.ToInt32(out.BatchSize),
		MaximumBatchingWindowInSeconds: aws.ToInt32(out.MaximumBatchingWindowInSeconds),
		StartingPosition:               string(out.StartingPosition),
		FilterCriteria:                 filterCriteriaFromAWS(out.FilterCriteria),
		BisectBatchOnFunctionError:     aws.ToBool(out.BisectBatchOnFunctionError),
		MaximumRetryAttempts:           out.MaximumRetryAttempts,
		MaximumRecordAgeInSeconds:      out.MaximumRecordAgeInSeconds,
		ParallelizationFactor:          aws.ToInt32(out.ParallelizationFactor),
		TumblingWindowInSeconds:        aws.ToInt32(out.TumblingWindowInSeconds),
		DestinationConfig:              destinationFromAWS(out.DestinationConfig),
		ScalingConfig:                  scalingFromAWS(out.ScalingConfig),
		FunctionResponseTypes:          fromFunctionResponseTypes(out.FunctionResponseTypes),
		LastModified:                   timeString(out.LastModified),
	}
}

func timeString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func filterCriteriaFromAWS(input *lambdatypes.FilterCriteria) *FilterCriteriaSpec {
	if input == nil {
		return nil
	}
	filters := make([]FilterSpec, 0, len(input.Filters))
	for _, filter := range input.Filters {
		filters = append(filters, FilterSpec{Pattern: aws.ToString(filter.Pattern)})
	}
	return &FilterCriteriaSpec{Filters: filters}
}

func destinationFromAWS(input *lambdatypes.DestinationConfig) *DestinationSpec {
	if input == nil || input.OnFailure == nil {
		return nil
	}
	return &DestinationSpec{OnFailure: OnFailureSpec{DestinationArn: aws.ToString(input.OnFailure.Destination)}}
}

func scalingFromAWS(input *lambdatypes.ScalingConfig) *ScalingSpec {
	if input == nil || input.MaximumConcurrency == nil {
		return nil
	}
	return &ScalingSpec{MaximumConcurrency: aws.ToInt32(input.MaximumConcurrency)}
}

func outputsFromObserved(observed ObservedState) EventSourceMappingOutputs {
	return EventSourceMappingOutputs{UUID: observed.UUID, EventSourceArn: observed.EventSourceArn, FunctionArn: observed.FunctionArn, State: observed.State, LastModified: observed.LastModified, BatchSize: observed.BatchSize}
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException")
}

func IsConflict(err error) bool {
	return awserr.HasCode(err, "ResourceConflictException")
}

func IsInvalidParameter(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValueException")
}
