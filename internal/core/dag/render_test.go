package dag

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

// ---------------------------------------------------------------------------
// Graph accessor tests
// ---------------------------------------------------------------------------

func TestGraph_Node_ReturnsNode(t *testing.T) {
	g := newTestGraph(t, newNode("bucket"), newNode("vpc"))

	node := g.Node("bucket")
	require.NotNil(t, node)
	assert.Equal(t, "bucket", node.Name)

	assert.Nil(t, g.Node("nonexistent"))
}

func TestGraph_Dependencies_ReturnsSorted(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "db", "cache"),
		newNode("db"),
		newNode("cache"),
	)
	assert.Equal(t, []string{"cache", "db"}, g.Dependencies("app"))
	assert.Empty(t, g.Dependencies("db"))
	assert.Nil(t, g.Dependencies("nonexistent"))
}

func TestGraph_Levels_SingleNode(t *testing.T) {
	g := newTestGraph(t, newNode("a"))
	levels := g.Levels()
	assert.Equal(t, 0, levels["a"])
}

func TestGraph_Levels_LinearChain(t *testing.T) {
	g := newTestGraph(t,
		newNode("c", "b"),
		newNode("b", "a"),
		newNode("a"),
	)
	levels := g.Levels()
	assert.Equal(t, 0, levels["a"])
	assert.Equal(t, 1, levels["b"])
	assert.Equal(t, 2, levels["c"])
}

func TestGraph_Levels_Diamond(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "api", "worker"),
		newNode("api", "db"),
		newNode("worker", "db"),
		newNode("db"),
	)
	levels := g.Levels()
	assert.Equal(t, 0, levels["db"])
	assert.Equal(t, 1, levels["api"])
	assert.Equal(t, 1, levels["worker"])
	assert.Equal(t, 2, levels["app"])
}

func TestGraph_Levels_IndependentRoots(t *testing.T) {
	g := newTestGraph(t,
		newNode("alpha"),
		newNode("beta"),
		newNode("gamma"),
	)
	levels := g.Levels()
	assert.Equal(t, 0, levels["alpha"])
	assert.Equal(t, 0, levels["beta"])
	assert.Equal(t, 0, levels["gamma"])
}

func TestGraph_Levels_WideGraph(t *testing.T) {
	// vpc -> subnet, sg, igw (all level 1)
	// subnet + sg -> instance (level 2)
	g := newTestGraph(t,
		newNode("instance", "subnet", "sg"),
		newNode("subnet", "vpc"),
		newNode("sg", "vpc"),
		newNode("igw", "vpc"),
		newNode("vpc"),
	)
	levels := g.Levels()
	assert.Equal(t, 0, levels["vpc"])
	assert.Equal(t, 1, levels["subnet"])
	assert.Equal(t, 1, levels["sg"])
	assert.Equal(t, 1, levels["igw"])
	assert.Equal(t, 2, levels["instance"])
}

// ---------------------------------------------------------------------------
// Render tests
// ---------------------------------------------------------------------------

func TestRender_EmptyGraph(t *testing.T) {
	g, err := NewGraph(nil)
	require.NoError(t, err)
	assert.Equal(t, "(empty graph)", Render(g, nil))
}

func TestRender_SingleNode_NoKind(t *testing.T) {
	g := newTestGraph(t, newNode("bucket"))
	output := Render(g, nil)
	assert.Contains(t, output, "bucket")
	assert.Contains(t, output, "┌")
	assert.Contains(t, output, "└")
}

func TestRender_SingleNode_WithKind(t *testing.T) {
	g := newTestGraph(t, newNode("bucket"))
	output := Render(g, func(name string) string { return "S3Bucket" })
	assert.Contains(t, output, "S3Bucket")
	assert.Contains(t, output, "bucket")
}

func TestRender_LinearChain_HasConnectors(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "db"),
		newNode("db"),
	)
	output := Render(g, nil)
	assert.Contains(t, output, "db")
	assert.Contains(t, output, "app")
	// Should have vertical connectors.
	assert.Contains(t, output, "│")
}

