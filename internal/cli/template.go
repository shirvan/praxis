// template.go implements the `praxis template` command group.
//
// Templates are CUE source files stored in the Praxis registry. Operators
// register templates once, and developers deploy them by name with variables,
// avoiding the need to distribute raw CUE files.
//
// The template lifecycle:
//  1. `praxis template register <file.cue>` — store a CUE template
//  2. `praxis template list`                — list registered templates
//  3. `praxis template describe <name>`     — show details and variable schema
//  4. `praxis deploy <name> --var ...`      — instantiate a registered template
//  5. `praxis template delete <name>`       — remove a template
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newTemplateCmd builds the `praxis template` parent command. It groups
// register, list, describe, and delete subcommands.
func newTemplateCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage CUE templates",
		Long:  `Register, list, describe, and delete CUE templates in the Praxis registry.`,
	}

	cmd.AddCommand(
		newTemplateRegisterCmd(flags),
		newTemplateListCmd(flags),
		newTemplateDescribeCmd(flags),
		newTemplateDeleteCmd(flags),
	)

	return cmd
}

// newTemplateRegisterCmd builds `praxis template register <file.cue>`.
// Reads a CUE file from disk and sends it to PraxisCommandService.RegisterTemplate
// via the Restate ingress endpoint. The template name defaults to the filename
// stem unless --name is provided.
func newTemplateRegisterCmd(flags *rootFlags) *cobra.Command {
	var (
		name        string
		description string
	)

	cmd := &cobra.Command{
		Use:   "register <file.cue>",
		Short: "Register or update a CUE template",
		Long: `Register a CUE template from a file. If a template with the same name
already exists, it is updated.

The template name defaults to the filename without extension:

    praxis template register stack1.cue                   # name: stack1
    praxis template register stack1.cue --name my-stack   # name: my-stack`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]
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
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Template name (defaults to filename without extension)")
	cmd.Flags().StringVar(&description, "description", "", "Human-readable description")

	return cmd
}

// newTemplateListCmd builds `praxis template list`. Queries
// PraxisCommandService.ListTemplates and displays name, description, and
// last-updated timestamp.
func newTemplateListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
		},
	}
}

// newTemplateDescribeCmd builds `praxis template describe <name>`. Fetches
// the full template record including metadata and the variable schema, then
// renders a detail view with a table of accepted variables.
func newTemplateDescribeCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "describe <name>",
		Short: "Show template details and variable schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			renderer := flags.renderer()
			client := flags.newClient()
			ctx := context.Background()

			record, err := client.GetTemplate(ctx, args[0])
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
			renderer.writeLabelValue("Digest", 12, record.Digest[:12])
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
		},
	}
}

// newTemplateDeleteCmd builds `praxis template delete <name>`. Removes a
// registered template from the Praxis registry. Does NOT affect existing
// deployments that were created from the template.
func newTemplateDeleteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a registered template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			renderer := flags.renderer()
			client := flags.newClient()
			ctx := context.Background()

			err := client.DeleteTemplate(ctx, args[0])
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(map[string]string{"deleted": args[0]})
			}

			renderer.successLine(fmt.Sprintf("Deleted template %q", args[0]))
			return nil
		},
	}
}
