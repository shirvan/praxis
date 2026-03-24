package cli

import (
	"context"
	"encoding/json"
	"fmt"
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

// printTable renders a simple ASCII table with aligned columns to stdout.
// It uses a tabwriter for consistent spacing across all commands.
func printTable(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	// Header row in all caps.
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	// Separator line under each header.
	separators := make([]string, len(headers))
	for i, h := range headers {
		separators[i] = strings.Repeat("-", len(h))
	}
	fmt.Fprintln(w, strings.Join(separators, "\t"))
	// Data rows.
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}

// --------------------------------------------------------------------------
// Deployment formatters
// --------------------------------------------------------------------------

// printDeploymentDetail renders a full deployment record with per-resource
// breakdown and outputs. This is the main display for `praxis get Deployment/<key>`.
func printDeploymentDetail(d *types.DeploymentDetail) {
	fmt.Printf("Deployment: %s\n", d.Key)
	fmt.Printf("Status:     %s\n", d.Status)
	if d.TemplatePath != "" {
		fmt.Printf("Template:   %s\n", d.TemplatePath)
	}
	if d.Error != "" {
		fmt.Printf("Error:      %s\n", d.Error)
	}
	fmt.Printf("Created:    %s\n", formatTime(d.CreatedAt))
	fmt.Printf("Updated:    %s\n", formatTime(d.UpdatedAt))
	fmt.Println()

	if len(d.Resources) > 0 {
		headers := []string{"RESOURCE", "KIND", "STATUS", "ERROR"}
		rows := make([][]string, 0, len(d.Resources))
		for _, r := range d.Resources {
			errMsg := "-"
			if r.Error != "" {
				errMsg = truncate(r.Error, 60)
			}
			rows = append(rows, []string{
				r.Name,
				r.Kind,
				string(r.Status),
				errMsg,
			})
		}
		printTable(headers, rows)

		// Print outputs section for resources that have them.
		hasOutputs := false
		for _, r := range d.Resources {
			if len(r.Outputs) > 0 {
				hasOutputs = true
				break
			}
		}
		if hasOutputs {
			fmt.Println()
			fmt.Println("Outputs:")
			for _, r := range d.Resources {
				for key, value := range r.Outputs {
					fmt.Printf("  %s.%s = %v\n", r.Name, key, value)
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
			fmt.Println()
			fmt.Println("Errors:")
			for _, r := range errorResources {
				fmt.Printf("\n  %s (%s):\n", r.Name, r.Kind)
				fmt.Printf("    %s\n", r.Error)
			}
		}
	}
}

// printDeploymentSummaries renders a compact listing table for
// `praxis list deployments`.
func printDeploymentSummaries(summaries []types.DeploymentSummary) {
	if len(summaries) == 0 {
		fmt.Println("No deployments found.")
		return
	}
	headers := []string{"KEY", "STATUS", "RESOURCES", "CREATED", "UPDATED"}
	rows := make([][]string, 0, len(summaries))
	for _, s := range summaries {
		rows = append(rows, []string{
			s.Key,
			string(s.Status),
			fmt.Sprintf("%d", s.Resources),
			formatTime(s.CreatedAt),
			formatTime(s.UpdatedAt),
		})
	}
	printTable(headers, rows)
}

// --------------------------------------------------------------------------
// Plan formatters
// --------------------------------------------------------------------------

// printPlan renders a plan summary to stdout. Each resource
// is shown with its planned operation and any field-level changes.
func printPlan(plan *types.PlanResult) {
	if plan == nil || len(plan.Resources) == 0 {
		fmt.Println("No changes. Infrastructure is up-to-date.")
		return
	}

	for _, rd := range plan.Resources {
		symbol := operationSymbol(rd.Operation)
		fmt.Printf("%s %s (%s)\n", symbol, rd.ResourceKey, rd.ResourceType)

		for _, fd := range rd.FieldDiffs {
			switch rd.Operation {
			case types.OpCreate:
				fmt.Printf("    + %s = %v\n", fd.Path, fd.NewValue)
			case types.OpDelete:
				fmt.Printf("    - %s = %v\n", fd.Path, fd.OldValue)
			case types.OpUpdate:
				fmt.Printf("    ~ %s: %v => %v\n", fd.Path, fd.OldValue, fd.NewValue)
			}
		}
		fmt.Println()
	}

	fmt.Println(plan.Summary.String())
}

func printDataSources(dataSources map[string]types.DataSourceResult) {
	if len(dataSources) == 0 {
		return
	}

	names := make([]string, 0, len(dataSources))
	for name := range dataSources {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Println("Data sources:")
	for _, name := range names {
		result := dataSources[name]
		fmt.Printf("  %s (%s)\n", name, result.Kind)
		keys := make([]string, 0, len(result.Outputs))
		for key := range result.Outputs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("    %s = %v\n", key, result.Outputs[key])
		}
	}
	fmt.Println()
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
func printEvents(events []orchestrator.DeploymentEvent) {
	for _, e := range events {
		ts := formatTime(e.CreatedAt)
		// Build the event line: [timestamp] STATUS resource: message
		parts := []string{fmt.Sprintf("[%s]", ts)}
		if e.Status != "" {
			parts = append(parts, string(e.Status))
		}
		if e.ResourceName != "" {
			parts = append(parts, fmt.Sprintf("%s/%s:", e.ResourceKind, e.ResourceName))
		}
		parts = append(parts, e.Message)
		if e.Error != "" {
			parts = append(parts, fmt.Sprintf("(error: %s)", e.Error))
		}
		fmt.Println(strings.Join(parts, " "))
	}
}

// --------------------------------------------------------------------------
// Import / resource formatters
// --------------------------------------------------------------------------

// printImportResult renders the result of a resource import operation.
func printImportResult(resp *types.ImportResponse) {
	fmt.Printf("Key:    %s\n", resp.Key)
	fmt.Printf("Status: %s\n", resp.Status)
	if len(resp.Outputs) > 0 {
		fmt.Println("Outputs:")
		for k, v := range resp.Outputs {
			fmt.Printf("  %s = %v\n", k, v)
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
func printTimeoutError(timeout time.Duration, deploymentKey string) {
	fmt.Fprintf(os.Stderr, "\nError: timed out after %s waiting for deployment %q\n", timeout, deploymentKey)
	fmt.Fprintf(os.Stderr, "Resume watching:   praxis observe Deployment/%s\n", deploymentKey)
	fmt.Fprintf(os.Stderr, "Full details:      praxis get Deployment/%s\n", deploymentKey)
}
