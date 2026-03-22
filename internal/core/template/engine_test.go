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

// ── Comprehensions (for loops) ──────────────────────────

func TestEngine_EvaluateBytes_ForComprehension(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	buckets: [...string]
}
resources: {
	for _, name in variables.buckets {
		"bucket-\(name)": {
			kind: "S3Bucket"
			spec: {
				region:     "us-east-1"
				bucketName: name
			}
		}
	}
}
`), map[string]any{
		"buckets": []any{"orders", "payments", "logs"},
	})
	require.NoError(t, err)
	assert.Len(t, specs, 3)
	assert.Contains(t, specs, "bucket-orders")
	assert.Contains(t, specs, "bucket-payments")
	assert.Contains(t, specs, "bucket-logs")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket-orders"], &parsed))
	assert.Equal(t, "S3Bucket", parsed["kind"])
	spec := parsed["spec"].(map[string]any)
	assert.Equal(t, "orders", spec["bucketName"])
}

func TestEngine_EvaluateBytes_ForComprehensionEmpty(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	buckets: [...string]
}
resources: {
	static: {
		kind: "S3Bucket"
		spec: { region: "us-east-1", bucketName: "static" }
	}
	for _, name in variables.buckets {
		"bucket-\(name)": {
			kind: "S3Bucket"
			spec: { region: "us-east-1", bucketName: name }
		}
	}
}
`), map[string]any{
		"buckets": []any{},
	})
	require.NoError(t, err)
	assert.Len(t, specs, 1)
	assert.Contains(t, specs, "static")
}

func TestEngine_EvaluateBytes_ForComprehensionWithDeps(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	instances: [...string]
}
resources: {
	sg: {
		kind: "SecurityGroup"
		spec: {
			groupName:   "shared-sg"
			description: "shared"
			vpcId:       "vpc-123"
		}
	}
	for _, name in variables.instances {
		"instance-\(name)": {
			kind: "EC2Instance"
			spec: {
				region:       "us-east-1"
				imageId:      "ami-012345678"
				instanceType: "t3.micro"
				subnetId:     "subnet-abc"
				securityGroupIds: ["${resources.sg.outputs.groupId}"]
				tags: { app: name }
			}
		}
	}
}
`), map[string]any{
		"instances": []any{"web-a", "web-b"},
	})
	require.NoError(t, err)
	assert.Len(t, specs, 3)
	assert.Contains(t, specs, "sg")
	assert.Contains(t, specs, "instance-web-a")
	assert.Contains(t, specs, "instance-web-b")

	// Verify the output expression is preserved as a literal string
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["instance-web-a"], &parsed))
	spec := parsed["spec"].(map[string]any)
	sgIds := spec["securityGroupIds"].([]any)
	assert.Equal(t, "${resources.sg.outputs.groupId}", sgIds[0])
}

func TestEngine_EvaluateBytes_NestedComprehension(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	envs: [...string]
	svcs: [...string]
}
resources: {
	for _, env in variables.envs
	for _, svc in variables.svcs {
		"\(svc)-\(env)": {
			kind: "S3Bucket"
			spec: {
				region:     "us-east-1"
				bucketName: "\(svc)-\(env)"
			}
		}
	}
}
`), map[string]any{
		"envs": []any{"dev", "prod"},
		"svcs": []any{"api", "web"},
	})
	require.NoError(t, err)
	assert.Len(t, specs, 4)
	assert.Contains(t, specs, "api-dev")
	assert.Contains(t, specs, "api-prod")
	assert.Contains(t, specs, "web-dev")
	assert.Contains(t, specs, "web-prod")
}

// ── Conditionals (if guards) ────────────────────────────

