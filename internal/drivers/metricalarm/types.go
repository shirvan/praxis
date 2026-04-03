// Package metricalarm implements the Praxis driver for AWS CloudWatch Metric Alarm resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon CloudWatch; the driver state couples both together with status tracking.
package metricalarm

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS CloudWatch Metric Alarm driver.
const ServiceName = "MetricAlarm"

// MetricAlarmSpec declares the user's desired configuration for a AWS CloudWatch Metric Alarm.
// Fields are validated before any AWS call and mapped to Amazon CloudWatch API inputs.
type MetricAlarmSpec struct {
	Account                 string            `json:"account,omitempty"`
	Region                  string            `json:"region"`
	AlarmName               string            `json:"alarmName"`
	Namespace               string            `json:"namespace"`
	MetricName              string            `json:"metricName"`
	Dimensions              map[string]string `json:"dimensions,omitempty"`
	Statistic               string            `json:"statistic,omitempty"`
	ExtendedStatistic       string            `json:"extendedStatistic,omitempty"`
	Period                  int32             `json:"period"`
	EvaluationPeriods       int32             `json:"evaluationPeriods"`
	DatapointsToAlarm       *int32            `json:"datapointsToAlarm,omitempty"`
	Threshold               float64           `json:"threshold"`
	ComparisonOperator      string            `json:"comparisonOperator"`
	TreatMissingData        string            `json:"treatMissingData,omitempty"`
	AlarmDescription        string            `json:"alarmDescription,omitempty"`
	ActionsEnabled          bool              `json:"actionsEnabled"`
	AlarmActions            []string          `json:"alarmActions,omitempty"`
	OKActions               []string          `json:"okActions,omitempty"`
	InsufficientDataActions []string          `json:"insufficientDataActions,omitempty"`
	Unit                    string            `json:"unit,omitempty"`
	Tags                    map[string]string `json:"tags,omitempty"`
	ManagedKey              string            `json:"managedKey,omitempty"`
}

// MetricAlarmOutputs holds the values produced after provisioning a AWS CloudWatch Metric Alarm.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type MetricAlarmOutputs struct {
	AlarmArn    string `json:"alarmArn"`
	AlarmName   string `json:"alarmName"`
	StateValue  string `json:"stateValue"`
	StateReason string `json:"stateReason,omitempty"`
}

// ObservedState captures the live configuration of a AWS CloudWatch Metric Alarm
// as read from Amazon CloudWatch. It is compared against the spec
// during drift detection.
type ObservedState struct {
	AlarmArn                string            `json:"alarmArn"`
	AlarmName               string            `json:"alarmName"`
	Namespace               string            `json:"namespace"`
	MetricName              string            `json:"metricName"`
	Dimensions              map[string]string `json:"dimensions,omitempty"`
	Statistic               string            `json:"statistic,omitempty"`
	ExtendedStatistic       string            `json:"extendedStatistic,omitempty"`
	Period                  int32             `json:"period"`
	EvaluationPeriods       int32             `json:"evaluationPeriods"`
	DatapointsToAlarm       int32             `json:"datapointsToAlarm"`
	Threshold               float64           `json:"threshold"`
	ComparisonOperator      string            `json:"comparisonOperator"`
	TreatMissingData        string            `json:"treatMissingData,omitempty"`
	AlarmDescription        string            `json:"alarmDescription,omitempty"`
	ActionsEnabled          bool              `json:"actionsEnabled"`
	AlarmActions            []string          `json:"alarmActions,omitempty"`
	OKActions               []string          `json:"okActions,omitempty"`
	InsufficientDataActions []string          `json:"insufficientDataActions,omitempty"`
	Unit                    string            `json:"unit,omitempty"`
	StateValue              string            `json:"stateValue"`
	StateReason             string            `json:"stateReason,omitempty"`
	Tags                    map[string]string `json:"tags,omitempty"`
}

// MetricAlarmState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type MetricAlarmState struct {
	Desired            MetricAlarmSpec      `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            MetricAlarmOutputs   `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
