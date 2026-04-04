//go:build integration

// Integration tests for the Templates V2 Deploy flow:
//
//	Register template -> Deploy with variables -> Verify provisioning
//
// These tests exercise the user-facing Deploy and PlanDeploy handlers
// end-to-end against the full in-process Praxis stack with Moto.
//
// Run with:
//
//	go test ./tests/integration/ -run TestDeploy -v -count=1 -tags=integration -timeout=10m
package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/pkg/types"
)

// ---------------------------------------------------------------------------
// CUE template helpers (with variables block)
// ---------------------------------------------------------------------------

// s3TemplateWithVariables returns a CUE template with a variables block and a
// single S3 bucket. The bucket name is derived from the "name" variable.
func s3TemplateWithVariables() string {
	return `
variables: {
	name:        string
	environment: "dev" | "staging" | "prod"
}

resources: {
	bucket: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: {
			name: "\(variables.name)-\(variables.environment)-assets"
		}
		spec: {
			region:     "us-east-1"
			versioning: true
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}
}
`
}

// multiResourceTemplateWithVariables returns a CUE template with variables
// and two resources (S3 + SG) where the bucket depends on the SG via expressions.
func multiResourceTemplateWithVariables() string {
	return `
variables: {
	name:        string
	environment: "dev" | "staging" | "prod"
	vpcId:       string
}

resources: {
	appSG: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: {
			name: "\(variables.name)-\(variables.environment)-sg"
		}
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-sg"
			description: "SG for \(variables.name)"
			vpcId:       variables.vpcId
			ingressRules: [{
				protocol:  "tcp"
				fromPort:  443
				toPort:    443
				cidrBlock: "0.0.0.0/0"
			}]
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}
	bucket: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: {
			name: "\(variables.name)-\(variables.environment)-assets"
		}
		spec: {
			region:     "us-east-1"
			versioning: true
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				app:        variables.name
				env:        variables.environment
				secGroupId: "${resources.appSG.outputs.groupId}"
			}
		}
	}
}
`
}

// ---------------------------------------------------------------------------
// Deploy Test Cases
// ---------------------------------------------------------------------------

// TestDeploy_HappyPath exercises the full register -> deploy -> verify flow:
//
//  1. Register a CUE template with a variables block
//  2. Deploy with valid variables via PraxisCommandService.Deploy
//  3. Poll until the deployment reaches Complete
//  4. Verify the S3 bucket was actually created in Moto
//  5. Verify the deployment state has correct resource outputs
func TestDeploy_HappyPath(t *testing.T) {
	env := setupCoreStack(t)
	name := uniqueName(t, "dep")
	templateName := "deploy-s3-" + name

	// --- Step 1: Register the template ---
	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:        templateName,
		Source:      s3TemplateWithVariables(),
		Description: "deploy integration test template",
	})
	require.NoError(t, err, "RegisterTemplate should succeed")

	// --- Step 2: Deploy with variables ---
	expectedBucket := fmt.Sprintf("%s-dev-assets", name)
	deployKey := "deploy-test-" + name

	resp, err := ingress.Service[command.DeployRequest, command.DeployResponse](
		env.ingress, "PraxisCommandService", "Deploy",
	).Request(t.Context(), command.DeployRequest{
		Template:      templateName,
		DeploymentKey: deployKey,
		Variables: map[string]any{
			"name":        name,
			"environment": "dev",
		},
		Account: integrationAccountName,
	})
	require.NoError(t, err, "Deploy should succeed")
	assert.Equal(t, deployKey, resp.DeploymentKey)
	assert.Equal(t, types.DeploymentPending, resp.Status)

	// --- Step 3: Poll until terminal ---
	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		60*time.Second,
	)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"deployment should reach Complete")

	// --- Step 4: Verify bucket exists in Moto ---
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &expectedBucket,
	})
	require.NoError(t, err, "S3 bucket %q should exist after deploy", expectedBucket)

	// --- Step 5: Verify resource outputs ---
	require.Contains(t, state.Resources, "bucket")
	assert.Equal(t, types.DeploymentResourceReady, state.Resources["bucket"].Status)
	require.Contains(t, state.Outputs, "bucket")
	assert.Equal(t, expectedBucket, state.Outputs["bucket"]["bucketName"])

	// Verify the bucket tags reflect the template variables.
	tagging, err := env.s3Client.GetBucketTagging(context.Background(), &s3sdk.GetBucketTaggingInput{
		Bucket: &expectedBucket,
	})
	require.NoError(t, err)
	tagMap := make(map[string]string)
	for _, tag := range tagging.TagSet {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, name, tagMap["app"])
	assert.Equal(t, "dev", tagMap["env"])
}

