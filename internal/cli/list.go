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
// sinks, events.
//
// Usage:
//
//	praxis list deployments
//	praxis list templates
//	praxis list workspaces
//	praxis list sinks
//	praxis list events [Kind/Key]
func newListCmd(flags *rootFlags) *cobra.Command {
	var (
		wsFilter   string
		sinceRaw   string
		typePrefix string
		severity   string
		resource   string
		limit      int
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
    praxis list schemas               List resource kinds and their CUE schemas (offline)
    praxis list events Deployment/x   Events for one deployment
    praxis list <Kind>                List cloud resources by Kind (e.g. S3Bucket, EC2Instance)

Use -o json for machine-readable output.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			resourceType := args[0]
			switch resourceType {
			case "deployments", "deployment", "deploy":
				return listDeployments(ctx, flags, wsFilter)
			case "templates", "template":
				return listTemplates(ctx, flags)
			case "workspaces", "workspace":
				return listWorkspaces(ctx, flags)
			case "sinks", "sink":
				return listSinks(ctx, flags)
			case "schemas", "schema":
				return listSchemas(flags)
			case "events", "event":
				var scope string
				if len(args) > 1 {
					scope = args[1]
				}
				return listEvents(ctx, flags, scope, wsFilter, sinceRaw, typePrefix, severity, resource, limit)
			default:
				// Cloud resource Kind (e.g. S3Bucket, EC2Instance, VPC).
				return listCloudResources(ctx, flags, resourceType, wsFilter)
			}
		},
	}

	cmd.Flags().StringVarP(&wsFilter, "workspace", "w", "", "Filter by workspace (env: PRAXIS_WORKSPACE)")
	cmd.Flags().StringVar(&sinceRaw, "since", "", "Show events from the last duration (e.g. 1h, 7d)")
	cmd.Flags().StringVar(&typePrefix, "type", "", "Filter events by type prefix")
	cmd.Flags().StringVar(&severity, "severity", "", "Filter events by severity (info, warn, error)")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter events by resource name")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum events to return")

	return cmd
}

// listDeployments queries the global deployment index and renders the results.
func listDeployments(ctx context.Context, flags *rootFlags, workspace string) error {
	client := flags.newClient()
	renderer := flags.renderer()

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
func listTemplates(ctx context.Context, flags *rootFlags) error {
	renderer := flags.renderer()
	client := flags.newClient()

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
func listWorkspaces(ctx context.Context, flags *rootFlags) error {
	renderer := flags.renderer()
	client := flags.newClient()

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
// Header values are redacted in every output mode.
func listSinks(ctx context.Context, flags *rootFlags) error {
	sinks, err := flags.newClient().ListNotificationSinks(ctx)
	if err != nil {
		return err
	}
	for i := range sinks {
		sinks[i] = redactSinkHeaders(sinks[i])
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

// listEvents lists events for a single deployment. Events are stored per
// deployment; pass a Deployment/<key> scope to select the stream.
func listEvents(ctx context.Context, flags *rootFlags, scope, wsFilter, sinceRaw, typePrefix, severity, resource string, limit int) error {
	since, err := parseLookback(sinceRaw)
	if err != nil {
		return err
	}

	if scope == "" {
		return fmt.Errorf("praxis list events requires a deployment scope, e.g. praxis list events Deployment/my-app")
	}

	kind, key, err := parseKindKey(scope)
	if err != nil {
		return err
	}
	if kind != "Deployment" {
		return fmt.Errorf("events list only supports Deployment resources, got %q", kind)
	}
	query := orchestrator.EventQuery{
		DeploymentKey: key,
		Workspace:     wsFilter,
		TypePrefix:    typePrefix,
		Severity:      severity,
		Resource:      resource,
		Since:         since,
		Limit:         limit,
	}
	return listDeploymentEvents(ctx, flags.newClient(), key, query, flags.outputFormat(), flags.renderer())
}

// listCloudResources queries all deployments for resources matching the given
// Kind and renders them as a table. This walks the deployment index because
// there is no dedicated per-Kind resource index service yet.
func listCloudResources(ctx context.Context, flags *rootFlags, kind, workspace string) error {
	client := flags.newClient()

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
