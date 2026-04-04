package dag

import (
	"fmt"
	"sort"
	"strings"
)

// RenderOptions controls how the ASCII graph is drawn.
type RenderOptions struct {
	// ShowKind includes the resource kind on a second line inside each box.
	// When false, only the resource name is shown.
	ShowKind bool
}

// Render produces a terminal-friendly ASCII representation of the dependency
// DAG using Unicode box-drawing characters. Resources are laid out in
// horizontal layers by depth (roots at top, leaves at bottom). Within each
// layer nodes are sorted alphabetically for deterministic output.
//
// The kindFunc parameter maps resource names to a display label for the
// resource kind. Pass nil to omit kind labels.
func Render(g *Graph, kindFunc func(name string) string) string {
	names := g.NodeNames()
	if len(names) == 0 {
		return "(empty graph)"
	}

	// Single node — no connectors needed.
	if len(names) == 1 {
		name := names[0]
		kind := ""
		if kindFunc != nil {
			kind = kindFunc(name)
		}
		return renderBox(name, kind)
	}

	// Assign each node to a depth level.
	levels := g.Levels()
	maxLevel := 0
	for _, lvl := range levels {
		if lvl > maxLevel {
			maxLevel = lvl
		}
	}

	// Group nodes by level.
	layers := make([][]string, maxLevel+1)
	for _, name := range names {
		lvl := levels[name]
		layers[lvl] = append(layers[lvl], name)
	}
	for i := range layers {
		sort.Strings(layers[i])
	}

	var buf strings.Builder

	// For each layer, render the boxes and then the connector lines to the
	// next layer.
	for lvl := 0; lvl <= maxLevel; lvl++ {
		layer := layers[lvl]

		// Build boxes for this layer.
		boxes := make([]string, len(layer))
		widths := make([]int, len(layer))
		for i, name := range layer {
			kind := ""
			if kindFunc != nil {
				kind = kindFunc(name)
			}
			boxes[i] = renderBox(name, kind)
			widths[i] = boxWidth(name, kind)
		}

		// Write boxes side by side with a gutter.
		boxLines := splitBoxes(boxes)
		for _, line := range boxLines {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}

		// Draw connectors to the next layer.
		if lvl < maxLevel {
			nextLayer := layers[lvl+1]
			connectorLines := renderConnectors(layer, widths, nextLayer, g)
			for _, cl := range connectorLines {
				buf.WriteString(cl)
				buf.WriteByte('\n')
			}
		}
	}

	return strings.TrimRight(buf.String(), "\n")
}

// ---------------------------------------------------------------------------
// Box rendering
// ---------------------------------------------------------------------------

const boxGutter = 2 // spaces between boxes on the same layer

func renderBox(name, kind string) string {
	width := len(name)
	if kind != "" && len(kind) > width {
		width = len(kind)
	}
	// Pad inner width + 2 spaces of margin.
	inner := width + 2

	top := "┌" + strings.Repeat("─", inner) + "┐"
	bot := "└" + strings.Repeat("─", inner) + "┘"

	var lines []string
	lines = append(lines, top)
	if kind != "" {
		lines = append(lines, "│ "+pad(kind, width)+" │")
		lines = append(lines, "│ "+pad(name, width)+" │")
	} else {
		lines = append(lines, "│ "+pad(name, width)+" │")
	}
	lines = append(lines, bot)
	return strings.Join(lines, "\n")
}

