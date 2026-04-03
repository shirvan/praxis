// Package listener implements the Praxis driver for AWS ELBv2 Listener resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Elastic Load Balancing v2; the driver state couples both together with status tracking.
package listener

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS ELBv2 Listener driver.
const ServiceName = "Listener"

// ListenerSpec declares the user's desired configuration for a AWS ELBv2 Listener.
// Fields are validated before any AWS call and mapped to Elastic Load Balancing v2 API inputs.
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

// ListenerAction defines a default action for a listener (forward, redirect, or fixed-response).
type ListenerAction struct {
	Type                string               `json:"type"`
	TargetGroupArn      string               `json:"targetGroupArn,omitempty"`
	RedirectConfig      *RedirectConfig      `json:"redirectConfig,omitempty"`
	FixedResponseConfig *FixedResponseConfig `json:"fixedResponseConfig,omitempty"`
}

// RedirectConfig specifies HTTP redirect parameters (protocol, host, port, path, query, status code).
type RedirectConfig struct {
	Protocol   string `json:"protocol"`
	Host       string `json:"host"`
	Port       string `json:"port"`
	Path       string `json:"path"`
	Query      string `json:"query"`
	StatusCode string `json:"statusCode"`
}

// FixedResponseConfig specifies a static HTTP response returned by the load balancer.
type FixedResponseConfig struct {
	StatusCode  string `json:"statusCode"`
	ContentType string `json:"contentType"`
	MessageBody string `json:"messageBody"`
}

// ListenerOutputs holds the values produced after provisioning a AWS ELBv2 Listener.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type ListenerOutputs struct {
	ListenerArn string `json:"listenerArn"`
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
}

// ObservedState captures the live configuration of a AWS ELBv2 Listener
// as read from Elastic Load Balancing v2. It is compared against the spec
// during drift detection.
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

// ListenerState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
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
