package esm

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
)

func applyDefaults(s EventSourceMappingSpec) EventSourceMappingSpec {
	if s.FunctionResponseTypes == nil {
		s.FunctionResponseTypes = []string{}
	} else {
		s.FunctionResponseTypes = append([]string(nil), s.FunctionResponseTypes...)
		slices.Sort(s.FunctionResponseTypes)
	}
	return s
}
func validateProvisionSpec(s EventSourceMappingSpec) error {
	if strings.TrimSpace(s.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(s.FunctionName) == "" {
		return fmt.Errorf("functionName is required")
	}
	if strings.TrimSpace(s.EventSourceArn) == "" {
		return fmt.Errorf("eventSourceArn is required")
	}
	return nil
}
func startingPositionChanged(a, b EventSourceMappingSpec) bool {
	if a.StartingPosition != b.StartingPosition {
		return true
	}
	if a.StartingPositionTimestamp == nil && b.StartingPositionTimestamp == nil {
		return false
	}
	if a.StartingPositionTimestamp == nil || b.StartingPositionTimestamp == nil {
		return true
	}
	return *a.StartingPositionTimestamp != *b.StartingPositionTimestamp
}
func specFromObserved(o ObservedState) EventSourceMappingSpec {
	enabled := o.State != "Disabled"
	return applyDefaults(EventSourceMappingSpec{FunctionName: o.FunctionArn, EventSourceArn: o.EventSourceArn, Enabled: enabled, BatchSize: int32Ptr(o.BatchSize), MaximumBatchingWindowInSeconds: int32Ptr(o.MaximumBatchingWindowInSeconds), StartingPosition: o.StartingPosition, FilterCriteria: o.FilterCriteria, BisectBatchOnFunctionError: boolPtr(o.BisectBatchOnFunctionError), MaximumRetryAttempts: o.MaximumRetryAttempts, MaximumRecordAgeInSeconds: o.MaximumRecordAgeInSeconds, ParallelizationFactor: int32Ptr(o.ParallelizationFactor), TumblingWindowInSeconds: int32Ptr(o.TumblingWindowInSeconds), DestinationConfig: o.DestinationConfig, ScalingConfig: o.ScalingConfig, FunctionResponseTypes: append([]string(nil), o.FunctionResponseTypes...)})
}
func int32Ptr(v int32) *int32               { return &v }
func boolPtr(v bool) *bool                  { return &v }
func EncodedEventSourceKey(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