func boxWidth(name, kind string) int {
	w := len(name)
	if kind != "" && len(kind) > w {
		w = len(kind)
	}
	return w + 4 // "│ " + content + " │"
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// splitBoxes takes a set of multi-line box strings and merges them
// horizontally with a gutter.
func splitBoxes(boxes []string) []string {
	if len(boxes) == 0 {
		return nil
	}

	// Split each box into lines.
	allLines := make([][]string, len(boxes))
	maxLines := 0
	boxWidths := make([]int, len(boxes))
	for i, box := range boxes {
		allLines[i] = strings.Split(box, "\n")
		if len(allLines[i]) > maxLines {
			maxLines = len(allLines[i])
		}
		// Width from the first line.
		if len(allLines[i]) > 0 {
			boxWidths[i] = runeLen(allLines[i][0])
		}
	}

	// Pad shorter boxes with blank lines and merge.
	merged := make([]string, maxLines)
	gutter := strings.Repeat(" ", boxGutter)
	for row := 0; row < maxLines; row++ {
		var parts []string
		for i := range boxes {
			if row < len(allLines[i]) {
				parts = append(parts, allLines[i][row])
			} else {
				parts = append(parts, strings.Repeat(" ", boxWidths[i]))
			}
		}
		merged[row] = strings.Join(parts, gutter)
	}
	return merged
}

// ---------------------------------------------------------------------------
// Connector rendering
// ---------------------------------------------------------------------------

// renderConnectors draws the lines between a parent layer and a child layer.
//
// For each child, we find which parents it depends on in the current layer,
// then draw vertical pipes from the parent midpoints down, and horizontal
// lines merging into the child's midpoint.
func renderConnectors(parents []string, parentWidths []int, children []string, g *Graph) []string {
	// Compute midpoint x-offset for each parent box.
	parentMids := make([]int, len(parents))
	x := 0
	for i, w := range parentWidths {
		parentMids[i] = x + w/2
		x += w + boxGutter
	}

	// Build a quick lookup: parent name → index in this layer.
	parentIdx := make(map[string]int, len(parents))
	for i, name := range parents {
		parentIdx[name] = i
	}

	// Compute child box midpoints. To do this we need child box widths.
	childWidths := make([]int, len(children))
	for i, name := range children {
		childWidths[i] = boxWidth(name, "") // kind not needed for midpoint calc
	}
	childMids := make([]int, len(children))
	cx := 0
	for i, w := range childWidths {
		childMids[i] = cx + w/2
		cx += w + boxGutter
	}

	// Collect all active x-positions for vertical pipes (downward from parents).
	// For each child, determine which parent columns connect to it.
	type connection struct {
		parentXs []int
		childX   int
	}
	var conns []connection
	for i, child := range children {
		deps := g.Dependencies(child)
		var pxs []int
		for _, dep := range deps {
			if idx, ok := parentIdx[dep]; ok {
				pxs = append(pxs, parentMids[idx])
			}
		}
		if len(pxs) > 0 {
			sort.Ints(pxs)
			conns = append(conns, connection{parentXs: pxs, childX: childMids[i]})
		}
	}

	if len(conns) == 0 {
		return []string{""}
	}

	// Find the total width we need.
	totalWidth := 0
	for _, pw := range parentWidths {
		totalWidth += pw + boxGutter
	}
	for _, cw := range childWidths {
		w := cw + boxGutter
		if w > totalWidth {
			totalWidth = w
		}
	}
	totalWidth += 10 // extra padding

	// Line 1: vertical pipes dropping from parent midpoints.
	line1 := makeCharLine(totalWidth, ' ')
	for _, conn := range conns {
		for _, px := range conn.parentXs {
			if px < totalWidth {
				line1[px] = '│'
			}
		}
	}

	// Line 2: Build a composite routing line. We place horizontal spans
	// for each connection then classify each x-position based on what
	// passes through it (parent pipe above, child pipe below, horizontal).
	//
	// Track per-column roles: 'P' = parent drop, 'C' = child receive,
	// 'H' = horizontal pass-through, or combinations.
	type colRole struct {
		parentDrop   bool
		childReceive bool
		horizontal   bool
	}
	roles := make([]colRole, totalWidth)

	for _, conn := range conns {
		// Determine the horizontal span: from leftmost involved x to rightmost.
		allXs := append([]int(nil), conn.parentXs...)
		allXs = append(allXs, conn.childX)
		sort.Ints(allXs)
		minX := allXs[0]
		maxX := allXs[len(allXs)-1]

		for x := minX; x <= maxX; x++ {
			if x < totalWidth {
				roles[x].horizontal = true
			}
		}
		for _, px := range conn.parentXs {
			if px < totalWidth {
				roles[px].parentDrop = true
			}
		}
		if conn.childX < totalWidth {
			roles[conn.childX].childReceive = true
		}
	}

	line2 := makeCharLine(totalWidth, ' ')
	for x := 0; x < totalWidth; x++ {
		r := roles[x]
		if !r.parentDrop && !r.childReceive && !r.horizontal {
			continue
		}

		hasLeft := x > 0 && roles[x-1].horizontal
		hasRight := x < totalWidth-1 && roles[x+1].horizontal

		if r.parentDrop && r.childReceive {
			// Both: vertical pipe (straight through).
			if r.horizontal && (hasLeft || hasRight) {
				line2[x] = '┼'
			} else {
				line2[x] = '│'
			}
		} else if r.parentDrop {
			// Coming from above.
			if hasLeft && hasRight {
				line2[x] = '┴'
			} else if hasLeft {
				line2[x] = '┘'
			} else if hasRight {
				line2[x] = '└'
			} else {
				line2[x] = '│'
			}
		} else if r.childReceive {
			// Going down to child.
			if hasLeft && hasRight {
				line2[x] = '┬'
			} else if hasLeft {
				line2[x] = '┐'
			} else if hasRight {
				line2[x] = '┌'
			} else {
				line2[x] = '│'
			}
		} else if r.horizontal {
			line2[x] = '─'
		}
	}

	// Line 3: vertical pipes dropping into child midpoints.
	line3 := makeCharLine(totalWidth, ' ')
	for _, conn := range conns {
		if conn.childX < totalWidth {
			line3[conn.childX] = '│'
		}
	}

	return []string{
		trimRight(string(line1)),
		trimRight(string(line2)),
		trimRight(string(line3)),
	}
}

func makeCharLine(width int, fill rune) []rune {
	line := make([]rune, width)
	for i := range line {
		line[i] = fill
	}
	return line
}

func trimRight(s string) string {
	return strings.TrimRight(s, " ")
}

func runeLen(s string) int {
	// For box-drawing characters, each is one rune but we need display width.
	// Since we use ASCII + single-width Unicode box chars, len([]rune(s)) is fine.
	return len([]rune(s))
}

// RenderSimple produces a compact tree-style view of the graph, using
// indentation to show depth and arrows for dependencies. This is a simpler
// alternative to the full box-drawing layout.
//
// Example output:
//
//	Layer 0
//	  vpc
//	  bucket
//	Layer 1
//	  subnet ← vpc
//	  security_group ← vpc
//	Layer 2
//	  instance ← subnet, security_group
func RenderSimple(g *Graph) string {
	names := g.NodeNames()
	if len(names) == 0 {
		return "(empty graph)"
	}

	levels := g.Levels()
	maxLevel := 0
	for _, lvl := range levels {
		if lvl > maxLevel {
			maxLevel = lvl
		}
	}

	layers := make([][]string, maxLevel+1)
	for _, name := range names {
		layers[levels[name]] = append(layers[levels[name]], name)
	}
	for i := range layers {
		sort.Strings(layers[i])
	}

	var buf strings.Builder
	for lvl := 0; lvl <= maxLevel; lvl++ {
		fmt.Fprintf(&buf, "Layer %d\n", lvl)
		for _, name := range layers[lvl] {
			deps := g.Dependencies(name)
			if len(deps) == 0 {
				fmt.Fprintf(&buf, "  %s\n", name)
			} else {
				fmt.Fprintf(&buf, "  %s ← %s\n", name, strings.Join(deps, ", "))
			}
		}
	}
	return strings.TrimRight(buf.String(), "\n")
}
