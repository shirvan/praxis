package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/registry"
	"github.com/shirvan/praxis/pkg/types"
)

func (s *PraxisCommandService) AddPolicy(ctx restate.Context, req types.AddPolicyRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return restate.TerminalError(fmt.Errorf("policy name is required"), 400)
	}
	if strings.TrimSpace(req.Source) == "" {
		return restate.TerminalError(fmt.Errorf("policy source is required"), 400)
	}
	scopeKey, err := policyScopeKeyFromRequest(req.Scope, req.TemplateName)
	if err != nil {
		return err
	}
	_, err = restate.WithRequestType[types.AddPolicyRequest, restate.Void](
		restate.Object[restate.Void](ctx, registry.PolicyRegistryServiceName, scopeKey, "AddPolicy"),
	).Request(req)
	return err
}

func (s *PraxisCommandService) RemovePolicy(ctx restate.Context, req types.RemovePolicyRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return restate.TerminalError(fmt.Errorf("policy name is required"), 400)
	}
	scopeKey, err := policyScopeKeyFromRequest(req.Scope, req.TemplateName)
	if err != nil {
		return err
	}
	_, err = restate.WithRequestType[string, restate.Void](
		restate.Object[restate.Void](ctx, registry.PolicyRegistryServiceName, scopeKey, "RemovePolicy"),
	).Request(req.Name)
	return err
}

func (s *PraxisCommandService) ListPolicies(ctx restate.Context, req types.ListPoliciesRequest) ([]types.PolicySummary, error) {
	scopeKey, err := policyScopeKeyFromRequest(req.Scope, req.TemplateName)
	if err != nil {
		return nil, err
	}
	record, err := restate.Object[types.PolicyRecord](ctx, registry.PolicyRegistryServiceName, scopeKey, "GetPolicies").Request(restate.Void{})
	if err != nil {
		return nil, err
	}
	return policySummaries(record), nil
}

func (s *PraxisCommandService) GetPolicy(ctx restate.Context, req types.GetPolicyRequest) (types.Policy, error) {
	if strings.TrimSpace(req.Name) == "" {
		return types.Policy{}, restate.TerminalError(fmt.Errorf("policy name is required"), 400)
	}
	scopeKey, err := policyScopeKeyFromRequest(req.Scope, req.TemplateName)
	if err != nil {
		return types.Policy{}, err
	}
	return restate.Object[types.Policy](ctx, registry.PolicyRegistryServiceName, scopeKey, "GetPolicy").Request(req.Name)
}

func policyScopeKeyFromRequest(scope types.PolicyScope, templateName string) (string, error) {
	switch scope {
	case types.PolicyScopeGlobal:
		return registry.PolicyScopeKey(scope, ""), nil
	case types.PolicyScopeTemplate:
		if strings.TrimSpace(templateName) == "" {
			return "", restate.TerminalError(fmt.Errorf("template name is required for template-scoped policies"), 400)
		}
		return registry.PolicyScopeKey(scope, templateName), nil
	default:
		return "", restate.TerminalError(fmt.Errorf("invalid policy scope %q", scope), 400)
	}
}

func policySummaries(record types.PolicyRecord) []types.PolicySummary {
	if len(record.Policies) == 0 {
		return nil
	}
	out := make([]types.PolicySummary, 0, len(record.Policies))
	for _, policy := range record.Policies {
		out = append(out, types.PolicySummary{
			Name:         policy.Name,
			Scope:        policy.Scope,
			TemplateName: policy.TemplateName,
			Description:  policy.Description,
			UpdatedAt:    policy.CreatedAt,
		})
	}
	return out
}
