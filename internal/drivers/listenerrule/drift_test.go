package listenerrule

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := ListenerRuleSpec{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:       map[string]string{"env": "dev"},
	}
	observed := ObservedState{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:       map[string]string{"env": "dev"},
	}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_PriorityDrift(t *testing.T) {
	desired := ListenerRuleSpec{
		Priority:   20,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	observed := ObservedState{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_ConditionDrift(t *testing.T) {
	desired := ListenerRuleSpec{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/v2/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	observed := ObservedState{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_ActionDrift(t *testing.T) {
	desired := ListenerRuleSpec{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg-new"}},
	}
	observed := ObservedState{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg-old"}},
	}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_ConditionOrderIndependent(t *testing.T) {
	desired := ListenerRuleSpec{
		Priority: 10,
		Conditions: []RuleCondition{
			{Field: "host-header", Values: []string{"example.com"}},
			{Field: "path-pattern", Values: []string{"/api/*"}},
		},
		Actions: []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	observed := ObservedState{
		Priority: 10,
		Conditions: []RuleCondition{
			{Field: "path-pattern", Values: []string{"/api/*"}},
			{Field: "host-header", Values: []string{"example.com"}},
		},
		Actions: []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_WeightedForwardDrift(t *testing.T) {
	desired := ListenerRuleSpec{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions: []RuleAction{{Type: "forward", ForwardConfig: &ForwardConfig{
			TargetGroups: []WeightedTargetGroup{
				{TargetGroupArn: "arn:tg-a", Weight: 80},
				{TargetGroupArn: "arn:tg-b", Weight: 20},
			},
		}}},
	}
	observed := ObservedState{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions: []RuleAction{{Type: "forward", ForwardConfig: &ForwardConfig{
			TargetGroups: []WeightedTargetGroup{
				{TargetGroupArn: "arn:tg-a", Weight: 50},
				{TargetGroupArn: "arn:tg-b", Weight: 50},
			},
		}}},
	}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_TagDrift(t *testing.T) {
	desired := ListenerRuleSpec{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:       map[string]string{"env": "prod"},
	}
	observed := ObservedState{
		Priority:   10,
		Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:    []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:       map[string]string{"env": "dev"},
	}
	assert.True(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_ImmutableListenerArn(t *testing.T) {
	desired := ListenerRuleSpec{ListenerArn: "arn:listener-new", Priority: 10, Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}}, Actions: []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}}}
	observed := ObservedState{ListenerArn: "arn:listener-old", Priority: 10, Conditions: []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}}, Actions: []RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}}}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.NotEmpty(t, diffs)
	found := false
	for _, d := range diffs {
		if d.Path == "spec.listenerArn (immutable, requires replacement)" {
			found = true
		}
	}
	assert.True(t, found, "should contain immutable listenerArn diff")
}

func TestConditionsEqual_DifferentValueOrder(t *testing.T) {
	a := []RuleCondition{{Field: "path-pattern", Values: []string{"/b/*", "/a/*"}}}
	b := []RuleCondition{{Field: "path-pattern", Values: []string{"/a/*", "/b/*"}}}
	assert.True(t, conditionsEqual(a, b), "values order should not matter")
}

func TestConditionsEqual_HttpHeaderCaseInsensitive(t *testing.T) {
	a := []RuleCondition{{Field: "http-header", HttpHeaderConfig: &HttpHeaderConfig{Name: "X-Custom", Values: []string{"val"}}}}
	b := []RuleCondition{{Field: "http-header", HttpHeaderConfig: &HttpHeaderConfig{Name: "x-custom", Values: []string{"val"}}}}
	assert.True(t, conditionsEqual(a, b), "header name comparison should be case-insensitive")
}

func TestActionsEqual_ForwardConfigOrderIndependent(t *testing.T) {
	a := []RuleAction{{Type: "forward", ForwardConfig: &ForwardConfig{
		TargetGroups: []WeightedTargetGroup{
			{TargetGroupArn: "arn:tg-b", Weight: 20},
			{TargetGroupArn: "arn:tg-a", Weight: 80},
		},
	}}}
	b := []RuleAction{{Type: "forward", ForwardConfig: &ForwardConfig{
		TargetGroups: []WeightedTargetGroup{
			{TargetGroupArn: "arn:tg-a", Weight: 80},
			{TargetGroupArn: "arn:tg-b", Weight: 20},
		},
	}}}
	assert.True(t, actionsEqual(a, b), "target group order should not matter")
}

func TestWeightsEquivalent(t *testing.T) {
	assert.True(t, weightsEquivalent(0, 1), "0 and 1 should be equivalent (defaults)")
	assert.True(t, weightsEquivalent(1, 0), "1 and 0 should be equivalent (defaults)")
	assert.True(t, weightsEquivalent(50, 50))
	assert.False(t, weightsEquivalent(50, 80))
}

func TestNormalizeConditions(t *testing.T) {
	conditions := []RuleCondition{
		{Field: "host-header", Values: []string{"z.com", "a.com"}},
		{Field: "path-pattern", Values: []string{"/b", "/a"}},
	}
	norm := normalizeConditions(conditions)
	assert.Equal(t, "host-header", norm[0].Field)
	assert.Equal(t, []string{"a.com", "z.com"}, norm[0].Values)
	assert.Equal(t, "path-pattern", norm[1].Field)
	assert.Equal(t, []string{"/a", "/b"}, norm[1].Values)
}
