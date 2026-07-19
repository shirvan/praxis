//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	route53sdk "github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/route53healthcheck"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func setupRoute53HealthCheckDriver(t *testing.T) (*ingress.Client, *route53sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	r53Client := awsclient.NewRoute53Client(awsCfg)
	ensureRoute53Enabled(t, r53Client)
	driver := route53healthcheck.NewGenericHealthCheckDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, r53Client
}

func TestRoute53HealthCheckProvision_CreatesHTTPCheck(t *testing.T) {
	client, r53Client := setupRoute53HealthCheckDriver(t)
	key := "integ-http-check"

	outputs, err := ingress.Object[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs](
		client, route53healthcheck.ServiceName, key, "Provision",
	).Request(t.Context(), route53healthcheck.HealthCheckSpec{
		Account:          integrationAccountName,
		Type:             "HTTP",
		FQDN:             "example.com",
		Port:             80,
		ResourcePath:     "/health",
		RequestInterval:  30,
		FailureThreshold: 3,
		Tags:             map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.HealthCheckId)

	desc, err := r53Client.GetHealthCheck(context.Background(), &route53sdk.GetHealthCheckInput{
		HealthCheckId: aws.String(outputs.HealthCheckId),
	})
	require.NoError(t, err)
	assert.Equal(t, "example.com", aws.ToString(desc.HealthCheck.HealthCheckConfig.FullyQualifiedDomainName))
}

func TestRoute53HealthCheckProvision_UpdatesPort(t *testing.T) {
	client, r53Client := setupRoute53HealthCheckDriver(t)
	key := "integ-update-port"
	spec := route53healthcheck.HealthCheckSpec{
		Account:          integrationAccountName,
		Type:             "HTTP",
		FQDN:             "example.com",
		Port:             80,
		ResourcePath:     "/health",
		RequestInterval:  30,
		FailureThreshold: 3,
		Tags:             map[string]string{},
	}

	outputs, err := ingress.Object[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs](
		client, route53healthcheck.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	spec.Port = 8080
	_, err = ingress.Object[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs](
		client, route53healthcheck.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	desc, err := r53Client.GetHealthCheck(context.Background(), &route53sdk.GetHealthCheckInput{
		HealthCheckId: aws.String(outputs.HealthCheckId),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(8080), aws.ToInt32(desc.HealthCheck.HealthCheckConfig.Port))
}

func TestRoute53HealthCheckDelete_RemovesCheck(t *testing.T) {
	client, r53Client := setupRoute53HealthCheckDriver(t)
	key := "integ-delete-check"

	outputs, err := ingress.Object[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs](
		client, route53healthcheck.ServiceName, key, "Provision",
	).Request(t.Context(), route53healthcheck.HealthCheckSpec{
		Account:          integrationAccountName,
		Type:             "HTTP",
		FQDN:             "example.com",
		Port:             80,
		ResourcePath:     "/",
		RequestInterval:  30,
		FailureThreshold: 3,
		Tags:             map[string]string{},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, route53healthcheck.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = r53Client.GetHealthCheck(context.Background(), &route53sdk.GetHealthCheckInput{
		HealthCheckId: aws.String(outputs.HealthCheckId),
	})
	require.Error(t, err)
}

func TestRoute53HealthCheckReconcile_DetectsFailureThresholdDrift(t *testing.T) {
	client, r53Client := setupRoute53HealthCheckDriver(t)
	key := "integ-failure-threshold-drift"

	outputs, err := ingress.Object[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs](
		client, route53healthcheck.ServiceName, key, "Provision",
	).Request(t.Context(), route53healthcheck.HealthCheckSpec{
		Account:          integrationAccountName,
		Type:             "HTTP",
		FQDN:             "example.com",
		Port:             80,
		ResourcePath:     "/health",
		RequestInterval:  30,
		FailureThreshold: 3,
		Tags:             map[string]string{},
	})
	require.NoError(t, err)
	require.NotEmpty(t, outputs.HealthCheckId)

	// Externally change the failure threshold to introduce drift.
	_, err = r53Client.UpdateHealthCheck(context.Background(), &route53sdk.UpdateHealthCheckInput{
		HealthCheckId:    aws.String(outputs.HealthCheckId),
		FailureThreshold: aws.Int32(5),
	})
	require.NoError(t, err)

	// Verify the external mutation landed before reconciling; otherwise there
	// is no observable drift and the scenario can only run against real AWS.
	desc, err := r53Client.GetHealthCheck(context.Background(), &route53sdk.GetHealthCheckInput{
		HealthCheckId: aws.String(outputs.HealthCheckId),
	})
	require.NoError(t, err)
	if aws.ToInt32(desc.HealthCheck.HealthCheckConfig.FailureThreshold) != 5 {
		t.Skip("Moto does not apply UpdateHealthCheck FailureThreshold")
	}

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, route53healthcheck.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")

	desc, err = r53Client.GetHealthCheck(context.Background(), &route53sdk.GetHealthCheckInput{
		HealthCheckId: aws.String(outputs.HealthCheckId),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), aws.ToInt32(desc.HealthCheck.HealthCheckConfig.FailureThreshold), "failure threshold should be restored to desired value")
}

func TestRoute53HealthCheckGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupRoute53HealthCheckDriver(t)
	key := "integ-status-check"

	_, err := ingress.Object[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs](
		client, route53healthcheck.ServiceName, key, "Provision",
	).Request(t.Context(), route53healthcheck.HealthCheckSpec{
		Account:          integrationAccountName,
		Type:             "HTTP",
		FQDN:             "example.com",
		Port:             80,
		ResourcePath:     "/",
		RequestInterval:  30,
		FailureThreshold: 3,
		Tags:             map[string]string{},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, route53healthcheck.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
