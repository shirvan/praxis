package dag

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shirvan/praxis/pkg/types"
)

// ---------------------------------------------------------------------------
// Graph — the core DAG data structure
// ---------------------------------------------------------------------------

// Graph is the canonical in-memory representation of a deployment dependency
// DAG.
//
// Terminology used throughout the package:
//
//   - edges[name] lists the dependencies a node needs before it can start.
//   - reverse[name] lists the dependents that become unblocked when name
//     completes successfully.
//
// Both adjacency maps are stored in sorted order so every caller observes the
// same deterministic behavior regardless of Go's random map iteration.
type Graph struct {
	nodes   map[string]*types.ResourceNode
	edges   map[string][]string
	reverse map[string][]string
}

// NewGraph validates a set of resource nodes and constructs a deterministic DAG
// from them.
//
// Validation performed here is intentionally strict because downstream
// orchestrator logic assumes the graph is trustworthy:
//
//   - resource names must be unique
//   - every dependency must reference an existing resource
//   - dependency lists are deduplicated and sorted
//   - the graph must be acyclic
func NewGraph(nodes []*types.ResourceNode) (*Graph, error) {
	g := &Graph{
		nodes:   make(map[string]*types.ResourceNode, len(nodes)),
		edges:   make(map[string][]string, len(nodes)),
		reverse: make(map[string][]string, len(nodes)),
	}

	// Phase 1: Register all nodes, checking for duplicates and nil entries.
	for _, node := range nodes {
		if node == nil {
			return nil, fmt.Errorf("graph contains nil resource node")
		}
		if node.Name == "" {
			return nil, fmt.Errorf("graph contains resource with empty name")
		}
		if _, exists := g.nodes[node.Name]; exists {
			return nil, fmt.Errorf("duplicate resource name %q in dependency graph", node.Name)
		}
		g.nodes[node.Name] = node
	}

	// Phase 2: Build both forward (edges) and reverse adjacency lists.
	// Forward edges: edges[A] = [B, C] means A depends on B and C.
	// Reverse edges: reverse[B] = [A] means A is a dependent of B.
	for name, node := range g.nodes {
		deps := dedupeAndSortStrings(node.Dependencies)
		g.edges[name] = deps
		// Ensure every node has a reverse entry even if no one depends on it.
		if _, exists := g.reverse[name]; !exists {
			g.reverse[name] = nil
		}
		for _, dep := range deps {
			// Validate referential integrity: every dependency must exist.
			if _, ok := g.nodes[dep]; !ok {
				return nil, fmt.Errorf("resource %q depends on unknown resource %q", name, dep)
			}
			g.reverse[dep] = append(g.reverse[dep], name)
		}
	}

	// Sort reverse adjacency lists for deterministic dependent iteration.
	for name := range g.reverse {
		sort.Strings(g.reverse[name])
	}

	// Phase 3: Cycle detection — must come after all edges are built.
	if err := g.detectCycles(); err != nil {
		return nil, err
	}

	return g, nil
}

// TopologicalOrder returns a deterministic execution order where every resource
// appears after all of its dependencies.
//
// Algorithm: Kahn's algorithm (BFS-based topological sort).
//
//  1. Compute in-degree for each node (number of dependencies).
//  2. Seed the ready queue with all roots (in-degree == 0), sorted alphabetically.
//  3. Pop the first ready node, append it to the output order.
//  4. For each dependent of that node, decrement its in-degree. If it reaches 0,
//     insert it into the ready queue maintaining sorted order (via insertSorted).
//  5. Repeat until the ready queue is empty.
//
// Kahn's algorithm is used here instead of reusing the DFS cycle detector so we
// can preserve a stable alphabetical order within each ready set. This means
// independent resources at the same "depth" are always ordered alphabetically,
// making plan output and dispatch order predictable across runs.
func (g *Graph) TopologicalOrder() []string {
	if len(g.nodes) == 0 {
		return nil
	}

	// In-degree counts how many unmet dependencies each node has.
	// A node with in-degree 0 is immediately schedulable.
	inDegree := make(map[string]int, len(g.nodes))
	for name := range g.nodes {
		inDegree[name] = len(g.edges[name])
	}

	// Seed with root nodes (no dependencies) in sorted order.
	ready := g.Roots()
	order := make([]string, 0, len(g.nodes))

	for len(ready) > 0 {
		// Dequeue the first (alphabetically smallest) ready node.
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)

		// "Complete" this node: decrement in-degree for all dependents.
		for _, dependent := range g.reverse[name] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				// All dependencies met — insert into ready queue, maintaining sort.
				ready = insertSorted(ready, dependent)
			}
		}
	}

	return order
}

