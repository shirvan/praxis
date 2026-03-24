// Package command implements the Praxis command surface.
//
// The command service accepts user intent, performs synchronous preparation
// work such as template evaluation and validation, and then hands long-running
// lifecycle work to the orchestrator workflows and virtual objects.
package command

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/resolver"
	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/internal/core/workspace"
)

// PraxisCommandService is the high-level command entry point for Praxis Core.
//
// It intentionally remains thin and mostly stateless:
//   - template preparation happens synchronously inside the request
//   - durable deployment lifecycle state lives in DeploymentStateObj
//   - asynchronous apply/delete execution lives in workflows
//   - provider-specific type branching stays behind the adapter registry
type PraxisCommandService struct {
	cfg       config.Config
	auth      authservice.AuthClient
	engine    *template.Engine
	providers *provider.Registry
}

// NewPraxisCommandService constructs the command surface with the
// concrete dependencies it needs.
func NewPraxisCommandService(cfg config.Config, auth authservice.AuthClient, providers *provider.Registry) *PraxisCommandService {
	if providers == nil {
		providers = provider.NewRegistry(auth)
	}

	return &PraxisCommandService{
		cfg:       cfg,
		auth:      auth,
		engine:    template.NewEngine(cfg.SchemaDir),
		providers: providers,
	}
}

// ServiceName pins the stable Restate service name for the command surface.
func (*PraxisCommandService) ServiceName() string {
	return "PraxisCommandService"
}

func trimTemplate(templateBody string) string {
	return strings.TrimSpace(templateBody)
}

func marshalPrettyJSON(value any) (string, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal pretty JSON: %w", err)
	}
	return string(encoded), nil
}

func (s *PraxisCommandService) resolveRequestAccount(requestAccount string, variables map[string]any) (string, error) {
	accountName := strings.TrimSpace(requestAccount)
	if raw, ok := variables["account"]; ok {
		accountValue, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("variables.account must be a string")
		}
		accountName = strings.TrimSpace(accountValue)
		if accountName == "" {
			return "", fmt.Errorf("variables.account must not be empty")
		}
	}
	return accountName, nil
}

// resolveWorkspaceDefaults loads workspace defaults and merges them with
// explicit request values. Explicit values always take precedence.
func (s *PraxisCommandService) resolveWorkspaceDefaults(
	ctx restate.Context,
	requestAccount string,
	requestWorkspace string,
	variables map[string]any,
) (account string, mergedVars map[string]any, err error) {
	account, err = s.resolveRequestAccount(requestAccount, variables)
	if err != nil {
		return "", nil, err
	}
	mergedVars = variables

	// If a workspace is specified, load its defaults.
	if requestWorkspace != "" {
		wsInfo, wsErr := restate.Object[workspace.WorkspaceInfo](
			ctx, workspace.WorkspaceServiceName, requestWorkspace, "Get",
		).Request(restate.Void{})
		if wsErr != nil {
			return "", nil, fmt.Errorf("workspace %q: %w", requestWorkspace, wsErr)
		}

		// Workspace defaults apply only when the request doesn't override.
		if account == "" {
			account = wsInfo.Account
		}

		// Merge workspace variables (lower priority) with request variables (higher).
		if len(wsInfo.Variables) > 0 {
			merged := make(map[string]any, len(wsInfo.Variables)+len(variables))
			for k, v := range wsInfo.Variables {
				merged[k] = v
			}
			for k, v := range variables {
				merged[k] = v
			}
			mergedVars = merged
		}
	}

	// Existing fallback chain: env var → "default".
	if account == "" {
		account = os.Getenv("PRAXIS_ACCOUNT")
	}
	if account == "" {
		account = "default"
	}

	return account, mergedVars, nil
}

func (s *PraxisCommandService) newSSMResolver(ctx restate.Context, accountName string) (*resolver.RestateSSMResolver, error) {
	awsCfg, err := s.auth.GetCredentials(ctx, accountName)
	if err != nil {
		return nil, fmt.Errorf("resolve AWS config for account %q: %w", accountName, err)
	}
	return resolver.NewRestateSSMResolver(resolver.NewSSMResolver(ssm.NewFromConfig(awsCfg))), nil
}
