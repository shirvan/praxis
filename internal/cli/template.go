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

// newTemplateCmd builds the `praxis template` command group.
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

			fmt.Printf("Registered template %q (digest: %s)\n", resp.Name, resp.Digest[:12])
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Template name (defaults to filename without extension)")
	cmd.Flags().StringVar(&description, "description", "", "Human-readable description")

	return cmd
}

func newTemplateListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
				fmt.Println("No templates registered.")
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
			printTable(headers, rows)
			return nil
		},
	}
}

func newTemplateDescribeCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "describe <name>",
		Short: "Show template details and variable schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			ctx := context.Background()

			record, err := client.GetTemplate(ctx, args[0])
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(record)
			}

			fmt.Printf("Template:    %s\n", record.Metadata.Name)
			if record.Metadata.Description != "" {
				fmt.Printf("Description: %s\n", record.Metadata.Description)
			}
			fmt.Printf("Digest:      %s\n", record.Digest[:12])
			fmt.Printf("Created:     %s\n", formatTime(record.Metadata.CreatedAt))
			fmt.Printf("Updated:     %s\n", formatTime(record.Metadata.UpdatedAt))

			if len(record.VariableSchema) > 0 {
				fmt.Println()
				fmt.Println("Variables:")
				headers := []string{"NAME", "TYPE", "REQUIRED", "DEFAULT", "CONSTRAINT"}
				rows := make([][]string, 0, len(record.VariableSchema))
				for name, field := range record.VariableSchema {
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
				printTable(headers, rows)
			}

			return nil
		},
	}
}

func newTemplateDeleteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a registered template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			ctx := context.Background()

			err := client.DeleteTemplate(ctx, args[0])
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(map[string]string{"deleted": args[0]})
			}

			fmt.Printf("Deleted template %q\n", args[0])
			return nil
		},
	}
}
