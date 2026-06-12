//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/pkg/types"
)

func TestPolicy_GlobalPolicyBlocksInvalidTemplate(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "policy")

	_, err := ingress.Service[command.AddPolicyRequest, restate.Void](
		env.ingress, "PraxisCommandService", "AddPolicy",
	).Request(t.Context(), command.AddPolicyRequest{
		Name:   "require-encryption",
		Scope:  types.PolicyScopeGlobal,
		Source: `resources: [_]: spec: encryption: enabled: true`,
	})
	require.NoError(t, err)

	resp, err := ingress.Service[command.ValidateTemplateRequest, command.ValidateTemplateResponse](
		env.ingress, "PraxisCommandService", "ValidateTemplate",
	).Request(t.Context(), command.ValidateTemplateRequest{
		Source: `
resources: {
	bucket: {
		apiVersion: "praxis.io/v1"
		kind: "S3Bucket"
		metadata: { name: "` + bucketName + `" }
		spec: {
			region: "us-east-1"
			encryption: enabled: false
		}
	}
}`,
		Mode: types.ValidateModeStatic,
	})
	require.NoError(t, err)
	assert.False(t, resp.Valid)
	require.NotEmpty(t, resp.Errors)
	assert.Equal(t, "PolicyViolation", resp.Errors[0].Kind)
	assert.Equal(t, "require-encryption", resp.Errors[0].Policy)
}

// TestPolicy_GlobalPolicyBlocksApply verifies enforcement, not just static
// validation: an Apply whose template violates a registered global policy must
// be rejected terminally and must not provision anything.
func TestPolicy_GlobalPolicyBlocksApply(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "policy-apply")
	deployKey := "test-policy-apply-" + bucketName

	_, err := ingress.Service[command.AddPolicyRequest, restate.Void](
		env.ingress, "PraxisCommandService", "AddPolicy",
	).Request(t.Context(), command.AddPolicyRequest{
		Name:   "require-encryption-apply",
		Scope:  types.PolicyScopeGlobal,
		Source: `resources: [_]: spec: encryption: enabled: true`,
	})
	require.NoError(t, err)

	_, err = ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template: `
resources: {
	bucket: {
		apiVersion: "praxis.io/v1"
		kind: "S3Bucket"
		metadata: { name: "` + bucketName + `" }
		spec: {
			region: "us-east-1"
			encryption: enabled: false
		}
	}
}`,
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.Error(t, err, "apply violating a global policy must be rejected")
	assert.Contains(t, err.Error(), "require-encryption-apply",
		"the rejection must name the violated policy")
	assert.Contains(t, err.Error(), "encryption.enabled")

	// Nothing may have been provisioned.
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
	require.Error(t, err, "the bucket must not exist after a policy-rejected apply")
}

func TestPolicy_TemplateScopedPolicyAppliesOnlyToTemplateRef(t *testing.T) {
	env := setupCoreStack(t)
	templateName := "policy-scoped"
	bucketName := uniqueName(t, "scoped")

	_, err := ingress.Service[command.RegisterTemplateRequest, command.RegisterTemplateResponse](
		env.ingress, "PraxisCommandService", "RegisterTemplate",
	).Request(t.Context(), command.RegisterTemplateRequest{
		Name: templateName,
		Source: `
resources: {
	bucket: {
		apiVersion: "praxis.io/v1"
		kind: "S3Bucket"
		metadata: { name: "` + bucketName + `" }
		spec: {
			region: "us-east-1"
			encryption: enabled: false
		}
	}
}`,
	})
	require.NoError(t, err)

	_, err = ingress.Service[command.AddPolicyRequest, restate.Void](
		env.ingress, "PraxisCommandService", "AddPolicy",
	).Request(t.Context(), command.AddPolicyRequest{
		Name:         "require-encryption",
		Scope:        types.PolicyScopeTemplate,
		TemplateName: templateName,
		Source:       `resources: [_]: spec: encryption: enabled: true`,
	})
	require.NoError(t, err)

	refResp, err := ingress.Service[command.ValidateTemplateRequest, command.ValidateTemplateResponse](
		env.ingress, "PraxisCommandService", "ValidateTemplate",
	).Request(t.Context(), command.ValidateTemplateRequest{
		TemplateRef: &types.TemplateRef{Name: templateName},
		Mode:        types.ValidateModeStatic,
	})
	require.NoError(t, err)
	assert.False(t, refResp.Valid)

	inlineResp, err := ingress.Service[command.ValidateTemplateRequest, command.ValidateTemplateResponse](
		env.ingress, "PraxisCommandService", "ValidateTemplate",
	).Request(t.Context(), command.ValidateTemplateRequest{
		Source: `
resources: {
	bucket: {
		apiVersion: "praxis.io/v1"
		kind: "S3Bucket"
		metadata: { name: "` + bucketName + `-inline" }
		spec: {
			region: "us-east-1"
			encryption: enabled: false
		}
	}
}`,
		Mode: types.ValidateModeStatic,
	})
	require.NoError(t, err)
	assert.True(t, inlineResp.Valid)
}
