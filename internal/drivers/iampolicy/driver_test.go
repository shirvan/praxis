package iampolicy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewIAMPolicyDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestSpecFromObserved_RoundTrip(t *testing.T) {
	obs := ObservedState{
		Arn:            "arn:aws:iam::123456789012:policy/app-policy",
		PolicyId:       "ANPAEXAMPLE",
		PolicyName:     "app-policy",
		Path:           "/app/",
		Description:    "app policy",
		PolicyDocument: `{"Version":"2012-10-17","Statement":[]}`,
		Tags:           map[string]string{"env": "dev", "praxis:managed-key": "ignore-me"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.PolicyName, spec.PolicyName)
	assert.Equal(t, obs.Path, spec.Path)
	assert.Equal(t, obs.Description, spec.Description)
	assert.Equal(t, `{"Statement":[],"Version":"2012-10-17"}`, spec.PolicyDocument)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		Arn:        "arn:aws:iam::123456789012:policy/app-policy",
		PolicyId:   "ANPAEXAMPLE",
		PolicyName: "app-policy",
	})

	assert.Equal(t, "arn:aws:iam::123456789012:policy/app-policy", outputs.Arn)
	assert.Equal(t, "ANPAEXAMPLE", outputs.PolicyId)
	assert.Equal(t, "app-policy", outputs.PolicyName)
}

func TestFindOldestNonDefault(t *testing.T) {
	now := time.Now().UTC()
	versions := []PolicyVersionInfo{
		{VersionID: "v4", IsDefaultVersion: true, CreateDate: now},
		{VersionID: "v1", CreateDate: now.Add(-4 * time.Hour)},
		{VersionID: "v2", CreateDate: now.Add(-3 * time.Hour)},
	}

	assert.Equal(t, "v1", findOldestNonDefault(versions))
}

func TestStateShape(t *testing.T) {
	state := IAMPolicyState{Status: types.StatusReady}
	assert.Equal(t, types.StatusReady, state.Status)
}
