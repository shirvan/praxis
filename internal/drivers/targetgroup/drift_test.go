package targetgroup

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------- HasDrift ----------

func TestHasDrift_NoDrift(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{
		Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Tags: map[string]string{"env": "prod"}, Targets: []Target{{ID: "i-1", Port: 80}},
	})
	observed := ObservedState{
		Protocol: spec.Protocol, Port: spec.Port, VpcId: spec.VpcId, TargetType: spec.TargetType,
		ProtocolVersion: spec.ProtocolVersion, HealthCheck: spec.HealthCheck,
		DeregistrationDelay: spec.DeregistrationDelay, Stickiness: spec.Stickiness,
		Tags: map[string]string{"env": "prod"}, Targets: []Target{{ID: "i-1", Port: 80}},
	}
	assert.False(t, HasDrift(spec, observed))
}

func TestHasDrift_ProtocolChanged(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTPS", Port: 443, VpcId: "vpc-1", TargetType: "instance"})
	observed := ObservedState{Protocol: "HTTP", Port: 443, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_PortChanged(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 8080, VpcId: "vpc-1", TargetType: "instance"})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_HealthCheckChanged(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: HealthCheck{Protocol: "HTTP", Path: "/healthz", Port: "80", HealthyThreshold: 3, UnhealthyThreshold: 3, Interval: 30, Timeout: 5}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck:         HealthCheck{Protocol: "HTTP", Path: "/", Port: "80", HealthyThreshold: 3, UnhealthyThreshold: 3, Interval: 30, Timeout: 5},
		DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_DeregistrationDelayChanged(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance", DeregistrationDelay: 60})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: 300, Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_StickinessChanged(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Stickiness: &Stickiness{Enabled: true, Type: "lb_cookie", Duration: 3600}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Stickiness: &Stickiness{Enabled: false, Type: "lb_cookie", Duration: 3600}, Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_StickinessNilVsSet(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Stickiness: &Stickiness{Enabled: true, Type: "lb_cookie", Duration: 86400}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Stickiness: nil, Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_TargetsAndTags(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance", Tags: map[string]string{"env": "dev"}, Targets: []Target{{ID: "i-1", Port: 80}}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance", HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{"env": "prod"}, Targets: []Target{{ID: "i-2", Port: 80}}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_TagOnlyDrift(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Tags: map[string]string{"env": "staging"}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Tags: map[string]string{"env": "prod"}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_PraxisTagsIgnored(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Tags: map[string]string{"env": "prod"}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Tags: map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~my-tg"}}
	assert.False(t, HasDrift(spec, observed))
}

func TestHasDrift_NilVsEmptyTags(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance"})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Tags: map[string]string{}}
	assert.False(t, HasDrift(spec, observed))
}

func TestHasDrift_VpcIdChanged(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-2", TargetType: "instance"})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, observed))
}

func TestHasDrift_TargetTypeChanged(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "ip"})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, observed))
}

// ---------- ComputeFieldDiffs ----------

func TestComputeFieldDiffs_NoDiffs(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{
		Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Tags: map[string]string{"env": "prod"}, Targets: []Target{{ID: "i-1", Port: 80}},
	})
	observed := ObservedState{
		Protocol: spec.Protocol, Port: spec.Port, VpcId: spec.VpcId, TargetType: spec.TargetType,
		ProtocolVersion: spec.ProtocolVersion, HealthCheck: spec.HealthCheck,
		DeregistrationDelay: spec.DeregistrationDelay, Stickiness: spec.Stickiness,
		Tags: map[string]string{"env": "prod"}, Targets: []Target{{ID: "i-1", Port: 80}},
	}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Empty(t, diffs)
}

func TestComputeFieldDiffs_ProtocolImmutable(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTPS", Port: 443, VpcId: "vpc-1", TargetType: "instance"})
	observed := ObservedState{Protocol: "HTTP", Port: 443, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Len(t, diffs, 1)
	assert.Contains(t, diffs[0].Path, "immutable")
	assert.Contains(t, diffs[0].Path, "spec.protocol")
	assert.Equal(t, "HTTP", diffs[0].OldValue)
	assert.Equal(t, "HTTPS", diffs[0].NewValue)
}

func TestComputeFieldDiffs_PortImmutable(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 8080, VpcId: "vpc-1", TargetType: "instance"})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Len(t, diffs, 1)
	assert.Contains(t, diffs[0].Path, "immutable")
	assert.Contains(t, diffs[0].Path, "spec.port")
}

func TestComputeFieldDiffs_HealthCheck(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: HealthCheck{Protocol: "HTTP", Path: "/new", Port: "80", HealthyThreshold: 3, UnhealthyThreshold: 3, Interval: 30, Timeout: 5}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck:         HealthCheck{Protocol: "HTTP", Path: "/old", Port: "80", HealthyThreshold: 3, UnhealthyThreshold: 3, Interval: 30, Timeout: 5},
		DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.healthCheck", diffs[0].Path)
}

