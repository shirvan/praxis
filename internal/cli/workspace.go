package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/internal/core/workspace"
)

// newWorkspaceCmd builds the `praxis workspace` command group.
func newWorkspaceCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage workspaces",
		Long: `A workspace groups related deployments under a single account and region
with shared default variables. The active workspace is injected into every
apply, plan, deploy and import command automatically.`,
	}

	cmd.AddCommand(
		newWorkspaceCreateCmd(flags),
		newWorkspaceListCmd(flags),
		newWorkspaceSelectCmd(flags),
		newWorkspaceShowCmd(flags),
		newWorkspaceDeleteCmd(flags),
	)

	return cmd
}

// newWorkspaceCreateCmd builds `praxis workspace create <name>`.
func newWorkspaceCreateCmd(flags *rootFlags) *cobra.Command {
	var (
		account    string
		region     string
		vars       []string
		selectFlag bool
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create or update a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cliCfg := LoadCLIConfig()

			if err := workspace.ValidateName(name); err != nil {
				return err
			}
			if strings.TrimSpace(account) == "" {
				return fmt.Errorf("--account is required")
			}
			if strings.TrimSpace(region) == "" {
				return fmt.Errorf("--region is required")
			}

			variables, err := parseStringVariables(vars)
			if err != nil {
				return err
			}

			client := flags.newClient()
			ctx := context.Background()

			cfg := workspace.WorkspaceConfig{
				Name:      name,
				Account:   account,
				Region:    region,
				Variables: variables,
			}
			if err := client.ConfigureWorkspace(ctx, cfg); err != nil {
				return err
			}

			fmt.Printf("Workspace %q created.\n", name)

			names, err := client.ListWorkspaces(ctx)
			if err != nil {
				return err
			}

			// Auto-select if --select is set or this is the first workspace.
			if selectFlag || (cliCfg.ActiveWorkspace == "" && len(names) == 1) {
				cliCfg.ActiveWorkspace = name
				if err := SaveCLIConfig(cliCfg); err != nil {
					return fmt.Errorf("save config: %w", err)
				}
				fmt.Printf("Switched to workspace %q.\n", name)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&account, "account", "", "AWS account alias (required)")
	cmd.Flags().StringVar(&region, "region", "", "Default AWS region (required)")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "Default variable in key=value format (repeatable)")
	cmd.Flags().BoolVar(&selectFlag, "select", false, "Set as the active workspace after creation")

	return cmd
}

// newWorkspaceListCmd builds `praxis workspace list`.
func newWorkspaceListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
				fmt.Println("No workspaces configured.")
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

			fmt.Printf("%-20s  %-20s  %-15s  %s\n", "NAME", "ACCOUNT", "REGION", "ACTIVE")
			for _, info := range infos {
				marker := ""
				if info.Name == cliCfg.ActiveWorkspace {
					marker = "*"
				}
				fmt.Printf("%-20s  %-20s  %-15s  %s\n", info.Name, info.Account, info.Region, marker)
			}
			return nil
		},
	}
}

// newWorkspaceSelectCmd builds `praxis workspace select <name>`.
func newWorkspaceSelectCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "select <name>",
		Short: "Set the active workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			client := flags.newClient()
			ctx := context.Background()

			// Validate the workspace exists.
			if _, err := client.GetWorkspace(ctx, name); err != nil {
				return fmt.Errorf("workspace %q: %w", name, err)
			}

			cliCfg := LoadCLIConfig()
			cliCfg.ActiveWorkspace = name
			if err := SaveCLIConfig(cliCfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			fmt.Printf("Switched to workspace %q.\n", name)
			return nil
		},
	}
}

// newWorkspaceShowCmd builds `praxis workspace show [name]`.
func newWorkspaceShowCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show workspace details",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			fmt.Printf("Name:      %s\n", info.Name)
			fmt.Printf("Account:   %s\n", info.Account)
			fmt.Printf("Region:    %s\n", info.Region)
			if len(info.Variables) > 0 {
				fmt.Println("Variables:")
				keys := make([]string, 0, len(info.Variables))
				for k := range info.Variables {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					v := info.Variables[k]
					fmt.Printf("  %s = %s\n", k, v)
				}
			}
			return nil
		},
	}
}

// newWorkspaceDeleteCmd builds `praxis workspace delete <name>`.
func newWorkspaceDeleteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			client := flags.newClient()
			ctx := context.Background()

			if err := client.DeleteWorkspace(ctx, name); err != nil {
				return err
			}

			// Clear active workspace if it was the deleted one.
			cliCfg := LoadCLIConfig()
			if cliCfg.ActiveWorkspace == name {
				cliCfg.ActiveWorkspace = ""
				if err := SaveCLIConfig(cliCfg); err != nil {
					return fmt.Errorf("save config: %w", err)
				}
			}

			fmt.Printf("Workspace %q deleted.\n", name)
			return nil
		},
	}
}

// parseStringVariables converts a slice of "key=value" strings into a
// map[string]string. Unlike parseVariables (which returns map[string]any for
// template variables), workspace variables are always strings.
func parseStringVariables(vars []string) (map[string]string, error) {
	if len(vars) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(vars))
	for _, v := range vars {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("invalid variable %q: expected key=value", v)
		}
		result[strings.TrimSpace(parts[0])] = parts[1]
	}
	return result, nil
}
