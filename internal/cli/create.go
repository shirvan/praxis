package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newCreateCmd builds the `praxis create` verb command.
// Subcommands: workspace, template, sink.
func newCreateCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <resource>",
		Short: "Create a resource",
		Long: `Create a new resource in Praxis.

Supported resources:

    praxis create workspace <name> --account <acct> --region <region>
    praxis create template <file.cue> [--name <name>]
    praxis create sink --name <name> --type <type> --url <url>`,
	}

	cmd.AddCommand(
		newCreateWorkspaceCmd(flags),
		newCreateTemplateCmd(flags),
		newCreateSinkCmd(flags),
	)

	return cmd
}

// newCreateWorkspaceCmd builds `praxis create workspace <name>`.
func newCreateWorkspaceCmd(flags *rootFlags) *cobra.Command {
	var (
		account    string
		region     string
		vars       []string
		selectFlag bool
	)

	cmd := &cobra.Command{
		Use:   "workspace <name>",
		Short: "Create or update a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return createWorkspace(flags, args[0], account, region, vars, selectFlag)
		},
	}

	cmd.Flags().StringVar(&account, "account", "", "AWS account alias (required)")
	cmd.Flags().StringVar(&region, "region", "", "Default AWS region (required)")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "Default variable in key=value format (repeatable)")
	cmd.Flags().BoolVar(&selectFlag, "select", false, "Set as the active workspace after creation")

	return cmd
}

// newCreateTemplateCmd builds `praxis create template <file.cue>`.
func newCreateTemplateCmd(flags *rootFlags) *cobra.Command {
	var (
		name        string
		description string
	)

	cmd := &cobra.Command{
		Use:   "template <file.cue>",
		Short: "Register or update a CUE template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return createTemplate(flags, args[0], name, description)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Template name (defaults to filename without extension)")
	cmd.Flags().StringVar(&description, "description", "", "Human-readable description")

	return cmd
}

// newCreateSinkCmd builds `praxis create sink`.
func newCreateSinkCmd(flags *rootFlags) *cobra.Command {
	var (
		name             string
		sinkType         string
		url              string
		typeFilters      string
		categoryFilters  string
		severityFilters  string
		workspaceFilters string
		deploymentFilter string
		headers          []string
		maxRetries       int
		backoffMs        int
		fromFile         string
		contentMode      string
	)

	cmd := &cobra.Command{
		Use:   "sink",
		Short: "Create or update a notification sink",
		RunE: func(cmd *cobra.Command, args []string) error {
			sink, err := buildNotificationSink(fromFile, name, sinkType, url, typeFilters, categoryFilters, severityFilters, workspaceFilters, deploymentFilter, headers, maxRetries, backoffMs, contentMode)
			if err != nil {
				return err
			}
			if err := flags.newClient().UpsertNotificationSink(context.Background(), sink); err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(sink)
			}
			flags.renderer().successLine(fmt.Sprintf("Notification sink %q saved.", sink.Name))
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Sink name")
	cmd.Flags().StringVar(&sinkType, "type", "", "Sink type: webhook, structured_log, cloudevents_http")
	cmd.Flags().StringVar(&url, "url", "", "Endpoint URL for webhook or cloudevents_http sinks")
	cmd.Flags().StringVar(&typeFilters, "filter-types", "", "Comma-separated event type prefixes")
	cmd.Flags().StringVar(&categoryFilters, "filter-categories", "", "Comma-separated event categories")
	cmd.Flags().StringVar(&severityFilters, "filter-severities", "", "Comma-separated severities")
	cmd.Flags().StringVar(&workspaceFilters, "filter-workspaces", "", "Comma-separated workspace names")
	cmd.Flags().StringVar(&deploymentFilter, "filter-deployments", "", "Comma-separated deployment globs")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "HTTP header in key=value form")
	cmd.Flags().IntVar(&maxRetries, "max-retries", 3, "Maximum delivery retry attempts")
	cmd.Flags().IntVar(&backoffMs, "backoff-ms", 1000, "Initial delivery retry backoff in milliseconds")
	cmd.Flags().StringVarP(&fromFile, "file", "f", "", "Read sink config from JSON file or - for stdin")
	cmd.Flags().StringVar(&contentMode, "content-mode", "structured", "CloudEvents HTTP content mode")
	return cmd
}

// createTemplate is the shared logic for registering a template.
func createTemplate(flags *rootFlags, filePath, name, description string) error {
	renderer := flags.renderer()

	content, err := os.ReadFile(filePath) //nolint:gosec // G304: path is user-supplied CLI argument
	if err != nil {
		return fmt.Errorf("read template %q: %w", filePath, err)
	}

	templateName := name
	if templateName == "" {
		base := filepath.Base(filePath)
		templateName = strings.TrimSuffix(base, filepath.Ext(base))
	}

	client := flags.newClient()
	ctx := context.Background()

	resp, err := client.RegisterTemplate(ctx, types.RegisterTemplateRequest{
		Name:        templateName,
		Source:      string(content),
		Description: description,
	})
	if err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(resp)
	}

	renderer.successLine(fmt.Sprintf("Registered template %q (digest: %s)", resp.Name, resp.Digest[:12]))
	return nil
}
