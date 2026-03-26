package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewDashboardDriver(nil)
	assert.Equal(t, "Dashboard", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		DashboardArn:  "arn:aws:cloudwatch::123456789012:dashboard/my-dash",
		DashboardName: "my-dash",
		DashboardBody: `{"widgets":[]}`,
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.DashboardName, spec.DashboardName)
	assert.Equal(t, obs.DashboardBody, spec.DashboardBody)
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		DashboardArn:  "arn:aws:cloudwatch::123456789012:dashboard/my-dash",
		DashboardName: "my-dash",
		DashboardBody: `{"widgets":[]}`,
	})

	assert.Equal(t, "arn:aws:cloudwatch::123456789012:dashboard/my-dash", out.DashboardArn)
	assert.Equal(t, "my-dash", out.DashboardName)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(DashboardSpec{DashboardName: "  my-dash  ", Region: "  us-east-1  ", DashboardBody: `  {"widgets":[]}  `})
	assert.Equal(t, "my-dash", spec.DashboardName)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, `{"widgets":[]}`, spec.DashboardBody)
}

func TestValidateSpec(t *testing.T) {
	base := applyDefaults(DashboardSpec{
		Region:        "us-east-1",
		DashboardName: "my-dash",
		DashboardBody: `{"widgets":[]}`,
	})
	assert.NoError(t, validateSpec(base))

	noRegion := base
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := base
	noName.DashboardName = ""
	assert.Error(t, validateSpec(noName))

	noBody := base
	noBody.DashboardBody = ""
	assert.Error(t, validateSpec(noBody))
}
