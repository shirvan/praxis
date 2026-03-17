package template

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEngine_Evaluate_ValidTemplate(t *testing.T) {
	dir := t.TempDir()

	tmpl := `
resources: {
	bucket: {
		kind: "S3Bucket"
		spec: {
			region:     "us-east-1"
			bucketName: "my-test-bucket"
			versioning: true
		}
	}
}
`
	tmplPath := filepath.Join(dir, "infra.cue")
	require.NoError(t, os.WriteFile(tmplPath, []byte(tmpl), 0644))

	eng := NewEngine("")
	specs, err := eng.Evaluate(tmplPath, nil)
	require.NoError(t, err)
	require.Contains(t, specs, "bucket")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))
	assert.Equal(t, "S3Bucket", parsed["kind"])

	spec := parsed["spec"].(map[string]any)
	assert.Equal(t, "us-east-1", spec["region"])
	assert.Equal(t, "my-test-bucket", spec["bucketName"])
}

func TestEngine_Evaluate_WithVariables(t *testing.T) {
	dir := t.TempDir()

	tmpl := `
variables: {
	env:    string
	region: string
}
resources: {
	bucket: {
		kind: "S3Bucket"
		spec: {
			region:     variables.region
			bucketName: "app-" + variables.env + "-data"
		}
	}
}
`
	tmplPath := filepath.Join(dir, "infra.cue")
	require.NoError(t, os.WriteFile(tmplPath, []byte(tmpl), 0644))

	eng := NewEngine("")
	vars := map[string]any{"env": "staging", "region": "eu-west-1"}
	specs, err := eng.Evaluate(tmplPath, vars)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))

	spec := parsed["spec"].(map[string]any)
	assert.Equal(t, "eu-west-1", spec["region"])
	assert.Equal(t, "app-staging-data", spec["bucketName"])
}

func TestEngine_Evaluate_MissingResources(t *testing.T) {
	dir := t.TempDir()

	tmpl := `
hello: "world"
`
	tmplPath := filepath.Join(dir, "bad.cue")
	require.NoError(t, os.WriteFile(tmplPath, []byte(tmpl), 0644))

	eng := NewEngine("")
	_, err := eng.Evaluate(tmplPath, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrCUEValidation, tErrs[0].Kind)
	assert.Contains(t, tErrs[0].Message, "resources")
}

func TestEngine_Evaluate_MissingKind(t *testing.T) {
	dir := t.TempDir()

	tmpl := `
resources: {
	bucket: {
		spec: { region: "us-east-1" }
	}
}
`
	tmplPath := filepath.Join(dir, "no-kind.cue")
	require.NoError(t, os.WriteFile(tmplPath, []byte(tmpl), 0644))

	eng := NewEngine("")
	_, err := eng.Evaluate(tmplPath, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrCUEValidation, tErrs[0].Kind)
	assert.Contains(t, tErrs[0].Message, "kind")
}

func TestEngine_Evaluate_MultipleResources(t *testing.T) {
	dir := t.TempDir()

	tmpl := `
resources: {
	logs: {
		kind: "S3Bucket"
		spec: { region: "us-east-1", bucketName: "logs" }
	}
	data: {
		kind: "S3Bucket"
		spec: { region: "us-west-2", bucketName: "data" }
	}
}
`
	tmplPath := filepath.Join(dir, "multi.cue")
	require.NoError(t, os.WriteFile(tmplPath, []byte(tmpl), 0644))

	eng := NewEngine("")
	specs, err := eng.Evaluate(tmplPath, nil)
	require.NoError(t, err)
	assert.Len(t, specs, 2)
	assert.Contains(t, specs, "logs")
	assert.Contains(t, specs, "data")
}

func TestEngine_Evaluate_InvalidCUE(t *testing.T) {
	dir := t.TempDir()

	tmpl := `this is not valid CUE {{{`
	tmplPath := filepath.Join(dir, "broken.cue")
	require.NoError(t, os.WriteFile(tmplPath, []byte(tmpl), 0644))

	eng := NewEngine("")
	_, err := eng.Evaluate(tmplPath, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrCUELoad, tErrs[0].Kind)
}

func TestEngine_Evaluate_NonExistentFile(t *testing.T) {
	eng := NewEngine("")
	_, err := eng.Evaluate("/nonexistent/path/infra.cue", nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrCUELoad, tErrs[0].Kind)
}

func TestEngine_Evaluate_CollectsMultipleErrors(t *testing.T) {
	dir := t.TempDir()

	tmpl := `
resources: {
	a: { spec: { x: 1 } }
	b: { spec: { y: 2 } }
}
`
	tmplPath := filepath.Join(dir, "multi-err.cue")
	require.NoError(t, os.WriteFile(tmplPath, []byte(tmpl), 0644))

	eng := NewEngine("")
	_, err := eng.Evaluate(tmplPath, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Len(t, tErrs, 2, "should collect errors for both resources")
}

func TestEngine_EvaluateBytesWithPolicies_Passes(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: { name: "assets-prod" }
		spec: {
			region: "us-east-1"
			encryption: enabled: true
			tags: { environment: "prod" }
		}
	}
}`), []PolicySource{{
		Name:   "require-encryption",
		Source: []byte(`resources: [_]: spec: encryption: enabled: true`),
	}}, nil)
	require.NoError(t, err)
	assert.Contains(t, specs, "bucket")
}

func TestEngine_EvaluateBytesWithPolicies_Violation(t *testing.T) {
	eng := NewEngine("")
	_, err := eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: { name: "assets-prod" }
		spec: {
			region: "us-east-1"
			encryption: enabled: false
		}
	}
}`), []PolicySource{{
		Name:   "require-encryption",
		Source: []byte(`resources: [_]: spec: encryption: enabled: true`),
	}}, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrPolicyViolation, tErrs[0].Kind)
	assert.Equal(t, "require-encryption", tErrs[0].PolicyName)
	assert.Contains(t, tErrs[0].Path, "resources.bucket.spec.encryption.enabled")
}

func TestEngine_EvaluateBytesWithPolicies_InvalidPolicy(t *testing.T) {
	eng := NewEngine("")
	_, err := eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: { name: "assets-prod" }
		spec: { region: "us-east-1" }
	}
}`), []PolicySource{{
		Name:   "broken",
		Source: []byte(`this is not valid cue {`),
	}}, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrPolicyViolation, tErrs[0].Kind)
	assert.Equal(t, "broken", tErrs[0].PolicyName)
	assert.Equal(t, "policy:broken", tErrs[0].Path)
}
