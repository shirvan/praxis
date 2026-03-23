package listener

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "Listener"

type ListenerSpec struct {
	Account         string            `json:"account,omitempty"`
	LoadBalancerArn string            `json:"loadBalancerArn"`
	Port            int               `json:"port"`
	Protocol        string            `json:"protocol"`
	SslPolicy       string            `json:"sslPolicy,omitempty"`
	CertificateArn  string            `json:"certificateArn,omitempty"`
	AlpnPolicy      string            `json:"alpnPolicy,omitempty"`
	DefaultActions  []ListenerAction  `json:"defaultActions"`
	Tags            map[string]string `json:"tags,omitempty"`
}

type ListenerAction struct {
	Type                string               `json:"type"`
	TargetGroupArn      string               `json:"targetGroupArn,omitempty"`
	RedirectConfig      *RedirectConfig      `json:"redirectConfig,omitempty"`
	FixedResponseConfig *FixedResponseConfig `json:"fixedResponseConfig,omitempty"`
}

type RedirectConfig struct {
	Protocol   string `json:"protocol"`
	Host       string `json:"host"`
	Port       string `json:"port"`
	Path       string `json:"path"`
	Query      string `json:"query"`
	StatusCode string `json:"statusCode"`
}

type FixedResponseConfig struct {
	StatusCode  string `json:"statusCode"`
	ContentType string `json:"contentType"`
	MessageBody string `json:"messageBody"`
}

type ListenerOutputs struct {
	ListenerArn string `json:"listenerArn"`
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
}

type ObservedState struct {
	ListenerArn     string            `json:"listenerArn"`
	LoadBalancerArn string            `json:"loadBalancerArn"`
	Port            int               `json:"port"`
	Protocol        string            `json:"protocol"`
	SslPolicy       string            `json:"sslPolicy,omitempty"`
	CertificateArn  string            `json:"certificateArn,omitempty"`
	AlpnPolicy      string            `json:"alpnPolicy,omitempty"`
	DefaultActions  []ListenerAction  `json:"defaultActions"`
	Tags            map[string]string `json:"tags"`
}

type ListenerState struct {
	Desired            ListenerSpec         `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            ListenerOutputs      `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
