package iamrole

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewIAMRoleDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestSpecFromObserved_RoundTrip(t *testing.T) {
	obs := ObservedState{
		Arn:                      "arn:aws:iam::123456789012:role/app-role",
		RoleId:                   "AROAEXAMPLE",
		RoleName:                 "app-role",
		Path:                     "/app/",
		AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[]}`,
		Description:              "app role",
		MaxSessionDuration:       7200,
		PermissionsBoundary:      "arn:aws:iam::123456789012:policy/boundary",
		InlinePolicies:           map[string]string{"inline": `{"Version":"2012-10-17","Statement":[]}`},
		ManagedPolicyArns:        []string{"arn:aws:iam::123456789012:policy/b", "arn:aws:iam::123456789012:policy/a"},
		Tags:                     map[string]string{"env": "dev", "praxis:managed-key": "ignore-me"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.RoleName, spec.RoleName)
	assert.Equal(t, obs.Path, spec.Path)
	assert.Equal(t, obs.Description, spec.Description)
	assert.Equal(t, obs.MaxSessionDuration, spec.MaxSessionDuration)
	assert.Equal(t, obs.PermissionsBoundary, spec.PermissionsBoundary)
	assert.Equal(t, `{"Statement":[],"Version":"2012-10-17"}`, spec.AssumeRolePolicyDocument)
	assert.Equal(t, `{"Statement":[],"Version":"2012-10-17"}`, spec.InlinePolicies["inline"])
	assert.Equal(t, []string{"arn:aws:iam::123456789012:policy/a", "arn:aws:iam::123456789012:policy/b"}, spec.ManagedPolicyArns)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		Arn:      "arn:aws:iam::123456789012:role/app-role",
		RoleId:   "AROAEXAMPLE",
		RoleName: "app-role",
	})

	assert.Equal(t, "arn:aws:iam::123456789012:role/app-role", outputs.Arn)
	assert.Equal(t, "AROAEXAMPLE", outputs.RoleId)
	assert.Equal(t, "app-role", outputs.RoleName)
}

func TestDiffStringSets(t *testing.T) {
	add, remove := diffStringSets([]string{"a", "b"}, []string{"b", "c"})
	assert.Equal(t, []string{"a"}, add)
	assert.Equal(t, []string{"c"}, remove)
}

func TestStateShape(t *testing.T) {
	state := IAMRoleState{Status: types.StatusReady}
	assert.Equal(t, types.StatusReady, state.Status)
}