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
	"fmt"
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

		switch rd.Operation {
		case types.OpCreate:
			fmt.Fprintf(&b, "  # %s %q will be created\n", rd.ResourceType, rd.ResourceKey)
			fmt.Fprintf(&b, "  + resource %q %q {\n", rd.ResourceType, rd.ResourceKey)
		case types.OpUpdate:
			fmt.Fprintf(&b, "  # %s %q will be updated in-place\n", rd.ResourceType, rd.ResourceKey)
			fmt.Fprintf(&b, "  ~ resource %q %q {\n", rd.ResourceType, rd.ResourceKey)
		case types.OpDelete:
			fmt.Fprintf(&b, "  # %s %q will be destroyed\n", rd.ResourceType, rd.ResourceKey)
			fmt.Fprintf(&b, "  - resource %q %q {\n", rd.ResourceType, rd.ResourceKey)
		}

		for _, fd := range rd.FieldDiffs {
			switch rd.Operation {
			case types.OpCreate:
				fmt.Fprintf(&b, "      + %s: %s\n", fd.Path, formatValue(fd.NewValue))
			case types.OpUpdate:
				fmt.Fprintf(&b, "      ~ %s: %s -> %s\n", fd.Path, formatValue(fd.OldValue), formatValue(fd.NewValue))
			case types.OpDelete:
				fmt.Fprintf(&b, "      - %s: %s\n", fd.Path, formatValue(fd.OldValue))
			}
		}

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
