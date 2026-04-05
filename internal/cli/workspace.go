// workspace.go contains shared helpers for workspace operations.
//
// Workspace commands are now accessed through top-level verbs:
//   - `praxis create workspace <name>`   (create.go)
//   - `praxis set workspace <name>`      (set.go)
//   - `praxis get workspace [name]`      (get.go)
//   - `praxis list workspaces`           (list.go)
//   - `praxis delete workspace/<name>`   (delete.go)
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/shirvan/praxis/internal/core/workspace"
)

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

// createWorkspace is the shared logic for workspace creation, used by both
// the old `workspace create` and the new `create workspace` commands.
func createWorkspace(flags *rootFlags, name, account, region string, vars []string, selectFlag bool) error {
	renderer := flags.renderer()
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
		Events:    nil,
	}
	if err := client.ConfigureWorkspace(ctx, cfg); err != nil {
		return err
	}

	renderer.successLine(fmt.Sprintf("Workspace %q created.", name))

	names, err := client.ListWorkspaces(ctx)
	if err != nil {
		return err
	}

	if selectFlag || (cliCfg.ActiveWorkspace == "" && len(names) == 1) {
		cliCfg.ActiveWorkspace = name
		if err := SaveCLIConfig(cliCfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		renderer.successLine(fmt.Sprintf("Switched to workspace %q.", name))
	}

	return nil
}
