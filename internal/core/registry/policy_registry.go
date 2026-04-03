package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"cuelang.org/go/cue/cuecontext"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// PolicyRegistry is a Restate Virtual Object that stores policy records
// keyed by scope identifier. Policies are CUE constraints that are unified
// with templates during evaluation to enforce organizational rules.
//
// Key encoding:
//   - Global scope:   key = "global"
//   - Template scope: key = "template:<templateName>"
//
// Each scope key holds a single PolicyRecord containing an ordered list of
// Policy entries. Multiple policies in the same scope are unified together
// during template evaluation, producing cumulative constraints.
type PolicyRegistry struct{}

// ServiceName returns the Restate service name for this Virtual Object.
func (PolicyRegistry) ServiceName() string {
	return PolicyRegistryServiceName
}

// AddPolicy appends a new policy to the scope's policy list. This is an
// exclusive handler — Restate serializes concurrent writes to the same scope.
// The CUE source is compiled and validated before storage. Duplicate names
// within the same scope are rejected with a 409 TerminalError.
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

// RemovePolicy deletes a policy by name from the scope's policy list.
// Returns a TerminalError(404) if the policy is not found.
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

// GetPolicies returns all policies in a scope. This is a shared (read-only)
// handler. Returns an empty PolicyRecord with just the scope set if no
// policies have been added yet.
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

// GetPolicy retrieves a single policy by name from the scope's list.
// Returns a TerminalError(404) if the policy is not found.
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

// addPolicyRecord is a pure function that validates inputs and appends a new
// policy to the record. Validation:
//  1. Name and source must be non-empty.
//  2. CUE source must compile without errors.
//  3. Scope and template name must match the VO key.
//  4. Duplicate policy names are rejected (409).
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

// removePolicyRecord is a pure function that removes a named policy from the
// record's policy list. Returns 404 if the policy is not found.
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

// findPolicy searches for a policy by name in the record's list.
// Returns the policy if found, or TerminalError(404) if not.
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

// policyDigest computes a SHA-256 hex digest of the CUE source for
// change detection, identical in approach to templateDigest.
func policyDigest(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}
