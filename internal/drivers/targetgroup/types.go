// Package targetgroup implements the Praxis driver for AWS ELBv2 Target Group resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Elastic Load Balancing v2; the driver state couples both together with status tracking.
package targetgroup

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS ELBv2 Target Group driver.
const ServiceName = "TargetGroup"

// TargetGroupSpec declares the user's desired configuration for a AWS ELBv2 Target Group.
// Fields are validated before any AWS call and mapped to Elastic Load Balancing v2 API inputs.
type TargetGroupSpec struct {
	Account             string            `json:"account,omitempty"`
	Region              string            `json:"region"`
	Name                string            `json:"name"`
	Protocol            string            `json:"protocol"`
	Port                int               `json:"port"`
	VpcId               string            `json:"vpcId"`
	TargetType          string            `json:"targetType"`
	ProtocolVersion     string            `json:"protocolVersion,omitempty"`
	HealthCheck         HealthCheck       `json:"healthCheck"`
	DeregistrationDelay int               `json:"deregistrationDelay"`
	Stickiness          *Stickiness       `json:"stickiness,omitempty"`
	Targets             []Target          `json:"targets,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
}

// HealthCheck defines the health check parameters for a target group.
type HealthCheck struct {
	Protocol           string `json:"protocol"`
	Path               string `json:"path,omitempty"`
	Port               string `json:"port"`
	HealthyThreshold   int32  `json:"healthyThreshold"`
	UnhealthyThreshold int32  `json:"unhealthyThreshold"`
	Interval           int32  `json:"interval"`
	Timeout            int32  `json:"timeout"`
	Matcher            string `json:"matcher,omitempty"`
}

// Stickiness configures session affinity (sticky sessions) for a target group.
type Stickiness struct {
	Enabled  bool   `json:"enabled"`
	Type     string `json:"type"`
	Duration int    `json:"duration"`
}

// Target identifies a single registration in the target group (instance, IP, or Lambda).
type Target struct {
	ID               string `json:"id"`
	Port             int    `json:"port,omitempty"`
	AvailabilityZone string `json:"availabilityZone,omitempty"`
}

// TargetGroupOutputs holds the values produced after provisioning a AWS ELBv2 Target Group.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type TargetGroupOutputs struct {
	TargetGroupArn  string `json:"targetGroupArn"`
	TargetGroupName string `json:"targetGroupName"`
}

// ObservedState captures the live configuration of a AWS ELBv2 Target Group
// as read from Elastic Load Balancing v2. It is compared against the spec
// during drift detection.
type ObservedState struct {
	TargetGroupArn      string            `json:"targetGroupArn"`
	Name                string            `json:"name"`
	Protocol            string            `json:"protocol"`
	Port                int               `json:"port"`
	VpcId               string            `json:"vpcId"`
	TargetType          string            `json:"targetType"`
	ProtocolVersion     string            `json:"protocolVersion,omitempty"`
	HealthCheck         HealthCheck       `json:"healthCheck"`
	DeregistrationDelay int               `json:"deregistrationDelay"`
	Stickiness          *Stickiness       `json:"stickiness,omitempty"`
	Targets             []Target          `json:"targets,omitempty"`
	Tags                map[string]string `json:"tags"`
}

// TargetGroupState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type TargetGroupState struct {
	Desired            TargetGroupSpec      `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            TargetGroupOutputs   `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