func TestEngine_EvaluateBytes_ConditionalResource_True(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	enableLogging: bool
}
resources: {
	main: {
		kind: "S3Bucket"
		spec: { region: "us-east-1", bucketName: "main" }
	}
	if variables.enableLogging {
		logs: {
			kind: "S3Bucket"
			spec: { region: "us-east-1", bucketName: "logs" }
		}
	}
}
`), map[string]any{"enableLogging": true})
	require.NoError(t, err)
	assert.Len(t, specs, 2)
	assert.Contains(t, specs, "main")
	assert.Contains(t, specs, "logs")
}

func TestEngine_EvaluateBytes_ConditionalResource_False(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	enableLogging: bool
}
resources: {
	main: {
		kind: "S3Bucket"
		spec: { region: "us-east-1", bucketName: "main" }
	}
	if variables.enableLogging {
		logs: {
			kind: "S3Bucket"
			spec: { region: "us-east-1", bucketName: "logs" }
		}
	}
}
`), map[string]any{"enableLogging": false})
	require.NoError(t, err)
	assert.Len(t, specs, 1)
	assert.Contains(t, specs, "main")
}

// ── Hidden fields, let bindings, definitions ────────────

func TestEngine_EvaluateBytes_HiddenFields(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	name: string
	env:  string
}

_naming: {
	prefix: "\(variables.name)-\(variables.env)"
	region: "us-east-1"
}

resources: {
	bucket: {
		kind: "S3Bucket"
		spec: {
			region:     _naming.region
			bucketName: "\(_naming.prefix)-data"
		}
	}
}
`), map[string]any{"name": "myapp", "env": "prod"})
	require.NoError(t, err)
	assert.Len(t, specs, 1)
	assert.Contains(t, specs, "bucket")
	// _naming must NOT be in the output
	assert.NotContains(t, specs, "_naming")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))
	spec := parsed["spec"].(map[string]any)
	assert.Equal(t, "us-east-1", spec["region"])
	assert.Equal(t, "myapp-prod-data", spec["bucketName"])
}

func TestEngine_EvaluateBytes_LetBindings(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	name: string
	env:  string
}
resources: {
	let prefix = "\(variables.name)-\(variables.env)"
	bucket: {
		kind: "S3Bucket"
		spec: {
			region:     "us-east-1"
			bucketName: "\(prefix)-assets"
		}
	}
	logs: {
		kind: "S3Bucket"
		spec: {
			region:     "us-east-1"
			bucketName: "\(prefix)-logs"
		}
	}
}
`), map[string]any{"name": "orders", "env": "dev"})
	require.NoError(t, err)
	assert.Len(t, specs, 2)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))
	assert.Equal(t, "orders-dev-assets", parsed["spec"].(map[string]any)["bucketName"])

	require.NoError(t, json.Unmarshal(specs["logs"], &parsed))
	assert.Equal(t, "orders-dev-logs", parsed["spec"].(map[string]any)["bucketName"])
}

func TestEngine_EvaluateBytes_UserDefinitions(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
variables: {
	name: string
	env:  string
}

#StandardTags: {
	app:       variables.name
	env:       variables.env
	managedBy: "praxis"
}

resources: {
	bucket: {
		kind: "S3Bucket"
		spec: {
			region:     "us-east-1"
			bucketName: "\(variables.name)-\(variables.env)"
			tags:       #StandardTags
		}
	}
}
`), map[string]any{"name": "api", "env": "staging"})
	require.NoError(t, err)
	assert.Len(t, specs, 1)
	// #StandardTags must NOT appear as a resource
	assert.NotContains(t, specs, "#StandardTags")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))
	tags := parsed["spec"].(map[string]any)["tags"].(map[string]any)
	assert.Equal(t, "api", tags["app"])
	assert.Equal(t, "staging", tags["env"])
	assert.Equal(t, "praxis", tags["managedBy"])
}

func TestEngine_EvaluateBytes_StructEmbedding(t *testing.T) {
	eng := NewEngine("")
	specs, err := eng.EvaluateBytes([]byte(`
_baseSpec: {
	region: "us-east-1"
	tags: managedBy: "praxis"
}

resources: {
	bucket: {
		kind: "S3Bucket"
		spec: {
			_baseSpec
			bucketName: "my-bucket"
			versioning: true
		}
	}
}
`), nil)
	require.NoError(t, err)
	assert.Len(t, specs, 1)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))
	spec := parsed["spec"].(map[string]any)
	assert.Equal(t, "us-east-1", spec["region"])
	assert.Equal(t, "my-bucket", spec["bucketName"])
	assert.Equal(t, true, spec["versioning"])
	tags := spec["tags"].(map[string]any)
	assert.Equal(t, "praxis", tags["managedBy"])
}