func TestRender_Diamond_AllNodesPresent(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "api", "worker"),
		newNode("api", "db"),
		newNode("worker", "db"),
		newNode("db"),
	)
	output := Render(g, nil)
	assert.Contains(t, output, "db")
	assert.Contains(t, output, "api")
	assert.Contains(t, output, "worker")
	assert.Contains(t, output, "app")
}

func TestRender_IndependentRoots_SameLayer(t *testing.T) {
	g := newTestGraph(t,
		newNode("alpha"),
		newNode("beta"),
		newNode("gamma"),
	)
	output := Render(g, nil)
	lines := strings.Split(output, "\n")
	// All three should appear on the same row (boxes side by side).
	// The first line of boxes is the top border.
	topLine := lines[0]
	// Count how many box tops appear.
	assert.Equal(t, 3, strings.Count(topLine, "┌"))
}

func TestRender_WithKindFunc_ShowsKinds(t *testing.T) {
	kinds := map[string]string{
		"vpc":    "VPC",
		"subnet": "Subnet",
	}
	g := newTestGraph(t,
		newNode("subnet", "vpc"),
		newNode("vpc"),
	)
	output := Render(g, func(name string) string { return kinds[name] })
	assert.Contains(t, output, "VPC")
	assert.Contains(t, output, "Subnet")
	assert.Contains(t, output, "vpc")
	assert.Contains(t, output, "subnet")
}

func TestRender_ComplexGraph_NoError(t *testing.T) {
	// Realistic VPC stack: vpc -> subnet, sg, igw; subnet+sg -> instance
	g := newTestGraph(t,
		newNode("instance", "subnet", "sg"),
		newNode("subnet", "vpc"),
		newNode("sg", "vpc"),
		newNode("igw", "vpc"),
		newNode("vpc"),
	)
	kinds := map[string]string{
		"vpc":      "VPC",
		"subnet":   "Subnet",
		"sg":       "SecurityGroup",
		"igw":      "InternetGateway",
		"instance": "EC2Instance",
	}
	output := Render(g, func(name string) string { return kinds[name] })

	// Verify all resources appear.
	for name := range kinds {
		assert.Contains(t, output, name, "missing node: %s", name)
	}
	for _, kind := range kinds {
		assert.Contains(t, output, kind, "missing kind: %s", kind)
	}

	// Verify layered structure (vpc should appear before subnet).
	vpcLine := lineContaining(output, "vpc")
	subnetLine := lineContaining(output, "subnet")
	assert.Less(t, vpcLine, subnetLine, "vpc should appear above subnet")
}

// ---------------------------------------------------------------------------
// RenderSimple tests
// ---------------------------------------------------------------------------

func TestRenderSimple_EmptyGraph(t *testing.T) {
	g, err := NewGraph(nil)
	require.NoError(t, err)
	assert.Equal(t, "(empty graph)", RenderSimple(g))
}

func TestRenderSimple_LinearChain(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "db"),
		newNode("db"),
	)
	output := RenderSimple(g)
	assert.Contains(t, output, "Layer 0")
	assert.Contains(t, output, "  db")
	assert.Contains(t, output, "Layer 1")
	assert.Contains(t, output, "  app ← db")
}

func TestRenderSimple_Diamond(t *testing.T) {
	g := newTestGraph(t,
		newNode("leaf", "left", "right"),
		newNode("left", "root"),
		newNode("right", "root"),
		newNode("root"),
	)
	output := RenderSimple(g)
	assert.Contains(t, output, "Layer 0")
	assert.Contains(t, output, "  root")
	assert.Contains(t, output, "Layer 1")
	assert.Contains(t, output, "  left ← root")
	assert.Contains(t, output, "  right ← root")
	assert.Contains(t, output, "Layer 2")
	assert.Contains(t, output, "  leaf ← left, right")
}

