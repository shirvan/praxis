package command

import (
	"strings"

	restate "github.com/restatedev/sdk-go"
)

// Apply evaluates the template, initializes durable deployment state, and then
// asynchronously starts the deployment workflow.
func (s *PraxisCommandService) Apply(ctx restate.Context, req ApplyRequest) (ApplyResponse, error) {
	account, err := s.resolveRequestAccount(req.Account, req.Variables)
	if err != nil {
		return ApplyResponse{}, restate.TerminalError(err, 400)
	}

	compiled, err := s.compileTemplate(ctx, req.Template, req.TemplateRef, req.Variables, account.Name)
	if err != nil {
		return ApplyResponse{}, err
	}

	deploymentKey := strings.TrimSpace(req.DeploymentKey)
	if deploymentKey == "" {
		deploymentKey, err = deriveDeploymentKey(compiled.Specs)
		if err != nil {
			return ApplyResponse{}, restate.TerminalError(err, 400)
		}
	}

	key, status, err := s.submitDeployment(ctx, deploymentKey, account.Name, req.Variables, compiled)
	if err != nil {
		return ApplyResponse{}, err
	}

	return ApplyResponse{
		DeploymentKey: key,
		Status:        status,
	}, nil
}
