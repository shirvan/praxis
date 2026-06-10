// service.go implements the WorkspaceService Restate Virtual Object.
//
// The Virtual Object is keyed by workspace name. Each workspace stores its
// configuration (account, region, default variables, event settings) in
// Restate's durable state under the "config" key.
//
// Handler overview:
//   - Configure (exclusive) — create or update a workspace
//   - Get (shared)          — read workspace info
//   - SetEventRetention     — validate & store a retention policy, schedule sweeps
//   - GetEventRetention     — read policy or return defaults
//   - Delete (exclusive)    — clear state and deregister from index
package workspace

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/cuevalidate"
	"github.com/shirvan/praxis/internal/core/orchestrator"
)

// WorkspaceServiceName is the Restate service name for workspace registration.
// Must match the binding in cmd/praxis-core/main.go.
const (
	WorkspaceServiceName = "WorkspaceService"
)

// WorkspaceService is a Restate Virtual Object keyed by workspace name.
type WorkspaceService struct {
	schemaDir string
}

// NewWorkspaceService creates a WorkspaceService. schemaDir is the path
// to the CUE schema directory used for validating retention policy input.
func NewWorkspaceService(schemaDir string) *WorkspaceService {
	return &WorkspaceService{schemaDir: schemaDir}
}

// ServiceName returns the Restate service registration name.
func (WorkspaceService) ServiceName() string { return WorkspaceServiceName }

// Configure creates or updates a workspace.
// Validates the config, stores it in state, and registers the name in the index.
func (WorkspaceService) Configure(ctx restate.ObjectContext, cfg WorkspaceConfig) error {
	key := restate.Key(ctx)
	existing, err := restate.Get[*WorkspaceConfig](ctx, "config")
	if err != nil {
		return err
	}

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

	// Verify the account alias is actually registered. GetStatus succeeds for
	// any key (returning a zero status for unconfigured aliases), so inspect
	// the response rather than the error; transport errors propagate as-is so
	// Restate can retry instead of misreporting them as an unknown alias.
	status, err := restate.Object[authservice.CredentialStatus](ctx, authservice.ServiceName, cfg.Account, "GetStatus").Request(restate.Void{})
	if err != nil {
		if restate.IsTerminalError(err) {
			return err
		}
		return fmt.Errorf("workspace %q: verify account alias %q: %w", cfg.Name, cfg.Account, err)
	}
	if status.CredentialSource == "" {
		return restate.TerminalError(
			fmt.Errorf("workspace %q: unknown account alias %q — register it with Auth.Configure first", cfg.Name, cfg.Account), 400,
		)
	}

	if existing != nil && cfg.Events == nil {
		cfg.Events = existing.Events
	}

	// Store the config.
	restate.Set(ctx, "config", cfg)

	// Register in the global index.
	restate.ObjectSend(ctx, WorkspaceIndexServiceName, WorkspaceIndexGlobalKey, "Register").Send(cfg.Name)
	if cfg.Events != nil && cfg.Events.Retention != nil {
		restate.ObjectSend(ctx, orchestrator.EventBusServiceName, orchestrator.EventBusGlobalKey, "ScheduleRetentionSweep").
			Send(orchestrator.RetentionSweepRequest{Workspace: cfg.Name})
	}

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
		Events:    cfg.Events,
	}, nil
}

// SetEventRetention stores the workspace retention policy after validating it
// against the CUE schema.
func (w *WorkspaceService) SetEventRetention(ctx restate.ObjectContext, policy EventRetentionPolicy) error {
	cfg, err := restate.Get[*WorkspaceConfig](ctx, "config")
	if err != nil {
		return err
	}
	if cfg == nil {
		return restate.TerminalError(fmt.Errorf("workspace %q is not configured", restate.Key(ctx)), 404)
	}

	var normalized EventRetentionPolicy
	if err := cuevalidate.DecodeFile(w.schemaDir, "notifications/retention.cue", "#RetentionPolicy", policy, &normalized); err != nil {
		return restate.TerminalError(fmt.Errorf("invalid retention policy: %w", err), 400)
	}

	if cfg.Events == nil {
		cfg.Events = &EventSettings{}
	}
	cfg.Events.Retention = &normalized
	restate.Set(ctx, "config", cfg)
	restate.ObjectSend(ctx, orchestrator.EventBusServiceName, orchestrator.EventBusGlobalKey, "ScheduleRetentionSweep").
		Send(orchestrator.RetentionSweepRequest{Workspace: restate.Key(ctx)})
	return nil
}

// GetEventRetention returns the workspace-specific policy or the system defaults
// when no override has been configured yet.
func (*WorkspaceService) GetEventRetention(ctx restate.ObjectSharedContext, _ restate.Void) (EventRetentionPolicy, error) {
	cfg, err := restate.Get[*WorkspaceConfig](ctx, "config")
	if err != nil {
		return EventRetentionPolicy{}, err
	}
	if cfg == nil {
		return EventRetentionPolicy{}, restate.TerminalError(fmt.Errorf("workspace %q is not configured", restate.Key(ctx)), 404)
	}
	if cfg.Events == nil || cfg.Events.Retention == nil {
		return DefaultEventRetentionPolicy(), nil
	}
	return *cfg.Events.Retention, nil
}

// Delete removes a workspace and deregisters it from the index.
func (WorkspaceService) Delete(ctx restate.ObjectContext, _ restate.Void) error {
	restate.ClearAll(ctx)

	// Deregister from the global index.
	restate.ObjectSend(ctx, WorkspaceIndexServiceName, WorkspaceIndexGlobalKey, "Deregister").Send(restate.Key(ctx))

	return nil
}