func TestComputeFieldDiffs_DeregistrationDelay(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance", DeregistrationDelay: 60})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: 300, Tags: map[string]string{}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.deregistrationDelay", diffs[0].Path)
	assert.Equal(t, 300, diffs[0].OldValue)
	assert.Equal(t, 60, diffs[0].NewValue)
}

func TestComputeFieldDiffs_Stickiness(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Stickiness: &Stickiness{Enabled: true, Type: "lb_cookie", Duration: 3600}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Stickiness: nil, Tags: map[string]string{}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.stickiness", diffs[0].Path)
}

func TestComputeFieldDiffs_TagAdded(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Tags: map[string]string{"env": "prod", "team": "infra"}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Tags: map[string]string{"env": "prod"}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "tags.team", diffs[0].Path)
	assert.Nil(t, diffs[0].OldValue)
	assert.Equal(t, "infra", diffs[0].NewValue)
}

func TestComputeFieldDiffs_TagRemoved(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Tags: map[string]string{}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Tags: map[string]string{"env": "prod"}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "tags.env", diffs[0].Path)
	assert.Equal(t, "prod", diffs[0].OldValue)
	assert.Nil(t, diffs[0].NewValue)
}

func TestComputeFieldDiffs_TargetAdded(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		Targets: []Target{{ID: "i-1", Port: 80}, {ID: "i-2", Port: 80}}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay,
		Tags: map[string]string{}, Targets: []Target{{ID: "i-1", Port: 80}}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.Len(t, diffs, 1)
	assert.Contains(t, diffs[0].Path, "spec.targets[")
	assert.Nil(t, diffs[0].OldValue)
}

func TestComputeFieldDiffs_MultipleDiffs(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTPS", Port: 443, VpcId: "vpc-1", TargetType: "instance",
		DeregistrationDelay: 60, Tags: map[string]string{"env": "staging"}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance",
		HealthCheck: spec.HealthCheck, DeregistrationDelay: 300, Tags: map[string]string{"env": "prod"}}
	diffs := ComputeFieldDiffs(spec, observed)
	assert.GreaterOrEqual(t, len(diffs), 4)
}

// ---------- Helper functions ----------

func TestStickinessEqual_BothNil(t *testing.T) {
	assert.True(t, stickinessEqual(nil, nil))
}

func TestStickinessEqual_OneNil(t *testing.T) {
	assert.False(t, stickinessEqual(&Stickiness{Enabled: true}, nil))
	assert.False(t, stickinessEqual(nil, &Stickiness{Enabled: true}))
}

func TestStickinessEqual_Equal(t *testing.T) {
	a := &Stickiness{Enabled: true, Type: "lb_cookie", Duration: 3600}
	b := &Stickiness{Enabled: true, Type: "lb_cookie", Duration: 3600}
	assert.True(t, stickinessEqual(a, b))
}

func TestStickinessEqual_Different(t *testing.T) {
	a := &Stickiness{Enabled: true, Type: "lb_cookie", Duration: 3600}
	b := &Stickiness{Enabled: false, Type: "lb_cookie", Duration: 3600}
	assert.False(t, stickinessEqual(a, b))
}

func TestTargetsEqual_MatchingOrder(t *testing.T) {
	a := []Target{{ID: "i-1", Port: 80}, {ID: "i-2", Port: 80}}
	b := []Target{{ID: "i-2", Port: 80}, {ID: "i-1", Port: 80}}
	assert.True(t, targetsEqual(a, b))
}

func TestTargetsEqual_Different(t *testing.T) {
	a := []Target{{ID: "i-1", Port: 80}}
	b := []Target{{ID: "i-2", Port: 80}}
	assert.False(t, targetsEqual(a, b))
}

func TestTagsMatch_BothNil(t *testing.T) {
	assert.True(t, drivers.TagsMatch(nil, nil))
}

func TestTagsMatch_NilVsEmpty(t *testing.T) {
	assert.True(t, drivers.TagsMatch(nil, map[string]string{}))
}

func TestTagsMatch_OnlyPraxisTags(t *testing.T) {
	assert.True(t, drivers.TagsMatch(nil, map[string]string{"praxis:managed-key": "key"}))
}

func TestFilterPraxisTags_Drift(t *testing.T) {
	filtered := drivers.FilterPraxisTags(map[string]string{
		"env":                "prod",
		"praxis:managed-key": "key",
	})
	assert.Equal(t, map[string]string{"env": "prod"}, filtered)
}

func TestFilterPraxisTags_Nil_Drift(t *testing.T) {
	assert.Equal(t, map[string]string{}, drivers.FilterPraxisTags(nil))
}
