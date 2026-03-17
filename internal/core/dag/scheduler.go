package dag

// Schedule answers runtime scheduling questions against a validated Graph.
//
// The orchestrator's needs are deliberately narrow:
//
//   - Given what has already completed and what has already been dispatched,
//     which resources are ready to start now?
//   - If one resource fails, which downstream resources can no longer proceed?
//
// Keeping this logic separate from Graph avoids overloading the graph type with
// workflow-specific state while still reusing the graph's deterministic order.
type Schedule struct {
	graph *Graph
}

// NewSchedule creates a scheduler over an already-validated graph.
func NewSchedule(g *Graph) *Schedule {
	return &Schedule{graph: g}
}

// Ready returns every resource whose dependencies are satisfied and which has
// not already been completed or dispatched.
//
// The method walks the graph's stable topological order instead of iterating over
// a map directly. That gives callers a deterministic dispatch order even when
// multiple resources become ready at the same time.
func (s *Schedule) Ready(completed, dispatched map[string]bool) []string {
	if s == nil || s.graph == nil {
		return nil
	}

	ready := make([]string, 0)
	for _, name := range s.graph.TopologicalOrder() {
		if completed[name] || dispatched[name] {
			continue
		}
		if s.graph.DependenciesMet(name, completed) {
			ready = append(ready, name)
		}
	}
	return ready
}

// AffectedByFailure returns all resources that transitively depend on the failed
// resource.
//
// The failed resource itself is not included. The result is filtered through the
// graph's topological order so callers receive a deterministic list where nearer
// dependents appear before deeper descendants when possible.
func (s *Schedule) AffectedByFailure(failed string) []string {
	if s == nil || s.graph == nil {
		return nil
	}

	affectedSet := make(map[string]bool)
	queue := append([]string(nil), s.graph.Dependents(failed)...)

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if affectedSet[current] {
			continue
		}
		affectedSet[current] = true
		queue = append(queue, s.graph.Dependents(current)...)
	}

	if len(affectedSet) == 0 {
		return nil
	}

	affected := make([]string, 0, len(affectedSet))
	for _, name := range s.graph.TopologicalOrder() {
		if affectedSet[name] {
			affected = append(affected, name)
		}
	}
	return affected
}
