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
	result, err := eng.EvaluateBytesWithPolicies([]byte(`
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
	assert.Contains(t, result.Resources, "bucket")
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

// ── Example Policy Validation ───────────────────────────

func TestEngine_ExamplePolicy_SecurityBaseline_Passes(t *testing.T) {
	eng := NewEngine("")
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "policies", "security-baseline.cue"))
	require.NoError(t, err)

	result, err := eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: { name: "assets-prod" }
		spec: {
			region:     "us-east-1"
			encryption: enabled: true
			tags: { environment: "prod", app: "myapp" }
		}
	}
	server: {
		kind: "EC2Instance"
		metadata: { name: "web-prod" }
		spec: {
			region: "us-east-1"
			imageId: "ami-0885b1f6bd170450c"
			instanceType: "t3.small"
			subnetId: "subnet-abc123"
			rootVolume: { sizeGiB: 20, volumeType: "gp3", encrypted: true }
			tags: { environment: "prod", app: "myapp" }
		}
	}
}`), []PolicySource{{
		Name:   "security-baseline",
		Source: policy,
	}}, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Resources, "bucket")
	assert.Contains(t, result.Resources, "server")
}

func TestEngine_ExamplePolicy_SecurityBaseline_ViolatesEncryption(t *testing.T) {
	eng := NewEngine("")
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "policies", "security-baseline.cue"))
	require.NoError(t, err)

	_, err = eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: { name: "assets-prod" }
		spec: {
			region:     "us-east-1"
			encryption: enabled: false
			tags: { environment: "prod", app: "myapp" }
		}
	}
}`), []PolicySource{{
		Name:   "security-baseline",
		Source: policy,
	}}, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrPolicyViolation, tErrs[0].Kind)
	assert.Equal(t, "security-baseline", tErrs[0].PolicyName)
}

func TestEngine_ExamplePolicy_SecurityBaseline_ViolatesMissingTags(t *testing.T) {
	eng := NewEngine("")
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "policies", "security-baseline.cue"))
	require.NoError(t, err)

	_, err = eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: { name: "assets-prod" }
		spec: {
			region:     "us-east-1"
			encryption: enabled: true
			tags: { purpose: "assets" }
		}
	}
}`), []PolicySource{{
		Name:   "security-baseline",
		Source: policy,
	}}, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrPolicyViolation, tErrs[0].Kind)
}

func TestEngine_ExamplePolicy_CostControls_Violation(t *testing.T) {
	eng := NewEngine("")
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "policies", "cost-controls.cue"))
	require.NoError(t, err)

	_, err = eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	server: {
		kind: "EC2Instance"
		metadata: { name: "big-server" }
		spec: {
			region: "us-east-1"
			imageId: "ami-0885b1f6bd170450c"
			instanceType: "m5.4xlarge"
			subnetId: "subnet-abc123"
			rootVolume: { sizeGiB: 20, volumeType: "gp3", encrypted: true }
			tags: { app: "myapp" }
		}
	}
}`), []PolicySource{{
		Name:   "cost-controls",
		Source: policy,
	}}, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrPolicyViolation, tErrs[0].Kind)
	assert.Equal(t, "cost-controls", tErrs[0].PolicyName)
}

func TestEngine_ExamplePolicy_ProdGuardrails_Passes(t *testing.T) {
	eng := NewEngine("")
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "policies", "prod-guardrails.cue"))
	require.NoError(t, err)

	result, err := eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	"web-prod": {
		kind: "EC2Instance"
		metadata: { name: "web-prod" }
		spec: {
			region: "us-east-1"
			imageId: "ami-0885b1f6bd170450c"
			instanceType: "t3.small"
			subnetId: "subnet-abc123"
			monitoring: true
			rootVolume: { sizeGiB: 20, volumeType: "gp3", encrypted: true }
			tags: { app: "myapp" }
		}
	}
	"data-prod": {
		kind: "S3Bucket"
		metadata: { name: "data-prod" }
		spec: {
			region: "us-east-1"
			acl: "private"
			versioning: true
			encryption: enabled: true
			tags: { app: "myapp" }
		}
	}
}`), []PolicySource{{
		Name:   "prod-guardrails",
		Source: policy,
	}}, nil)
	require.NoError(t, err)
	assert.Len(t, result.Resources, 2)
}

