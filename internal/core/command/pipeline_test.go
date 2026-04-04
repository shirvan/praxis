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
	source, templatePath, err := service.resolveTemplateSource(nil, " resources: {} ", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "resources: {}", source)
	assert.Equal(t, "inline://template.cue", templatePath)
}

func TestResolveTemplateSource_InlineWithPathHint(t *testing.T) {
	service := &PraxisCommandService{}
	source, templatePath, err := service.resolveTemplateSource(nil, " resources: {} ", nil, "webapp.cue")
	require.NoError(t, err)
	assert.Equal(t, "resources: {}", source)
	assert.Equal(t, "webapp.cue", templatePath)
}

func TestResolveTemplateSource_RejectsBothInlineAndRef(t *testing.T) {
	service := &PraxisCommandService{}
	_, _, err := service.resolveTemplateSource(nil, "resources: {}", &types.TemplateRef{Name: "webapp"}, "")
	require.Error(t, err)
}

func TestResolveTemplateSource_RejectsMissingTemplate(t *testing.T) {
	service := &PraxisCommandService{}
	_, _, err := service.resolveTemplateSource(nil, "", nil, "")
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

	rendered, err := renderResolvedTemplate(nil, specs, sensitive)
	require.NoError(t, err)
	assert.Contains(t, rendered, `"password": "***"`)
	assert.NotContains(t, rendered, "super-secret")
}

func TestRenderResolvedTemplate_IncludesDataSources(t *testing.T) {
	rendered, err := renderResolvedTemplate(map[string]types.DataSourceResult{
		"existingVpc": {
			Kind: "VPC",
			Outputs: map[string]any{
				"vpcId": "vpc-123",
			},
		},
	}, map[string]json.RawMessage{
		"bucket": json.RawMessage(`{"kind":"S3Bucket","spec":{"region":"us-east-1"}}`),
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, rendered, `"data"`)
	assert.Contains(t, rendered, `"existingVpc"`)
	assert.Contains(t, rendered, `"vpcId": "vpc-123"`)
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

func TestParseLifecycle_Present(t *testing.T) {
	raw := json.RawMessage(`{
		"kind": "S3Bucket",
		"lifecycle": {
			"preventDestroy": true,
			"ignoreChanges": ["tags.lastModified", "tags.updatedBy"]
		},
		"spec": {"region": "us-east-1"}
	}`)

	lc, err := parseLifecycle(raw)
	require.NoError(t, err)
	require.NotNil(t, lc)
	assert.True(t, lc.PreventDestroy)
	assert.Equal(t, []string{"tags.lastModified", "tags.updatedBy"}, lc.IgnoreChanges)
}

func TestParseLifecycle_Absent(t *testing.T) {
	raw := json.RawMessage(`{"kind": "S3Bucket", "spec": {"region": "us-east-1"}}`)
	lc, err := parseLifecycle(raw)
	require.NoError(t, err)
	assert.Nil(t, lc)
}

func TestParseLifecycle_Empty(t *testing.T) {
	raw := json.RawMessage(`{"kind": "S3Bucket", "lifecycle": {}, "spec": {"region": "us-east-1"}}`)
	lc, err := parseLifecycle(raw)
	require.NoError(t, err)
	require.NotNil(t, lc)
	assert.False(t, lc.PreventDestroy)
	assert.Nil(t, lc.IgnoreChanges)
}

func TestPlanResourcesFromGraph_PreservesLifecycle(t *testing.T) {
	lifecycle := &types.LifecyclePolicy{PreventDestroy: true}
	nodes := []*types.ResourceNode{
		{Name: "bucket", Kind: "S3Bucket", Key: "bucket", Spec: json.RawMessage(`{"kind":"S3Bucket"}`), Lifecycle: lifecycle},
	}

	graph, err := dag.NewGraph(nodes)
	require.NoError(t, err)

	resources := planResourcesFromGraph(nodes, graph)
	require.Len(t, resources, 1)
	require.NotNil(t, resources[0].Lifecycle)
	assert.True(t, resources[0].Lifecycle.PreventDestroy)
}

func TestFilterIgnoredFields_ExactMatch(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "tags.env", OldValue: "dev", NewValue: "prod"},
		{Path: "spec.versioning", OldValue: false, NewValue: true},
	}
	filtered := filterIgnoredFields(fields, []string{"tags.env"})
	require.Len(t, filtered, 1)
	assert.Equal(t, "spec.versioning", filtered[0].Path)
}

func TestFilterIgnoredFields_PrefixMatch(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "tags.lastModified", OldValue: "old", NewValue: "new"},
		{Path: "tags.updatedBy", OldValue: "old", NewValue: "new"},
		{Path: "spec.region", OldValue: "us-east-1", NewValue: "us-west-2"},
	}
	filtered := filterIgnoredFields(fields, []string{"tags"})
	require.Len(t, filtered, 1)
	assert.Equal(t, "spec.region", filtered[0].Path)
}

func TestFilterIgnoredFields_NoMatch(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "spec.versioning", OldValue: false, NewValue: true},
	}
	filtered := filterIgnoredFields(fields, []string{"tags.env"})
	require.Len(t, filtered, 1)
}

func TestFilterIgnoredFields_EmptyIgnoreList(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "spec.versioning", OldValue: false, NewValue: true},
	}
	filtered := filterIgnoredFields(fields, nil)
	require.Len(t, filtered, 1)
}

func TestIsIgnoredPath_DoesNotMatchPartialPrefix(t *testing.T) {
	// "tag" should NOT match "tags.env"
	assert.False(t, isIgnoredPath("tags.env", []string{"tag"}))
	// "tags.en" should NOT match "tags.env"
	assert.False(t, isIgnoredPath("tags.env", []string{"tags.en"}))
	// "tags" SHOULD match "tags.env" (prefix + dot)
	assert.True(t, isIgnoredPath("tags.env", []string{"tags"}))
}
