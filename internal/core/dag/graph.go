package dag

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shirvan/praxis/pkg/types"
)

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

	for name, node := range g.nodes {
		deps := dedupeAndSortStrings(node.Dependencies)
		g.edges[name] = deps
		if _, exists := g.reverse[name]; !exists {
			g.reverse[name] = nil
		}
		for _, dep := range deps {
			if _, ok := g.nodes[dep]; !ok {
				return nil, fmt.Errorf("resource %q depends on unknown resource %q", name, dep)
			}
			g.reverse[dep] = append(g.reverse[dep], name)
		}
	}

	for name := range g.reverse {
		sort.Strings(g.reverse[name])
	}

	if err := g.detectCycles(); err != nil {
		return nil, err
	}

	return g, nil
}

// TopologicalOrder returns a deterministic execution order where every resource
// appears after all of its dependencies.
//
// Kahn's algorithm is used here instead of reusing the DFS cycle detector so we
// can preserve a stable alphabetical order within each ready set.
func (g *Graph) TopologicalOrder() []string {
	if len(g.nodes) == 0 {
		return nil
	}

	inDegree := make(map[string]int, len(g.nodes))
	for name := range g.nodes {
		inDegree[name] = len(g.edges[name])
	}

	ready := g.Roots()
	order := make([]string, 0, len(g.nodes))

	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)

		for _, dependent := range g.reverse[name] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				ready = insertSorted(ready, dependent)
			}
		}
	}

	return order
}

// Roots returns the resources that have no dependencies and may start
// immediately.
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
// Unknown resources return false so callers do not accidentally schedule a node
// that was never present in the graph.
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
func (g *Graph) Dependents(resource string) []string {
	dependents := g.reverse[resource]
	if len(dependents) == 0 {
		return nil
	}
	return append([]string(nil), dependents...)
}

// ReverseTopo returns the exact reverse of the graph's topological order. This
// is the ordering used by later delete and rollback flows, where dependents must
// be torn down before their dependencies.
func (g *Graph) ReverseTopo() []string {
	order := g.TopologicalOrder()
	for left, right := 0, len(order)-1; left < right; left, right = left+1, right-1 {
		order[left], order[right] = order[right], order[left]
	}
	return order
}

// Subgraph returns a new Graph containing only the named targets and their
// transitive dependencies. Every target must exist in the graph.
func (g *Graph) Subgraph(targets []string) (*Graph, error) {
	// Collect the closure: targets + all transitive deps.
	include := make(map[string]bool, len(targets))
	var walk func(string)
	walk = func(name string) {
		if include[name] {
			return
		}
		include[name] = true
		for _, dep := range g.edges[name] {
			walk(dep)
		}
	}
	for _, target := range targets {
		if _, ok := g.nodes[target]; !ok {
			return nil, fmt.Errorf("target resource %q does not exist in the graph", target)
		}
		walk(target)
	}

	// Build a new node slice from the closure.
	nodes := make([]*types.ResourceNode, 0, len(include))
	for name := range include {
		nodes = append(nodes, g.nodes[name])
	}
	return NewGraph(nodes)
}

func (g *Graph) detectCycles() error {
	const (
		visitUnvisited = iota
		visitVisiting
		visitVisited
	)

	state := make(map[string]int, len(g.nodes))
	stack := make([]string, 0, len(g.nodes))

	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case visitVisited:
			return nil
		case visitVisiting:
			return fmt.Errorf("dependency cycle detected: %s", formatCycle(stack, name))
		}

		state[name] = visitVisiting
		stack = append(stack, name)
		for _, dep := range g.edges[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = visitVisited
		return nil
	}

	for _, name := range g.sortedNodeNames() {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func (g *Graph) sortedNodeNames() []string {
	names := make([]string, 0, len(g.nodes))
	for name := range g.nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func formatCycle(stack []string, repeated string) string {
	start := 0
	for index, name := range stack {
		if name == repeated {
			start = index
			break
		}
	}
	cycle := append([]string(nil), stack[start:]...)
	cycle = append(cycle, repeated)
	return strings.Join(cycle, " -> ")
}

func dedupeAndSortStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
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

func insertSorted(values []string, value string) []string {
	index := sort.SearchStrings(values, value)
	values = append(values, "")
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}
