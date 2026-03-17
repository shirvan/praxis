// Package command implements the Praxis command surface.
//
// The command service accepts user intent, performs synchronous preparation
// work such as template evaluation and validation, and then hands long-running
// lifecycle work to the orchestrator workflows and virtual objects.
package command

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/core/config"
	"github.com/praxiscloud/praxis/internal/core/provider"
	"github.com/praxiscloud/praxis/internal/core/resolver"
	"github.com/praxiscloud/praxis/internal/core/template"
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
	auth      *auth.Registry
	engine    *template.Engine
	providers *provider.Registry
}

// NewPraxisCommandService constructs the command surface with the
// concrete dependencies it needs.
func NewPraxisCommandService(cfg config.Config, accounts *auth.Registry, providers *provider.Registry) *PraxisCommandService {
	if accounts == nil {
		accounts = cfg.Auth()
	}
	if providers == nil {
		providers = provider.NewRegistry()
	}

	return &PraxisCommandService{
		cfg:       cfg,
		auth:      accounts,
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

func (s *PraxisCommandService) resolveRequestAccount(requestAccount string, variables map[string]any) (auth.Account, error) {
	accountName := strings.TrimSpace(requestAccount)
	if raw, ok := variables["account"]; ok {
		accountValue, ok := raw.(string)
		if !ok {
			return auth.Account{}, fmt.Errorf("variables.account must be a string")
		}
		accountName = strings.TrimSpace(accountValue)
		if accountName == "" {
			return auth.Account{}, fmt.Errorf("variables.account must not be empty")
		}
	}
	return s.auth.Lookup(accountName)
}

func (s *PraxisCommandService) newSSMResolver(accountName string) (*resolver.RestateSSMResolver, error) {
	awsCfg, err := s.auth.Resolve(accountName)
	if err != nil {
		return nil, fmt.Errorf("resolve AWS config for account %q: %w", accountName, err)
	}
	return resolver.NewRestateSSMResolver(resolver.NewSSMResolver(ssm.NewFromConfig(awsCfg))), nil
}
