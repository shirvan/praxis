package iamgroup

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := IAMGroupSpec{
		Path:              "/app/",
		InlinePolicies:    map[string]string{"inline-access": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`},
		ManagedPolicyArns: []string{"arn:aws:iam::123456789012:policy/example"},
	}
	observed := ObservedState{
		Path:              "/app/",
		InlinePolicies:    map[string]string{"inline-access": `{"Statement":[{"Action":"s3:GetObject","Effect":"Allow","Resource":"*"}],"Version":"2012-10-17"}`},
		ManagedPolicyArns: []string{"arn:aws:iam::123456789012:policy/example"},
	}

	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_PathDrift(t *testing.T) {
	assert.True(t, HasDrift(IAMGroupSpec{Path: "/app/"}, ObservedState{Path: "/legacy/"}))
}

func TestHasDrift_InlinePolicyChanged(t *testing.T) {
	assert.True(t, HasDrift(
		IAMGroupSpec{InlinePolicies: map[string]string{"inline-access": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`}},
		ObservedState{InlinePolicies: map[string]string{"inline-access": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"*"}]}`}},
	))
}

func TestHasDrift_ManagedPolicyOrderIndependent(t *testing.T) {
	assert.False(t, HasDrift(
		IAMGroupSpec{ManagedPolicyArns: []string{"arn:2", "arn:1"}},
		ObservedState{ManagedPolicyArns: []string{"arn:1", "arn:2"}},
	))
}

func TestComputeFieldDiffs(t *testing.T) {
	diffs := ComputeFieldDiffs(
		IAMGroupSpec{
			Path:              "/app/",
			InlinePolicies:    map[string]string{"new": `{"Version":"2012-10-17","Statement":[]}`},
			ManagedPolicyArns: []string{"arn:2"},
		},
		ObservedState{
			Path:              "/legacy/",
			InlinePolicies:    map[string]string{"old": `{"Version":"2012-10-17","Statement":[]}`},
			ManagedPolicyArns: []string{"arn:1"},
		},
	)

	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.path", OldValue: "/legacy/", NewValue: "/app/"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.inlinePolicies.new", OldValue: nil, NewValue: `{"Statement":[],"Version":"2012-10-17"}`})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.inlinePolicies.old", OldValue: `{"Statement":[],"Version":"2012-10-17"}`, NewValue: nil})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.managedPolicyArns", OldValue: []string{"arn:1"}, NewValue: []string{"arn:2"}})
}

func TestNormalizePolicyDocument(t *testing.T) {
	doc := `{
		"Version": "2012-10-17",
		"Statement": [
			{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}
		]
	}`
	encoded := url.QueryEscape(doc)
	normalized := normalizePolicyDocument(encoded)
	require.JSONEq(t, doc, normalized)
}
