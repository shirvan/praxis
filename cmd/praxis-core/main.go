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

// This binary exposes the full Praxis Core surface under one Restate-discoverable
// endpoint: orchestration workflows, durable deployment state/read-model objects,
// and the command service.
func main() {
	cfg := config.Load()

	// Load bootstrap accounts config from environment variables.
	bootstrap := authservice.LoadBootstrapFromEnv()

	// Create auth client for Core components — resolves credentials via Restate RPC.
	authClient := authservice.NewAuthClient()

	providers := provider.NewRegistry(authClient)

	srv := server.NewRestate().
		Bind(restate.Reflect(authservice.NewAuthService(bootstrap))).
		Bind(restate.Reflect(workspace.WorkspaceService{})).
		Bind(restate.Reflect(workspace.WorkspaceIndex{})).
		Bind(restate.Reflect(command.NewPraxisCommandService(cfg, authClient, providers))).
		Bind(restate.Reflect(orchestrator.NewDeploymentWorkflow(providers))).
		Bind(restate.Reflect(orchestrator.NewDeploymentDeleteWorkflow(providers))).
		Bind(restate.Reflect(orchestrator.DeploymentStateObj{})).
		Bind(restate.Reflect(orchestrator.DeploymentIndex{})).
		Bind(restate.Reflect(orchestrator.DeploymentEvents{})).
		Bind(restate.Reflect(registry.TemplateRegistry{})).
		Bind(restate.Reflect(registry.TemplateIndex{})).
		Bind(restate.Reflect(registry.PolicyRegistry{}))

	if strings.TrimSpace(cfg.PolicyDir) != "" {
		go seedPoliciesFromDir(cfg)
	}

	slog.Info("starting Praxis core runtime", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("Praxis core exited", "err", err.Error())
		os.Exit(1)
	}
}

func seedPoliciesFromDir(cfg config.Config) {
	entries, err := os.ReadDir(cfg.PolicyDir)
	if err != nil {
		slog.Error("failed to read policy seed directory", "dir", cfg.PolicyDir, "err", err.Error())
		return
	}

	client := ingress.NewClient(cfg.RestateEndpoint)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".cue" {
			continue
		}

		path := filepath.Join(cfg.PolicyDir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			slog.Error("failed to read seeded policy", "path", path, "err", err.Error())
			continue
		}

		request := types.AddPolicyRequest{
			Name:   strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Scope:  types.PolicyScopeGlobal,
			Source: string(content),
		}

		var lastErr error
		for attempt := 0; attempt < 20; attempt++ {
			_, err = ingress.Service[types.AddPolicyRequest, restate.Void](client, "PraxisCommandService", "AddPolicy").Request(context.Background(), request)
			if err == nil {
				lastErr = nil
				break
			}
			lastErr = err
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
