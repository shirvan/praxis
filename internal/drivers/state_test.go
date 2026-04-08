package drivers

import (
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
