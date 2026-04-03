// Package listenerrule implements the Praxis driver for AWS ELBv2 Listener Rule resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Elastic Load Balancing v2; the driver state couples both together with status tracking.
package listenerrule

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS ELBv2 Listener Rule driver.
const ServiceName = "ListenerRule"

// ListenerRuleSpec declares the user's desired configuration for a AWS ELBv2 Listener Rule.
// Fields are validated before any AWS call and mapped to Elastic Load Balancing v2 API inputs.
type ListenerRuleSpec struct {
	Account     string            `json:"account,omitempty"`
	ListenerArn string            `json:"listenerArn"`
	Priority    int               `json:"priority"`
	Conditions  []RuleCondition   `json:"conditions"`
	Actions     []RuleAction      `json:"actions"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// RuleCondition is a match condition for a listener rule (path, host, header, query-string, source-ip, or method).
type RuleCondition struct {
	Field             string             `json:"field"`
	Values            []string           `json:"values,omitempty"`
	HttpHeaderConfig  *HttpHeaderConfig  `json:"httpHeaderConfig,omitempty"`
	QueryStringConfig *QueryStringConfig `json:"queryStringConfig,omitempty"`
}

// HttpHeaderConfig matches requests by a specific HTTP header name and a set of allowed values.
type HttpHeaderConfig struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// QueryStringConfig matches requests by query-string key/value pairs.
type QueryStringConfig struct {
	Values []QueryStringKV `json:"values"`
}

// QueryStringKV is a single key/value pair used in query-string condition matching.
type QueryStringKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// RuleAction defines what happens when a listener rule matches (forward, redirect, or fixed-response).
type RuleAction struct {
	Type                string               `json:"type"`
	Order               int                  `json:"order,omitempty"`
	TargetGroupArn      string               `json:"targetGroupArn,omitempty"`
	ForwardConfig       *ForwardConfig       `json:"forwardConfig,omitempty"`
	RedirectConfig      *RedirectConfig      `json:"redirectConfig,omitempty"`
	FixedResponseConfig *FixedResponseConfig `json:"fixedResponseConfig,omitempty"`
}

// ForwardConfig enables weighted target group routing and optional stickiness for forward actions.
type ForwardConfig struct {
	TargetGroups []WeightedTargetGroup `json:"targetGroups"`
	Stickiness   *ForwardStickiness    `json:"stickiness,omitempty"`
}

// WeightedTargetGroup pairs a target group ARN with a routing weight (0-999).
type WeightedTargetGroup struct {
	TargetGroupArn string `json:"targetGroupArn"`
	Weight         int    `json:"weight"`
}

// ForwardStickiness controls target group level stickiness for weighted forward actions.
type ForwardStickiness struct {
	Enabled  bool `json:"enabled"`
	Duration int  `json:"duration"`
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

// ListenerRuleOutputs holds the values produced after provisioning a AWS ELBv2 Listener Rule.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type ListenerRuleOutputs struct {
	RuleArn  string `json:"ruleArn"`
	Priority int    `json:"priority"`
}

// ObservedState captures the live configuration of a AWS ELBv2 Listener Rule
// as read from Elastic Load Balancing v2. It is compared against the spec
// during drift detection.
type ObservedState struct {
	RuleArn     string            `json:"ruleArn"`
	ListenerArn string            `json:"listenerArn"`
	Priority    int               `json:"priority"`
	IsDefault   bool              `json:"isDefault"`
	Conditions  []RuleCondition   `json:"conditions"`
	Actions     []RuleAction      `json:"actions"`
	Tags        map[string]string `json:"tags"`
}

// ListenerRuleState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type ListenerRuleState struct {
	Desired            ListenerRuleSpec     `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            ListenerRuleOutputs  `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
