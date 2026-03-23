package listenerrule

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "ListenerRule"

type ListenerRuleSpec struct {
	Account     string            `json:"account,omitempty"`
	ListenerArn string            `json:"listenerArn"`
	Priority    int               `json:"priority"`
	Conditions  []RuleCondition   `json:"conditions"`
	Actions     []RuleAction      `json:"actions"`
	Tags        map[string]string `json:"tags,omitempty"`
}

type RuleCondition struct {
	Field             string             `json:"field"`
	Values            []string           `json:"values,omitempty"`
	HttpHeaderConfig  *HttpHeaderConfig  `json:"httpHeaderConfig,omitempty"`
	QueryStringConfig *QueryStringConfig `json:"queryStringConfig,omitempty"`
}

type HttpHeaderConfig struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type QueryStringConfig struct {
	Values []QueryStringKV `json:"values"`
}

type QueryStringKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type RuleAction struct {
	Type                string               `json:"type"`
	Order               int                  `json:"order,omitempty"`
	TargetGroupArn      string               `json:"targetGroupArn,omitempty"`
	ForwardConfig       *ForwardConfig       `json:"forwardConfig,omitempty"`
	RedirectConfig      *RedirectConfig      `json:"redirectConfig,omitempty"`
	FixedResponseConfig *FixedResponseConfig `json:"fixedResponseConfig,omitempty"`
}

type ForwardConfig struct {
	TargetGroups []WeightedTargetGroup `json:"targetGroups"`
	Stickiness   *ForwardStickiness    `json:"stickiness,omitempty"`
}

type WeightedTargetGroup struct {
	TargetGroupArn string `json:"targetGroupArn"`
	Weight         int    `json:"weight"`
}

type ForwardStickiness struct {
	Enabled  bool `json:"enabled"`
	Duration int  `json:"duration"`
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

type ListenerRuleOutputs struct {
	RuleArn  string `json:"ruleArn"`
	Priority int    `json:"priority"`
}

type ObservedState struct {
	RuleArn     string            `json:"ruleArn"`
	ListenerArn string            `json:"listenerArn"`
	Priority    int               `json:"priority"`
	IsDefault   bool              `json:"isDefault"`
	Conditions  []RuleCondition   `json:"conditions"`
	Actions     []RuleAction      `json:"actions"`
	Tags        map[string]string `json:"tags"`
}

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
