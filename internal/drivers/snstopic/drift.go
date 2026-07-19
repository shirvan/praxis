// Package snstopic – drift.go
//
// This file implements drift detection for AWS SNS Topic.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package snstopic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if the desired spec and observed state differ on mutable fields.
// Optional attributes are declarative: omission means the provider default or
// absence, never "leave an existing value unmanaged".
func HasDrift(desired SNSTopicSpec, observed ObservedState) bool {
	if desired.TopicName != "" && observed.TopicName != "" && desired.TopicName != observed.TopicName {
		return true
	}
	if desired.FifoTopic != observed.FifoTopic {
		return true
	}
	if desired.DisplayName != observed.DisplayName {
		return true
	}
	if !topicPoliciesEqual(desired.Policy, observed) {
		return true
	}
	if !optionalTopicPoliciesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		return true
	}
	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		return true
	}
	if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
		return true
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

// ComputeFieldDiffs returns field-level differences for plan output.
func ComputeFieldDiffs(desired SNSTopicSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff

	if desired.TopicName != "" && desired.TopicName != observed.TopicName {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.topicName (immutable, requires replacement)",
			OldValue: observed.TopicName,
			NewValue: desired.TopicName,
		})
	}
	if desired.FifoTopic != observed.FifoTopic {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.fifoTopic (immutable, requires replacement)",
			OldValue: observed.FifoTopic,
			NewValue: desired.FifoTopic,
		})
	}
	if desired.DisplayName != observed.DisplayName {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.displayName",
			OldValue: observed.DisplayName,
			NewValue: desired.DisplayName,
		})
	}
	if !topicPoliciesEqual(desired.Policy, observed) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.policy",
			OldValue: observed.Policy,
			NewValue: desired.Policy,
		})
	}
	if !optionalTopicPoliciesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.deliveryPolicy",
			OldValue: observed.DeliveryPolicy,
			NewValue: desired.DeliveryPolicy,
		})
	}
	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.kmsMasterKeyId",
			OldValue: observed.KmsMasterKeyId,
			NewValue: desired.KmsMasterKeyId,
		})
	}
	if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.contentBasedDeduplication",
			OldValue: observed.ContentBasedDeduplication,
			NewValue: desired.ContentBasedDeduplication,
		})
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "tags",
			OldValue: drivers.FilterPraxisTags(observed.Tags),
			NewValue: drivers.FilterPraxisTags(desired.Tags),
		})
	}

	return diffs
}

// policiesEqual compares two JSON policy strings semantically.
// Handles whitespace and key ordering differences.
func policiesEqual(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	var aObj, bObj any
	if json.Unmarshal([]byte(a), &aObj) != nil {
		return a == b
	}
	if json.Unmarshal([]byte(b), &bObj) != nil {
		return a == b
	}
	aNorm, _ := json.Marshal(aObj)
	bNorm, _ := json.Marshal(bObj)
	return bytes.Equal(aNorm, bNorm)
}

// optionalTopicPoliciesEqual accepts both missing and empty-object provider
// representations for a removed delivery policy.
func optionalTopicPoliciesEqual(a, b string) bool {
	if isEmptyJSONObject(a) && isEmptyJSONObject(b) {
		return true
	}
	return policiesEqual(a, b)
}

func isEmptyJSONObject(policy string) bool {
	if policy == "" {
		return true
	}
	var decoded map[string]any
	return json.Unmarshal([]byte(policy), &decoded) == nil && len(decoded) == 0
}

type topicPolicyDocument struct {
	Version   string                 `json:"Version"`
	ID        string                 `json:"Id"`
	Statement []topicPolicyStatement `json:"Statement"`
}

type topicPolicyStatement struct {
	SID       string                       `json:"Sid"`
	Effect    string                       `json:"Effect"`
	Principal map[string]string            `json:"Principal"`
	Action    []string                     `json:"Action"`
	Resource  string                       `json:"Resource"`
	Condition map[string]map[string]string `json:"Condition"`
}

var defaultTopicPolicyActions = []string{
	"SNS:AddPermission",
	"SNS:DeleteTopic",
	"SNS:GetTopicAttributes",
	"SNS:ListSubscriptionsByTopic",
	"SNS:Publish",
	"SNS:RemovePermission",
	"SNS:SetTopicAttributes",
	"SNS:Subscribe",
}

// topicPoliciesEqual treats an omitted desired policy as SNS's documented
// owner policy, not as permission to preserve an arbitrary custom policy.
func topicPoliciesEqual(desired string, observed ObservedState) bool {
	if desired != "" {
		return policiesEqual(desired, observed.Policy)
	}
	return isDefaultTopicPolicy(observed.Policy, observed.TopicArn, observed.Owner)
}

func isDefaultTopicPolicy(policy, topicArn, owner string) bool {
	if policy == "" {
		return true
	}
	var document topicPolicyDocument
	if json.Unmarshal([]byte(policy), &document) != nil || len(document.Statement) != 1 {
		return false
	}
	statement := document.Statement[0]
	if document.Version != "2008-10-17" || !strings.HasSuffix(document.ID, "__default_policy_ID") ||
		!strings.HasSuffix(statement.SID, "__default_statement_ID") || statement.Effect != "Allow" ||
		statement.Resource != topicArn || !maps.Equal(statement.Principal, map[string]string{"AWS": "*"}) {
		return false
	}
	actions := slices.Clone(statement.Action)
	slices.Sort(actions)
	if !slices.Equal(actions, defaultTopicPolicyActions) {
		return false
	}
	if maps.Equal(statement.Condition["StringEquals"], map[string]string{"AWS:SourceOwner": owner}) && len(statement.Condition) == 1 {
		return true
	}
	return maps.Equal(statement.Condition["StringLike"], map[string]string{"AWS:SourceArn": "arn:aws:*:*:" + owner + ":*"}) && len(statement.Condition) == 1
}

// defaultTopicPolicy restores SNS's documented owner-only default when a
// previously configured custom policy is omitted from desired state.
func defaultTopicPolicy(observed ObservedState) (string, error) {
	if observed.TopicArn == "" || observed.Owner == "" {
		return "", fmt.Errorf("cannot restore default SNS topic policy without topic ARN and owner")
	}
	document := topicPolicyDocument{
		Version: "2008-10-17",
		ID:      "__default_policy_ID",
		Statement: []topicPolicyStatement{{
			SID:       "__default_statement_ID",
			Effect:    "Allow",
			Principal: map[string]string{"AWS": "*"},
			Action:    slices.Clone(defaultTopicPolicyActions),
			Resource:  observed.TopicArn,
			Condition: map[string]map[string]string{"StringEquals": {"AWS:SourceOwner": observed.Owner}},
		}},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode default SNS topic policy: %w", err)
	}
	return string(encoded), nil
}

func mergeTags(user, system map[string]string) map[string]string {
	merged := make(map[string]string, len(user)+len(system))
	maps.Copy(merged, user)
	maps.Copy(merged, system)
	return merged
}
