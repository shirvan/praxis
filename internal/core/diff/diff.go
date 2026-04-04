// Package diff implements the plan diff engine for Praxis.
//
// The diff engine compares desired resource state (from rendered templates)
// against current infrastructure state (from driver Read calls) and produces
// a PlanResult containing per-resource diffs with field-level granularity.
// This is the core of the "praxis plan" output — analogous to "terraform plan".
//
// The package provides three responsibilities:
//  1. NewPlanResult — creates an empty plan container.
//  2. Add — appends a resource diff (create/update/delete/no-op) with field
//     diffs and updates summary counters.
//  3. Render — formats a PlanResult into human-readable plan output using
//     Terraform-inspired sigils (+, ~, -).
package diff

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shirvan/praxis/pkg/types"
)

// NewPlanResult creates an empty plan result ready for resource diffs.
// The caller populates it by calling Add for each resource comparison.
func NewPlanResult() *types.PlanResult {
	return &types.PlanResult{
		Summary: types.PlanSummary{},
	}
}

// Add appends a resource diff to the plan result and updates summary counts.
// Each call represents one resource's comparison between desired and actual state.
//
// Parameters:
//   - resourceType: the driver type (e.g. "aws:ec2:instance").
//   - resourceKey: the user-defined resource name from the template.
//   - op: the diff operation (create, update, delete, or no-op).
//   - fields: per-field diffs showing old/new values for changed fields.
//
// Summary counters are incremented automatically based on the operation type,
// so the caller never needs to manage counts manually.
func Add(plan *types.PlanResult, resourceType, resourceKey string, op types.DiffOperation, fields []types.FieldDiff) {
	rd := types.ResourceDiff{
		ResourceKey:  resourceKey,
		ResourceType: resourceType,
		Operation:    op,
		FieldDiffs:   fields,
	}
	plan.Resources = append(plan.Resources, rd)

	switch op {
	case types.OpCreate:
		plan.Summary.ToCreate++
	case types.OpUpdate:
		plan.Summary.ToUpdate++
	case types.OpDelete:
		plan.Summary.ToDelete++
	case types.OpNoOp:
		plan.Summary.Unchanged++
	}
}

// Render produces a plan-style human-readable string from a PlanResult.
// The output format mirrors Terraform's plan display:
//
//   - resource "type" "name" { ... }   — will be created
//     ~ resource "type" "name" { ... }   — will be updated in-place
//   - resource "type" "name" { ... }   — will be destroyed
//
// No-op resources are omitted from the output. If there are no changes at all,
// a short "Infrastructure is up-to-date" message is returned.
func Render(plan *types.PlanResult) string {
	if !plan.Summary.HasChanges() {
		return "No changes. Infrastructure is up-to-date.\n"
	}

	var b strings.Builder
	b.WriteString("Praxis will perform the following actions:\n\n")

	for _, rd := range plan.Resources {
		if rd.Operation == types.OpNoOp {
			continue
		}

		symbol := "+"
		actionVerb := "will be created"
		switch rd.Operation {
		case types.OpUpdate:
			symbol = "~"
			actionVerb = "will be updated in-place"
		case types.OpDelete:
			symbol = "-"
			actionVerb = "will be destroyed"
		}

		fmt.Fprintf(&b, "  # %s %q %s\n", rd.ResourceType, rd.ResourceKey, actionVerb)

		if len(rd.FieldDiffs) == 0 {
			fmt.Fprintf(&b, "  %s resource %q %q {}\n\n", symbol, rd.ResourceType, rd.ResourceKey)
			continue
		}

		fmt.Fprintf(&b, "  %s resource %q %q {\n", symbol, rd.ResourceType, rd.ResourceKey)
		nodes := GroupFields(rd.FieldDiffs)
		renderNodes(&b, nodes, rd.Operation, 6)
		b.WriteString("    }\n\n")
	}

	b.WriteString(plan.Summary.String())
	b.WriteString("\n")

	return b.String()
}

// formatValue converts an interface{} to a display-friendly string for plan output.
// Nil values render as "(not set)", strings are quoted, bools are bare words,
// and maps of string->string render as "{key=\"value\"}" pairs.
// Any other type falls through to fmt.Sprintf for a best-effort representation.
func formatValue(v any) string {
	if v == nil {
		return "(not set)"
	}
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case map[string]string:
		if len(val) == 0 {
			return "{}"
		}
		pairs := make([]string, 0, len(val))
		for k, v := range val {
			pairs = append(pairs, fmt.Sprintf("%s=%q", k, v))
		}
		return fmt.Sprintf("{%s}", strings.Join(pairs, ", "))
	default:
		return fmt.Sprintf("%v", val)
	}
}

