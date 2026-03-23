package listenerrule

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewListenerRuleDriver(nil)
	assert.Equal(t, "ListenerRule", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		ListenerArn: "arn:aws:elasticloadbalancing:us-east-1:123:listener/app/my-alb/lb-id/listener-id",
		Priority:    10,
		Conditions:  []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:     []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:        map[string]string{"env": "dev", "praxis:rule-name": "my-rule"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, obs.ListenerArn, spec.ListenerArn)
	assert.Equal(t, obs.Priority, spec.Priority)
	assert.Equal(t, obs.Conditions, spec.Conditions)
	assert.Equal(t, obs.Actions, spec.Actions)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestValidateSpec_Valid(t *testing.T) {
	spec := ListenerRuleSpec{
		ListenerArn: "arn:listener",
		Priority:    10,
		Conditions:  []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:     []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	assert.NoError(t, validateSpec(spec))
}

func TestValidateSpec_MissingListenerArn(t *testing.T) {
	spec := ListenerRuleSpec{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	assert.Error(t, validateSpec(spec))
}

func TestValidateSpec_InvalidPriority(t *testing.T) {
	base := ListenerRuleSpec{
		ListenerArn: "arn:listener",
		Conditions:  []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:     []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	base.Priority = 0
	assert.Error(t, validateSpec(base))
	base.Priority = 50001
	assert.Error(t, validateSpec(base))
}

func TestValidateSpec_NoConditions(t *testing.T) {
	spec := ListenerRuleSpec{
		ListenerArn: "arn:listener",
		Priority:    10,
		Actions:     []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	assert.Error(t, validateSpec(spec))
}

func TestValidateSpec_NoActions(t *testing.T) {
	spec := ListenerRuleSpec{
		ListenerArn: "arn:listener",
		Priority:    10,
		Conditions:  []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
	}
	assert.Error(t, validateSpec(spec))
}

func TestHasImmutableChange(t *testing.T) {
	spec := ListenerRuleSpec{ListenerArn: "arn:listener-a"}
	obs := ObservedState{ListenerArn: "arn:listener-a"}
	assert.False(t, hasImmutableChange(spec, obs))
	spec.ListenerArn = "arn:listener-b"
	assert.True(t, hasImmutableChange(spec, obs))
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{RuleArn: "arn:rule", Priority: 10}
	out := outputsFromObserved(obs)
	assert.Equal(t, "arn:rule", out.RuleArn)
	assert.Equal(t, 10, out.Priority)
}

func TestParsePriority(t *testing.T) {
	assert.Equal(t, 10, parsePriority("10"))
	assert.Equal(t, 0, parsePriority("default"))
	assert.Equal(t, 0, parsePriority(""))
	assert.Equal(t, 100, parsePriority("100"))
}

func TestExtractListenerArnFromRuleArn(t *testing.T) {
	ruleArn := "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener-rule/app/my-alb/50dc6c495c0c9188/f2f7dc8efc522ab2/1234567890123456"
	expected := "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/my-alb/50dc6c495c0c9188/f2f7dc8efc522ab2"
	assert.Equal(t, expected, extractListenerArnFromRuleArn(ruleArn))
}
