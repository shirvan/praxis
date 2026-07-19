//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ssmsdk "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/ssmparameter"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueParameterName(t *testing.T) string {
	t.Helper()
	random := make([]byte, 6)
	_, err := rand.Read(random)
	require.NoError(t, err)
	suffix := hex.EncodeToString(random)
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("/praxis/test/%s-%s", strings.Trim(name, "-"), suffix)
}

func setupSSMParameterDriver(t *testing.T) (*ingress.Client, *ssmsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	ssmClient := awsclient.NewSSMClient(awsCfg)
	driver := ssmparameter.NewGenericSSMParameterDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, ssmClient
}

func registerSSMParameterCleanup(t *testing.T, ssmClient *ssmsdk.Client, name string) {
	t.Helper()
	t.Cleanup(func() {
		_, err := ssmClient.DeleteParameter(context.Background(), &ssmsdk.DeleteParameterInput{Name: aws.String(name)})
		if err != nil && !isSSMParameterNotFound(err) {
			t.Errorf("delete SSM parameter %s during cleanup: %v", name, err)
		}
	})
}

func isSSMParameterNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ParameterNotFound")
}

func TestSSMParameterProvision_CreatesParameter(t *testing.T) {
	client, ssmClient := setupSSMParameterDriver(t)
	name := uniqueParameterName(t)
	registerSSMParameterCleanup(t, ssmClient, name)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	outputs, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), ssmparameter.SSMParameterSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		ParameterName: name,
		Value:         "db.internal",
		Description:   "integration test parameter",
		Tags:          map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.ParameterName)
	assert.Equal(t, "String", outputs.Type)
	assert.Equal(t, int64(1), outputs.Version)
	assert.NotEmpty(t, outputs.ARN)

	got, err := ssmClient.GetParameter(context.Background(), &ssmsdk.GetParameterInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "db.internal", aws.ToString(got.Parameter.Value))
}

func TestSSMParameterProvision_Idempotent(t *testing.T) {
	client, ssmClient := setupSSMParameterDriver(t)
	name := uniqueParameterName(t)
	registerSSMParameterCleanup(t, ssmClient, name)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	spec := ssmparameter.SSMParameterSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		ParameterName: name,
		Value:         "db.internal",
		Tags:          map[string]string{"env": "test"},
	}

	out1, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	assert.Equal(t, out1.ARN, out2.ARN)
	assert.Equal(t, out1.Version, out2.Version, "an in-sync re-provision must not bump the version")
}

func TestSSMParameterProvision_UpdatesValue(t *testing.T) {
	client, ssmClient := setupSSMParameterDriver(t)
	name := uniqueParameterName(t)
	registerSSMParameterCleanup(t, ssmClient, name)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	spec := ssmparameter.SSMParameterSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		ParameterName: name,
		Value:         "v1",
	}

	out1, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	spec.Value = "v2"
	out2, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Greater(t, out2.Version, out1.Version, "overwrite should bump the parameter version")

	got, err := ssmClient.GetParameter(context.Background(), &ssmsdk.GetParameterInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "v2", aws.ToString(got.Parameter.Value))
}

func TestSSMParameterImport_ExistingParameter(t *testing.T) {
	client, ssmClient := setupSSMParameterDriver(t)
	name := uniqueParameterName(t)
	registerSSMParameterCleanup(t, ssmClient, name)

	_, err := ssmClient.PutParameter(context.Background(), &ssmsdk.PutParameterInput{
		Name:  aws.String(name),
		Value: aws.String("pre-existing"),
		Type:  "String",
	})
	require.NoError(t, err)

	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	outputs, err := ingress.Object[types.ImportRef, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.ParameterName)
	assert.NotEmpty(t, outputs.ARN)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ssmparameter.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestSSMParameterDelete_RemovesParameter(t *testing.T) {
	client, ssmClient := setupSSMParameterDriver(t)
	name := uniqueParameterName(t)
	registerSSMParameterCleanup(t, ssmClient, name)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	_, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), ssmparameter.SSMParameterSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		ParameterName: name,
		Value:         "to-be-deleted",
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, ssmparameter.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = ssmClient.GetParameter(context.Background(), &ssmsdk.GetParameterInput{Name: aws.String(name)})
	require.Error(t, err, "parameter should be deleted")
}

func TestSSMParameterReconcile_DetectsValueDrift(t *testing.T) {
	client, ssmClient := setupSSMParameterDriver(t)
	name := uniqueParameterName(t)
	registerSSMParameterCleanup(t, ssmClient, name)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	_, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), ssmparameter.SSMParameterSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		ParameterName: name,
		Value:         "desired-value",
	})
	require.NoError(t, err)

	// Externally change the value to introduce drift
	_, err = ssmClient.PutParameter(context.Background(), &ssmsdk.PutParameterInput{
		Name:      aws.String(name),
		Value:     aws.String("drifted-value"),
		Type:      "String",
		Overwrite: aws.Bool(true),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ssmparameter.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")

	got, err := ssmClient.GetParameter(context.Background(), &ssmsdk.GetParameterInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "desired-value", aws.ToString(got.Parameter.Value), "drift correction should restore the desired value")
}

func TestSSMParameterProvision_SecureString(t *testing.T) {
	client, ssmClient := setupSSMParameterDriver(t)
	name := uniqueParameterName(t)
	registerSSMParameterCleanup(t, ssmClient, name)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	outputs, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), ssmparameter.SSMParameterSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		ParameterName: name,
		Type:          "SecureString",
		Value:         "super-secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "SecureString", outputs.Type)

	got, err := ssmClient.GetParameter(context.Background(), &ssmsdk.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "super-secret", aws.ToString(got.Parameter.Value))
}

func TestSSMParameterReconcile_ExternalDeleteRequiresReplacement(t *testing.T) {
	client, ssmClient := setupSSMParameterDriver(t)
	name := uniqueParameterName(t)
	registerSSMParameterCleanup(t, ssmClient, name)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	spec := ssmparameter.SSMParameterSpec{
		Account: integrationAccountName, Region: "us-east-1", ParameterName: name, Value: "desired-value",
	}

	_, err := ingress.Object[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs](
		client, ssmparameter.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	_, err = ssmClient.DeleteParameter(context.Background(), &ssmsdk.DeleteParameterInput{Name: aws.String(name)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ssmparameter.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	_, err = ssmClient.GetParameter(context.Background(), &ssmsdk.GetParameterInput{Name: aws.String(name)})
	require.Error(t, err, "Reconcile must report replacement without recreating the parameter")
}