// TestDeploy_MissingRequiredVariable verifies that Deploy fails fast when
// a required variable is missing, before the CUE pipeline runs.
func TestDeploy_MissingRequiredVariable(t *testing.T) {
	env := setupCoreStack(t)
	name := uniqueName(t, "miss")
	templateName := "deploy-missing-" + name

	// Register
	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: s3TemplateWithVariables(),
	})
	require.NoError(t, err)

	// Deploy without the required "environment" variable
	_, err = ingress.Service[command.DeployRequest, command.DeployResponse](
		env.ingress, "PraxisCommandService", "Deploy",
	).Request(t.Context(), command.DeployRequest{
		Template: templateName,
		Variables: map[string]any{
			"name": name,
			// "environment" is missing
		},
		Account: integrationAccountName,
	})
	require.Error(t, err, "Deploy should fail when a required variable is missing")
	assert.Contains(t, strings.ToLower(err.Error()), "environment",
		"error should mention the missing variable name")
}

// TestDeploy_InvalidEnumValue verifies that Deploy rejects variables with
// values outside the allowed enum set.
func TestDeploy_InvalidEnumValue(t *testing.T) {
	env := setupCoreStack(t)
	name := uniqueName(t, "enum")
	templateName := "deploy-enum-" + name

	// Register
	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: s3TemplateWithVariables(),
	})
	require.NoError(t, err)

	// Deploy with an invalid enum value for "environment"
	_, err = ingress.Service[command.DeployRequest, command.DeployResponse](
		env.ingress, "PraxisCommandService", "Deploy",
	).Request(t.Context(), command.DeployRequest{
		Template: templateName,
		Variables: map[string]any{
			"name":        name,
			"environment": "invalid-env",
		},
		Account: integrationAccountName,
	})
	require.Error(t, err, "Deploy should fail for invalid enum value")
	assert.Contains(t, err.Error(), "invalid-env",
		"error should mention the invalid value")
}

// TestDeploy_TemplateNotFound verifies that Deploy fails when the template
// has not been registered.
func TestDeploy_TemplateNotFound(t *testing.T) {
	env := setupCoreStack(t)

	_, err := ingress.Service[command.DeployRequest, command.DeployResponse](
		env.ingress, "PraxisCommandService", "Deploy",
	).Request(t.Context(), command.DeployRequest{
		Template: "nonexistent-template",
		Variables: map[string]any{
			"name":        "test",
			"environment": "dev",
		},
		Account: integrationAccountName,
	})
	require.Error(t, err, "Deploy should fail for unregistered template")
}

// TestDeploy_PlanDeploy_DryRun verifies that PlanDeploy returns a plan
// without creating any resources.
func TestDeploy_PlanDeploy_DryRun(t *testing.T) {
	env := setupCoreStack(t)
	name := uniqueName(t, "plandep")
	templateName := "plan-deploy-" + name

	// Register
	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: s3TemplateWithVariables(),
	})
	require.NoError(t, err)

	// PlanDeploy (dry-run)
	resp, err := ingress.Service[command.PlanDeployRequest, command.PlanDeployResponse](
		env.ingress, "PraxisCommandService", "PlanDeploy",
	).Request(t.Context(), command.PlanDeployRequest{
		Template: templateName,
		Variables: map[string]any{
			"name":        name,
			"environment": "dev",
		},
		Account: integrationAccountName,
	})
	require.NoError(t, err, "PlanDeploy should succeed")
	require.NotNil(t, resp.Plan, "plan should not be nil")
	assert.Equal(t, 1, resp.Plan.Summary.ToCreate,
		"plan should show 1 resource to create")
	assert.Equal(t, 0, resp.Plan.Summary.ToUpdate)
	assert.Equal(t, 0, resp.Plan.Summary.ToDelete)
	assert.NotEmpty(t, resp.Rendered, "rendered output should be non-empty")

	// Verify nothing was actually provisioned
	expectedBucket := fmt.Sprintf("%s-dev-assets", name)
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &expectedBucket,
	})
	require.Error(t, err, "bucket should NOT exist after PlanDeploy (dry run)")
}

// TestDeploy_MultiResource_WithDependencies exercises Deploy with a
// multi-resource template that has cross-resource dependencies.
func TestDeploy_MultiResource_WithDependencies(t *testing.T) {
	env := setupCoreStack(t)
	name := uniqueName(t, "depmr")
	templateName := "deploy-multi-" + name
	vpcId := defaultVpcId(t, env.ec2Client)

	// Register the multi-resource template
	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: multiResourceTemplateWithVariables(),
	})
	require.NoError(t, err)

	// Deploy
	deployKey := "deploy-multi-" + name
	resp, err := ingress.Service[command.DeployRequest, command.DeployResponse](
		env.ingress, "PraxisCommandService", "Deploy",
	).Request(t.Context(), command.DeployRequest{
		Template:      templateName,
		DeploymentKey: deployKey,
		Variables: map[string]any{
			"name":        name,
			"environment": "dev",
			"vpcId":       vpcId,
		},
		Account: integrationAccountName,
	})
	require.NoError(t, err, "Deploy should succeed")
	assert.Equal(t, types.DeploymentPending, resp.Status)

	// Poll until terminal
	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		90*time.Second,
	)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"multi-resource deploy should reach Complete")

	// Verify both resources are ready
	require.Contains(t, state.Resources, "appSG")
	assert.Equal(t, types.DeploymentResourceReady, state.Resources["appSG"].Status)
	require.Contains(t, state.Resources, "bucket")
	assert.Equal(t, types.DeploymentResourceReady, state.Resources["bucket"].Status)

	// Verify hydration: bucket tags should contain the SG's actual groupId
	expectedBucket := fmt.Sprintf("%s-dev-assets", name)
	sgGroupId, ok := state.Outputs["appSG"]["groupId"].(string)
	require.True(t, ok && sgGroupId != "", "SG should have a groupId output")

	tagging, err := env.s3Client.GetBucketTagging(context.Background(), &s3sdk.GetBucketTaggingInput{
		Bucket: &expectedBucket,
	})
	require.NoError(t, err)
	tagMap := make(map[string]string)
	for _, tag := range tagging.TagSet {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, sgGroupId, tagMap["secGroupId"],
		"bucket's secGroupId tag should contain the SG's actual groupId (expression hydration)")
}