// FieldNode represents either a leaf field or a group of nested fields in the
// plan output. GroupFields produces a tree of FieldNodes from flat FieldDiffs.
type FieldNode struct {
	Key      string
	IsGroup  bool
	Diff     *types.FieldDiff
	Children []FieldNode
}

// GroupFields organizes flat field diffs into a hierarchical tree for display.
// It strips the "spec." prefix and merges fields sharing a common dot-separated
// prefix into nested groups. Single-child groups are kept flat to avoid
// unnecessary nesting.
func GroupFields(diffs []types.FieldDiff) []FieldNode {
	cleaned := make([]types.FieldDiff, len(diffs))
	for i, d := range diffs {
		cleaned[i] = d
		cleaned[i].Path = strings.TrimPrefix(d.Path, "spec.")
	}
	return groupFieldDiffs(cleaned)
}

func groupFieldDiffs(diffs []types.FieldDiff) []FieldNode {
	type group struct {
		children []types.FieldDiff
	}
	groups := make(map[string]*group)
	groupOrder := make([]string, 0)
	var flat []types.FieldDiff

	for _, d := range diffs {
		if idx := strings.IndexByte(d.Path, '.'); idx >= 0 {
			prefix := d.Path[:idx]
			child := d
			child.Path = d.Path[idx+1:]
			if g, ok := groups[prefix]; ok {
				g.children = append(g.children, child)
			} else {
				groups[prefix] = &group{children: []types.FieldDiff{child}}
				groupOrder = append(groupOrder, prefix)
			}
		} else {
			flat = append(flat, d)
		}
	}

	entries := make([]FieldNode, 0, len(flat)+len(groups))
	for i := range flat {
		entries = append(entries, FieldNode{Key: flat[i].Path, Diff: &flat[i]})
	}
	for _, prefix := range groupOrder {
		g := groups[prefix]
		if len(g.children) == 1 {
			child := g.children[0]
			fd := types.FieldDiff{
				Path:     prefix + "." + child.Path,
				OldValue: child.OldValue,
				NewValue: child.NewValue,
			}
			entries = append(entries, FieldNode{Key: fd.Path, Diff: &fd})
		} else {
			entries = append(entries, FieldNode{
				Key:      prefix,
				IsGroup:  true,
				Children: groupFieldDiffs(g.children),
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
	return entries
}

func renderNodes(b *strings.Builder, nodes []FieldNode, op types.DiffOperation, indent int) {
	symbol := "+"
	switch op {
	case types.OpUpdate:
		symbol = "~"
	case types.OpDelete:
		symbol = "-"
	}

	pad := strings.Repeat(" ", indent)
	maxKeyLen := 0
	for _, n := range nodes {
		if len(n.Key) > maxKeyLen {
			maxKeyLen = len(n.Key)
		}
	}

	for _, n := range nodes {
		if n.IsGroup {
			fmt.Fprintf(b, "%s%s %-*s {\n", pad, symbol, maxKeyLen, n.Key)
			renderNodes(b, n.Children, op, indent+4)
			fmt.Fprintf(b, "%s  }\n", pad)
		} else {
			switch op {
			case types.OpCreate:
				fmt.Fprintf(b, "%s%s %-*s = %s\n", pad, symbol, maxKeyLen, n.Key, formatValue(n.Diff.NewValue))
			case types.OpUpdate:
				fmt.Fprintf(b, "%s%s %-*s = %s -> %s\n", pad, symbol, maxKeyLen, n.Key, formatValue(n.Diff.OldValue), formatValue(n.Diff.NewValue))
			case types.OpDelete:
				fmt.Fprintf(b, "%s%s %-*s = %s\n", pad, symbol, maxKeyLen, n.Key, formatValue(n.Diff.OldValue))
			}
		}
	}
}

// FieldDiffsFromJSON converts raw JSON spec bytes into a flat list of
// FieldDiff entries (all with NewValue only). This is used for resources
// whose specs contain unresolved expressions — we can still show the
// known fields even though the resource can't be diffed against cloud state.
func FieldDiffsFromJSON(raw json.RawMessage) []types.FieldDiff {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	var diffs []types.FieldDiff
	flattenJSON("spec", decoded, &diffs)
	return diffs
}

func flattenJSON(path string, value any, diffs *[]types.FieldDiff) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			return
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			flattenJSON(path+"."+key, typed[key], diffs)
		}
	case []any:
		for index, item := range typed {
			flattenJSON(path+"."+strconv.Itoa(index), item, diffs)
		}
	default:
		*diffs = append(*diffs, types.FieldDiff{Path: path, NewValue: typed})
	}
}
