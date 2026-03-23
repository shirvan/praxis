//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/lambdalayer"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueLayerName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupLambdaLayerDriver(t *testing.T) (*ingress.Client, *lambdasdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	lambdaClient := awsclient.NewLambdaClient(awsCfg)
	driver := lambdalayer.NewLambdaLayerDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), lambdaClient
}

func defaultLayerSpec(name string) lambdalayer.LambdaLayerSpec {
	return lambdalayer.LambdaLayerSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		LayerName:          name,
		Description:        "test layer",
		CompatibleRuntimes: []string{"python3.12"},
		Code:               lambdalayer.CodeSpec{ZipFile: minimalLambdaZip()},
	}
}

func TestLambdaLayerProvision_PublishesVersion(t *testing.T) {
	client, lambdaClient := setupLambdaLayerDriver(t)
	name := uniqueLayerName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs](
		client, lambdalayer.ServiceName, key, "Provision",
	).Request(t.Context(), defaultLayerSpec(name))
	require.NoError(t, err)
	assert.Equal(t, name, outputs.LayerName)
	assert.NotEmpty(t, outputs.LayerArn)
	assert.NotEmpty(t, outputs.LayerVersionArn)
	assert.Equal(t, int64(1), outputs.Version)

	// Verify layer exists in LocalStack
	desc, err := lambdaClient.GetLayerVersion(context.Background(), &lambdasdk.GetLayerVersionInput{
		LayerName:     aws.String(name),
		VersionNumber: aws.Int64(1),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(desc.LayerArn), name)
}

func TestLambdaLayerProvision_Idempotent(t *testing.T) {
	client, _ := setupLambdaLayerDriver(t)
	name := uniqueLayerName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	spec := defaultLayerSpec(name)

	out1, err := ingress.Object[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs](
		client, lambdalayer.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	// Re-provision with same spec should not publish a new version.
	out2, err := ingress.Object[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs](
		client, lambdalayer.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.Version, out2.Version)
	assert.Equal(t, out1.LayerVersionArn, out2.LayerVersionArn)
}

func TestLambdaLayerImport_ExistingLayer(t *testing.T) {
	client, lambdaClient := setupLambdaLayerDriver(t)
	name := uniqueLayerName(t)

	// Create layer directly in LocalStack
	_, err := lambdaClient.PublishLayerVersion(context.Background(), &lambdasdk.PublishLayerVersionInput{
		LayerName:          aws.String(name),
		Description:        aws.String("external layer"),
		CompatibleRuntimes: []lambdatypes.Runtime{lambdatypes.RuntimePython312},
		Content:            &lambdatypes.LayerVersionContentInput{ZipFile: minimalLambdaZipBytes()},
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, lambdalayer.LambdaLayerOutputs](
		client, lambdalayer.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
		Mode:       types.ModeManaged,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.LayerName)
	assert.NotEmpty(t, outputs.LayerArn)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, lambdalayer.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
}

func TestLambdaLayerDelete_RemovesLayer(t *testing.T) {
	client, lambdaClient := setupLambdaLayerDriver(t)
	name := uniqueLayerName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs](
		client, lambdalayer.ServiceName, key, "Provision",
	).Request(t.Context(), defaultLayerSpec(name))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, lambdalayer.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify layer version is gone
	_, err = lambdaClient.GetLayerVersion(context.Background(), &lambdasdk.GetLayerVersionInput{
		LayerName:     aws.String(name),
		VersionNumber: aws.Int64(outputs.Version),
	})
	require.Error(t, err, "layer version should be deleted from LocalStack")
}

func TestLambdaLayerGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupLambdaLayerDriver(t)
	name := uniqueLayerName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs](
		client, lambdalayer.ServiceName, key, "Provision",
	).Request(t.Context(), defaultLayerSpec(name))
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, lambdalayer.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
