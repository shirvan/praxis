package route53healthcheck_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/internal/drivers/route53healthcheck"
)

func TestHasDrift_NoDrift(t *testing.T) {
	assert.False(t, route53healthcheck.HasDrift(
		route53healthcheck.HealthCheckSpec{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{"env": "dev"}},
		route53healthcheck.ObservedState{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{"env": "dev"}},
	))
}

func TestHasDrift_PortChange(t *testing.T) {
	assert.True(t, route53healthcheck.HasDrift(
		route53healthcheck.HealthCheckSpec{Type: "HTTP", IPAddress: "1.2.3.4", Port: 8080, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{}},
		route53healthcheck.ObservedState{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{}},
	))
}

func TestHasDrift_TagChange(t *testing.T) {
	assert.True(t, route53healthcheck.HasDrift(
		route53healthcheck.HealthCheckSpec{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{"env": "prod"}},
		route53healthcheck.ObservedState{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{"env": "dev"}},
	))
}

func TestHasDrift_PraxisTagsIgnored(t *testing.T) {
	assert.False(t, route53healthcheck.HasDrift(
		route53healthcheck.HealthCheckSpec{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{"env": "dev"}},
		route53healthcheck.ObservedState{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{"env": "dev", "praxis:managed-key": "hc-http"}},
	))
}

func TestComputeFieldDiffs_ImmutableType(t *testing.T) {
	diffs := route53healthcheck.ComputeFieldDiffs(
		route53healthcheck.HealthCheckSpec{Type: "HTTPS", IPAddress: "1.2.3.4", Port: 443, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{}},
		route53healthcheck.ObservedState{Type: "HTTP", IPAddress: "1.2.3.4", Port: 443, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{}},
	)
	found := false
	for _, diff := range diffs {
		if diff.Path == "spec.type (immutable, ignored)" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestComputeFieldDiffs_MultipleChanges(t *testing.T) {
	diffs := route53healthcheck.ComputeFieldDiffs(
		route53healthcheck.HealthCheckSpec{Type: "HTTP", IPAddress: "5.6.7.8", Port: 8080, FailureThreshold: 5, RequestInterval: 30, Tags: map[string]string{"env": "prod"}},
		route53healthcheck.ObservedState{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{"env": "dev"}},
	)
	paths := map[string]bool{}
	for _, diff := range diffs {
		paths[diff.Path] = true
	}
	assert.True(t, paths["spec.ipAddress"])
	assert.True(t, paths["spec.port"])
	assert.True(t, paths["spec.failureThreshold"])
	assert.True(t, paths["tags.env"])
}

func TestComputeFieldDiffs_DefaultPortApplied(t *testing.T) {
	// When port is 0 in spec, normalizeHealthCheckSpec defaults it to 80 for HTTP
	diffs := route53healthcheck.ComputeFieldDiffs(
		route53healthcheck.HealthCheckSpec{Type: "HTTP", IPAddress: "1.2.3.4", FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{}},
		route53healthcheck.ObservedState{Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{}},
	)
	assert.Empty(t, diffs)
}
