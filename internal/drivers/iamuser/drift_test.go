package iamuser

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := IAMUserSpec{
		Path:                "/app/",
		PermissionsBoundary: "arn:aws:iam::123456789012:policy/boundary",
		InlinePolicies: map[string]string{
			"inline": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		},
		ManagedPolicyArns: []string{"arn:aws:iam::123456789012:policy/a", "arn:aws:iam::123456789012:policy/b"},
		Groups:            []string{"dev", "ops"},
		Tags:              map[string]string{"env": "dev"},
	}
	observed := ObservedState{
		Path:                "/app/",
		PermissionsBoundary: "arn:aws:iam::123456789012:policy/boundary",
		InlinePolicies: map[string]string{
			"inline": `{"Statement":[{"Resource":"*","Action":"s3:GetObject","Effect":"Allow"}],"Version":"2012-10-17"}`,
		},
		ManagedPolicyArns: []string{"arn:aws:iam::123456789012:policy/b", "arn:aws:iam::123456789012:policy/a"},
		Groups:            []string{"ops", "dev"},
		Tags:              map[string]string{"env": "dev"},
	}

	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_PathDrift(t *testing.T) {
	assert.True(t, HasDrift(IAMUserSpec{Path: "/app/"}, ObservedState{Path: "/legacy/"}))
}

func TestHasDrift_PermissionsBoundaryDrift(t *testing.T) {
	assert.True(t, HasDrift(IAMUserSpec{PermissionsBoundary: "a"}, ObservedState{PermissionsBoundary: "b"}))
}

func TestHasDrift_InlinePolicyChanged(t *testing.T) {
	assert.True(t, HasDrift(
		IAMUserSpec{InlinePolicies: map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:GetObject"}]}`}},
		ObservedState{InlinePolicies: map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:PutObject"}]}`}},
	))
}

func TestHasDrift_ManagedPolicyAdded(t *testing.T) {
	assert.True(t, HasDrift(
		IAMUserSpec{ManagedPolicyArns: []string{"a", "b"}},
		ObservedState{ManagedPolicyArns: []string{"a"}},
	))
}

func TestHasDrift_GroupOrderIndependent(t *testing.T) {
	assert.False(t, HasDrift(
		IAMUserSpec{Groups: []string{"dev", "ops"}},
		ObservedState{Groups: []string{"ops", "dev"}},
	))
}

func TestHasDrift_TagDrift(t *testing.T) {
	assert.True(t, HasDrift(
		IAMUserSpec{Tags: map[string]string{"env": "prod"}},
		ObservedState{Tags: map[string]string{"env": "dev"}},
	))
}

func TestComputeFieldDiffs_AllMutableFields(t *testing.T) {
	diffs := ComputeFieldDiffs(
		IAMUserSpec{
			Path:                "/app/",
			PermissionsBoundary: "new-boundary",
			InlinePolicies:      map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:GetObject"}]}`},
			ManagedPolicyArns:   []string{"a", "b"},
			Groups:              []string{"dev", "ops"},
			Tags:                map[string]string{"env": "prod"},
		},
		ObservedState{
			Path:                "/legacy/",
			PermissionsBoundary: "old-boundary",
			InlinePolicies:      map[string]string{"inline": `{"Version":"2012-10-17","Statement":[{"Action":"s3:PutObject"}]}`},
			ManagedPolicyArns:   []string{"a"},
			Groups:              []string{"dev"},
			Tags:                map[string]string{"env": "dev", "owner": "alice"},
		},
	)

	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.path", OldValue: "/legacy/", NewValue: "/app/"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.permissionsBoundary", OldValue: "old-boundary", NewValue: "new-boundary"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.managedPolicyArns", OldValue: []string{"a"}, NewValue: []string{"a", "b"}})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.groups", OldValue: []string{"dev"}, NewValue: []string{"dev", "ops"}})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "tags.env", OldValue: "dev", NewValue: "prod"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "tags.owner", OldValue: "alice", NewValue: nil})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.inlinePolicies.inline", OldValue: `{"Statement":[{"Action":"s3:PutObject"}],"Version":"2012-10-17"}`, NewValue: `{"Statement":[{"Action":"s3:GetObject"}],"Version":"2012-10-17"}`})
}
