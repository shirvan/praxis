package dag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScheduleReady_AllIndependentResourcesReadyImmediately(t *testing.T) {
	g := newTestGraph(t,
		newNode("alpha"),
		newNode("beta"),
		newNode("gamma"),
	)
	schedule := NewSchedule(g)

	assert.Equal(t, []string{"alpha", "beta", "gamma"}, schedule.Ready(map[string]bool{}, map[string]bool{}))
}

func TestScheduleReady_LinearChain_ReadinessProgressesOneStepAtATime(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "db"),
		newNode("db", "network"),
		newNode("network"),
	)
	schedule := NewSchedule(g)

	assert.Equal(t, []string{"network"}, schedule.Ready(map[string]bool{}, map[string]bool{}))
	assert.Equal(t, []string{"db"}, schedule.Ready(map[string]bool{"network": true}, map[string]bool{"network": true}))
	assert.Equal(t, []string{"app"}, schedule.Ready(
		map[string]bool{"network": true, "db": true},
		map[string]bool{"network": true, "db": true},
	))
}

func TestScheduleReady_DiamondGraph_UnlocksParallelWork(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "db", "queue"),
		newNode("db", "network"),
		newNode("queue", "network"),
		newNode("network"),
	)
	schedule := NewSchedule(g)

	assert.Equal(t, []string{"network"}, schedule.Ready(map[string]bool{}, map[string]bool{}))
	assert.Equal(t, []string{"db", "queue"}, schedule.Ready(
		map[string]bool{"network": true},
		map[string]bool{"network": true},
	))
	assert.Equal(t, []string{"app"}, schedule.Ready(
		map[string]bool{"network": true, "db": true, "queue": true},
		map[string]bool{"network": true, "db": true, "queue": true},
	))
}

func TestScheduleReady_PartialCompletion_OnlyExactMatchesReturned(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api", "assets"),
		newNode("api", "db"),
		newNode("assets", "network"),
		newNode("db", "network"),
		newNode("network"),
	)
	schedule := NewSchedule(g)

	ready := schedule.Ready(
		map[string]bool{"network": true},
		map[string]bool{"network": true, "assets": true},
	)
	assert.Equal(t, []string{"db"}, ready)
}

func TestScheduleAffectedByFailure_ReturnsTransitiveDependents(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api", "assets"),
		newNode("api", "db"),
		newNode("assets", "network"),
		newNode("db", "network"),
		newNode("network"),
	)
	schedule := NewSchedule(g)

	assert.Equal(t, []string{"assets", "db", "api", "frontend"}, schedule.AffectedByFailure("network"))
	assert.Equal(t, []string{"api", "frontend"}, schedule.AffectedByFailure("db"))
	assert.Empty(t, schedule.AffectedByFailure("frontend"))
}

func TestScheduleReadyForDelete_UsesReverseDependencyOrder(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api", "assets"),
		newNode("api", "db"),
		newNode("assets", "network"),
		newNode("db", "network"),
		newNode("network"),
	)
	schedule := NewSchedule(g)

	assert.Equal(t, []string{"frontend"}, schedule.ReadyForDelete(map[string]bool{}, map[string]bool{}))
	assert.Equal(t, []string{"api", "assets"}, schedule.ReadyForDelete(
		map[string]bool{"frontend": true},
		map[string]bool{"frontend": true},
	))
	assert.Equal(t, []string{"db"}, schedule.ReadyForDelete(
		map[string]bool{"frontend": true, "api": true, "assets": true},
		map[string]bool{"frontend": true, "api": true, "assets": true},
	))
	assert.Equal(t, []string{"network"}, schedule.ReadyForDelete(
		map[string]bool{"frontend": true, "api": true, "assets": true, "db": true},
		map[string]bool{"frontend": true, "api": true, "assets": true, "db": true},
	))
}
