package registry

import (
	"fmt"
	"strings"

	"github.com/shirvan/praxis/pkg/types"
)

const (
	TemplateRegistryServiceName = "TemplateRegistry"
	TemplateIndexServiceName    = "TemplateIndex"
	TemplateIndexGlobalKey      = "global"
	PolicyRegistryServiceName   = "PolicyRegistry"
	stateKey                    = "record"
	policyStateKey              = "record"
)

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

func ParsePolicyScopeKey(key string) (types.PolicyScope, string, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == string(types.PolicyScopeGlobal) {
		return types.PolicyScopeGlobal, "", nil
	}
	const prefix = "template:"
	if strings.HasPrefix(trimmed, prefix) {
		templateName := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		if templateName == "" {
			return "", "", fmt.Errorf("template-scoped policy key requires a template name")
		}
		return types.PolicyScopeTemplate, templateName, nil
	}
	return "", "", fmt.Errorf("invalid policy scope key %q", key)
}
