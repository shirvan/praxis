package targetgroup

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "TargetGroup"

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

type Stickiness struct {
	Enabled  bool   `json:"enabled"`
	Type     string `json:"type"`
	Duration int    `json:"duration"`
}

type Target struct {
	ID               string `json:"id"`
	Port             int    `json:"port,omitempty"`
	AvailabilityZone string `json:"availabilityZone,omitempty"`
}

type TargetGroupOutputs struct {
	TargetGroupArn  string `json:"targetGroupArn"`
	TargetGroupName string `json:"targetGroupName"`
}

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