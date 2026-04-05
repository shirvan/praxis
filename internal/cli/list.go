package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/workspace"
)

// newListCmd builds the `praxis list` subcommand.
//
// List supports multiple resource types: deployments, templates, workspaces,
// sinks, events, concierge history.
//
// Usage:
//
//	praxis list deployments
//	praxis list templates
//	praxis list workspaces
//	praxis list sinks
//	praxis list events [Kind/Key]
//	praxis list concierge
func newListCmd(flags *rootFlags) *cobra.Command {
	var (
		wsFilter   string
		sinceRaw   string
		typePrefix string
		severity   string
		resource   string
		limit      int
		session    string
	)

	cmd := &cobra.Command{
		Use:   "list <resource-type> [scope]",
		Short: "List resources",
		Long: `List queries Praxis Core for known resources of the specified type.

Supported resource types:

    praxis list deployments           List all known deployments
    praxis list templates             List registered templates
    praxis list workspaces            List workspaces
    praxis list sinks                 List notification sinks
    praxis list events                Cross-deployment event search
    praxis list events Deployment/x   Events for one deployment
    praxis list concierge             Show conversation history
    praxis list <Kind>                List cloud resources by Kind (e.g. S3Bucket, EC2Instance)

Use -o json for machine-readable output.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := args[0]
			switch resourceType {
			case "deployments", "deployment", "deploy":
				return listDeployments(flags, wsFilter)
			case "templates", "template":
				return listTemplates(flags)
			case "workspaces", "workspace":
				return listWorkspaces(flags)
			case "sinks", "sink":
				return listSinks(flags)
			case "events", "event":
				var scope string
				if len(args) > 1 {
					scope = args[1]
				}
				return listEvents(flags, scope, wsFilter, sinceRaw, typePrefix, severity, resource, limit)
			case "concierge":
				return listConciergeHistory(flags, session)
			default:
				// Cloud resource Kind (e.g. S3Bucket, EC2Instance, VPC).
				return listCloudResources(flags, resourceType, wsFilter)
			}
		},
	}

	cmd.Flags().StringVarP(&wsFilter, "workspace", "w", "", "Filter by workspace (env: PRAXIS_WORKSPACE)")
	cmd.Flags().StringVar(&sinceRaw, "since", "", "Show events from the last duration (e.g. 1h, 7d)")
	cmd.Flags().StringVar(&typePrefix, "type", "", "Filter events by type prefix")
	cmd.Flags().StringVar(&severity, "severity", "", "Filter events by severity (info, warn, error)")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter events by resource name")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum events to return")
	cmd.Flags().StringVar(&session, "session", "", "Concierge session ID (default: \"default\")")

	return cmd
}

// listDeployments queries the global deployment index and renders the results.
func listDeployments(flags *rootFlags, workspace string) error {
	client := flags.newClient()
	renderer := flags.renderer()
	ctx := context.Background()

	summaries, err := client.ListDeployments(ctx, workspace)
	if err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(summaries)
	}

	printDeploymentSummaries(renderer, summaries)
	return nil
}

// listTemplates queries the template registry and renders the results.
func listTemplates(flags *rootFlags) error {
	renderer := flags.renderer()
	client := flags.newClient()
	ctx := context.Background()

	templates, err := client.ListTemplates(ctx)
	if err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(templates)
	}

	if len(templates) == 0 {
		_, _ = fmt.Fprintln(renderer.out, renderer.renderMuted("No templates registered."))
		return nil
	}

	headers := []string{"NAME", "DESCRIPTION", "UPDATED"}
	rows := make([][]string, 0, len(templates))
	for _, t := range templates {
		desc := t.Description
		if desc == "" {
			desc = "-"
		}
		rows = append(rows, []string{
			t.Name,
			truncate(desc, 50),
			formatTime(t.UpdatedAt),
		})
	}
	printTable(renderer, headers, rows)
	return nil
}

// listWorkspaces queries the workspace index and renders the results.
func listWorkspaces(flags *rootFlags) error {
	renderer := flags.renderer()
	client := flags.newClient()
	ctx := context.Background()

	names, err := client.ListWorkspaces(ctx)
	if err != nil {
		return err
	}

	if len(names) == 0 {
		if flags.outputFormat() == OutputJSON {
			return printJSON([]workspace.WorkspaceInfo{})
		}
		_, _ = fmt.Fprintln(renderer.out, renderer.renderMuted("No workspaces configured."))
		return nil
	}

	cliCfg := LoadCLIConfig()
	infos := make([]workspace.WorkspaceInfo, 0, len(names))
	for _, n := range names {
		info, err := client.GetWorkspace(ctx, n)
		if err != nil {
			return err
		}
		infos = append(infos, *info)
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(infos)
	}

	headers := []string{"NAME", "ACCOUNT", "REGION", "ACTIVE"}
	rows := make([][]string, 0, len(infos))
	for _, info := range infos {
		marker := ""
		if info.Name == cliCfg.ActiveWorkspace {
			marker = "yes"
		}
		rows = append(rows, []string{info.Name, info.Account, info.Region, marker})
	}
	printTable(renderer, headers, rows)
	return nil
}

// listSinks queries the notification sink configuration and renders the results.
func listSinks(flags *rootFlags) error {
	sinks, err := flags.newClient().ListNotificationSinks(context.Background())
	if err != nil {
		return err
	}
	if flags.outputFormat() == OutputJSON {
		return printJSON(sinks)
	}
	rows := make([][]string, 0, len(sinks))
	for i := range sinks {
		rows = append(rows, []string{sinks[i].Name, sinks[i].Type, sinkStateLabel(sinks[i]), fmt.Sprintf("%d", sinks[i].ConsecutiveFailures), sinks[i].URL})
	}
	printTable(flags.renderer(), []string{"NAME", "TYPE", "STATE", "FAILURES", "URL"}, rows)
	return nil
}

// listEvents handles both per-deployment event listing and cross-deployment queries.
func listEvents(flags *rootFlags, scope, wsFilter, sinceRaw, typePrefix, severity, resource string, limit int) error {
	since, err := parseLookback(sinceRaw)
	if err != nil {
		return err
	}

	if scope != "" {
		// Per-deployment event listing: list events Deployment/<key>
		kind, key, err := parseKindKey(scope)
		if err != nil {
			return err
		}
		if kind != "Deployment" {
			return fmt.Errorf("events list only supports Deployment resources, got %q", kind)
		}
		query := orchestrator.EventQuery{
			DeploymentKey: key,
			TypePrefix:    typePrefix,
			Severity:      severity,
			Resource:      resource,
			Since:         since,
		}
		return listDeploymentEvents(context.Background(), flags.newClient(), key, query, flags.outputFormat(), flags.renderer())
	}

	// Cross-deployment event query
	query := orchestrator.EventQuery{
		Workspace:  wsFilter,
		TypePrefix: typePrefix,
		Severity:   severity,
		Resource:   resource,
		Since:      since,
		Limit:      limit,
	}
	events, err := flags.newClient().QueryCloudEvents(context.Background(), query)
	if err != nil {
		return err
	}
	if flags.outputFormat() == OutputJSON {
		return printJSON(events)
	}
	printCloudEvents(flags.renderer(), events)
	return nil
}

// listConciergeHistory shows conversation history for a concierge session.
func listConciergeHistory(flags *rootFlags, session string) error {
	if session == "" {
		session = "default"
	}

	client := flags.newClient()

	messages, err := client.ConciergeGetHistory(context.Background(), session)
	if err != nil {
		if isConciergeUnavailable(err) {
			_, _ = fmt.Fprint(flags.renderer().out, conciergeUnavailableMsg)
			return nil
		}
		return fmt.Errorf("list concierge: %w", err)
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(messages)
	}

	if len(messages) == 0 {
		_, _ = fmt.Fprintln(flags.renderer().out, flags.renderer().renderMuted("No conversation history."))
		return nil
	}

	for _, msg := range messages {
		fmt.Printf("[%s] %s\n%s\n\n", msg.Timestamp, msg.Role, msg.Content)
	}
	return nil
}

// listCloudResources queries all deployments for resources matching the given
// Kind and renders them as a table. This walks the deployment index because
// there is no dedicated per-Kind resource index service yet.
func listCloudResources(flags *rootFlags, kind, workspace string) error {
	client := flags.newClient()
	ctx := context.Background()

	items, err := client.ListResourcesByKind(ctx, kind, workspace)
	if err != nil {
		return fmt.Errorf("listing %s resources: %w", kind, err)
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(items)
	}

	renderer := flags.renderer()
	if len(items) == 0 {
		_, _ = fmt.Fprintf(renderer.out, "%s\n", renderer.renderMuted(fmt.Sprintf("No %s resources found.", kind)))
		return nil
	}

	headers := []string{"KEY", "DEPLOYMENT", "WORKSPACE", "STATUS"}
	rows := make([][]string, 0, len(items))
	for _, r := range items {
		rows = append(rows, []string{r.Key, r.DeploymentKey, r.Workspace, r.Status})
	}
	printTable(renderer, headers, rows)
	return nil
}
