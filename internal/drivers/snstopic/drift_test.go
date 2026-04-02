package snstopic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := SNSTopicSpec{
		DisplayName:               "My Topic",
		KmsMasterKeyId:            "alias/my-key",
		ContentBasedDeduplication: true,
		Tags:                      map[string]string{"env": "prod"},
	}
	observed := ObservedState{
		DisplayName:               "My Topic",
		KmsMasterKeyId:            "alias/my-key",
		ContentBasedDeduplication: true,
		Tags:                      map[string]string{"env": "prod"},
	}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_DisplayNameChanged(t *testing.T) {
	desired := SNSTopicSpec{DisplayName: "New Name"}
	observed := ObservedState{DisplayName: "Old Name"}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_KmsKeyChanged(t *testing.T) {
	desired := SNSTopicSpec{KmsMasterKeyId: "alias/new-key"}
	observed := ObservedState{KmsMasterKeyId: "alias/old-key"}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_ContentBasedDeduplicationChanged(t *testing.T) {
	desired := SNSTopicSpec{ContentBasedDeduplication: true}
	observed := ObservedState{ContentBasedDeduplication: false}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_TagsChanged(t *testing.T) {
	desired := SNSTopicSpec{Tags: map[string]string{"env": "prod"}}
	observed := ObservedState{Tags: map[string]string{"env": "dev"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_TagAdded(t *testing.T) {
	desired := SNSTopicSpec{Tags: map[string]string{"env": "prod", "team": "infra"}}
	observed := ObservedState{Tags: map[string]string{"env": "prod"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_TagRemoved(t *testing.T) {
	desired := SNSTopicSpec{Tags: map[string]string{}}
	observed := ObservedState{Tags: map[string]string{"env": "prod"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_PraxisTagsIgnored(t *testing.T) {
	desired := SNSTopicSpec{Tags: map[string]string{"env": "prod"}}
	observed := ObservedState{Tags: map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~my-topic"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_NilVsEmptyTags(t *testing.T) {
	desired := SNSTopicSpec{Tags: nil}
	observed := ObservedState{Tags: map[string]string{}}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_PolicySemanticEquality(t *testing.T) {
	desired := SNSTopicSpec{Policy: `{"Version":"2012-10-17","Statement":[]}`}
	observed := ObservedState{Policy: `{ "Statement": [], "Version": "2012-10-17" }`}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_PolicyChanged(t *testing.T) {
	desired := SNSTopicSpec{Policy: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow"}]}`}
	observed := ObservedState{Policy: `{"Version":"2012-10-17","Statement":[]}`}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_DeliveryPolicyChanged(t *testing.T) {
	desired := SNSTopicSpec{DeliveryPolicy: `{"http":{"defaultHealthyRetryPolicy":{"numRetries":3}}}`}
	observed := ObservedState{DeliveryPolicy: ""}
	assert.True(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_NoDiffs(t *testing.T) {
	desired := SNSTopicSpec{DisplayName: "My Topic", Tags: map[string]string{"env": "prod"}}
	observed := ObservedState{DisplayName: "My Topic", Tags: map[string]string{"env": "prod"}}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Empty(t, diffs)
}

func TestComputeFieldDiffs_DisplayName(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSTopicSpec{DisplayName: "New Name"},
		ObservedState{DisplayName: "Old Name"},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.displayName", diffs[0].Path)
	assert.Equal(t, "Old Name", diffs[0].OldValue)
	assert.Equal(t, "New Name", diffs[0].NewValue)
}

func TestComputeFieldDiffs_KmsKey(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSTopicSpec{KmsMasterKeyId: "alias/new"},
		ObservedState{KmsMasterKeyId: "alias/old"},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.kmsMasterKeyId", diffs[0].Path)
}

func TestComputeFieldDiffs_ContentBasedDeduplication(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSTopicSpec{ContentBasedDeduplication: true},
		ObservedState{ContentBasedDeduplication: false},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.contentBasedDeduplication", diffs[0].Path)
}

func TestComputeFieldDiffs_Tags(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSTopicSpec{Tags: map[string]string{"env": "prod"}},
		ObservedState{Tags: map[string]string{"env": "dev"}},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "tags", diffs[0].Path)
}

func TestComputeFieldDiffs_MultipleDiffs(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SNSTopicSpec{DisplayName: "New", KmsMasterKeyId: "alias/new", Tags: map[string]string{"a": "1"}},
		ObservedState{DisplayName: "Old", KmsMasterKeyId: "alias/old", Tags: map[string]string{"b": "2"}},
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

func TestPoliciesEqual_InvalidJSON(t *testing.T) {
	assert.False(t, policiesEqual("{bad", `{"a":1}`))
	assert.False(t, policiesEqual(`{"a":1}`, "{bad"))
}

func TestTagsMatch_BothNil(t *testing.T) {
	assert.True(t, tagsMatch(nil, nil))
}

func TestTagsMatch_NilVsEmpty(t *testing.T) {
	assert.True(t, tagsMatch(nil, map[string]string{}))
}

func TestTagsMatch_OnlyPraxisTags(t *testing.T) {
	assert.True(t, tagsMatch(nil, map[string]string{"praxis:managed-key": "key"}))
}

func TestMergeTags(t *testing.T) {
	merged := mergeTags(
		map[string]string{"env": "prod"},
		map[string]string{"praxis:managed-key": "key"},
	)
	assert.Equal(t, "prod", merged["env"])
	assert.Equal(t, "key", merged["praxis:managed-key"])
}

func TestFilterPraxisTags(t *testing.T) {
	filtered := filterPraxisTags(map[string]string{
		"env":                "prod",
		"praxis:managed-key": "key",
	})
	assert.Equal(t, map[string]string{"env": "prod"}, filtered)
}

func TestFilterPraxisTags_Nil(t *testing.T) {
	filtered := filterPraxisTags(nil)
	assert.Equal(t, map[string]string{}, filtered)
}
