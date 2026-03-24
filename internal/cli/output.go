package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

// OutputFormat selects between human-readable table output and machine-readable
// JSON. Commands default to table unless the user passes --output json.
type OutputFormat string

const (
	// OutputTable renders data as aligned ASCII columns.
	OutputTable OutputFormat = "table"
	// OutputJSON renders data as indented JSON.
	OutputJSON OutputFormat = "json"
)

// --------------------------------------------------------------------------
// JSON output
// --------------------------------------------------------------------------

// printJSON writes the value as indented JSON to stdout. This is the canonical
// machine-readable output path used when --output json is set.
func printJSON(v any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// --------------------------------------------------------------------------
// Table output helpers
// --------------------------------------------------------------------------

// printPlainTable renders a simple ASCII table with aligned columns.
func printPlainTable(out io.Writer, headers []string, rows [][]string) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	// Header row in all caps.
	_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))
	// Separator line under each header.
	separators := make([]string, len(headers))
	for i, h := range headers {
		separators[i] = strings.Repeat("-", len(h))
	}
	_, _ = fmt.Fprintln(w, strings.Join(separators, "\t"))
	// Data rows.
	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
}

func printTable(r *Renderer, headers []string, rows [][]string) {
	r.printTable(headers, rows)
}

// --------------------------------------------------------------------------
// Deployment formatters
// --------------------------------------------------------------------------

// printDeploymentDetail renders a full deployment record with per-resource
// breakdown and outputs. This is the main display for `praxis get Deployment/<key>`.
func printDeploymentDetail(r *Renderer, d *types.DeploymentDetail) {
	r.writeLabelValue("Deployment", 11, d.Key)
	r.writeLabelStyledValue("Status", 11, r.renderStatus(string(d.Status)))
	if d.TemplatePath != "" {
		r.writeLabelValue("Template", 11, d.TemplatePath)
	}
	if d.Error != "" {
		r.writeLabelValue("Error", 11, d.Error)
	}
	r.writeLabelValue("Created", 11, r.renderMuted(formatTime(d.CreatedAt)))
	r.writeLabelValue("Updated", 11, r.renderMuted(formatTime(d.UpdatedAt)))
	_, _ = fmt.Fprintln(r.out)

	if len(d.Resources) > 0 {
		headers := []string{"RESOURCE", "KIND", "STATUS", "ERROR"}
		rows := make([][]string, 0, len(d.Resources))
		for _, resource := range d.Resources {
			errMsg := "-"
			if resource.Error != "" {
				errMsg = truncate(resource.Error, 60)
			}
			rows = append(rows, []string{
				resource.Name,
				resource.Kind,
				renderResourceStatus(r, resource.Status),
				errMsg,
			})
		}
		printTable(r, headers, rows)

		// Print outputs section for resources that have them.
		hasOutputs := false
		for _, r := range d.Resources {
			if len(r.Outputs) > 0 {
				hasOutputs = true
				break
			}
		}
		if hasOutputs {
			_, _ = fmt.Fprintln(r.out)
			_, _ = fmt.Fprintln(r.out, r.renderSection("Outputs:"))
			for _, resource := range d.Resources {
				keys := make([]string, 0, len(resource.Outputs))
				for key := range resource.Outputs {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					_, _ = fmt.Fprintf(r.out, "  %s.%s = %v\n", resource.Name, key, resource.Outputs[key])
				}
			}
		}

		// Print full error details for any resource that has a non-empty error.
		// The table above truncates errors to 60 chars; this section shows them
		// in full so the user can diagnose failures without digging into logs.
		var errorResources []types.DeploymentResource
		for _, r := range d.Resources {
			if r.Error != "" {
				errorResources = append(errorResources, r)
			}
		}
		if len(errorResources) > 0 {
			_, _ = fmt.Fprintln(r.out)
			_, _ = fmt.Fprintln(r.out, r.renderSection("Errors:"))
			for _, resource := range errorResources {
				_, _ = fmt.Fprintf(r.out, "\n  %s (%s):\n", resource.Name, resource.Kind)
				_, _ = fmt.Fprintf(r.out, "    %s\n", resource.Error)
			}
		}
	}
}

func renderResourceStatus(r *Renderer, status types.DeploymentResourceStatus) string {
	return r.renderStatus(string(status))
}

// printDeploymentSummaries renders a compact listing table for
// `praxis list deployments`.
func printDeploymentSummaries(r *Renderer, summaries []types.DeploymentSummary) {
	if len(summaries) == 0 {
		_, _ = fmt.Fprintln(r.out, r.renderMuted("No deployments found."))
		return
	}
	headers := []string{"KEY", "STATUS", "RESOURCES", "CREATED", "UPDATED"}
	rows := make([][]string, 0, len(summaries))
	for _, s := range summaries {
		rows = append(rows, []string{
			s.Key,
			r.renderStatus(string(s.Status)),
			fmt.Sprintf("%d", s.Resources),
			formatTime(s.CreatedAt),
			formatTime(s.UpdatedAt),
		})
	}
	printTable(r, headers, rows)
}

// --------------------------------------------------------------------------
// Plan formatters
// --------------------------------------------------------------------------

