package workspace

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"
)

const (
	WorkspaceServiceName = "WorkspaceService"
)

// WorkspaceService is a Restate Virtual Object keyed by workspace name.
type WorkspaceService struct{}

func (WorkspaceService) ServiceName() string { return WorkspaceServiceName }

// Configure creates or updates a workspace.
// Validates the config, stores it in state, and registers the name in the index.
func (WorkspaceService) Configure(ctx restate.ObjectContext, cfg WorkspaceConfig) error {
	key := restate.Key(ctx)

	// Validate name matches the Virtual Object key.
	if cfg.Name != key {
		return restate.TerminalError(
			fmt.Errorf("workspace name %q does not match object key %q", cfg.Name, key), 400,
		)
	}

	if err := ValidateName(cfg.Name); err != nil {
		return restate.TerminalError(err, 400)
	}
	if strings.TrimSpace(cfg.Account) == "" {
		return restate.TerminalError(fmt.Errorf("workspace %q: account is required", cfg.Name), 400)
	}
	if strings.TrimSpace(cfg.Region) == "" {
		return restate.TerminalError(fmt.Errorf("workspace %q: region is required", cfg.Name), 400)
	}

	// Verify the account alias exists via Auth.GetStatus.
	_, err := restate.Object[any](ctx, "AuthService", cfg.Account, "GetStatus").Request(restate.Void{})
	if err != nil {
		return restate.TerminalError(
			fmt.Errorf("workspace %q: unknown account alias %q — register it with Auth.Configure first", cfg.Name, cfg.Account), 400,
		)
	}

	// Store the config.
	restate.Set(ctx, "config", cfg)

	// Register in the global index.
	restate.ObjectSend(ctx, WorkspaceIndexServiceName, WorkspaceIndexGlobalKey, "Register").Send(cfg.Name)

	return nil
}

// Get returns the workspace configuration.
// Returns TerminalError(404) if the workspace has not been configured.
func (WorkspaceService) Get(ctx restate.ObjectSharedContext, _ restate.Void) (WorkspaceInfo, error) {
	cfg, err := restate.Get[*WorkspaceConfig](ctx, "config")
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if cfg == nil {
		return WorkspaceInfo{}, restate.TerminalError(
			fmt.Errorf("workspace %q is not configured", restate.Key(ctx)), 404,
		)
	}

	return WorkspaceInfo{
		Name:      cfg.Name,
		Account:   cfg.Account,
		Region:    cfg.Region,
		Variables: cfg.Variables,
	}, nil
}

// Delete removes a workspace and deregisters it from the index.
func (WorkspaceService) Delete(ctx restate.ObjectContext, _ restate.Void) error {
	restate.ClearAll(ctx)

	// Deregister from the global index.
	restate.ObjectSend(ctx, WorkspaceIndexServiceName, WorkspaceIndexGlobalKey, "Deregister").Send(restate.Key(ctx))

	return nil
}
