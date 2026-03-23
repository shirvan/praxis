package iamrole

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := IAMRoleSpec{
		AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		Description:              "app role",
		MaxSessionDuration:       3600,
		PermissionsBoundary:      "arn:aws:iam::123456789012:policy/boundary",
		InlinePolicies:           map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:GetObject","Effect":"Allow","Resource":"*"}]}`},
		ManagedPolicyArns:        []string{"arn:aws:iam::123456789012:policy/a", "arn:aws:iam::123456789012:policy/b"},
		Tags:                     map[string]string{"env": "dev"},
	}
	observed := ObservedState{
		AssumeRolePolicyDocument: `{"Statement":[{"Action":"sts:AssumeRole","Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"}}],"Version":"2012-10-17"}`,
		Description:              "app role",
		MaxSessionDuration:       3600,
		PermissionsBoundary:      "arn:aws:iam::123456789012:policy/boundary",
		InlinePolicies:           map[string]string{"inline": `{"Statement":[{"Resource":"*","Effect":"Allow","Action":"s3:GetObject"}],"Version":"2012-10-17"}`},
		ManagedPolicyArns:        []string{"arn:aws:iam::123456789012:policy/b", "arn:aws:iam::123456789012:policy/a"},
		Tags:                     map[string]string{"env": "dev"},
	}

	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_TrustPolicyURLEncoded(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	encoded := url.QueryEscape(doc)
	assert.False(t, HasDrift(IAMRoleSpec{AssumeRolePolicyDocument: doc, MaxSessionDuration: 3600}, ObservedState{AssumeRolePolicyDocument: encoded, MaxSessionDuration: 3600}))
}

func TestHasDrift_InlinePolicyChanged(t *testing.T) {
	assert.True(t, HasDrift(
		IAMRoleSpec{AssumeRolePolicyDocument: `{}`, MaxSessionDuration: 3600, InlinePolicies: map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:GetObject"}]}`}},
		ObservedState{AssumeRolePolicyDocument: `{}`, MaxSessionDuration: 3600, InlinePolicies: map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:PutObject"}]}`}},
	))
}

func TestHasDrift_ManagedPolicyAdded(t *testing.T) {
	assert.True(t, HasDrift(
		IAMRoleSpec{AssumeRolePolicyDocument: `{}`, MaxSessionDuration: 3600, ManagedPolicyArns: []string{"a", "b"}},
		ObservedState{AssumeRolePolicyDocument: `{}`, MaxSessionDuration: 3600, ManagedPolicyArns: []string{"a"}},
	))
}

func TestHasDrift_TagDrift(t *testing.T) {
	assert.True(t, HasDrift(
		IAMRoleSpec{AssumeRolePolicyDocument: `{}`, MaxSessionDuration: 3600, Tags: map[string]string{"env": "prod"}},
		ObservedState{AssumeRolePolicyDocument: `{}`, MaxSessionDuration: 3600, Tags: map[string]string{"env": "dev"}},
	))
}

func TestComputeFieldDiffs_AllFields(t *testing.T) {
	diffs := ComputeFieldDiffs(
		IAMRoleSpec{
			Path:                     "/app/",
			AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[{"Action":"sts:AssumeRole","Effect":"Allow"}]}`,
			Description:              "new",
			MaxSessionDuration:       7200,
			PermissionsBoundary:      "new-boundary",
			InlinePolicies:           map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:GetObject"}]}`},
			ManagedPolicyArns:        []string{"a", "b"},
			Tags:                     map[string]string{"env": "prod"},
		},
		ObservedState{
			Path:                     "/legacy/",
			AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[{"Action":"sts:TagSession","Effect":"Allow"}]}`,
			Description:              "old",
			MaxSessionDuration:       3600,
			PermissionsBoundary:      "old-boundary",
			InlinePolicies:           map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:PutObject"}]}`},
			ManagedPolicyArns:        []string{"a"},
			Tags:                     map[string]string{"env": "dev", "owner": "alice"},
		},
	)

	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.path (immutable, ignored)", OldValue: "/legacy/", NewValue: "/app/"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.description", OldValue: "old", NewValue: "new"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.maxSessionDuration", OldValue: int32(3600), NewValue: int32(7200)})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.permissionsBoundary", OldValue: "old-boundary", NewValue: "new-boundary"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.managedPolicyArns", OldValue: []string{"a"}, NewValue: []string{"a", "b"}})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "tags.env", OldValue: "dev", NewValue: "prod"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "tags.owner", OldValue: "alice", NewValue: nil})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.inlinePolicies.inline", OldValue: `{"Statement":[{"Action":"s3:PutObject"}],"Version":"2012-10-17"}`, NewValue: `{"Statement":[{"Action":"s3:GetObject"}],"Version":"2012-10-17"}`})
}

func TestNormalizePolicyDocument(t *testing.T) {
	doc := `{
		"Version": "2012-10-17",
		"Statement": [{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"Service":"ec2.amazonaws.com"}}]
	}`
	encoded := url.QueryEscape(doc)
	normalized := normalizePolicyDocument(encoded)
	require.JSONEq(t, doc, normalized)
}