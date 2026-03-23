package iamuser

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewIAMUserDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestSpecFromObserved_RoundTrip(t *testing.T) {
	obs := ObservedState{
		Arn:                 "arn:aws:iam::123456789012:user/app-user",
		UserId:              "AIDAEXAMPLE",
		UserName:            "app-user",
		Path:                "/app/",
		PermissionsBoundary: "arn:aws:iam::123456789012:policy/boundary",
		InlinePolicies: map[string]string{
			"inline": `{"Version":"2012-10-17","Statement":[]}`,
		},
		ManagedPolicyArns: []string{"arn:aws:iam::123456789012:policy/b", "arn:aws:iam::123456789012:policy/a"},
		Groups:            []string{"ops", "dev"},
		Tags:              map[string]string{"env": "dev", "praxis:managed-key": "ignore-me"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.UserName, spec.UserName)
	assert.Equal(t, obs.Path, spec.Path)
	assert.Equal(t, obs.PermissionsBoundary, spec.PermissionsBoundary)
	assert.Equal(t, `{"Statement":[],"Version":"2012-10-17"}`, spec.InlinePolicies["inline"])
	assert.Equal(t, []string{"arn:aws:iam::123456789012:policy/a", "arn:aws:iam::123456789012:policy/b"}, spec.ManagedPolicyArns)
	assert.Equal(t, []string{"dev", "ops"}, spec.Groups)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		Arn:      "arn:aws:iam::123456789012:user/app-user",
		UserId:   "AIDAEXAMPLE",
		UserName: "app-user",
	})

	assert.Equal(t, "arn:aws:iam::123456789012:user/app-user", outputs.Arn)
	assert.Equal(t, "AIDAEXAMPLE", outputs.UserId)
	assert.Equal(t, "app-user", outputs.UserName)
}

func TestStateShape(t *testing.T) {
	state := IAMUserState{Status: types.StatusReady}
	assert.Equal(t, types.StatusReady, state.Status)
}
