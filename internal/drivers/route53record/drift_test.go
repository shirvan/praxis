package route53record_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/internal/drivers/route53record"
)

func TestHasDrift_NoDrift(t *testing.T) {
	assert.False(t, route53record.HasDrift(
		route53record.RecordSpec{HostedZoneId: "Z123", Name: "example.com", Type: "A", TTL: 300, ResourceRecords: []string{"1.2.3.4"}},
		route53record.ObservedState{HostedZoneId: "Z123", Name: "example.com", Type: "A", TTL: 300, ResourceRecords: []string{"1.2.3.4"}},
	))
}

func TestHasDrift_TTLChange(t *testing.T) {
	assert.True(t, route53record.HasDrift(
		route53record.RecordSpec{HostedZoneId: "Z123", Name: "example.com", Type: "A", TTL: 600, ResourceRecords: []string{"1.2.3.4"}},
		route53record.ObservedState{HostedZoneId: "Z123", Name: "example.com", Type: "A", TTL: 300, ResourceRecords: []string{"1.2.3.4"}},
	))
}

func TestHasDrift_ResourceRecordChange(t *testing.T) {
	assert.True(t, route53record.HasDrift(
		route53record.RecordSpec{HostedZoneId: "Z123", Name: "example.com", Type: "A", TTL: 300, ResourceRecords: []string{"5.6.7.8"}},
		route53record.ObservedState{HostedZoneId: "Z123", Name: "example.com", Type: "A", TTL: 300, ResourceRecords: []string{"1.2.3.4"}},
	))
}

func TestHasDrift_AliasMatch(t *testing.T) {
	alias := &route53record.AliasTarget{HostedZoneId: "Z999", DNSName: "elb.example.com", EvaluateTargetHealth: true}
	assert.False(t, route53record.HasDrift(
		route53record.RecordSpec{HostedZoneId: "Z123", Name: "example.com", Type: "A", AliasTarget: alias},
		route53record.ObservedState{HostedZoneId: "Z123", Name: "example.com", Type: "A", AliasTarget: alias},
	))
}

func TestComputeFieldDiffs_Multiple(t *testing.T) {
	diffs := route53record.ComputeFieldDiffs(
		route53record.RecordSpec{HostedZoneId: "Z123", Name: "example.com", Type: "A", TTL: 600, ResourceRecords: []string{"5.6.7.8"}, HealthCheckId: "hc-1"},
		route53record.ObservedState{HostedZoneId: "Z123", Name: "example.com", Type: "A", TTL: 300, ResourceRecords: []string{"1.2.3.4"}},
	)
	paths := map[string]bool{}
	for _, diff := range diffs {
		paths[diff.Path] = true
	}
	assert.True(t, paths["spec.ttl"])
	assert.True(t, paths["spec.resourceRecords"])
	assert.True(t, paths["spec.healthCheckId"])
}

func TestComputeFieldDiffs_ImmutableFields(t *testing.T) {
	diffs := route53record.ComputeFieldDiffs(
		route53record.RecordSpec{HostedZoneId: "Z123", Name: "new.example.com", Type: "CNAME", TTL: 300, ResourceRecords: []string{"target.com"}},
		route53record.ObservedState{HostedZoneId: "Z123", Name: "old.example.com", Type: "A", TTL: 300, ResourceRecords: []string{"target.com"}},
	)
	found := map[string]bool{}
	for _, diff := range diffs {
		found[diff.Path] = true
	}
	assert.True(t, found["spec.name (immutable, ignored)"])
	assert.True(t, found["spec.type (immutable, ignored)"])
}
