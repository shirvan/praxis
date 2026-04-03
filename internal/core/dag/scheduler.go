package dag

// ---------------------------------------------------------------------------
// Schedule — runtime dispatch queries over a validated Graph
// ---------------------------------------------------------------------------
//
// Schedule answers runtime scheduling questions against a validated Graph.
// It is the bridge between the static DAG and the dynamic execution state
// maintained by the Restate orchestrator.
//
// The orchestrator's needs are deliberately narrow:
//
//   - Given what has already completed and what has already been dispatched,
//     which resources are ready to start now? (eager dispatch for parallelism)
//   - If one resource fails, which downstream resources can no longer proceed?
//     (failure propagation to skip unreachable resources)
//
// Keeping this logic separate from Graph avoids overloading the graph type with
// workflow-specific state while still reusing the graph's deterministic order.
//
// The Schedule type is intentionally stateless — all execution state (completed,
// dispatched) is passed in by the caller. This makes it safe for concurrent use
// and trivially testable.
type Schedule struct {
	graph *Graph
}

// NewSchedule creates a scheduler over an already-validated graph.
// The graph must have passed cycle detection and referential integrity checks.
func NewSchedule(g *Graph) *Schedule {
	return &Schedule{graph: g}
}

// Ready returns every resource whose dependencies are satisfied and which has
// not already been completed or dispatched.
//
// This is the core of Praxis' "eager dispatch" strategy. Instead of waiting for
// an entire topological level to complete before starting the next level, Ready
// returns ALL resources that can run right now. This enables maximum parallelism:
// if resources A, B, C have no dependencies, all three are dispatched in the
// first call. When A completes and resource D only depends on A, D is returned
// in the next call even if B and C are still running.
//
// The method walks the graph's stable topological order instead of iterating over
// a map directly. That gives callers a deterministic dispatch order even when
// multiple resources become ready at the same time.
//
// Parameters:
//   - completed: resources that have finished successfully.
//   - dispatched: resources that are currently in-flight (sent to a driver but
//     not yet completed). These are excluded to prevent double-dispatch.
func (s *Schedule) Ready(completed, dispatched map[string]bool) []string {
	if s == nil || s.graph == nil {
		return nil
	}

	ready := make([]string, 0)
	for _, name := range s.graph.TopologicalOrder() {
		// Skip resources that are already done or in-flight.
		if completed[name] || dispatched[name] {
			continue
		}
		// Check if every dependency of this resource has completed.
		if s.graph.DependenciesMet(name, completed) {
			ready = append(ready, name)
		}
	}
	return ready
}

// AffectedByFailure returns all resources that transitively depend on the failed
// resource. These resources can never have their dependencies met and should be
// marked as skipped by the orchestrator.
//
// Algorithm: BFS from the failed resource's direct dependents, following reverse
// edges transitively. A visited set prevents re-processing in diamond dependency
// patterns (where two paths lead to the same downstream resource).
//
// The failed resource itself is not included in the result. The result is
// filtered through the graph's topological order so callers receive a
// deterministic list where nearer dependents appear before deeper descendants.
func (s *Schedule) AffectedByFailure(failed string) []string {
	if s == nil || s.graph == nil {
		return nil
	}

	// BFS: seed the queue with the direct dependents of the failed resource.
	affectedSet := make(map[string]bool)
	queue := append([]string(nil), s.graph.Dependents(failed)...)

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if affectedSet[current] {
			continue // Already visited — handles diamond dependencies.
		}
		affectedSet[current] = true
		// Continue BFS into the current node's dependents.
		queue = append(queue, s.graph.Dependents(current)...)
	}

	if len(affectedSet) == 0 {
		return nil
	}

	// Filter through topological order for deterministic output.
	affected := make([]string, 0, len(affectedSet))
	for _, name := range s.graph.TopologicalOrder() {
		if affectedSet[name] {
			affected = append(affected, name)
		}
	}
	return affected
}
