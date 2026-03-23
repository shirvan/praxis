package iamgroup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewIAMGroupDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestSpecFromObserved_RoundTrip(t *testing.T) {
	obs := ObservedState{
		Arn:               "arn:aws:iam::123456789012:group/app-group",
		GroupId:           "AGPAEXAMPLE",
		GroupName:         "app-group",
		Path:              "/app/",
		InlinePolicies:    map[string]string{"inline-access": `{"Version":"2012-10-17","Statement":[]}`},
		ManagedPolicyArns: []string{"arn:aws:iam::123456789012:policy/app-policy"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.GroupName, spec.GroupName)
	assert.Equal(t, obs.Path, spec.Path)
	assert.Equal(t, map[string]string{"inline-access": `{"Statement":[],"Version":"2012-10-17"}`}, spec.InlinePolicies)
	assert.Equal(t, []string{"arn:aws:iam::123456789012:policy/app-policy"}, spec.ManagedPolicyArns)
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		Arn:       "arn:aws:iam::123456789012:group/app-group",
		GroupId:   "AGPAEXAMPLE",
		GroupName: "app-group",
	})

	assert.Equal(t, "arn:aws:iam::123456789012:group/app-group", outputs.Arn)
	assert.Equal(t, "AGPAEXAMPLE", outputs.GroupId)
	assert.Equal(t, "app-group", outputs.GroupName)
}

func TestStateShape(t *testing.T) {
	state := IAMGroupState{Status: types.StatusReady}
	assert.Equal(t, types.StatusReady, state.Status)
}
