// Package route53healthcheck implements the Restate virtual-object driver for AWS Route53 Health Checks.
// Supports endpoint checks (HTTP/HTTPS/TCP), calculated (aggregated) checks, and CloudWatch
// metric-based checks. Type and requestInterval are immutable after creation. Uses version-based
// optimistic concurrency for updates.
package route53healthcheck

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate virtual object service name used to register and address this driver.
const ServiceName = "Route53HealthCheck"

// HealthCheckSpec defines the desired state of a Route53 health check.
type HealthCheckSpec struct {
	Account                      string            `json:"account,omitempty"`                      // AWS account alias.
	Type                         string            `json:"type"`                                   // Check type: HTTP, HTTPS, TCP, CALCULATED, CLOUDWATCH_METRIC (immutable).
	IPAddress                    string            `json:"ipAddress,omitempty"`                    // Endpoint IP (for endpoint checks).
	Port                         int32             `json:"port,omitempty"`                         // Endpoint port (default: 80 for HTTP, 443 for HTTPS).
	ResourcePath                 string            `json:"resourcePath,omitempty"`                 // HTTP request path.
	FQDN                         string            `json:"fqdn,omitempty"`                         // Endpoint domain name.
	SearchString                 string            `json:"searchString,omitempty"`                 // Required for STR_MATCH types.
	RequestInterval              int32             `json:"requestInterval,omitempty"`              // Seconds between checks (immutable, default 30).
	FailureThreshold             int32             `json:"failureThreshold,omitempty"`             // Number of consecutive failures.
	ChildHealthChecks            []string          `json:"childHealthChecks,omitempty"`            // IDs of child health checks (CALCULATED type only).
	HealthThreshold              int32             `json:"healthThreshold,omitempty"`              // Minimum healthy children (CALCULATED type).
	CloudWatchAlarmName          string            `json:"cloudWatchAlarmName,omitempty"`          // CloudWatch alarm name (CLOUDWATCH_METRIC type).
	CloudWatchAlarmRegion        string            `json:"cloudWatchAlarmRegion,omitempty"`        // CloudWatch alarm region.
	InsufficientDataHealthStatus string            `json:"insufficientDataHealthStatus,omitempty"` // Status when data is insufficient.
	Disabled                     bool              `json:"disabled,omitempty"`                     // Whether the check is disabled.
	InvertHealthCheck            bool              `json:"invertHealthCheck,omitempty"`            // Invert the health check result.
	EnableSNI                    bool              `json:"enableSNI,omitempty"`                    // Enable SNI for HTTPS checks.
	Regions                      []string          `json:"regions,omitempty"`                      // AWS regions for health checking.
	Tags                         map[string]string `json:"tags,omitempty"`                         // User-managed tags ("praxis:"-prefixed excluded from drift).
	ManagedKey                   string            `json:"managedKey,omitempty"`                   // CallerReference for idempotent creation.
}

// HealthCheckOutputs holds the Route53-assigned health check ID returned after provisioning.
type HealthCheckOutputs struct {
	HealthCheckId string `json:"healthCheckId"`
}

// ObservedState captures the last-known live state from AWS, including the version number
// used for optimistic concurrency during updates.
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

// HealthCheckState is the full persisted state of this virtual-object, stored via restate.Set.
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