func TestEngine_EvaluateBytesWithPolicies_ExtractsDataSources(t *testing.T) {
	eng := NewEngine("")
	result, err := eng.EvaluateBytesWithPolicies([]byte(`
data: {
	existingVpc: {
		kind: "VPC"
		region: "us-east-1"
		filter: {
			name: "production-vpc"
		}
	}
}
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: { name: "assets" }
		spec: {
			region: "us-east-1"
			bucketName: "assets"
		}
	}
}`), nil, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Resources, "bucket")
	require.Contains(t, result.DataSources, "existingVpc")
	assert.Equal(t, "VPC", result.DataSources["existingVpc"].Kind)
	assert.Equal(t, "production-vpc", result.DataSources["existingVpc"].Filter.Name)
}

func TestEngine_ExamplePolicy_ProdGuardrails_ViolatesMonitoring(t *testing.T) {
	eng := NewEngine("")
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "policies", "prod-guardrails.cue"))
	require.NoError(t, err)

	_, err = eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	"web-prod": {
		kind: "EC2Instance"
		metadata: { name: "web-prod" }
		spec: {
			region: "us-east-1"
			imageId: "ami-0885b1f6bd170450c"
			instanceType: "t3.small"
			subnetId: "subnet-abc123"
			monitoring: false
			rootVolume: { sizeGiB: 20, volumeType: "gp3", encrypted: true }
			tags: { app: "myapp" }
		}
	}
}`), []PolicySource{{
		Name:   "prod-guardrails",
		Source: policy,
	}}, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrPolicyViolation, tErrs[0].Kind)
}

func TestEngine_ExamplePolicy_NetworkHardening_ViolatesPublicBucket(t *testing.T) {
	eng := NewEngine("")
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "policies", "network-hardening.cue"))
	require.NoError(t, err)

	_, err = eng.EvaluateBytesWithPolicies([]byte(`
resources: {
	website: {
		kind: "S3Bucket"
		metadata: { name: "website-assets" }
		spec: {
			region: "us-east-1"
			acl: "public-read"
			encryption: enabled: true
			tags: { app: "site" }
		}
	}
}`), []PolicySource{{
		Name:   "network-hardening",
		Source: policy,
	}}, nil)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrPolicyViolation, tErrs[0].Kind)
	assert.Equal(t, "network-hardening", tErrs[0].PolicyName)
}

// ── Lifecycle Block Tests ───────────────────────────

func TestEngine_EvaluateBytes_LifecycleBlockWithSchema(t *testing.T) {
	absSchemaDir, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas", "aws"))
	require.NoError(t, err)
	eng := NewEngine(absSchemaDir)

	specs, err := eng.EvaluateBytes([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: name: "test-bucket"
		lifecycle: {
			preventDestroy: true
			ignoreChanges: ["tags.lastModified"]
		}
		spec: {
			region: "us-east-1"
			tags: env: "prod"
		}
	}
}
`), nil)
	require.NoError(t, err)
	require.Contains(t, specs, "bucket")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))

	lc, ok := parsed["lifecycle"].(map[string]any)
	require.True(t, ok, "lifecycle block should be present in rendered output")
	assert.Equal(t, true, lc["preventDestroy"])

	ignoreChanges, ok := lc["ignoreChanges"].([]any)
	require.True(t, ok)
	assert.Equal(t, "tags.lastModified", ignoreChanges[0])
}

func TestEngine_EvaluateBytes_LifecycleBlockOptional(t *testing.T) {
	absSchemaDir, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas", "aws"))
	require.NoError(t, err)
	eng := NewEngine(absSchemaDir)

	// Template without lifecycle should still work fine.
	specs, err := eng.EvaluateBytes([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		metadata: name: "test-bucket"
		spec: {
			region: "us-east-1"
			tags: env: "dev"
		}
	}
}
`), nil)
	require.NoError(t, err)
	require.Contains(t, specs, "bucket")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))
	_, hasLifecycle := parsed["lifecycle"]
	assert.False(t, hasLifecycle, "lifecycle should not appear when not declared")
}

func TestEngine_EvaluateBytes_LifecycleBlockNoSchema(t *testing.T) {
	eng := NewEngine("")

	// Without schema validation, lifecycle block passes through too.
	specs, err := eng.EvaluateBytes([]byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
		lifecycle: {
			preventDestroy: true
		}
		spec: {
			region: "us-east-1"
		}
	}
}
`), nil)
	require.NoError(t, err)
	require.Contains(t, specs, "bucket")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(specs["bucket"], &parsed))
	lc, ok := parsed["lifecycle"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, lc["preventDestroy"])
}