// printPlan renders a plan summary to stdout. Each resource
// is shown with its planned operation and any field-level changes.
func printPlan(r *Renderer, plan *types.PlanResult) {
	if plan == nil || len(plan.Resources) == 0 {
		_, _ = fmt.Fprintln(r.out, r.renderMuted("No changes. Infrastructure is up-to-date."))
		return
	}

	for _, rd := range plan.Resources {
		symbol := operationSymbol(rd.Operation)
		_, _ = fmt.Fprintln(r.out, r.renderDiff(rd.Operation, fmt.Sprintf("%s %s (%s)", symbol, rd.ResourceKey, rd.ResourceType)))

		for _, fd := range rd.FieldDiffs {
			line := ""
			switch rd.Operation {
			case types.OpCreate:
				line = fmt.Sprintf("    + %s = %v", fd.Path, fd.NewValue)
			case types.OpDelete:
				line = fmt.Sprintf("    - %s = %v", fd.Path, fd.OldValue)
			case types.OpUpdate:
				line = fmt.Sprintf("    ~ %s: %v => %v", fd.Path, fd.OldValue, fd.NewValue)
			}
			if line != "" {
				_, _ = fmt.Fprintln(r.out, r.renderDiff(rd.Operation, line))
			}
		}
		_, _ = fmt.Fprintln(r.out)
	}

	_, _ = fmt.Fprintln(r.out, r.renderSection(plan.Summary.String()))
}

func printDataSources(r *Renderer, dataSources map[string]types.DataSourceResult) {
	if len(dataSources) == 0 {
		return
	}

	names := make([]string, 0, len(dataSources))
	for name := range dataSources {
		names = append(names, name)
	}
	sort.Strings(names)

	_, _ = fmt.Fprintln(r.out, r.renderSection("Data sources:"))
	for _, name := range names {
		result := dataSources[name]
		_, _ = fmt.Fprintf(r.out, "  %s (%s)\n", name, result.Kind)
		keys := make([]string, 0, len(result.Outputs))
		for key := range result.Outputs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, _ = fmt.Fprintf(r.out, "    %s = %v\n", key, result.Outputs[key])
		}
	}
	_, _ = fmt.Fprintln(r.out)
}

// operationSymbol returns the prefix for each diff operation.
func operationSymbol(op types.DiffOperation) string {
	switch op {
	case types.OpCreate:
		return "+"
	case types.OpUpdate:
		return "~"
	case types.OpDelete:
		return "-"
	default:
		return " "
	}
}

// --------------------------------------------------------------------------
// Event formatters
// --------------------------------------------------------------------------

// printEvents renders deployment progress events in a human-readable timeline
// format. Used by the `observe` command for incremental progress display.
func printEvents(r *Renderer, events []orchestrator.DeploymentEvent) {
	for i := range events {
		e := &events[i]
		ts := formatTime(e.CreatedAt)
		// Build the event line: [timestamp] STATUS resource: message
		parts := []string{r.renderMuted(fmt.Sprintf("[%s]", ts))}
		if e.Status != "" {
			parts = append(parts, r.renderStatus(string(e.Status)))
		}
		if e.ResourceName != "" {
			resource := fmt.Sprintf("%s/%s:", e.ResourceKind, e.ResourceName)
			if r.styles {
				resource = r.theme.Header.Render(resource)
			}
			parts = append(parts, resource)
		}
		parts = append(parts, e.Message)
		if e.Error != "" {
			parts = append(parts, r.renderDiff(types.OpDelete, fmt.Sprintf("(error: %s)", e.Error)))
		}
		_, _ = fmt.Fprintln(r.out, strings.Join(parts, " "))
	}
}

// --------------------------------------------------------------------------
// Import / resource formatters
// --------------------------------------------------------------------------

// printImportResult renders the result of a resource import operation.
func printImportResult(r *Renderer, resp *types.ImportResponse) {
	r.writeLabelValue("Key", 7, resp.Key)
	r.writeLabelStyledValue("Status", 7, r.renderStatus(string(resp.Status)))
	if len(resp.Outputs) > 0 {
		_, _ = fmt.Fprintln(r.out, r.renderSection("Outputs:"))
		keys := make([]string, 0, len(resp.Outputs))
		for k := range resp.Outputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, _ = fmt.Fprintf(r.out, "  %s = %v\n", key, resp.Outputs[key])
		}
	}
}

// --------------------------------------------------------------------------
// Utility helpers
// --------------------------------------------------------------------------

// formatTime formats a time value into a consistent, human-readable UTC string.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

// truncate shortens a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// --------------------------------------------------------------------------
// Timeout helpers
// --------------------------------------------------------------------------

// isTimeoutError returns true if the context's deadline was exceeded.
func isTimeoutError(ctx context.Context, err error) bool {
	return ctx.Err() == context.DeadlineExceeded
}

// printTimeoutError emits a user-friendly message when a polling wait exceeds
// the configured --timeout duration.
func printTimeoutError(r *Renderer, timeout time.Duration, deploymentKey string) {
	prefix := "Error:"
	if r.styles {
		prefix = r.theme.Error.Render(prefix)
	}
	_, _ = fmt.Fprintf(r.errOut, "\n%s timed out after %s waiting for deployment %q\n", prefix, timeout, deploymentKey)
	_, _ = fmt.Fprintf(r.errOut, "Resume watching:   praxis observe Deployment/%s\n", deploymentKey)
	_, _ = fmt.Fprintf(r.errOut, "Full details:      praxis get Deployment/%s\n", deploymentKey)
}