func TestRenderSimple_MultiplRoots(t *testing.T) {
	g := newTestGraph(t,
		newNode("a"),
		newNode("b"),
		newNode("c", "a", "b"),
	)
	output := RenderSimple(g)
	assert.Contains(t, output, "Layer 0")
	assert.Contains(t, output, "  a")
	assert.Contains(t, output, "  b")
	assert.Contains(t, output, "Layer 1")
	assert.Contains(t, output, "  c ← a, b")
}

// ---------------------------------------------------------------------------
// GraphNode serialization test
// ---------------------------------------------------------------------------

func TestGraphNodes_FromGraph(t *testing.T) {
	g := newTestGraph(t,
		newNode("app", "db"),
		newNode("db"),
	)

	// Simulate what the handler would do.
	nodes := make([]types.GraphNode, 0, len(g.NodeNames()))
	for _, name := range g.NodeNames() {
		n := g.Node(name)
		nodes = append(nodes, types.GraphNode{
			Name:         n.Name,
			Kind:         n.Kind,
			Dependencies: n.Dependencies,
		})
	}

	require.Len(t, nodes, 2)
	assert.Equal(t, "app", nodes[0].Name)
	assert.Equal(t, []string{"db"}, nodes[0].Dependencies)
	assert.Equal(t, "db", nodes[1].Name)
	assert.Empty(t, nodes[1].Dependencies)

	// Verify it round-trips through JSON.
	data, err := json.Marshal(nodes)
	require.NoError(t, err)
	var decoded []types.GraphNode
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, nodes, decoded)
}

// ---------------------------------------------------------------------------
// Box rendering unit tests
// ---------------------------------------------------------------------------

func TestRenderBox_NameOnly(t *testing.T) {
	box := renderBox("vpc", "")
	assert.Contains(t, box, "│ vpc │")
	assert.Contains(t, box, "┌")
	assert.Contains(t, box, "└")
}

func TestRenderBox_WithKind(t *testing.T) {
	box := renderBox("my_sg", "SecurityGroup")
	assert.Contains(t, box, "│ SecurityGroup │")
	assert.Contains(t, box, "│ my_sg         │")
}

func TestBoxWidth(t *testing.T) {
	assert.Equal(t, 7, boxWidth("vpc", ""))                 // width = 4 + 3
	assert.Equal(t, 17, boxWidth("my_sg", "SecurityGroup")) // width = max(5, 13) + 4 = 17
	assert.Equal(t, len("SecurityGroup")+4, boxWidth("my_sg", "SecurityGroup"))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// lineContaining returns the line number (0-indexed) of the first line
// containing the substring, or -1 if not found.
func lineContaining(output, substr string) int {
	for i, line := range strings.Split(output, "\n") {
		if strings.Contains(line, substr) {
			return i
		}
	}
	return -1
}

// TestRender_VisualOutput prints the rendered graph for manual inspection.
// Not a correctness test — purely for visual verification during development.
func TestRender_VisualOutput(t *testing.T) {
	g := newTestGraph(t,
		newKindNode("instance", "EC2Instance", "subnet", "sg"),
		newKindNode("subnet", "Subnet", "vpc"),
		newKindNode("sg", "SecurityGroup", "vpc"),
		newKindNode("igw", "InternetGateway", "vpc"),
		newKindNode("vpc", "VPC"),
		newKindNode("bucket", "S3Bucket"),
	)

	kinds := map[string]string{
		"vpc": "VPC", "subnet": "Subnet", "sg": "SecurityGroup",
		"igw": "InternetGateway", "instance": "EC2Instance", "bucket": "S3Bucket",
	}

	t.Log("\n" + Render(g, func(name string) string { return kinds[name] }))
	t.Log("\n" + RenderSimple(g))
}

func newKindNode(name, kind string, deps ...string) *types.ResourceNode {
	return &types.ResourceNode{
		Name:         name,
		Kind:         kind,
		Key:          name,
		Spec:         json.RawMessage(`{}`),
		Dependencies: deps,
	}
}
