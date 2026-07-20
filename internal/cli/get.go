package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newGetCmd builds the `praxis get` subcommand.
//
// Get retrieves the current state of a deployment or individual resource. The
// argument format is <Kind>/<Key>:
//
//   - Deployment/<key>  — Shows deployment status with per-resource breakdown
//   - S3Bucket/<key>    — Shows a single S3 bucket resource status
//   - SecurityGroup/<key> — Shows a single security group status
//
// Usage:
//
//	praxis get Deployment/my-webapp
//	praxis get Deployment/my-webapp -o json
//	praxis get S3Bucket/my-bucket
func newGetCmd(flags *rootFlags) *cobra.Command {
	var (
		showDeps    bool
		showInputs  bool
		showOutputs bool
		showErrors  bool
		showAll     bool
	)

	cmd := &cobra.Command{
		Use:   "get <Kind>/<Key>",
		Short: "Show deployment or resource details",
		Long: `Get retrieves the current state of a deployment or an individual resource.

For deployments, it shows the overall status plus a per-resource breakdown
with any outputs collected during provisioning:

    praxis get Deployment/my-webapp

By default only metadata and the resource table are shown. Use flags to
include additional sections:

    praxis get Deployment/my-webapp --deps --outputs
    praxis get Deployment/my-webapp --all

For individual resources, it shows the driver-level status and outputs:

    praxis get S3Bucket/my-bucket
    praxis get SecurityGroup/vpc-123~web-sg

Meta-resources can also be retrieved:

    praxis get workspace [name]
    praxis get template/<name>
    praxis get sink/<name>
    praxis get config <path>
    praxis get notifications
    praxis get schema <Kind>          Print the CUE schema for a resource kind (offline)

The argument must be in Kind/Key format. Use 'praxis list deployments' to
discover available deployment keys.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}

			client := flags.newClient()
			ctx := context.Background()

			// Route meta-resource kinds to their handlers.
			switch kind {
			case "template":
				return getTemplateDetail(flags, key)
			case "sink":
				return getSinkDetail(flags, key)
			case "Deployment":
				sections := deploymentSections{
					Deps:    showDeps || showAll,
					Inputs:  showInputs || showAll,
					Outputs: showOutputs || showAll,
					Errors:  showErrors || showAll,
				}
				return getDeployment(ctx, client, key, flags.outputFormat(), sections)
			default:
				key = flags.resolveResourceKey(kind, key)
				return getResource(ctx, client, kind, key, flags.outputFormat())
			}
		},
	}

	cmd.Flags().BoolVar(&showDeps, "deps", false, "Show resource dependency graph")
	cmd.Flags().BoolVar(&showInputs, "inputs", false, "Show resource input specs")
	cmd.Flags().BoolVar(&showOutputs, "outputs", false, "Show resource outputs")
	cmd.Flags().BoolVar(&showErrors, "errors", false, "Show full resource error details")
	cmd.Flags().BoolVar(&showAll, "all", false, "Show all sections (deps, inputs, outputs, errors)")

	// Add meta-resource subcommands for types that don't use Kind/Key syntax.
	cmd.AddCommand(
		newGetWorkspaceCmd(flags),
		newGetConfigCmd(flags),
		newGetNotificationsCmd(flags),
		newGetSchemaCmd(flags),
	)

	return cmd
}

// newGetWorkspaceCmd builds `praxis get workspace [name]`.
func newGetWorkspaceCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "workspace [name]",
		Short: "Show workspace details",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return getWorkspaceDetail(flags, args)
		},
	}
}

// newGetConfigCmd builds `praxis get config <path>`.
func newGetConfigCmd(flags *rootFlags) *cobra.Command {
	var workspaceName string
	cmd := &cobra.Command{
		Use:   "config <path>",
		Short: "Read workspace-scoped configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return getConfigValue(flags, args[0], workspaceName)
		},
	}
	cmd.Flags().StringVarP(&workspaceName, "workspace", "w", "", "Workspace name (env: PRAXIS_WORKSPACE, defaults to active workspace)")
	return cmd
}

// newGetNotificationsCmd builds `praxis get notifications`.
func newGetNotificationsCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "notifications",
		Short: "Show aggregate notification sink health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getNotificationHealth(flags)
		},
	}
}

// getWorkspaceDetail shows details for a specific or active workspace.
func getWorkspaceDetail(flags *rootFlags, args []string) error {
	renderer := flags.renderer()
	name := ""
	if len(args) > 0 {
		name = args[0]
	} else {
		cliCfg := LoadCLIConfig()
		name = cliCfg.ActiveWorkspace
	}
	if name == "" {
		return fmt.Errorf("no workspace specified and no active workspace set")
	}

	client := flags.newClient()
	ctx := context.Background()

	info, err := client.GetWorkspace(ctx, name)
	if err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(info)
	}

	renderer.writeLabelValue("Name", 10, info.Name)
	renderer.writeLabelValue("Account", 10, info.Account)
	renderer.writeLabelValue("Region", 10, info.Region)
	if len(info.Variables) > 0 {
		_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Variables:"))
		keys := make([]string, 0, len(info.Variables))
		for k := range info.Variables {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := info.Variables[k]
			_, _ = fmt.Fprintf(renderer.out, "  %s = %s\n", k, v)
		}
	}
	if info.Events != nil && info.Events.Retention != nil {
		_, _ = fmt.Fprintln(renderer.out)
		_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Event Retention:"))
		printEventRetentionPolicy(renderer, info.Events.Retention)
	}
	return nil
}

// getTemplateDetail shows details for a named template.
func getTemplateDetail(flags *rootFlags, name string) error {
	renderer := flags.renderer()
	client := flags.newClient()
	ctx := context.Background()

	record, err := client.GetTemplate(ctx, name)
	if err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(record)
	}

	renderer.writeLabelValue("Template", 12, record.Metadata.Name)
	if record.Metadata.Description != "" {
		renderer.writeLabelValue("Description", 12, record.Metadata.Description)
	}
	renderer.writeLabelValue("Digest", 12, shortDigest(record.Digest))
	renderer.writeLabelValue("Created", 12, formatTime(record.Metadata.CreatedAt))
	renderer.writeLabelValue("Updated", 12, formatTime(record.Metadata.UpdatedAt))

	if len(record.VariableSchema) > 0 {
		_, _ = fmt.Fprintln(renderer.out)
		_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Variables:"))
		headers := []string{"NAME", "TYPE", "REQUIRED", "DEFAULT", "CONSTRAINT"}
		rows := make([][]string, 0, len(record.VariableSchema))
		names := make([]string, 0, len(record.VariableSchema))
		for name := range record.VariableSchema {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			field := record.VariableSchema[name]
			def := "-"
			if field.Default != nil {
				def = fmt.Sprintf("%v", field.Default)
			}
			constraint := "-"
			if len(field.Enum) > 0 {
				constraint = strings.Join(field.Enum, " | ")
			}
			required := "no"
			if field.Required {
				required = "yes"
			}
			rows = append(rows, []string{name, field.Type, required, def, constraint})
		}
		printTable(renderer, headers, rows)
	}

	return nil
}

// getSinkDetail shows details for a named notification sink.
func getSinkDetail(flags *rootFlags, name string) error {
	sink, err := flags.newClient().GetNotificationSink(context.Background(), name)
	if err != nil {
		return err
	}
	if sink == nil {
		return fmt.Errorf("notification sink %q not found", name)
	}
	return printJSON(redactSinkHeaders(*sink))
}

// getConfigValue reads a workspace-scoped configuration value.
func getConfigValue(flags *rootFlags, path, workspaceName string) error {
	resolvedWorkspace, err := resolveWorkspaceName(workspaceName)
	if err != nil {
		return err
	}
	switch path {
	case "events.retention":
		policy, err := flags.newClient().GetWorkspaceEventRetention(context.Background(), resolvedWorkspace)
		if err != nil {
			return err
		}
		if flags.outputFormat() == OutputJSON {
			return printJSON(policy)
		}
		printEventRetentionPolicy(flags.renderer(), policy)
		return nil
	default:
		return fmt.Errorf("unsupported config path %q", path)
	}
}

// getNotificationHealth shows aggregate notification sink health.
func getNotificationHealth(flags *rootFlags) error {
	health, err := flags.newClient().GetNotificationSinkHealth(context.Background())
	if err != nil {
		return err
	}
	if flags.outputFormat() == OutputJSON {
		return printJSON(health)
	}
	printTable(flags.renderer(), []string{"TOTAL", "HEALTHY", "DEGRADED", "OPEN", "LAST DELIVERY"}, [][]string{{
		fmt.Sprintf("%d", health.Total),
		fmt.Sprintf("%d", health.Healthy),
		fmt.Sprintf("%d", health.Degraded),
		fmt.Sprintf("%d", health.Open),
		health.LastDeliveryAt,
	}})
	return nil
}

// --------------------------------------------------------------------------
// Kind/Key parsing
// --------------------------------------------------------------------------

// parseKindKey splits a "Kind/Key" argument into its two components. The key
// may itself contain "/" characters (though canonical keys use "~"), so only
// the first "/" is the separator.
func parseKindKey(arg string) (kind, key string, err error) {
	parts := strings.SplitN(arg, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid argument %q: expected Kind/Key (e.g., Deployment/my-webapp)", arg)
	}
	return normalizeKind(strings.TrimSpace(parts[0])), strings.TrimSpace(parts[1]), nil
}

// --------------------------------------------------------------------------
// Deployment get
// --------------------------------------------------------------------------

// deploymentSections controls which optional sections are rendered for
// `praxis get Deployment/<key>`. By default only the metadata header and
// resource table are shown.
type deploymentSections struct {
	Deps    bool
	Inputs  bool
	Outputs bool
	Errors  bool
}

// getDeployment retrieves and displays a full deployment detail record.
// When Inputs is requested, it also fetches the desired input spec from
// each driver for troubleshooting visibility.
func getDeployment(ctx context.Context, client *Client, key string, format OutputFormat, sections deploymentSections) error {
	renderer := defaultRenderer()
	detail, err := client.GetDeployment(ctx, key)
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("deployment %q not found", key)
	}

	// Fetch inputs for each resource from drivers only when needed.
	var resourceInputs map[string]map[string]any
	if sections.Inputs || format == OutputJSON {
		resourceInputs = make(map[string]map[string]any, len(detail.Resources))
		for i := range detail.Resources {
			r := &detail.Resources[i]
			inputs, inputErr := client.GetResourceInputs(ctx, r.Kind, r.Key)
			if inputErr == nil && inputs != nil {
				resourceInputs[r.Name] = inputs
			}
		}
	}

	if format == OutputJSON {
		result := map[string]any{
			"deployment": detail,
			"inputs":     resourceInputs,
		}
		return printJSON(result)
	}

	printDeploymentDetail(renderer, detail, sections, resourceInputs)
	return nil
}

// --------------------------------------------------------------------------
// Resource get
// --------------------------------------------------------------------------

// getResource retrieves a single resource's status, outputs, and desired input
// spec from its driver.
func getResource(ctx context.Context, client *Client, kind, key string, format OutputFormat) error {
	renderer := defaultRenderer()
	status, err := client.GetResourceStatus(ctx, kind, key)
	if err != nil {
		return err
	}

	outputs, err := client.GetResourceOutputs(ctx, kind, key)
	if err != nil {
		// Non-fatal: outputs may not be available for all resources.
		outputs = nil
	}

	inputs, err := client.GetResourceInputs(ctx, kind, key)
	if err != nil {
		// Non-fatal: inputs may not be available for new or deleted resources.
		inputs = nil
	}

	result := resourceResult(kind, key, status, inputs, outputs)

	if format == OutputJSON {
		return printJSON(result)
	}

	// Build a combined view for display.
	renderer.writeLabelValue("Resource", 11, kind+"/"+key)
	renderer.writeLabelStyledValue("Status", 11, renderer.renderStatus(string(status.Status)))
	renderer.writeLabelValue("Mode", 11, string(status.Mode))
	renderer.writeLabelValue("Reconcile", 11, string(status.Reconcile))
	renderer.writeLabelValue("Generation", 11, fmt.Sprintf("%d", status.Generation))
	if len(status.IgnoreChanges) > 0 {
		renderer.writeLabelValue("Ignored", 11, strings.Join(status.IgnoreChanges, ", "))
	}
	if status.Error != "" {
		renderer.writeLabelValue("Error", 11, status.Error)
	}
	if len(status.Conditions) > 0 {
		_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderSection("Conditions:"))
		for _, condition := range orderedConditions(status.Conditions) {
			line := fmt.Sprintf("  %-13s %s", condition.Type+":", condition.Status)
			if condition.Reason != "" {
				line += fmt.Sprintf(" (%s)", condition.Reason)
			}
			if condition.Message != "" {
				line += fmt.Sprintf(" - %s", condition.Message)
			}
			_, _ = fmt.Fprintln(renderer.out, line)
		}
	}
	if len(inputs) > 0 {
		_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderSection("Inputs:"))
		keys := make([]string, 0, len(inputs))
		for k := range inputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, inputKey := range keys {
			_, _ = fmt.Fprintf(renderer.out, "  %s = %v\n", inputKey, inputs[inputKey])
		}
	}
	if len(outputs) > 0 {
		_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderSection("Outputs:"))
		keys := make([]string, 0, len(outputs))
		for k := range outputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, outputKey := range keys {
			_, _ = fmt.Fprintf(renderer.out, "  %s = %v\n", outputKey, outputs[outputKey])
		}
	}
	return nil
}

func resourceResult(kind, key string, status *types.StatusResponse, inputs, outputs map[string]any) map[string]any {
	result := map[string]any{
		"kind":       kind,
		"key":        key,
		"status":     status.Status,
		"mode":       status.Mode,
		"reconcile":  status.Reconcile,
		"generation": status.Generation,
	}
	if len(status.IgnoreChanges) > 0 {
		result["ignoreChanges"] = status.IgnoreChanges
	}
	if len(status.Conditions) > 0 {
		result["conditions"] = status.Conditions
	}
	if status.Error != "" {
		result["error"] = status.Error
	}
	if inputs != nil {
		result["inputs"] = inputs
	}
	if outputs != nil {
		result["outputs"] = outputs
	}
	return result
}
