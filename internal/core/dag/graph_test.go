package dag

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestNewGraph_EmptyGraph(t *testing.T) {
	g, err := NewGraph(nil)
	require.NoError(t, err)
	assert.Empty(t, g.TopologicalOrder())
	assert.Empty(t, g.Roots())
	assert.Empty(t, g.ReverseTopo())
}

func TestNewGraph_SingleResource_NoDependencies(t *testing.T) {
	g := newTestGraph(t,
		newNode("bucket"),
	)

	assert.Equal(t, []string{"bucket"}, g.TopologicalOrder())
	assert.Equal(t, []string{"bucket"}, g.Roots())
	assert.Equal(t, []string{"bucket"}, g.ReverseTopo())
	assert.True(t, g.DependenciesMet("bucket", map[string]bool{}))
}

func TestNewGraph_LinearChain(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "db"),
		newNode("db", "network"),
		newNode("network"),
	)

	assert.Equal(t, []string{"network", "db", "app"}, g.TopologicalOrder())
	assert.Equal(t, []string{"app", "db", "network"}, g.ReverseTopo())
	assert.Equal(t, []string{"db"}, g.Dependents("network"))
	assert.True(t, g.DependenciesMet("app", map[string]bool{"db": true}))
	assert.False(t, g.DependenciesMet("app", map[string]bool{"network": true}))
}

func TestNewGraph_DiamondGraph_StableOrder(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "db", "queue"),
		newNode("db", "network"),
		newNode("queue", "network"),
		newNode("network"),
	)

	assert.Equal(t, []string{"network", "db", "queue", "app"}, g.TopologicalOrder())
	assert.Equal(t, []string{"db", "queue"}, g.Dependents("network"))
	assert.Equal(t, []string{"network"}, g.Roots())
}

func TestNewGraph_IndependentResources_AllRoots(t *testing.T) {
	g := newTestGraph(t,
		newNode("alpha"),
		newNode("gamma"),
		newNode("beta"),
	)

	assert.Equal(t, []string{"alpha", "beta", "gamma"}, g.TopologicalOrder())
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, g.Roots())
}

func TestNewGraph_CycleDetection_ReturnsPath(t *testing.T) {
	_, err := NewGraph([]*types.ResourceNode{
		newNode("app", "db"),
		newNode("db", "app"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dependency cycle detected")
	assert.Contains(t, err.Error(), "app -> db -> app")
}

func TestNewGraph_MissingDependency_ReturnsError(t *testing.T) {
	_, err := NewGraph([]*types.ResourceNode{
		newNode("app", "db"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "depends on unknown resource")
}

func TestNewGraph_ComplexGraph_TopologyVerified(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api", "assets"),
		newNode("api", "db", "network"),
		newNode("assets", "network"),
		newNode("db", "network", "kms"),
		newNode("kms"),
		newNode("network"),
	)

	order := g.TopologicalOrder()
	assertTopologicalOrder(t, g, order)
	assert.Equal(t, []string{"kms", "network", "assets", "db", "api", "frontend"}, order)
	assert.Equal(t, []string{"frontend", "api", "db", "assets", "network", "kms"}, g.ReverseTopo())
}

func TestNewGraph_ReverseTopo_IsExactReverse(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api"),
		newNode("api", "db"),
		newNode("db"),
	)

	topo := g.TopologicalOrder()
	reverse := g.ReverseTopo()
	for index, name := range topo {
		assert.Equal(t, name, reverse[len(reverse)-1-index])
	}
}

func TestNewGraph_DuplicateName_ReturnsError(t *testing.T) {
	_, err := NewGraph([]*types.ResourceNode{
		newNode("db"),
		newNode("db"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate resource name")
}

func TestSubgraph_SingleTarget_IncludesTransitiveDeps(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api"),
		newNode("api", "db"),
		newNode("db"),
		newNode("unrelated"),
	)

	sub, err := g.Subgraph([]string{"frontend"})
	require.NoError(t, err)

	order := sub.TopologicalOrder()
	assert.Equal(t, []string{"db", "api", "frontend"}, order)
}

func TestSubgraph_MiddleTarget_IncludesDepsOnly(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api"),
		newNode("api", "db"),
		newNode("db"),
	)

	sub, err := g.Subgraph([]string{"api"})
	require.NoError(t, err)

	order := sub.TopologicalOrder()
	assert.Equal(t, []string{"db", "api"}, order)
}

func TestSubgraph_MultipleTargets_Union(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api"),
		newNode("api", "db"),
		newNode("db"),
		newNode("worker", "queue"),
		newNode("queue"),
	)

	sub, err := g.Subgraph([]string{"frontend", "worker"})
	require.NoError(t, err)

	order := sub.TopologicalOrder()
	assert.Equal(t, []string{"db", "api", "frontend", "queue", "worker"}, order)
}

func TestSubgraph_RootTarget_NoExtraDeps(t *testing.T) {
	g := newTestGraph(t,
		newNode("frontend", "api"),
		newNode("api"),
	)

	sub, err := g.Subgraph([]string{"api"})
	require.NoError(t, err)

	order := sub.TopologicalOrder()
	assert.Equal(t, []string{"api"}, order)
}

func TestSubgraph_UnknownTarget_ReturnsError(t *testing.T) {
	g := newTestGraph(t,
		newNode("db"),
	)

	_, err := g.Subgraph([]string{"nonexistent"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target resource \"nonexistent\" does not exist")
}

func TestSubgraph_DiamondGraph_SharedDeps(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "api", "worker"),
		newNode("api", "db"),
		newNode("worker", "db"),
		newNode("db"),
	)

	sub, err := g.Subgraph([]string{"api", "worker"})
	require.NoError(t, err)

	order := sub.TopologicalOrder()
	assert.Equal(t, []string{"db", "api", "worker"}, order)
}

func newTestGraph(t *testing.T, nodes ...*types.ResourceNode) *Graph {
	t.Helper()
	g, err := NewGraph(nodes)
	require.NoError(t, err)
	return g
}

func newNode(name string, deps ...string) *types.ResourceNode {
	return &types.ResourceNode{
		Name:         name,
		Kind:         "TestKind",
		Key:          name,
		Spec:         json.RawMessage(`{"spec":{}}`),
		Dependencies: deps,
	}
}

func assertTopologicalOrder(t *testing.T, g *Graph, order []string) {
	t.Helper()
	position := make(map[string]int, len(order))
	for index, name := range order {
		position[name] = index
	}
	for node, deps := range g.edges {
		for _, dep := range deps {
			assert.Less(t, position[dep], position[node], "dependency %q must appear before %q", dep, node)
		}
	}
}
