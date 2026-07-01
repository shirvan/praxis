package ekscluster

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewEKSClusterDriver(nil)
	assert.Equal(t, "EKSCluster", drv.ServiceName())
}

func baseSpec() EKSClusterSpec {
	return EKSClusterSpec{
		Region:    "us-east-1",
		Name:      "prod",
		RoleArn:   "arn:aws:iam::123456789012:role/eks",
		SubnetIds: []string{"subnet-a", "subnet-b"},
	}
}

func TestApplyDefaults_TrimsAndInitializes(t *testing.T) {
	spec := applyDefaults(EKSClusterSpec{
		Region:  "  us-east-1  ",
		Name:    "  prod  ",
		RoleArn: "  arn:aws:iam::123456789012:role/eks  ",
		Version: "  1.29  ",
	})
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "arn:aws:iam::123456789012:role/eks", spec.RoleArn)
	assert.Equal(t, "1.29", spec.Version)
	assert.NotNil(t, spec.Tags)
}

func TestValidateSpec(t *testing.T) {
	assert.NoError(t, validateSpec(baseSpec()))

	noRegion := baseSpec()
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := baseSpec()
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	noRole := baseSpec()
	noRole.RoleArn = ""
	assert.Error(t, validateSpec(noRole))

	oneSubnet := baseSpec()
	oneSubnet.SubnetIds = []string{"subnet-a"}
	assert.Error(t, validateSpec(oneSubnet), "EKS requires at least two subnets")

	badLog := baseSpec()
	badLog.EnabledLoggingTypes = []string{"api", "bogus"}
	assert.Error(t, validateSpec(badLog))

	goodLog := baseSpec()
	goodLog.EnabledLoggingTypes = []string{"api", "audit", "scheduler"}
	assert.NoError(t, validateSpec(goodLog))
}

func TestSpecFromObserved_FiltersPraxisTags(t *testing.T) {
	obs := ObservedState{
		Name:                 "prod",
		RoleArn:              "arn:aws:iam::123456789012:role/eks",
		SubnetIds:            []string{"subnet-a", "subnet-b"},
		Version:              "1.29",
		EndpointPublicAccess: true,
		EnabledLoggingTypes:  []string{"api"},
		Tags:                 map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "1.29", spec.Version)
	assert.Equal(t, []string{"subnet-a", "subnet-b"}, spec.SubnetIds)
	assert.Equal(t, []string{"api"}, spec.EnabledLoggingTypes)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		ARN:             "arn:aws:eks:us-east-1:123456789012:cluster/prod",
		Name:            "prod",
		Status:          "ACTIVE",
		Version:         "1.29",
		PlatformVersion: "eks.5",
		Endpoint:        "https://example.eks.amazonaws.com",
	})
	assert.Equal(t, "arn:aws:eks:us-east-1:123456789012:cluster/prod", out.ARN)
	assert.Equal(t, "prod", out.Name)
	assert.Equal(t, "ACTIVE", out.Status)
	assert.Equal(t, "1.29", out.Version)
	assert.Equal(t, "eks.5", out.PlatformVersion)
	assert.Equal(t, "https://example.eks.amazonaws.com", out.Endpoint)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestTagDiff_AddsRemovesPreservesManagedKey(t *testing.T) {
	desired := map[string]string{"env": "prod", "team": "core"}
	observed := map[string]string{"env": "dev", "old": "1", "praxis:managed-key": "k"}
	toAdd, toRemove := tagDiff(desired, observed, "k")

	assert.Equal(t, "prod", toAdd["env"], "changed value should be re-tagged")
	assert.Equal(t, "core", toAdd["team"], "new tag should be added")
	assert.NotContains(t, toAdd, "praxis:managed-key", "managed key already present, not re-added")
	assert.Equal(t, []string{"old"}, toRemove, "stale tag should be removed; managed key preserved")
}

func TestTagDiff_ManagedKeyNeverDiffed(t *testing.T) {
	// The managed-key marker is synthesized on both the desired and observed
	// sides, so it must never surface as an add or a removal — reconciling it as
	// drift would fight the create-time tagging on every pass.
	toAdd, toRemove := tagDiff(map[string]string{}, map[string]string{}, "us-east-1~prod")
	assert.NotContains(t, toAdd, "praxis:managed-key")
	assert.Empty(t, toAdd)
	assert.Empty(t, toRemove)
}
