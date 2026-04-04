//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
	"github.com/shirvan/praxis/pkg/types"
)

func TestECRRepository_Provision(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRRepoDriver(t)
	repoName := uniqueRepoName(t)
	key := fmt.Sprintf("us-east-1~%s", repoName)

	outputs, err := ingress.Object[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs](
		client, ecrrepo.ServiceName, key, "Provision",
	).Request(t.Context(), ecrrepo.ECRRepositorySpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		RepositoryName:     repoName,
		ImageTagMutability: "IMMUTABLE",
		ImageScanningConfiguration: &ecrrepo.ImageScanningConfiguration{
			ScanOnPush: true,
		},
		Tags: map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, repoName, outputs.RepositoryName)
	assert.NotEmpty(t, outputs.RepositoryArn)
	assert.NotEmpty(t, outputs.RepositoryUri)

	// Verify repository exists in Moto
	desc, err := ecrClient.DescribeRepositories(context.Background(), &ecrsdk.DescribeRepositoriesInput{
		RepositoryNames: []string{repoName},
	})
	require.NoError(t, err)
	require.Len(t, desc.Repositories, 1)
	assert.Equal(t, repoName, aws.ToString(desc.Repositories[0].RepositoryName))
}

func TestECRRepository_Provision_Idempotent(t *testing.T) {
	t.Parallel()
	client, _ := setupECRRepoDriver(t)
	repoName := uniqueRepoName(t)
	key := fmt.Sprintf("us-east-1~%s", repoName)
	spec := ecrrepo.ECRRepositorySpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		RepositoryName:     repoName,
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "test"},
	}

	out1, err := ingress.Object[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs](
		client, ecrrepo.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs](
		client, ecrrepo.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	assert.Equal(t, out1.RepositoryArn, out2.RepositoryArn)
	assert.Equal(t, out1.RepositoryName, out2.RepositoryName)
}

func TestECRRepository_Import(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRRepoDriver(t)
	repoName := uniqueRepoName(t)

	// Create repository directly in Moto
	_, err := ecrClient.CreateRepository(context.Background(), &ecrsdk.CreateRepositoryInput{
		RepositoryName: aws.String(repoName),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", repoName)
	outputs, err := ingress.Object[types.ImportRef, ecrrepo.ECRRepositoryOutputs](
		client, ecrrepo.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: repoName,
		Mode:       types.ModeObserved,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, repoName, outputs.RepositoryName)
	assert.NotEmpty(t, outputs.RepositoryArn)

	// Verify status is observed after import
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ecrrepo.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestECRRepository_Delete(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRRepoDriver(t)
	repoName := uniqueRepoName(t)
	key := fmt.Sprintf("us-east-1~%s", repoName)

	_, err := ingress.Object[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs](
		client, ecrrepo.ServiceName, key, "Provision",
	).Request(t.Context(), ecrrepo.ECRRepositorySpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		RepositoryName: repoName,
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, ecrrepo.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify repository is gone
	_, err = ecrClient.DescribeRepositories(context.Background(), &ecrsdk.DescribeRepositoriesInput{
		RepositoryNames: []string{repoName},
	})
	require.Error(t, err, "repository should be deleted from Moto")
}

func TestECRRepository_Reconcile_DetectsScanningDrift(t *testing.T) {
	t.Parallel()
	client, ecrClient := setupECRRepoDriver(t)
	repoName := uniqueRepoName(t)
	key := fmt.Sprintf("us-east-1~%s", repoName)

	// Provision with scanOnPush=true
	_, err := ingress.Object[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs](
		client, ecrrepo.ServiceName, key, "Provision",
	).Request(t.Context(), ecrrepo.ECRRepositorySpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		RepositoryName:     repoName,
		ImageTagMutability: "MUTABLE",
		ImageScanningConfiguration: &ecrrepo.ImageScanningConfiguration{
			ScanOnPush: true,
		},
	})
	require.NoError(t, err)

	// Introduce drift: disable scanning directly via ECR API
	_, err = ecrClient.PutImageScanningConfiguration(context.Background(), &ecrsdk.PutImageScanningConfigurationInput{
		RepositoryName: aws.String(repoName),
		ImageScanningConfiguration: &ecrtypes.ImageScanningConfiguration{
			ScanOnPush: false,
		},
	})
	require.NoError(t, err)

	// Trigger reconcile
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ecrrepo.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")
}

func TestECRRepository_GetStatus(t *testing.T) {
	t.Parallel()
	client, _ := setupECRRepoDriver(t)
	repoName := uniqueRepoName(t)
	key := fmt.Sprintf("us-east-1~%s", repoName)

	_, err := ingress.Object[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs](
		client, ecrrepo.ServiceName, key, "Provision",
	).Request(t.Context(), ecrrepo.ECRRepositorySpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		RepositoryName: repoName,
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ecrrepo.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
