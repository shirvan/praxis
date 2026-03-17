package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyRequestJSONRoundTrip(t *testing.T) {
	req := ApplyRequest{
		TemplateRef:   &TemplateRef{Name: "webapp"},
		Variables:     map[string]any{"env": "prod"},
		DeploymentKey: "webapp-prod",
	}

	encoded, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded ApplyRequest
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, req.TemplateRef.Name, decoded.TemplateRef.Name)
	assert.Equal(t, req.DeploymentKey, decoded.DeploymentKey)
	assert.Equal(t, req.Variables["env"], decoded.Variables["env"])
}

func TestValidateTemplateRequestOmitempty(t *testing.T) {
	encoded, err := json.Marshal(ValidateTemplateRequest{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "templateRef")
	assert.NotContains(t, string(encoded), "variables")
	assert.NotContains(t, string(encoded), "mode")
}

func TestAddPolicyRequestJSONRoundTrip(t *testing.T) {
	req := AddPolicyRequest{
		Name:         "require-encryption",
		Scope:        PolicyScopeTemplate,
		TemplateName: "webapp",
		Source:       "resources: [_]: spec: encryption: enabled: true",
		Description:  "Require encryption",
	}

	encoded, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded AddPolicyRequest
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, req.Name, decoded.Name)
	assert.Equal(t, req.Scope, decoded.Scope)
	assert.Equal(t, req.TemplateName, decoded.TemplateName)
	assert.Equal(t, req.Source, decoded.Source)
	assert.Equal(t, req.Description, decoded.Description)
}
