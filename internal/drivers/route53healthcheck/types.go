package route53healthcheck

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "Route53HealthCheck"

type HealthCheckSpec struct {
	Account                      string            `json:"account,omitempty"`
	Type                         string            `json:"type"`
	IPAddress                    string            `json:"ipAddress,omitempty"`
	Port                         int32             `json:"port,omitempty"`
	ResourcePath                 string            `json:"resourcePath,omitempty"`
	FQDN                         string            `json:"fqdn,omitempty"`
	SearchString                 string            `json:"searchString,omitempty"`
	RequestInterval              int32             `json:"requestInterval,omitempty"`
	FailureThreshold             int32             `json:"failureThreshold,omitempty"`
	ChildHealthChecks            []string          `json:"childHealthChecks,omitempty"`
	HealthThreshold              int32             `json:"healthThreshold,omitempty"`
	CloudWatchAlarmName          string            `json:"cloudWatchAlarmName,omitempty"`
	CloudWatchAlarmRegion        string            `json:"cloudWatchAlarmRegion,omitempty"`
	InsufficientDataHealthStatus string            `json:"insufficientDataHealthStatus,omitempty"`
	Disabled                     bool              `json:"disabled,omitempty"`
	InvertHealthCheck            bool              `json:"invertHealthCheck,omitempty"`
	EnableSNI                    bool              `json:"enableSNI,omitempty"`
	Regions                      []string          `json:"regions,omitempty"`
	Tags                         map[string]string `json:"tags,omitempty"`
	ManagedKey                   string            `json:"managedKey,omitempty"`
}

type HealthCheckOutputs struct {
	HealthCheckId string `json:"healthCheckId"`
}

type ObservedState struct {
	HealthCheckId                string            `json:"healthCheckId"`
	CallerReference              string            `json:"callerReference,omitempty"`
	Version                      int64             `json:"version"`
	Type                         string            `json:"type"`
	IPAddress                    string            `json:"ipAddress,omitempty"`
	Port                         int32             `json:"port,omitempty"`
	ResourcePath                 string            `json:"resourcePath,omitempty"`
	FQDN                         string            `json:"fqdn,omitempty"`
	SearchString                 string            `json:"searchString,omitempty"`
	RequestInterval              int32             `json:"requestInterval,omitempty"`
	FailureThreshold             int32             `json:"failureThreshold,omitempty"`
	ChildHealthChecks            []string          `json:"childHealthChecks,omitempty"`
	HealthThreshold              int32             `json:"healthThreshold,omitempty"`
	CloudWatchAlarmName          string            `json:"cloudWatchAlarmName,omitempty"`
	CloudWatchAlarmRegion        string            `json:"cloudWatchAlarmRegion,omitempty"`
	InsufficientDataHealthStatus string            `json:"insufficientDataHealthStatus,omitempty"`
	Disabled                     bool              `json:"disabled,omitempty"`
	InvertHealthCheck            bool              `json:"invertHealthCheck,omitempty"`
	EnableSNI                    bool              `json:"enableSNI,omitempty"`
	Regions                      []string          `json:"regions,omitempty"`
	Tags                         map[string]string `json:"tags,omitempty"`
}

type HealthCheckState struct {
	Desired            HealthCheckSpec      `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            HealthCheckOutputs   `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
