package drivers

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestReconcileIntervalForKind_Default(t *testing.T) {
	saved := ReconcileInterval
	defer func() { ReconcileInterval = saved }()

	ReconcileInterval = DefaultReconcileInterval
	d := ReconcileIntervalForKind("S3Bucket")
	assert.Equal(t, 5*time.Minute, d)
}

func TestReconcileIntervalForKind_EnforcesMinimum(t *testing.T) {
	saved := ReconcileInterval
	defer func() { ReconcileInterval = saved }()

	ReconcileInterval = 1 * time.Second
	d := ReconcileIntervalForKind("S3Bucket")
	assert.Equal(t, MinReconcileInterval, d)
}

func TestReconcileIntervalForKind_CustomInterval(t *testing.T) {
	saved := ReconcileInterval
	defer func() { ReconcileInterval = saved }()

	ReconcileInterval = 10 * time.Minute
	d := ReconcileIntervalForKind("EC2Instance")
	assert.Equal(t, 10*time.Minute, d)
}

func TestReconcileDelayFor_Deterministic(t *testing.T) {
	saved := ReconcileInterval
	defer func() { ReconcileInterval = saved }()
	ReconcileInterval = DefaultReconcileInterval

	// Same key → same delay every time (required for journal replay).
	d1 := ReconcileDelayFor("S3Bucket", "my-key")
	d2 := ReconcileDelayFor("S3Bucket", "my-key")
	assert.Equal(t, d1, d2)
}

func TestReconcileDelayFor_WithinJitterBand(t *testing.T) {
	saved := ReconcileInterval
	defer func() { ReconcileInterval = saved }()
	ReconcileInterval = DefaultReconcileInterval

	interval := ReconcileIntervalForKind("S3Bucket")
	maxDelay := interval + interval/4
	for _, key := range []string{"a", "b", "orders-archive", "vpc~prod", "", "z-z-z"} {
		d := ReconcileDelayFor("S3Bucket", key)
		assert.GreaterOrEqual(t, d, interval, "delay must be at least the interval for key %q", key)
		assert.Less(t, d, maxDelay, "delay must stay under interval+25%% for key %q", key)
	}
}

func TestReconcileDelayFor_KeysDiffer(t *testing.T) {
	saved := ReconcileInterval
	defer func() { ReconcileInterval = saved }()
	ReconcileInterval = DefaultReconcileInterval

	// Distinct keys should generally get distinct jitter, spreading the herd.
	seen := map[time.Duration]bool{}
	for _, key := range []string{"k0", "k1", "k2", "k3", "k4", "k5", "k6", "k7"} {
		seen[ReconcileDelayFor("S3Bucket", key)] = true
	}
	assert.GreaterOrEqual(t, len(seen), 4, "expected jitter to spread keys across distinct delays")
}

func TestReconcileDelayFor_FillsJitterBand(t *testing.T) {
	saved := ReconcileInterval
	defer func() { ReconcileInterval = saved }()
	ReconcileInterval = DefaultReconcileInterval

	// Regression test: an earlier implementation took the uint32 hash modulo
	// the band, capping jitter at ~4.3s (uint32 max in nanoseconds) — a tiny
	// fraction of the 75s band at the default interval, leaving the reconcile
	// herd effectively synchronized. Assert that across many keys the jitter
	// actually reaches deep into the band, not just its first few seconds.
	interval := ReconcileIntervalForKind("S3Bucket")
	band := interval / 4
	maxJitter := time.Duration(0)
	for i := range 200 {
		key := fmt.Sprintf("resource-%d", i)
		j := ReconcileDelayFor("S3Bucket", key) - interval
		if j > maxJitter {
			maxJitter = j
		}
	}
	assert.Greater(t, maxJitter, band/2,
		"200 keys should produce at least one jitter beyond half the band (band=%s, max=%s)", band, maxJitter)
}

func TestDefaultMode_Empty(t *testing.T) {
	assert.Equal(t, types.ModeManaged, DefaultMode(""))
}

func TestDefaultMode_Observed(t *testing.T) {
	assert.Equal(t, types.ModeObserved, DefaultMode(types.ModeObserved))
}

func TestDefaultMode_Managed(t *testing.T) {
	assert.Equal(t, types.ModeManaged, DefaultMode(types.ModeManaged))
}

func TestTagsMatch_Equal(t *testing.T) {
	a := map[string]string{"env": "prod", "team": "platform"}
	b := map[string]string{"env": "prod", "team": "platform"}
	assert.True(t, TagsMatch(a, b))
}

func TestTagsMatch_Different(t *testing.T) {
	a := map[string]string{"env": "prod"}
	b := map[string]string{"env": "staging"}
	assert.False(t, TagsMatch(a, b))
}

func TestTagsMatch_MissingKey(t *testing.T) {
	a := map[string]string{"env": "prod", "team": "platform"}
	b := map[string]string{"env": "prod"}
	assert.False(t, TagsMatch(a, b))
}

func TestTagsMatch_BothNil(t *testing.T) {
	assert.True(t, TagsMatch(nil, nil))
}