// Roots returns the resources that have no dependencies and may start
// immediately. These are the entry points of the DAG — the resources that
// the orchestrator can dispatch in the very first wave of parallel execution.
// The returned slice is sorted alphabetically for deterministic ordering.
func (g *Graph) Roots() []string {
	roots := make([]string, 0)
	for name := range g.nodes {
		if len(g.edges[name]) == 0 {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	return roots
}

// DependenciesMet reports whether every dependency of resource has completed.
// This is the fundamental predicate the scheduler uses to decide if a resource
// is eligible for dispatch. It checks every entry in edges[resource] against
// the completed set.
//
// Edge case: unknown resources (not in the graph) return false so callers do
// not accidentally schedule a node that was never present in the graph.
func (g *Graph) DependenciesMet(resource string, completed map[string]bool) bool {
	deps, ok := g.edges[resource]
	if !ok {
		return false
	}
	for _, dep := range deps {
		if !completed[dep] {
			return false
		}
	}
	return true
}

// Dependents returns the direct dependents of a resource in sorted order.
// These are the resources that list `resource` as a dependency — i.e. the
// nodes reached by following a reverse edge. The returned slice is a copy
// so callers cannot mutate the graph's internal adjacency list.
func (g *Graph) Dependents(resource string) []string {
	dependents := g.reverse[resource]
	if len(dependents) == 0 {
		return nil
	}
	return append([]string(nil), dependents...)
}

// ReverseTopo returns the exact reverse of the graph's topological order. This
// is the ordering used by delete and rollback flows, where dependents must be
// torn down before their dependencies. For example, an EC2 instance must be
// terminated before the VPC it lives in can be deleted.
//
// Implementation: compute TopologicalOrder() then reverse the slice in-place.
func (g *Graph) ReverseTopo() []string {
	order := g.TopologicalOrder()
	for left, right := 0, len(order)-1; left < right; left, right = left+1, right-1 {
		order[left], order[right] = order[right], order[left]
	}
	return order
}

// Subgraph returns a new Graph containing only the named targets and their
// transitive dependencies. Every target must exist in the graph.
//
// This is used for targeted deployments (e.g. "praxis apply --target my_bucket")
// where the user wants to deploy a subset of the stack. The subgraph includes
// not just the named targets but every resource they transitively depend on,
// ensuring the deployment is self-contained.
//
// Algorithm: DFS from each target, following forward edges (dependencies) to
// collect the full transitive closure. Then build a fresh Graph from the
// collected nodes, which re-validates and re-sorts everything.
func (g *Graph) Subgraph(targets []string) (*Graph, error) {
	// Collect the closure: targets + all transitive deps via DFS.
	include := make(map[string]bool, len(targets))
	var walk func(string)
	walk = func(name string) {
		if include[name] {
			return // Already visited — avoid redundant traversal.
		}
		include[name] = true
		for _, dep := range g.edges[name] {
			walk(dep)
		}
	}
	for _, target := range targets {
		if _, ok := g.nodes[target]; !ok {
			return nil, fmt.Errorf("target resource %q does not exist in the graph; available resources: %s", target, strings.Join(g.NodeNames(), ", "))
		}
		walk(target)
	}

	// Build a new node slice from the closure and construct a fresh graph
	// which will re-validate, re-sort edges, and re-check for cycles.
	nodes := make([]*types.ResourceNode, 0, len(include))
	for name := range include {
		nodes = append(nodes, g.nodes[name])
	}
	return NewGraph(nodes)
}

// detectCycles uses iterative DFS with three-color marking to find cycles.
//
// Algorithm: classic DFS cycle detection for directed graphs.
//
//   - visitUnvisited (white): node has not been visited yet.
//   - visitVisiting (gray): node is on the current DFS path (in the recursion stack).
//   - visitVisited (black): node and all its descendants are fully explored.
//
// If DFS reaches a node that is already in the visitVisiting state, a back edge
// (and therefore a cycle) has been found. The stack is used to reconstruct
// the cycle path for a human-readable error message.
//
// Nodes are visited in sorted order so the first cycle reported is
// deterministic across runs.
func (g *Graph) detectCycles() error {
	const (
		visitUnvisited = iota // white — not yet visited
		visitVisiting         // gray  — on the current DFS path
		visitVisited          // black — fully explored
	)

	state := make(map[string]int, len(g.nodes))
	stack := make([]string, 0, len(g.nodes)) // Tracks the current DFS path for cycle reporting.

	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case visitVisited:
			// Already fully explored — no cycle through this node.
			return nil
		case visitVisiting:
			// Back edge found — this node is an ancestor on the current path.
			// Reconstruct the cycle from the stack for a clear error message.
			return fmt.Errorf("dependency cycle detected: %s — review the dependency expressions in these resources to break the cycle", formatCycle(stack, name))
		}

		// Mark as "visiting" (gray) and push onto the DFS path stack.
		state[name] = visitVisiting
		stack = append(stack, name)

		// Recurse into all dependencies (forward edges).
		for _, dep := range g.edges[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}

		// Pop from the DFS path and mark as "visited" (black).
		stack = stack[:len(stack)-1]
		state[name] = visitVisited
		return nil
	}

	// Visit all nodes in sorted order for deterministic first-cycle reporting.
	for _, name := range g.sortedNodeNames() {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// sortedNodeNames returns all node names in alphabetical order.
// Used internally to ensure deterministic iteration over the node map.
func (g *Graph) sortedNodeNames() []string {
	names := make([]string, 0, len(g.nodes))
	for name := range g.nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NodeNames returns the sorted names of all nodes in the graph.
func (g *Graph) NodeNames() []string {
	return g.sortedNodeNames()
}

// Node returns the ResourceNode for the given name, or nil if not present.
func (g *Graph) Node(name string) *types.ResourceNode {
	return g.nodes[name]
}

// Dependencies returns the direct dependencies of a resource (sorted).
// Returns nil if the resource does not exist in the graph.
func (g *Graph) Dependencies(name string) []string {
	return g.edges[name]
}

// Levels assigns each node to a depth level based on the longest path from
// any root. Roots (no dependencies) are level 0. A node's level is
// max(level of each dependency) + 1. The returned map is keyed by node name.
func (g *Graph) Levels() map[string]int {
	levels := make(map[string]int, len(g.nodes))
	for _, name := range g.TopologicalOrder() {
		maxDep := -1
		for _, dep := range g.edges[name] {
			if levels[dep] > maxDep {
				maxDep = levels[dep]
			}
		}
		levels[name] = maxDep + 1
	}
	return levels
}

// formatCycle produces a human-readable cycle path like "a -> b -> c -> a".
// It scans the DFS stack to find where the cycle begins (the first occurrence
// of the repeated node) and joins the path with " -> " arrows.
func formatCycle(stack []string, repeated string) string {
	start := 0
	for index, name := range stack {
		if name == repeated {
			start = index
			break
		}
	}
	cycle := append([]string(nil), stack[start:]...)
	cycle = append(cycle, repeated) // Close the cycle: a -> b -> ... -> a
	return strings.Join(cycle, " -> ")
}

// dedupeAndSortStrings removes duplicates and empty strings from a slice,
// returning the unique values in sorted order. This is applied to dependency
// lists during graph construction to ensure edge lists are canonical.
func dedupeAndSortStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue // Skip empty dependency names.
		}
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// insertSorted inserts a string into an already-sorted slice, maintaining sort
// order. Uses binary search (sort.SearchStrings) to find the insertion point,
// then shifts elements right to make room. This is used by TopologicalOrder to
// keep the ready queue sorted as new nodes become eligible.
func insertSorted(values []string, value string) []string {
	index := sort.SearchStrings(values, value)
	values = append(values, "")            // Grow the slice by one.
	copy(values[index+1:], values[index:]) // Shift elements right.
	values[index] = value                  // Insert at the correct position.
	return values
}
