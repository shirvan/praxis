// handlers_policy.go implements the policy registry CRUD handlers.
//
// Policies are CUE constraints that are evaluated alongside templates during
// the compilation pipeline. They enforce organizational rules (e.g., "all S3
// buckets must have encryption enabled") without modifying the templates
// themselves.
//
// Policies are scoped at two levels:
//   - Global: applied to every template evaluation.
//   - Template: applied only when the named template is evaluated.
//
// Like templates, the command service is a thin gateway — the actual durable
// policy storage lives in the PolicyRegistryObj virtual object, keyed by a
// composite scope key (e.g., "global:" or "template:my-template").
package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/registry"
	"github.com/shirvan/praxis/pkg/types"
)

// AddPolicy attaches a CUE policy to the specified scope (global or template).
// The policy source is stored durably and will be included in all future
// template evaluations matching that scope.
func (s *PraxisCommandService) AddPolicy(ctx restate.Context, req types.AddPolicyRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return restate.TerminalError(fmt.Errorf("policy name is required"), 400)
	}
	if strings.TrimSpace(req.Source) == "" {
		return restate.TerminalError(fmt.Errorf("policy source is required"), 400)
	}
	// Resolve the composite scope key used to address the correct
	// PolicyRegistryObj instance (e.g., "global:" or "template:my-vpc").
	scopeKey, err := policyScopeKeyFromRequest(req.Scope, req.TemplateName)
	if err != nil {
		return err
	}
	_, err = restate.WithRequestType[types.AddPolicyRequest, restate.Void](
		restate.Object[restate.Void](ctx, registry.PolicyRegistryServiceName, scopeKey, "AddPolicy"),
	).Request(req)
	return err
}

// RemovePolicy deletes a named policy from the specified scope.
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

// ListPolicies returns summary information for all policies in a scope.
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

// GetPolicy fetches a single policy by name from the specified scope.
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

// policyScopeKeyFromRequest converts the user-facing scope enum + optional
// template name into the composite key used to address the PolicyRegistryObj
// virtual object. Returns a TerminalError if the scope is invalid or if a
// template-scoped request is missing the template name.
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
		return "", restate.TerminalError(fmt.Errorf("invalid policy scope %q (supported: \"global\", \"template\")", scope), 400)
	}
}

// policySummaries converts a PolicyRecord (the durable storage format) into
// a slice of PolicySummary values suitable for API responses.
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
