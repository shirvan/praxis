package command

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/internal/core/resolver"
	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

func TestResolveTemplateSource_Inline(t *testing.T) {
	service := &PraxisCommandService{}
	source, templatePath, err := service.resolveTemplateSource(nil, " resources: {} ", nil)
	require.NoError(t, err)
	assert.Equal(t, "resources: {}", source)
	assert.Equal(t, "inline://template.cue", templatePath)
}

func TestResolveTemplateSource_RejectsBothInlineAndRef(t *testing.T) {
	service := &PraxisCommandService{}
	_, _, err := service.resolveTemplateSource(nil, "resources: {}", &types.TemplateRef{Name: "webapp"})
	require.Error(t, err)
}

func TestResolveTemplateSource_RejectsMissingTemplate(t *testing.T) {
	service := &PraxisCommandService{}
	_, _, err := service.resolveTemplateSource(nil, "", nil)
	require.Error(t, err)
}

func TestDeriveDeploymentKey_SingleResource(t *testing.T) {
	specs := map[string]json.RawMessage{
		"bucket": json.RawMessage(`{"kind":"S3Bucket","metadata":{"name":"assets-prod"},"spec":{"region":"us-east-1"}}`),
	}

	key, err := deriveDeploymentKey(specs)
	require.NoError(t, err)
	assert.Equal(t, "s3bucket-assets-prod", key)
}

func TestDeriveDeploymentKey_MultiResourceAddsStackSuffix(t *testing.T) {
	specs := map[string]json.RawMessage{
		"app":    json.RawMessage(`{"kind":"S3Bucket","metadata":{"name":"assets-prod"},"spec":{"region":"us-east-1"}}`),
		"worker": json.RawMessage(`{"kind":"SecurityGroup","metadata":{"name":"web-sg"},"spec":{"groupName":"web-sg","description":"web","vpcId":"vpc-1"}}`),
	}

	key, err := deriveDeploymentKey(specs)
	require.NoError(t, err)
	assert.Equal(t, "s3bucket-assets-prod-stack", key)
}

func TestRenderResolvedTemplate_MasksSensitiveValues(t *testing.T) {
	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"kind":"Database","spec":{"password":"super-secret","region":"us-east-1"}}`),
	}
	sensitive := resolver.NewSensitiveParams()
	sensitive.Add("db", "spec.password")

	rendered, err := renderResolvedTemplate(specs, sensitive)
	require.NoError(t, err)
	assert.Contains(t, rendered, `"password": "***"`)
	assert.NotContains(t, rendered, "super-secret")
}

func TestPlanResourcesFromGraph_UsesTopologicalOrder(t *testing.T) {
	nodes := []*types.ResourceNode{
		{Name: "bucket", Kind: "S3Bucket", Key: "bucket", Dependencies: []string{"sg"}, Spec: json.RawMessage(`{"kind":"S3Bucket"}`)},
		{Name: "sg", Kind: "SecurityGroup", Key: "vpc-1~web", Dependencies: nil, Spec: json.RawMessage(`{"kind":"SecurityGroup"}`)},
	}

	graph, err := dag.NewGraph(nodes)
	require.NoError(t, err)

	resources := planResourcesFromGraph(nodes, graph)
	require.Len(t, resources, 2)
	assert.Equal(t, "sg", resources[0].Name)
	assert.Equal(t, "bucket", resources[1].Name)
	assert.Equal(t, []string{"sg"}, resources[1].Dependencies)
}

func TestEngineEvaluateBytes_RendersInMemoryTemplate(t *testing.T) {
	engine := template.NewEngine("")
	specs, err := engine.EvaluateBytes([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: { name: "assets" }
		spec: {
			region: "us-east-1"
			bucketName: "assets"
		}
	}
}`), nil)
	require.NoError(t, err)
	assert.Contains(t, specs, "bucket")
}

func TestNormalizeIdentifier_CollapsesUnsafeCharacters(t *testing.T) {
	assert.Equal(t, "my-stack-prod", normalizeIdentifier(" My.Stack / Prod "))
}
