package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"cuelang.org/go/cue/cuecontext"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/pkg/types"
)

// PolicyRegistry stores policy records keyed by scope identifier.
type PolicyRegistry struct{}

func (PolicyRegistry) ServiceName() string {
	return PolicyRegistryServiceName
}

func (PolicyRegistry) AddPolicy(ctx restate.ObjectContext, req types.AddPolicyRequest) error {
	record, err := restate.Get[*types.PolicyRecord](ctx, policyStateKey)
	if err != nil {
		return err
	}

	now, err := restate.Run(ctx, func(runCtx restate.RunContext) (time.Time, error) {
		return time.Now().UTC(), nil
	})
	if err != nil {
		return err
	}

	updated, err := addPolicyRecord(restate.Key(ctx), record, req, now)
	if err != nil {
		return err
	}

	restate.Set(ctx, policyStateKey, updated)
	return nil
}

func (PolicyRegistry) RemovePolicy(ctx restate.ObjectContext, name string) error {
	record, err := restate.Get[*types.PolicyRecord](ctx, policyStateKey)
	if err != nil {
		return err
	}

	updated, err := removePolicyRecord(record, name)
	if err != nil {
		return err
	}

	restate.Set(ctx, policyStateKey, updated)
	return nil
}

func (PolicyRegistry) GetPolicies(ctx restate.ObjectSharedContext, _ restate.Void) (types.PolicyRecord, error) {
	record, err := restate.Get[*types.PolicyRecord](ctx, policyStateKey)
	if err != nil {
		return types.PolicyRecord{}, err
	}
	if record == nil {
		scope, _, parseErr := ParsePolicyScopeKey(restate.Key(ctx))
		if parseErr != nil {
			return types.PolicyRecord{}, restate.TerminalError(parseErr, 400)
		}
		return types.PolicyRecord{Scope: scope}, nil
	}
	return *record, nil
}

func (PolicyRegistry) GetPolicy(ctx restate.ObjectSharedContext, name string) (types.Policy, error) {
	record, err := restate.Get[*types.PolicyRecord](ctx, policyStateKey)
	if err != nil {
		return types.Policy{}, err
	}
	policy, err := findPolicy(record, name)
	if err != nil {
		return types.Policy{}, err
	}
	return policy, nil
}

func addPolicyRecord(key string, record *types.PolicyRecord, req types.AddPolicyRequest, now time.Time) (types.PolicyRecord, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("policy name is required"), 400)
	}
	if strings.TrimSpace(req.Source) == "" {
		return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("policy source is required"), 400)
	}
	if compiled := cuecontext.New().CompileString(req.Source); compiled.Err() != nil {
		return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("invalid CUE source: %w", compiled.Err()), 400)
	}

	scope, templateName, err := ParsePolicyScopeKey(key)
	if err != nil {
		return types.PolicyRecord{}, restate.TerminalError(err, 400)
	}
	if req.Scope != scope {
		return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("policy scope %q does not match object key %q", req.Scope, key), 400)
	}
	if scope == types.PolicyScopeTemplate && strings.TrimSpace(req.TemplateName) != templateName {
		return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("policy template name %q does not match object key %q", req.TemplateName, key), 400)
	}

	updated := types.PolicyRecord{Scope: scope}
	if record != nil {
		updated = *record
	}
	for _, existing := range updated.Policies {
		if existing.Name == name {
			return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("policy %q already exists in scope %q", name, key), 409)
		}
	}

	updated.Scope = scope
	updated.Policies = append(updated.Policies, types.Policy{
		Name:         name,
		Scope:        scope,
		TemplateName: templateName,
		Source:       req.Source,
		Digest:       policyDigest(req.Source),
		Description:  req.Description,
		CreatedAt:    now,
	})
	return updated, nil
}

func removePolicyRecord(record *types.PolicyRecord, name string) (types.PolicyRecord, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("policy name is required"), 400)
	}
	if record == nil || len(record.Policies) == 0 {
		return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("policy %q not found", trimmed), 404)
	}

	updated := types.PolicyRecord{Scope: record.Scope, Policies: make([]types.Policy, 0, len(record.Policies))}
	removed := false
	for _, policy := range record.Policies {
		if policy.Name == trimmed {
			removed = true
			continue
		}
		updated.Policies = append(updated.Policies, policy)
	}
	if !removed {
		return types.PolicyRecord{}, restate.TerminalError(fmt.Errorf("policy %q not found", trimmed), 404)
	}
	return updated, nil
}

func findPolicy(record *types.PolicyRecord, name string) (types.Policy, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return types.Policy{}, restate.TerminalError(fmt.Errorf("policy name is required"), 400)
	}
	if record == nil {
		return types.Policy{}, restate.TerminalError(fmt.Errorf("policy %q not found", trimmed), 404)
	}
	for _, policy := range record.Policies {
		if policy.Name == trimmed {
			return policy, nil
		}
	}
	return types.Policy{}, restate.TerminalError(fmt.Errorf("policy %q not found", trimmed), 404)
}

func policyDigest(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}
