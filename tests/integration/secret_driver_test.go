//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	smsdk "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/secret"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueSecretName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("praxis/test/%s-%d", name, time.Now().UnixNano()%100000)
}

func setupSecretDriver(t *testing.T) (*ingress.Client, *smsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	smClient := awsclient.NewSecretsManagerClient(awsCfg)
	driver := secret.NewSecretsManagerSecretDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, smClient
}

func baseSecretSpec(name string) secret.SecretsManagerSecretSpec {
	return secret.SecretsManagerSecretSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		Name:         name,
		SecretString: "s3cr3t-v1",
		Description:  "integration test secret",
		Tags:         map[string]string{"env": "test"},
	}
}

func provisionSecret(t *testing.T, client *ingress.Client, key string, spec secret.SecretsManagerSecretSpec) secret.SecretsManagerSecretOutputs {
	t.Helper()
	outputs, err := ingress.Object[secret.SecretsManagerSecretSpec, secret.SecretsManagerSecretOutputs](
		client, secret.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	return outputs
}

func secretKey(name string) string {
	return url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
}

func TestSecretProvision_CreatesSecret(t *testing.T) {
	client, smClient := setupSecretDriver(t)
	name := uniqueSecretName(t)
	key := secretKey(name)

	outputs := provisionSecret(t, client, key, baseSecretSpec(name))
	assert.Equal(t, name, outputs.Name)
	assert.NotEmpty(t, outputs.ARN)
	assert.NotEmpty(t, outputs.VersionID)

	got, err := smClient.GetSecretValue(context.Background(), &smsdk.GetSecretValueInput{SecretId: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t-v1", aws.ToString(got.SecretString))
}

func TestSecretProvision_Idempotent(t *testing.T) {
	client, _ := setupSecretDriver(t)
	name := uniqueSecretName(t)
	key := secretKey(name)
	spec := baseSecretSpec(name)

	out1 := provisionSecret(t, client, key, spec)
	out2 := provisionSecret(t, client, key, spec)

	assert.Equal(t, out1.ARN, out2.ARN)
	assert.Equal(t, out1.VersionID, out2.VersionID, "an in-sync re-provision must not create a new version")
}

func TestSecretProvision_UpdatesValue(t *testing.T) {
	client, smClient := setupSecretDriver(t)
	name := uniqueSecretName(t)
	key := secretKey(name)
	spec := baseSecretSpec(name)

	out1 := provisionSecret(t, client, key, spec)

	spec.SecretString = "s3cr3t-v2"
	out2 := provisionSecret(t, client, key, spec)
	assert.NotEqual(t, out1.VersionID, out2.VersionID, "PutSecretValue should mint a new version")

	got, err := smClient.GetSecretValue(context.Background(), &smsdk.GetSecretValueInput{SecretId: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t-v2", aws.ToString(got.SecretString))
}

func TestSecretImport_ExistingSecret(t *testing.T) {
	client, smClient := setupSecretDriver(t)
	name := uniqueSecretName(t)

	_, err := smClient.CreateSecret(context.Background(), &smsdk.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String("pre-existing"),
	})
	require.NoError(t, err)

	key := secretKey(name)
	outputs, err := ingress.Object[types.ImportRef, secret.SecretsManagerSecretOutputs](
		client, secret.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: name, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.Name)
	assert.NotEmpty(t, outputs.ARN)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, secret.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestSecretDelete_RemovesSecret(t *testing.T) {
	client, smClient := setupSecretDriver(t)
	name := uniqueSecretName(t)
	key := secretKey(name)

	provisionSecret(t, client, key, baseSecretSpec(name))

	_, err := ingress.Object[restate.Void, restate.Void](
		client, secret.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = smClient.DescribeSecret(context.Background(), &smsdk.DescribeSecretInput{SecretId: aws.String(name)})
	require.Error(t, err, "secret should be force-deleted immediately")
}

func TestSecretReconcile_DetectsValueDrift(t *testing.T) {
	client, smClient := setupSecretDriver(t)
	name := uniqueSecretName(t)
	key := secretKey(name)

	provisionSecret(t, client, key, baseSecretSpec(name))

	// Externally change the value to introduce drift.
	_, err := smClient.PutSecretValue(context.Background(), &smsdk.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String("drifted-value"),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, secret.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")

	got, err := smClient.GetSecretValue(context.Background(), &smsdk.GetSecretValueInput{SecretId: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t-v1", aws.ToString(got.SecretString), "drift correction should restore the desired value")
}

func TestSecretReconcile_DetectsTagDrift(t *testing.T) {
	client, smClient := setupSecretDriver(t)
	name := uniqueSecretName(t)
	key := secretKey(name)

	provisionSecret(t, client, key, baseSecretSpec(name))

	// Externally add a rogue tag to introduce drift.
	_, err := smClient.TagResource(context.Background(), &smsdk.TagResourceInput{
		SecretId: aws.String(name),
		Tags:     []smtypes.Tag{{Key: aws.String("rogue"), Value: aws.String("1")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, secret.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "tag drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct tag drift")

	got, err := smClient.DescribeSecret(context.Background(), &smsdk.DescribeSecretInput{SecretId: aws.String(name)})
	require.NoError(t, err)
	for _, tag := range got.Tags {
		assert.NotEqual(t, "rogue", aws.ToString(tag.Key), "rogue tag should have been removed")
	}
}

func TestSecretReconcile_ExternalDelete(t *testing.T) {
	client, smClient := setupSecretDriver(t)
	name := uniqueSecretName(t)
	key := secretKey(name)

	provisionSecret(t, client, key, baseSecretSpec(name))

	_, err := smClient.DeleteSecret(context.Background(), &smsdk.DeleteSecretInput{
		SecretId:                   aws.String(name),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, secret.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")
}