// TestDeploy_VariableSchemaExtraction verifies that registering a template
// with a variables block correctly extracts the variable schema, and that
// GetTemplate returns it.
func TestDeploy_VariableSchemaExtraction(t *testing.T) {
	env := setupCoreStack(t)
	name := uniqueName(t, "schema")
	templateName := "schema-test-" + name

	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: s3TemplateWithVariables(),
	})
	require.NoError(t, err)

	// Fetch the full template record and check the schema
	record, err := ingress.Service[string, types.TemplateRecord](
		env.ingress, "PraxisCommandService", "GetTemplate",
	).Request(t.Context(), templateName)
	require.NoError(t, err)

	require.NotNil(t, record.VariableSchema, "VariableSchema should be populated")
	require.Contains(t, record.VariableSchema, "name")
	require.Contains(t, record.VariableSchema, "environment")

	nameField := record.VariableSchema["name"]
	assert.Equal(t, "string", nameField.Type)
	assert.True(t, nameField.Required)

	envField := record.VariableSchema["environment"]
	assert.Equal(t, "string", envField.Type)
	assert.True(t, envField.Required)
	assert.ElementsMatch(t, []string{"dev", "staging", "prod"}, envField.Enum,
		"environment field should have enum values extracted from CUE disjunction")
}

// TestDeploy_ReRegisterTemplate verifies that updating a template works and
// subsequent deploys use the new source.
func TestDeploy_ReRegisterTemplate(t *testing.T) {
	env := setupCoreStack(t)
	name := uniqueName(t, "rereg")
	templateName := "rereg-" + name

	// Register v1
	regResp1, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: s3TemplateWithVariables(),
	})
	require.NoError(t, err)
	digest1 := regResp1.Digest

	// Re-register with same source --- digest should be unchanged
	regResp2, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: s3TemplateWithVariables(),
	})
	require.NoError(t, err)
	assert.Equal(t, digest1, regResp2.Digest,
		"re-registering identical source should produce the same digest")

	// Register v2 with modified source (add a tag)
	modifiedSource := strings.Replace(
		s3TemplateWithVariables(),
		`env: variables.environment`,
		"env: variables.environment\n\t\t\t\tversion: \"v2\"",
		1,
	)
	regResp3, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: modifiedSource,
	})
	require.NoError(t, err)
	assert.NotEqual(t, digest1, regResp3.Digest,
		"updated source should produce a different digest")

	// Deploy with v2 --- should succeed
	deployKey := "rereg-deploy-" + name
	_, err = ingress.Service[command.DeployRequest, command.DeployResponse](
		env.ingress, "PraxisCommandService", "Deploy",
	).Request(t.Context(), command.DeployRequest{
		Template:      templateName,
		DeploymentKey: deployKey,
		Variables: map[string]any{
			"name":        name,
			"environment": "dev",
		},
		Account: integrationAccountName,
	})
	require.NoError(t, err, "Deploy with re-registered template should succeed")

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		60*time.Second,
	)
	assert.Equal(t, types.DeploymentComplete, state.Status)
}

// TestDeploy_DeploymentKeyDerivation verifies that when no deploymentKey is
// provided, the Deploy handler derives one automatically.
func TestDeploy_DeploymentKeyDerivation(t *testing.T) {
	env := setupCoreStack(t)
	name := uniqueName(t, "autokey")
	templateName := "autokey-" + name

	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name:   templateName,
		Source: s3TemplateWithVariables(),
	})
	require.NoError(t, err)

	// Deploy without specifying a deployment key
	resp, err := ingress.Service[command.DeployRequest, command.DeployResponse](
		env.ingress, "PraxisCommandService", "Deploy",
	).Request(t.Context(), command.DeployRequest{
		Template: templateName,
		Variables: map[string]any{
			"name":        name,
			"environment": "staging",
		},
		Account: integrationAccountName,
	})
	require.NoError(t, err, "Deploy without explicit key should succeed")
	assert.NotEmpty(t, resp.DeploymentKey,
		"a deployment key should be derived automatically")

	// Wait for completion to confirm the derived key works
	state := pollDeploymentState(t, env.ingress, resp.DeploymentKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		60*time.Second,
	)
	assert.Equal(t, types.DeploymentComplete, state.Status)
}
