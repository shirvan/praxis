// output.go contains all output formatting functions for the CLI.
//
// The CLI supports two output modes:
//   - table (default): human-friendly tables and label/value lists via Renderer
//   - json: machine-readable indented JSON for scripting and AI agent consumption
//
// Every command follows the same pattern:
//  1. Call the Restate ingress client to fetch data.
//  2. If --output json, call printJSON and return.
//  3. Otherwise, call the appropriate print* function for styled table output.
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

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/internal/core/diff"
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

// printTable is a convenience wrapper that delegates to Renderer.printTable.
// It is called by command handlers that already hold a renderer reference.
func printTable(r *Renderer, headers []string, rows [][]string) {
	r.printTable(headers, rows)
}

// --------------------------------------------------------------------------
// Deployment formatters
// --------------------------------------------------------------------------

// printDeploymentDetail renders a full deployment record with per-resource
// breakdown, inputs, and outputs. This is the main display for
// `praxis get Deployment/<key>`. The optional resourceInputs map holds the
// desired input spec per resource name, fetched from each driver.
func printDeploymentDetail(r *Renderer, d *types.DeploymentDetail, resourceInputs ...map[string]map[string]any) {
	// Merge the variadic inputs into a single map for easy lookup.
	var inputs map[string]map[string]any
	if len(resourceInputs) > 0 {
		inputs = resourceInputs[0]
	}

	r.writeLabelValue("Deployment", 11, d.Key)
	r.writeLabelStyledValue("Status", 11, r.renderStatus(string(d.Status)))
	if d.TemplatePath != "" {
		r.writeLabelValue("Template", 11, d.TemplatePath)
	}
	if d.Workspace != "" {
		r.writeLabelValue("Workspace", 11, d.Workspace)
	}
	if d.ErrorCode != "" {
		r.writeLabelValue("ErrorCode", 11, string(d.ErrorCode))
	}
	if d.Error != "" {
		r.writeLabelValue("Error", 11, d.Error)
	}
	r.writeLabelValue("Created", 11, r.renderMuted(formatTime(d.CreatedAt)))
	r.writeLabelValue("Updated", 11, r.renderMuted(formatTime(d.UpdatedAt)))
	_, _ = fmt.Fprintln(r.out)

	if len(d.Resources) > 0 {
		headers := []string{"RESOURCE", "KIND", "KEY", "STATUS", "ERROR"}
		rows := make([][]string, 0, len(d.Resources))
		for _, resource := range d.Resources {
			errMsg := "-"
			if resource.Error != "" {
				errMsg = truncate(resource.Error, 60)
			}
			rows = append(rows, []string{
				resource.Name,
				resource.Kind,
				resource.Key,
				renderResourceStatus(r, resource.Status),
				errMsg,
			})
		}
		printTable(r, headers, rows)

		// Print dependency graph info for resources that have dependencies.
		hasDeps := false
		for _, res := range d.Resources {
			if len(res.DependsOn) > 0 {
				hasDeps = true
				break
			}
		}
		if hasDeps {
			_, _ = fmt.Fprintln(r.out)
			_, _ = fmt.Fprintln(r.out, r.renderSection("Dependencies:"))
			for _, resource := range d.Resources {
				if len(resource.DependsOn) > 0 {
					_, _ = fmt.Fprintf(r.out, "  %s → %s\n", resource.Name, strings.Join(resource.DependsOn, ", "))
				}
			}
		}

		// Print inputs section if available.
		if len(inputs) > 0 {
			_, _ = fmt.Fprintln(r.out)
			_, _ = fmt.Fprintln(r.out, r.renderSection("Inputs:"))
			for _, resource := range d.Resources {
				resInputs, ok := inputs[resource.Name]
				if !ok || len(resInputs) == 0 {
					continue
				}
				keys := make([]string, 0, len(resInputs))
				for key := range resInputs {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					_, _ = fmt.Fprintf(r.out, "  %s.%s = %v\n", resource.Name, key, resInputs[key])
				}
			}
		}

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

// renderResourceStatus applies status-aware coloring to a resource-level
// status string. Delegates to the renderer's generic renderStatus.
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

// printPlan renders a plan summary to stdout using Terraform-inspired block
// formatting. Each resource shows a descriptive comment, block structure with
// nested field grouping, and aligned values.
func printPlan(r *Renderer, plan *types.PlanResult) {
	if plan == nil || len(plan.Resources) == 0 {
		_, _ = fmt.Fprintln(r.out, r.renderMuted("No changes. Infrastructure is up-to-date."))
		return
	}

	for _, rd := range plan.Resources {
		if rd.Operation == types.OpNoOp {
			continue
		}
		printResourceDiff(r, rd)
	}

	_, _ = fmt.Fprintln(r.out, r.renderSection(plan.Summary.String()))
}

func printResourceDiff(r *Renderer, rd types.ResourceDiff) {
	actionVerb := "will be created"
	switch rd.Operation {
	case types.OpUpdate:
		actionVerb = "will be updated in-place"
	case types.OpDelete:
		actionVerb = "will be destroyed"
	}

	// Comment line describing the action.
	_, _ = fmt.Fprintln(r.out, r.renderMuted(fmt.Sprintf("  # %s %q %s", rd.ResourceType, rd.ResourceKey, actionVerb)))

	symbol := operationSymbol(rd.Operation)

	if len(rd.FieldDiffs) == 0 {
		_, _ = fmt.Fprintln(r.out, r.renderDiff(rd.Operation, fmt.Sprintf("  %s resource %q %q {}", symbol, rd.ResourceType, rd.ResourceKey)))
		_, _ = fmt.Fprintln(r.out)
		return
	}

	// Resource block header.
	_, _ = fmt.Fprintln(r.out, r.renderDiff(rd.Operation, fmt.Sprintf("  %s resource %q %q {", symbol, rd.ResourceType, rd.ResourceKey)))

	// Group and render field diffs with alignment.
	nodes := diff.GroupFields(rd.FieldDiffs)
	printFieldNodes(r, rd.Operation, nodes, 6)

	// Close block.
	_, _ = fmt.Fprintln(r.out, r.renderDiff(rd.Operation, "    }"))
	_, _ = fmt.Fprintln(r.out)
}

func printFieldNodes(r *Renderer, op types.DiffOperation, nodes []diff.FieldNode, indent int) {
	symbol := operationSymbol(op)
	pad := strings.Repeat(" ", indent)

	maxKeyLen := 0
	for _, n := range nodes {
		if len(n.Key) > maxKeyLen {
			maxKeyLen = len(n.Key)
		}
	}

	for _, n := range nodes {
		if n.IsGroup {
			_, _ = fmt.Fprintln(r.out, r.renderDiff(op, fmt.Sprintf("%s%s %-*s {", pad, symbol, maxKeyLen, n.Key)))
			printFieldNodes(r, op, n.Children, indent+4)
			_, _ = fmt.Fprintln(r.out, r.renderDiff(op, fmt.Sprintf("%s  }", pad)))
		} else {
			value := ""
			switch op {
			case types.OpCreate:
				value = formatPlanValue(n.Diff.NewValue)
			case types.OpDelete:
				value = formatPlanValue(n.Diff.OldValue)
			case types.OpUpdate:
				value = fmt.Sprintf("%s => %s", formatPlanValue(n.Diff.OldValue), formatPlanValue(n.Diff.NewValue))
			}
			_, _ = fmt.Fprintln(r.out, r.renderDiff(op, fmt.Sprintf("%s%s %-*s = %s", pad, symbol, maxKeyLen, n.Key, value)))
		}
	}
}

// formatPlanValue formats a value for plan output with proper quoting.
func formatPlanValue(v any) string {
	if v == nil {
		return "(known after apply)"
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
	default:
		return fmt.Sprintf("%v", val)
	}
}

// printGraph renders the resource dependency DAG from the plan response.
// It reconstructs a dag.Graph from the GraphNode slice and renders it
// using the ASCII box-drawing layout.
func printGraph(r *Renderer, graphNodes []types.GraphNode) {
	if len(graphNodes) == 0 {
		return
	}

	// Reconstruct ResourceNodes so we can build a dag.Graph.
	nodes := make([]*types.ResourceNode, len(graphNodes))
	kindMap := make(map[string]string, len(graphNodes))
	for i, gn := range graphNodes {
		nodes[i] = &types.ResourceNode{
			Name:         gn.Name,
			Kind:         gn.Kind,
			Key:          gn.Name,
			Spec:         []byte(`{}`),
			Dependencies: gn.Dependencies,
		}
		kindMap[gn.Name] = shortKind(gn.Kind)
	}

	g, err := dag.NewGraph(nodes)
	if err != nil {
		_, _ = fmt.Fprintf(r.out, "  (graph error: %v)\n", err)
		return
	}

	output := dag.Render(g, func(name string) string {
		return kindMap[name]
	})
	_, _ = fmt.Fprintln(r.out, output)
}

// shortKind strips common prefixes from resource kinds for compact display.
// "AWS::S3::Bucket" → "S3Bucket", "AWS::EC2::Instance" → "EC2Instance".
func shortKind(kind string) string {
	// Strip "AWS::" prefix.
	kind = strings.TrimPrefix(kind, "AWS::")
	// Collapse remaining "::" separators.
	kind = strings.ReplaceAll(kind, "::", "")
	return kind
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

func printCloudEvents(r *Renderer, events []orchestrator.SequencedCloudEvent) {
	for i := range events {
		e := events[i].Event
		ts := formatTime(e.Time())
		parts := []string{r.renderMuted(fmt.Sprintf("[%s]", ts))}
		status := cloudEventStatus(e)
		if status != "" {
			parts = append(parts, r.renderStatus(status))
		} else {
			parts = append(parts, r.renderMuted(e.Type()))
		}
		if e.Subject() != "" {
			resource := fmt.Sprintf("%s/%s:", cloudEventResourceKind(e), e.Subject())
			if r.styles {
				resource = r.theme.Header.Render(resource)
			}
			parts = append(parts, resource)
		}
		message := cloudEventMessage(e)
		if message == "" {
			message = e.Type()
		}
		parts = append(parts, message)
		if errMsg := cloudEventError(e); errMsg != "" {
			parts = append(parts, r.renderDiff(types.OpDelete, fmt.Sprintf("(error: %s)", errMsg)))
		}
		_, _ = fmt.Fprintln(r.out, strings.Join(parts, " "))
	}
}

func filterCloudEvents(events []orchestrator.SequencedCloudEvent, query orchestrator.EventQuery) []orchestrator.SequencedCloudEvent {
	out := make([]orchestrator.SequencedCloudEvent, 0, len(events))
	for _, event := range events {
		if matchesCloudEvent(event, query) {
			out = append(out, event)
		}
	}
	return out
}

func matchesCloudEvent(record orchestrator.SequencedCloudEvent, query orchestrator.EventQuery) bool {
	event := record.Event
	if query.DeploymentKey != "" && cloudEventExtension(event, orchestrator.EventExtensionDeployment) != query.DeploymentKey {
		return false
	}
	if query.Workspace != "" && cloudEventExtension(event, orchestrator.EventExtensionWorkspace) != query.Workspace {
		return false
	}
	if query.TypePrefix != "" && !strings.HasPrefix(event.Type(), query.TypePrefix) {
		return false
	}
	if query.Severity != "" && cloudEventExtension(event, orchestrator.EventExtensionSeverity) != query.Severity {
		return false
	}
	if query.Resource != "" && event.Subject() != query.Resource {
		return false
	}
	if !query.Since.IsZero() && event.Time().Before(query.Since) {
		return false
	}
	if !query.Until.IsZero() && event.Time().After(query.Until) {
		return false
	}
	return true
}

func isTerminalCloudEvent(record orchestrator.SequencedCloudEvent) bool {
	status := types.DeploymentStatus(cloudEventStatus(record.Event))
	if status != "" && isTerminalStatus(status) {
		return true
	}
	switch record.Event.Type() {
	case orchestrator.EventTypeDeploymentCompleted,
		orchestrator.EventTypeDeploymentFailed,
		orchestrator.EventTypeDeploymentCancelled,
		orchestrator.EventTypeDeploymentDeleteDone,
		orchestrator.EventTypeDeploymentDeleteFailed:
		return true
	default:
		return false
	}
}

func cloudEventPayload(event orchestrator.SequencedCloudEvent) map[string]any {
	var payload map[string]any
	if err := event.Event.DataAs(&payload); err != nil {
		return nil
	}
	return payload
}

func cloudEventDataString(event any, key string) string {
	switch typed := event.(type) {
	case orchestrator.SequencedCloudEvent:
		payload := cloudEventPayload(typed)
		if payload == nil {
			return ""
		}
		value, ok := payload[key]
		if !ok || value == nil {
			return ""
		}
		if s, ok := value.(string); ok {
			return s
		}
		return fmt.Sprint(value)
	default:
		return ""
	}
}

func cloudEventMessage(event cloudevents.Event) string {
	return cloudEventDataString(orchestrator.SequencedCloudEvent{Event: event}, "message")
}

func cloudEventError(event cloudevents.Event) string {
	return cloudEventDataString(orchestrator.SequencedCloudEvent{Event: event}, "error")
}

func cloudEventStatus(event cloudevents.Event) string {
	return cloudEventDataString(orchestrator.SequencedCloudEvent{Event: event}, "status")
}

func cloudEventResourceKind(event cloudevents.Event) string {
	kind := cloudEventExtension(event, orchestrator.EventExtensionResourceKind)
	if kind != "" {
		return kind
	}
	if payloadKind := cloudEventDataString(orchestrator.SequencedCloudEvent{Event: event}, "resourceKind"); payloadKind != "" {
		return payloadKind
	}
	return "Resource"
}

func cloudEventExtension(event cloudevents.Event, key string) string {
	value, ok := event.Extensions()[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
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
