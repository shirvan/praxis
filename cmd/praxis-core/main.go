// Package main is the entry point for the praxis-core service binary.
//
// praxis-core is the central control plane of Praxis. It hosts all Restate
// Virtual Object and Workflow handlers that make up the "brain" of the system:
//
//   - AuthService: durable multi-account AWS credential management
//   - WorkspaceService + WorkspaceIndex: workspace CRUD and listing
//   - PraxisCommandService: template/policy/resource commands (plan, apply, deploy, delete)
//   - DeploymentWorkflow / DeleteWorkflow / RollbackWorkflow: durable orchestration
//   - DeploymentStateObj + DeploymentIndex: deployment read-model and listing
//   - TemplateRegistry + TemplateIndex: template storage and search
//   - PolicyRegistry: policy storage and evaluation
//
// All handlers are registered with a single Restate server. The Restate runtime
// handles discovery, invocation routing, and durable execution guarantees.
//
// On startup, if PRAXIS_POLICY_DIR is set, the binary also seeds built-in
// policies from .cue files found in that directory (e.g., naming conventions,
// tag requirements).
package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/registry"
	"github.com/shirvan/praxis/internal/core/workspace"
	"github.com/shirvan/praxis/pkg/types"
)

// main wires together all Core services and starts the Restate HTTP endpoint.
// It also kicks off background policy seeding if a policy directory is configured.
func main() {
	// Load configuration from environment variables (listen address, schema dir, etc.).
	cfg := config.Load()

	// Load bootstrap accounts config from environment variables.
	// These are the initial AWS accounts available before any runtime configuration.
	bootstrap := authservice.LoadBootstrapFromEnv()

	// Create auth client for Core components — resolves credentials via Restate RPC
	// to the AuthService Virtual Object rather than loading AWS SDK config directly.
	authClient := authservice.NewAuthClient()

	// The provider registry maps resource kinds (e.g., "S3Bucket") to adapter
	// functions that know how to build driver call parameters from generic specs.
	providers := provider.NewRegistry(authClient)

	// Register every Core service with the Restate server.
	// restate.Reflect() uses Go struct method signatures to auto-generate
	// Restate handler definitions (Virtual Objects, Workflows, plain Services).
	srv := server.NewRestate().
		// AuthService: manages AWS account credentials as durable state.
		Bind(restate.Reflect(authservice.NewAuthService(bootstrap))).
		// WorkspaceService: CRUD for named workspaces (e.g., "staging", "prod").
		Bind(restate.Reflect(workspace.NewWorkspaceService(cfg.SchemaDir))).
		// WorkspaceIndex: cross-workspace listing using a durable index object.
		Bind(restate.Reflect(workspace.WorkspaceIndex{})).
		// PraxisCommandService: the main command handlers (plan, apply, deploy, etc.).
		Bind(restate.Reflect(command.NewPraxisCommandService(cfg, authClient, providers))).
		// DeploymentWorkflow: durable workflow that orchestrates create/update deploys.
		Bind(restate.Reflect(orchestrator.NewDeploymentWorkflow(providers))).
		// DeploymentDeleteWorkflow: durable workflow that orchestrates resource deletion.
		Bind(restate.Reflect(orchestrator.NewDeploymentDeleteWorkflow(providers))).
		// DeploymentRollbackWorkflow: durable workflow for deployment rollbacks.
		Bind(restate.Reflect(orchestrator.NewDeploymentRollbackWorkflow(providers))).
		// DeploymentStateObj: per-deployment read model (status, resources, errors).
		Bind(restate.Reflect(orchestrator.DeploymentStateObj{})).
		// DeploymentIndex: cross-deployment listing and search index.
		Bind(restate.Reflect(orchestrator.DeploymentIndex{})).
		// TemplateRegistry: stores and retrieves versioned templates.
		Bind(restate.Reflect(registry.TemplateRegistry{})).
		// TemplateIndex: searchable index over all registered templates.
		Bind(restate.Reflect(registry.TemplateIndex{})).
		// PolicyRegistry: stores CUE-based validation policies.
		Bind(restate.Reflect(registry.PolicyRegistry{}))

	// If a policy seed directory is configured, load .cue files in the background.
	// This runs in a goroutine because it needs the Restate server to be accepting
	// connections first (chicken-and-egg: we send ingress RPCs to register policies).
	if strings.TrimSpace(cfg.PolicyDir) != "" {
		go seedPoliciesFromDir(cfg)
	}

	slog.Info("starting Praxis core runtime", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("Praxis core exited", "err", err.Error())
		os.Exit(1)
	}
}

// seedPoliciesFromDir reads all .cue files from the configured policy directory
// and registers each one with the PolicyRegistry via the Restate ingress API.
//
// This function is designed to be called in a goroutine after the server starts
// because it sends HTTP requests to the Restate ingress endpoint, which must
// be accepting connections. It retries up to 20 times per policy with 250ms
// backoff to handle the startup race.
//
// Policies with a 409/already-exists response are silently skipped (idempotent).
func seedPoliciesFromDir(cfg config.Config) {
	// Read all entries in the policy directory.
	entries, err := os.ReadDir(cfg.PolicyDir)
	if err != nil {
		slog.Error("failed to read policy seed directory", "dir", cfg.PolicyDir, "err", err.Error())
		return
	}

	// Create a Restate ingress client pointing to the local Restate server.
	client := ingress.NewClient(cfg.RestateEndpoint)
	for _, entry := range entries {
		// Skip directories and non-CUE files.
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".cue" {
			continue
		}

		// Read the CUE policy source from disk.
		path := filepath.Join(cfg.PolicyDir, entry.Name())
		content, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed from configured policy directory
		if err != nil {
			slog.Error("failed to read seeded policy", "path", path, "err", err.Error())
			continue
		}

		// Build the AddPolicy request. The policy name is derived from the
		// filename without extension (e.g., "naming-convention.cue" → "naming-convention").
		// All seeded policies are global scope since they come from operator config.
		request := types.AddPolicyRequest{
			Name:   strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Scope:  types.PolicyScopeGlobal,
			Source: string(content),
		}

		// Retry loop: the Restate server may not be ready yet when this goroutine
		// first runs. We retry up to 20 times (total ~5s) with 250ms spacing.
		var lastErr error
		for range 20 {
			_, err = ingress.Service[types.AddPolicyRequest, restate.Void](client, "PraxisCommandService", "AddPolicy").Request(context.Background(), request)
			if err == nil {
				lastErr = nil
				break
			}
			lastErr = err
			// 409 or "already exists" means the policy was registered on a prior startup — skip.
			if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "already exists") {
				lastErr = nil
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if lastErr != nil {
			slog.Error("failed to seed policy", "path", path, "err", lastErr.Error())
		}
	}
}