// ── Example Template Tests ─────────────────────────

// exampleCase pairs a template path (relative to the repo root) with
// the variables needed for evaluation and the minimum expected resource
// count. Every example template in examples/ should have an entry here
// so we catch schema drift early.
type exampleCase struct {
	name         string
	path         string
	vars         map[string]any
	minResources int
}

func exampleCases() []exampleCase {
	return []exampleCase{
		// ── ACM ──
		{
			name:         "acm/basic-certificate",
			path:         "examples/acm/basic-certificate.cue",
			vars:         map[string]any{"name": "api", "environment": "prod", "domainName": "api.example.com"},
			minResources: 1,
		},
		{
			name: "acm/https-stack",
			path: "examples/acm/https-stack.cue",
			vars: map[string]any{
				"name": "api", "environment": "prod", "domainName": "api.example.com",
				"hostedZoneId":   "Z0123456789ABCDEF",
				"albArn":         "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/api-lb/1234567890abcdef",
				"targetGroupArn": "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/api-targets/1234567890abcdef",
			},
			minResources: 3,
		},
		{
			name:         "acm/wildcard-certificate",
			path:         "examples/acm/wildcard-certificate.cue",
			vars:         map[string]any{"name": "platform", "environment": "prod", "baseDomain": "example.com"},
			minResources: 1,
		},
		// ── EC2 ──
		{
			name: "ec2/bastion-host",
			path: "examples/ec2/bastion-host.cue",
			vars: map[string]any{
				"name": "ops", "environment": "dev",
				"vpcId": "vpc-abc123", "subnetId": "subnet-abc123",
				"allowedCidr": "203.0.113.0/24",
			},
			minResources: 3,
		},
		{
			name:         "ec2/dev-instance",
			path:         "examples/ec2/dev-instance.cue",
			vars:         map[string]any{"name": "myapp", "subnetId": "subnet-abc123"},
			minResources: 1,
		},
		{
			name:         "ec2/ebs-data-tier",
			path:         "examples/ec2/ebs-data-tier.cue",
			vars:         map[string]any{"name": "warehouse", "environment": "prod", "az": "us-east-1a"},
			minResources: 2,
		},
		{
			name:         "ec2/ec2-instance",
			path:         "examples/ec2/ec2-instance.cue",
			vars:         map[string]any{"name": "my-app", "environment": "dev", "subnetId": "subnet-abc123"},
			minResources: 1,
		},
		{
			name: "ec2/web-fleet",
			path: "examples/ec2/web-fleet.cue",
			vars: map[string]any{
				"name": "web", "environment": "prod",
				"vpcId": "vpc-abc123", "subnetIdA": "subnet-aaa", "subnetIdB": "subnet-bbb",
			},
			minResources: 4,
		},
		// ── Lifecycle ──
		{
			name:         "lifecycle/external-managed",
			path:         "examples/lifecycle/external-managed.cue",
			vars:         map[string]any{"name": "analytics", "environment": "prod"},
			minResources: 1,
		},
		{
			name:         "lifecycle/protected-db",
			path:         "examples/lifecycle/protected-db.cue",
			vars:         map[string]any{"name": "orders", "environment": "prod"},
			minResources: 1,
		},
		// ── S3 ──
		{
			name:         "s3/app-buckets",
			path:         "examples/s3/app-buckets.cue",
			vars:         map[string]any{"name": "myapp", "environment": "prod"},
			minResources: 3,
		},
		{
			name: "s3/dynamic-buckets",
			path: "examples/s3/dynamic-buckets.cue",
			vars: map[string]any{
				"name": "orders-api", "environment": "prod",
				"buckets":       []any{"assets", "uploads", "backups"},
				"enableLogging": true,
			},
			minResources: 4,
		},
		{
			name:         "s3/static-website",
			path:         "examples/s3/static-website.cue",
			vars:         map[string]any{"name": "docs", "environment": "prod"},
			minResources: 1,
		},
		// ── Stacks ──
		{
			name: "stacks/data-source-multi",
			path: "examples/stacks/data-source-multi.cue",
			vars: map[string]any{
				"name": "ci", "environment": "dev",
				"bucketName": "my-bucket", "roleName": "my-role",
			},
			minResources: 1,
		},
		{
			name:         "stacks/data-source-vpc",
			path:         "examples/stacks/data-source-vpc.cue",
			vars:         map[string]any{"name": "web", "environment": "dev", "vpcName": "main-vpc"},
			minResources: 1,
		},
		{
			name: "stacks/ec2-web-stack",
			path: "examples/stacks/ec2-web-stack.cue",
			vars: map[string]any{
				"name": "web", "environment": "dev",
				"cidrBlock": "10.0.0.0/16", "subnetId": "subnet-abc123",
			},
			minResources: 3,
		},
		{
			name:         "stacks/network-locked-app",
			path:         "examples/stacks/network-locked-app.cue",
			vars:         map[string]any{"name": "secure", "environment": "prod"},
			minResources: 5,
		},
		{
			name:         "stacks/three-tier-app",
			path:         "examples/stacks/three-tier-app.cue",
			vars:         map[string]any{"name": "acme", "environment": "prod", "instanceType": "t3.medium"},
			minResources: 10,
		},
		{
			name: "stacks/saas-platform",
			path: "examples/stacks/saas-platform.cue",
			vars: map[string]any{
				"name": "acme", "environment": "prod",
				"domainName": "acme.example.com", "hostedZoneId": "Z0123456789ABCDEF",
				"availabilityZones": []string{"us-east-1a", "us-east-1b"},
				"storageBuckets":    []string{"assets", "uploads", "backups"},
			},
			minResources: 20,
		},
		// ── VPC ──
		{
			name:         "vpc/basic-vpc",
			path:         "examples/vpc/basic-vpc.cue",
			vars:         map[string]any{"name": "myapp", "environment": "dev", "cidrBlock": "10.0.0.0/16"},
			minResources: 1,
		},
		{
			name: "vpc/dynamic-subnets",
			path: "examples/vpc/dynamic-subnets.cue",
			vars: map[string]any{
				"name": "platform", "environment": "prod", "region": "us-east-1", "vpcCidr": "10.0.0.0/16",
				"subnets": []any{
					map[string]any{"suffix": "public-a", "cidr": "10.0.1.0/24", "az": "us-east-1a", "public": true},
					map[string]any{"suffix": "public-b", "cidr": "10.0.2.0/24", "az": "us-east-1b", "public": true},
					map[string]any{"suffix": "private-a", "cidr": "10.0.10.0/24", "az": "us-east-1a"},
					map[string]any{"suffix": "private-b", "cidr": "10.0.11.0/24", "az": "us-east-1b"},
				},
			},
			minResources: 5,
		},
		{
			name:         "vpc/multi-az-vpc",
			path:         "examples/vpc/multi-az-vpc.cue",
			vars:         map[string]any{"name": "platform", "environment": "prod"},
			minResources: 10,
		},
		{
			name:         "vpc/vpc-peering",
			path:         "examples/vpc/vpc-peering.cue",
			vars:         map[string]any{"name": "analytics", "environment": "dev"},
			minResources: 7,
		},
	}
}

func TestEngine_Evaluate_AllExamples(t *testing.T) {
	absSchemaDir, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas", "aws"))
	require.NoError(t, err)
	eng := NewEngine(absSchemaDir)

	for _, tc := range exampleCases() {
		t.Run(tc.name, func(t *testing.T) {
			tmplPath, err := filepath.Abs(filepath.Join("..", "..", "..", tc.path))
			require.NoError(t, err)

			specs, err := eng.Evaluate(tmplPath, tc.vars)
			require.NoError(t, err, "%s should evaluate cleanly against schemas", tc.path)
			assert.GreaterOrEqual(t, len(specs), tc.minResources, "%s should produce at least %d resources", tc.path, tc.minResources)
		})
	}
}
