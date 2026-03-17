package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/registry"
	"github.com/praxiscloud/praxis/internal/core/template"
	"github.com/praxiscloud/praxis/pkg/types"
)

// Deploy is the user-facing deployment entry point. It requires a pre-registered
// template and validates variables against the template's schema before running
// the full pipeline.
func (s *PraxisCommandService) Deploy(ctx restate.Context, req DeployRequest) (DeployResponse, error) {
	templateName := strings.TrimSpace(req.Template)
	if templateName == "" {
		return DeployResponse{}, restate.TerminalError(
			fmt.Errorf("template name is required"), 400)
	}

	// Fetch variable schema from registry (shared handler — no lock).
	schema, err := restate.Object[types.VariableSchema](
		ctx, registry.TemplateRegistryServiceName, templateName, "GetVariableSchema",
	).Request(restate.Void{})
	if err != nil {
		return DeployResponse{}, err
	}

	// Fast preflight validation — reject bad variables before the CUE pipeline.
	if err := template.ValidateVariables(schema, req.Variables); err != nil {
		return DeployResponse{}, restate.TerminalError(err, 400)
	}

	account, err := s.resolveRequestAccount(req.Account, req.Variables)
	if err != nil {
		return DeployResponse{}, restate.TerminalError(err, 400)
	}

	// Compile the template via the existing pipeline using a TemplateRef.
	compiled, err := s.compileTemplate(ctx, "", &types.TemplateRef{Name: templateName}, req.Variables, account.Name)
	if err != nil {
		return DeployResponse{}, err
	}

	// Derive deployment key if not provided.
	deploymentKey := strings.TrimSpace(req.DeploymentKey)
	if deploymentKey == "" {
		deploymentKey, err = deriveDeploymentKey(compiled.Specs)
		if err != nil {
			return DeployResponse{}, restate.TerminalError(err, 400)
		}
	}

	key, status, err := s.submitDeployment(ctx, deploymentKey, account.Name, req.Variables, compiled)
	if err != nil {
		return DeployResponse{}, err
	}

	return DeployResponse{
		DeploymentKey: key,
		Status:        status,
	}, nil
}
