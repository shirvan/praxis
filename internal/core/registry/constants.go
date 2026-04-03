package registry

import (
	"fmt"
	"strings"

	"github.com/shirvan/praxis/pkg/types"
)

const (
	// TemplateRegistryServiceName is the Restate Virtual Object service name
	// for the per-template registry that stores full template records.
	TemplateRegistryServiceName = "TemplateRegistry"

	// TemplateIndexServiceName is the Restate Virtual Object service name
	// for the global template index that stores lightweight summaries.
	TemplateIndexServiceName = "TemplateIndex"

	// TemplateIndexGlobalKey is the single fixed key used by the TemplateIndex
	// Virtual Object. All template summaries are stored under this one key,
	// ensuring serialized access for the global listing.
	TemplateIndexGlobalKey = "global"

	// PolicyRegistryServiceName is the Restate Virtual Object service name
	// for the per-scope policy registry that stores CUE policy constraints.
	PolicyRegistryServiceName = "PolicyRegistry"

	// stateKey is the Restate state key used by TemplateRegistry to store
	// the types.TemplateRecord for a given template name.
	stateKey = "record"

	// policyStateKey is the Restate state key used by PolicyRegistry to store
	// the types.PolicyRecord for a given scope key.
	policyStateKey = "record"
)

// PolicyScopeKey encodes a policy scope and optional template name into the
// canonical Virtual Object key used by PolicyRegistry.
//
// Key encoding:
//   - Global scope:   "global"
//   - Template scope: "template:<templateName>"
func PolicyScopeKey(scope types.PolicyScope, templateName string) string {
	switch scope {
	case types.PolicyScopeGlobal:
		return string(types.PolicyScopeGlobal)
	case types.PolicyScopeTemplate:
		return "template:" + strings.TrimSpace(templateName)
	default:
		return ""
	}
}

// ParsePolicyScopeKey reverses PolicyScopeKey, extracting the scope and
// template name from a Virtual Object key string. Returns an error if the
// key does not match any known format.
func ParsePolicyScopeKey(key string) (types.PolicyScope, string, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == string(types.PolicyScopeGlobal) {
		return types.PolicyScopeGlobal, "", nil
	}
	const prefix = "template:"
	if templateName, ok := strings.CutPrefix(trimmed, prefix); ok {
		templateName = strings.TrimSpace(templateName)
		if templateName == "" {
			return "", "", fmt.Errorf("template-scoped policy key requires a template name")
		}
		return types.PolicyScopeTemplate, templateName, nil
	}
	return "", "", fmt.Errorf("invalid policy scope key %q", key)
}
