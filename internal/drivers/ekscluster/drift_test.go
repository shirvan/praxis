package ekscluster

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

func inSyncSpecObserved() (EKSClusterSpec, ObservedState) {
	spec := EKSClusterSpec{
		Region:                "us-east-1",
		Name:                  "prod",
		RoleArn:               "arn:aws:iam::123456789012:role/eks",
		SubnetIds:             []string{"subnet-a", "subnet-b"},
		Version:               "1.29",
		EndpointPublicAccess:  true,
		EndpointPrivateAccess: false,
		EnabledLoggingTypes:   []string{"api", "audit"},
		Tags:                  map[string]string{"env": "prod"},
	}
	observed := ObservedState{
		ARN:                   "arn:aws:eks:us-east-1:123456789012:cluster/prod",
		Name:                  "prod",
		Status:                "ACTIVE",
		RoleArn:               "arn:aws:iam::123456789012:role/eks",
		SubnetIds:             []string{"subnet-b", "subnet-a"}, // order-insensitive
		Version:               "1.29",
		EndpointPublicAccess:  true,
		EndpointPrivateAccess: false,
		PublicAccessCidrs:     []string{"0.0.0.0/0"},
		EnabledLoggingTypes:   []string{"audit", "api"}, // order-insensitive
		Tags:                  map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	return spec, observed
}

func TestHasDrift_InSync(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	assert.False(t, HasDrift(spec, observed), "in-sync spec/observed should not drift")
	assert.Empty(t, ComputeFieldDiffs(spec, observed))
}

func TestHasDrift_VersionUpgrade(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.Version = "1.30"
	assert.True(t, HasDrift(spec, observed))
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Contains(t, pathsOf(diffs), "spec.version")
}

func TestHasDrift_EmptyVersionNeverDrifts(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.Version = "" // track AWS default
	observed.Version = "1.31"
	assert.False(t, HasDrift(spec, observed), "an unset desired version tracks the AWS default")
}

func TestHasDrift_EndpointAccess(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.EndpointPrivateAccess = true
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.endpointPrivateAccess")
}

func TestHasDrift_PublicCidrsOnlyWhenPublicEnabled(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.PublicAccessCidrs = []string{"10.0.0.0/8"}
	assert.True(t, HasDrift(spec, observed))

	// When public access is disabled, CIDRs are irrelevant and must not drift.
	spec.EndpointPublicAccess = false
	observed.EndpointPublicAccess = false
	assert.False(t, HasDrift(spec, observed))
}

func TestHasDrift_LoggingTypes(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.EnabledLoggingTypes = []string{"api", "audit", "scheduler"}
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.enabledLoggingTypes")
}

func TestHasDrift_Tags(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.Tags = map[string]string{"env": "staging"}
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "tags.env")
}

func TestComputeFieldDiffs_ImmutableFieldsAnnotated(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.RoleArn = "arn:aws:iam::123456789012:role/other"
	spec.SubnetIds = []string{"subnet-x", "subnet-y"}
	paths := pathsOf(ComputeFieldDiffs(spec, observed))
	assert.Contains(t, paths, "spec.roleArn (immutable, requires replacement)")
	assert.Contains(t, paths, "spec.subnetIds (immutable, requires replacement)")
	// Immutable divergence must reach Converge so the driver can return the
	// explicit replacement-required conflict instead of silently reporting Ready.
	assert.True(t, HasDrift(spec, observed), "immutable fields must surface replacement-required drift")
}

func TestNormalizePublicCidrs(t *testing.T) {
	assert.Equal(t, []string{"0.0.0.0/0"}, normalizePublicCidrs(nil))
	assert.Equal(t, []string{"10.0.0.0/8"}, normalizePublicCidrs([]string{"10.0.0.0/8"}))
}

func TestStringSetEqual(t *testing.T) {
	assert.True(t, stringSetEqual([]string{"a", "b"}, []string{"b", "a"}))
	assert.False(t, stringSetEqual([]string{"a"}, []string{"a", "b"}))
	assert.True(t, stringSetEqual(nil, nil))
}

func pathsOf(diffs []drivers.FieldDiff) []string {
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Path)
	}
	return out
}
