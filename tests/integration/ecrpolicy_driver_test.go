//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	"github.com/shirvan/praxis/pkg/types"
)

const testLifecyclePolicyText = `{
  "rules": [
    {
      "rulePriority": 1,
      "description": "Expire untagged images older than 14 days",
      "selection": {
        "tagStatus": "untagged",
        "countType": "sinceImagePushed",
        "countUnit": "days",
        "countNumber": 14
      },
      "action": {
        "type": "expire"
      }
    }
  ]
}`

const testLifecyclePolicyTextDrifted = `{
  "rules": [
    {
      "rulePriority": 1,
      "description": "Expire untagged images older than 30 days",
      "selection": {
        "tagStatus": "untagged",
        "countType": "sinceImagePushed",
        "countUnit": "days",
        "countNumber": 30
      },
      "action": {
        "type": "expire"
      }
    }
  ]
}`

// createRepoForPolicy creates a prerequisite ECR repository directly.
func createRepoForPolicy(t *testing.T, ecrClient *ecrsdk.Client, name string) {
	t.Helper()
	_, err := ecrClient.CreateRepository(context.Background(), &ecrsdk.CreateRepositoryInput{
		RepositoryName: aws.String(name),
	})
	require.NoError(t, err)
}

func TestECRLifecyclePolicy_Provision(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRPolicyDriver(t)
	repoName := uniqueRepoName(t)
	createRepoForPolicy(t, ecrClient, repoName)
	key := fmt.Sprintf("us-east-1~%s", repoName)

	outputs, err := ingress.Object[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs](
		client, ecrpolicy.ServiceName, key, "Provision",
	).Request(t.Context(), ecrpolicy.ECRLifecyclePolicySpec{
		Account:             integrationAccountName,
		Region:              "us-east-1",
		RepositoryName:      repoName,
		LifecyclePolicyText: testLifecyclePolicyText,
	})
	require.NoError(t, err)
	assert.Equal(t, repoName, outputs.RepositoryName)

	// Verify lifecycle policy exists in Moto
	pol, err := ecrClient.GetLifecyclePolicy(context.Background(), &ecrsdk.GetLifecyclePolicyInput{
		RepositoryName: aws.String(repoName),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(pol.LifecyclePolicyText))
}

func TestECRLifecyclePolicy_Provision_Idempotent(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRPolicyDriver(t)
	repoName := uniqueRepoName(t)
	createRepoForPolicy(t, ecrClient, repoName)
	key := fmt.Sprintf("us-east-1~%s", repoName)
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		Account:             integrationAccountName,
		Region:              "us-east-1",
		RepositoryName:      repoName,
		LifecyclePolicyText: testLifecyclePolicyText,
	}

	out1, err := ingress.Object[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs](
		client, ecrpolicy.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs](
		client, ecrpolicy.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	assert.Equal(t, out1.RepositoryName, out2.RepositoryName)
}

func TestECRLifecyclePolicy_Import(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRPolicyDriver(t)
	repoName := uniqueRepoName(t)
	createRepoForPolicy(t, ecrClient, repoName)

	// Put a lifecycle policy directly
	_, err := ecrClient.PutLifecyclePolicy(context.Background(), &ecrsdk.PutLifecyclePolicyInput{
		RepositoryName:      aws.String(repoName),
		LifecyclePolicyText: aws.String(testLifecyclePolicyText),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", repoName)
	outputs, err := ingress.Object[types.ImportRef, ecrpolicy.ECRLifecyclePolicyOutputs](
		client, ecrpolicy.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: repoName,
		Mode:       types.ModeObserved,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, repoName, outputs.RepositoryName)

	// Verify status is observed after import
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ecrpolicy.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestECRLifecyclePolicy_Delete(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRPolicyDriver(t)
	repoName := uniqueRepoName(t)
	createRepoForPolicy(t, ecrClient, repoName)
	key := fmt.Sprintf("us-east-1~%s", repoName)

	_, err := ingress.Object[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs](
		client, ecrpolicy.ServiceName, key, "Provision",
	).Request(t.Context(), ecrpolicy.ECRLifecyclePolicySpec{
		Account:             integrationAccountName,
		Region:              "us-east-1",
		RepositoryName:      repoName,
		LifecyclePolicyText: testLifecyclePolicyText,
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, ecrpolicy.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify lifecycle policy is gone
	_, err = ecrClient.GetLifecyclePolicy(context.Background(), &ecrsdk.GetLifecyclePolicyInput{
		RepositoryName: aws.String(repoName),
	})
	require.Error(t, err, "lifecycle policy should be deleted")
}

func TestECRLifecyclePolicy_Reconcile_DetectsPolicyDrift(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRPolicyDriver(t)
	repoName := uniqueRepoName(t)
	createRepoForPolicy(t, ecrClient, repoName)
	key := fmt.Sprintf("us-east-1~%s", repoName)

	_, err := ingress.Object[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs](
		client, ecrpolicy.ServiceName, key, "Provision",
	).Request(t.Context(), ecrpolicy.ECRLifecyclePolicySpec{
		Account:             integrationAccountName,
		Region:              "us-east-1",
		RepositoryName:      repoName,
		LifecyclePolicyText: testLifecyclePolicyText,
	})
	require.NoError(t, err)

	// Introduce drift: change policy text directly via ECR API
	_, err = ecrClient.PutLifecyclePolicy(context.Background(), &ecrsdk.PutLifecyclePolicyInput{
		RepositoryName:      aws.String(repoName),
		LifecyclePolicyText: aws.String(testLifecyclePolicyTextDrifted),
	})
	require.NoError(t, err)

	// Trigger reconcile
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ecrpolicy.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")
}

func TestECRLifecyclePolicy_GetStatus(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRPolicyDriver(t)
	repoName := uniqueRepoName(t)
	createRepoForPolicy(t, ecrClient, repoName)
	key := fmt.Sprintf("us-east-1~%s", repoName)

	_, err := ingress.Object[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs](
		client, ecrpolicy.ServiceName, key, "Provision",
	).Request(t.Context(), ecrpolicy.ECRLifecyclePolicySpec{
		Account:             integrationAccountName,
		Region:              "us-east-1",
		RepositoryName:      repoName,
		LifecyclePolicyText: testLifecyclePolicyText,
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ecrpolicy.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
