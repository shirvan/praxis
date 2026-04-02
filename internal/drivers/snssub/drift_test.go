package snssub

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := SNSSubscriptionSpec{
		FilterPolicy:        `{"event":["order"]}`,
		FilterPolicyScope:   "MessageAttributes",
		RawMessageDelivery:  true,
		DeliveryPolicy:      `{"healthyRetryPolicy":{"numRetries":3}}`,
		RedrivePolicy:       `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:123:dlq"}`,
		SubscriptionRoleArn: "arn:aws:iam::123:role/sub-role",
	}
	observed := ObservedState{
		FilterPolicy:        `{"event":["order"]}`,
		FilterPolicyScope:   "MessageAttributes",
		RawMessageDelivery:  true,
		DeliveryPolicy:      `{"healthyRetryPolicy":{"numRetries":3}}`,
		RedrivePolicy:       `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:123:dlq"}`,
		SubscriptionRoleArn: "arn:aws:iam::123:role/sub-role",
	}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_FilterPolicyChanged(t *testing.T) {
	desired := SNSSubscriptionSpec{FilterPolicy: `{"event":["order","cancel"]}`}
	observed := ObservedState{FilterPolicy: `{"event":["order"]}`}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_FilterPolicyScopeChanged(t *testing.T) {
	desired := SNSSubscriptionSpec{FilterPolicyScope: "MessageBody", FilterPolicy: `{}`}
	observed := ObservedState{FilterPolicyScope: "MessageAttributes", FilterPolicy: `{}`}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_FilterPolicyScopeEmpty_NoChange(t *testing.T) {
	// When desired scope is empty, don't flag as drift
	desired := SNSSubscriptionSpec{FilterPolicyScope: ""}
	observed := ObservedState{FilterPolicyScope: "MessageAttributes"}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_RawMessageDeliveryChanged(t *testing.T) {
	desired := SNSSubscriptionSpec{RawMessageDelivery: true}
	observed := ObservedState{RawMessageDelivery: false}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_DeliveryPolicyChanged(t *testing.T) {
	desired := SNSSubscriptionSpec{DeliveryPolicy: `{"healthyRetryPolicy":{"numRetries":5}}`}
	observed := ObservedState{DeliveryPolicy: `{"healthyRetryPolicy":{"numRetries":3}}`}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_RedrivePolicyChanged(t *testing.T) {
	desired := SNSSubscriptionSpec{RedrivePolicy: `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:123:new-dlq"}`}
	observed := ObservedState{RedrivePolicy: `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:123:old-dlq"}`}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_SubscriptionRoleArnChanged(t *testing.T) {
	desired := SNSSubscriptionSpec{SubscriptionRoleArn: "arn:aws:iam::123:role/new-role"}
	observed := ObservedState{SubscriptionRoleArn: "arn:aws:iam::123:role/old-role"}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_PolicySemanticEquality(t *testing.T) {
	desired := SNSSubscriptionSpec{FilterPolicy: `{"event": ["order"]}`}
	observed := ObservedState{FilterPolicy: `{"event":["order"]}`}
	assert.False(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_NoDiffs(t *testing.T) {
	desired := SNSSubscriptionSpec{RawMessageDelivery: false}
	observed := ObservedState{RawMessageDelivery: false}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Empty(t, diffs)
}

func TestComputeFieldDiffs_FilterPolicy(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSSubscriptionSpec{FilterPolicy: `{"new":"policy"}`},
		ObservedState{FilterPolicy: `{"old":"policy"}`},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.filterPolicy", diffs[0].Path)
}

func TestComputeFieldDiffs_RawMessageDelivery(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSSubscriptionSpec{RawMessageDelivery: true},
		ObservedState{RawMessageDelivery: false},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.rawMessageDelivery", diffs[0].Path)
	assert.Equal(t, false, diffs[0].OldValue)
	assert.Equal(t, true, diffs[0].NewValue)
}

func TestComputeFieldDiffs_SubscriptionRoleArn(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSSubscriptionSpec{SubscriptionRoleArn: "arn:new"},
		ObservedState{SubscriptionRoleArn: "arn:old"},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.subscriptionRoleArn", diffs[0].Path)
}

func TestComputeFieldDiffs_MultipleDiffs(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSSubscriptionSpec{
			FilterPolicy:        `{"new":"filter"}`,
			RawMessageDelivery:  true,
			SubscriptionRoleArn: "arn:new",
		},
		ObservedState{
			FilterPolicy:        `{"old":"filter"}`,
			RawMessageDelivery:  false,
			SubscriptionRoleArn: "arn:old",
		},
	)
	assert.Len(t, diffs, 3)
}

func TestPoliciesEqual_BothEmpty(t *testing.T) {
	assert.True(t, policiesEqual("", ""))
}

func TestPoliciesEqual_OneEmpty(t *testing.T) {
	assert.False(t, policiesEqual(`{"a":1}`, ""))
	assert.False(t, policiesEqual("", `{"a":1}`))
}

func TestPoliciesEqual_SemanticMatch(t *testing.T) {
	assert.True(t, policiesEqual(`{ "b": 2, "a": 1 }`, `{"a":1,"b":2}`))
}

func TestPoliciesEqual_InvalidJSON(t *testing.T) {
	assert.False(t, policiesEqual("{bad", `{"a":1}`))
}
