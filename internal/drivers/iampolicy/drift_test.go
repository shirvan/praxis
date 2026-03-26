package iampolicy

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := IAMPolicySpec{
		PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		Tags:           map[string]string{"env": "dev"},
	}
	observed := ObservedState{
		PolicyDocument: `{"Statement":[{"Resource":"*","Action":"s3:GetObject","Effect":"Allow"}],"Version":"2012-10-17"}`,
		Tags:           map[string]string{"env": "dev"},
	}

	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_PolicyDocumentDrift(t *testing.T) {
	assert.True(t, HasDrift(
		IAMPolicySpec{PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`},
		ObservedState{PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"*"}]}`},
	))
}

func TestHasDrift_PolicyDocumentURLEncoded(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`
	encoded := url.QueryEscape(doc)

	assert.False(t, HasDrift(IAMPolicySpec{PolicyDocument: doc}, ObservedState{PolicyDocument: encoded}))
}

func TestHasDrift_TagDrift(t *testing.T) {
	assert.True(t, HasDrift(
		IAMPolicySpec{PolicyDocument: `{}`, Tags: map[string]string{"env": "prod"}},
		ObservedState{PolicyDocument: `{}`, Tags: map[string]string{"env": "dev"}},
	))
}

func TestComputeFieldDiffs_ImmutableFields(t *testing.T) {
	diffs := ComputeFieldDiffs(
		IAMPolicySpec{Path: "/app/", Description: "new", PolicyDocument: `{}`, Tags: map[string]string{}},
		ObservedState{Path: "/ops/", Description: "old", PolicyDocument: `{}`, Tags: map[string]string{}},
	)

	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.path (immutable, ignored)", OldValue: "/ops/", NewValue: "/app/"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.description (immutable, ignored)", OldValue: "old", NewValue: "new"})
}

func TestComputeFieldDiffs_DocumentAndTags(t *testing.T) {
	diffs := ComputeFieldDiffs(
		IAMPolicySpec{
			PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
			Tags:           map[string]string{"env": "prod", "team": "platform"},
		},
		ObservedState{
			PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"*"}]}`,
			Tags:           map[string]string{"env": "dev", "owner": "alice"},
		},
	)

	assert.ElementsMatch(t, []FieldDiffEntry{
		{Path: "spec.policyDocument", OldValue: `{"Statement":[{"Action":"s3:PutObject","Effect":"Allow","Resource":"*"}],"Version":"2012-10-17"}`, NewValue: `{"Statement":[{"Action":"s3:GetObject","Effect":"Allow","Resource":"*"}],"Version":"2012-10-17"}`},
		{Path: "tags.env", OldValue: "dev", NewValue: "prod"},
		{Path: "tags.team", OldValue: nil, NewValue: "platform"},
		{Path: "tags.owner", OldValue: "alice", NewValue: nil},
	}, diffs)
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
