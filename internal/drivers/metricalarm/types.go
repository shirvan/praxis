package metricalarm

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "MetricAlarm"

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

type MetricAlarmOutputs struct {
	AlarmArn    string `json:"alarmArn"`
	AlarmName   string `json:"alarmName"`
	StateValue  string `json:"stateValue"`
	StateReason string `json:"stateReason,omitempty"`
}

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
