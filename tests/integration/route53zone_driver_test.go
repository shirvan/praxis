//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	route53sdk "github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/route53zone"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func setupRoute53ZoneDriver(t *testing.T) (*ingress.Client, *route53sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	r53Client := awsclient.NewRoute53Client(awsCfg)
	ensureRoute53Enabled(t, r53Client)
	driver := route53zone.NewHostedZoneDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), r53Client
}

func ensureRoute53Enabled(t *testing.T, client *route53sdk.Client) {
	t.Helper()
	_, err := client.ListHostedZones(context.Background(), &route53sdk.ListHostedZonesInput{MaxItems: aws.Int32(1)})
	if err != nil && strings.Contains(err.Error(), "Service 'route53' is not enabled") {
		t.Skip("Moto Route53 service is not enabled")
	}
	require.NoError(t, err)
}

func uniqueZoneName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-%d.example.com", time.Now().UnixNano()%1000000)
}

func TestRoute53ZoneProvision_CreatesZone(t *testing.T) {
	client, r53Client := setupRoute53ZoneDriver(t)
	zoneName := uniqueZoneName(t)

	outputs, err := ingress.Object[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs](
		client, route53zone.ServiceName, zoneName, "Provision",
	).Request(t.Context(), route53zone.HostedZoneSpec{
		Account: integrationAccountName,
		Comment: "integration test zone",
		Tags:    map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.HostedZoneId)
	assert.Equal(t, zoneName, outputs.Name)
	assert.False(t, outputs.IsPrivate)

	// Verify zone exists in Moto
	desc, err := r53Client.GetHostedZone(context.Background(), &route53sdk.GetHostedZoneInput{Id: aws.String(outputs.HostedZoneId)})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(desc.HostedZone.Name), zoneName)
}

func TestRoute53ZoneProvision_Idempotent(t *testing.T) {
	client, _ := setupRoute53ZoneDriver(t)
	zoneName := uniqueZoneName(t)
	spec := route53zone.HostedZoneSpec{
		Account: integrationAccountName,
		Comment: "idempotent test",
		Tags:    map[string]string{"env": "test"},
	}

	out1, err := ingress.Object[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs](
		client, route53zone.ServiceName, zoneName, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs](
		client, route53zone.ServiceName, zoneName, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.HostedZoneId, out2.HostedZoneId)
}

func TestRoute53ZoneImport_ExistingZone(t *testing.T) {
	client, r53Client := setupRoute53ZoneDriver(t)
	zoneName := uniqueZoneName(t)

	// Create zone directly
	created, err := r53Client.CreateHostedZone(context.Background(), &route53sdk.CreateHostedZoneInput{
		CallerReference: aws.String(fmt.Sprintf("import-test-%d", time.Now().UnixNano())),
		Name:            aws.String(zoneName),
	})
	require.NoError(t, err)
	zoneID := aws.ToString(created.HostedZone.Id)
	// Strip /hostedzone/ prefix
	zoneID = strings.TrimPrefix(zoneID, "/hostedzone/")

	outputs, err := ingress.Object[types.ImportRef, route53zone.HostedZoneOutputs](
		client, route53zone.ServiceName, zoneName, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: zoneID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, zoneID, outputs.HostedZoneId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, route53zone.ServiceName, zoneName, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestRoute53ZoneDelete_RemovesZone(t *testing.T) {
	client, r53Client := setupRoute53ZoneDriver(t)
	zoneName := uniqueZoneName(t)

	outputs, err := ingress.Object[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs](
		client, route53zone.ServiceName, zoneName, "Provision",
	).Request(t.Context(), route53zone.HostedZoneSpec{
		Account: integrationAccountName,
		Tags:    map[string]string{},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, route53zone.ServiceName, zoneName, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = r53Client.GetHostedZone(context.Background(), &route53sdk.GetHostedZoneInput{Id: aws.String(outputs.HostedZoneId)})
	require.Error(t, err)
}

func TestRoute53ZoneReconcile_DetectsCommentDrift(t *testing.T) {
	client, r53Client := setupRoute53ZoneDriver(t)
	zoneName := uniqueZoneName(t)

	outputs, err := ingress.Object[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs](
		client, route53zone.ServiceName, zoneName, "Provision",
	).Request(t.Context(), route53zone.HostedZoneSpec{
		Account: integrationAccountName,
		Comment: "original comment",
		Tags:    map[string]string{},
	})
	require.NoError(t, err)

	// Introduce drift: change comment directly
	_, err = r53Client.UpdateHostedZoneComment(context.Background(), &route53sdk.UpdateHostedZoneCommentInput{
		Id:      aws.String(outputs.HostedZoneId),
		Comment: aws.String("drifted comment"),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, route53zone.ServiceName, zoneName, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	// Verify comment was restored
	desc, err := r53Client.GetHostedZone(context.Background(), &route53sdk.GetHostedZoneInput{Id: aws.String(outputs.HostedZoneId)})
	require.NoError(t, err)
	assert.Equal(t, "original comment", aws.ToString(desc.HostedZone.Config.Comment))
}

func TestRoute53ZoneGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupRoute53ZoneDriver(t)
	zoneName := uniqueZoneName(t)

	_, err := ingress.Object[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs](
		client, route53zone.ServiceName, zoneName, "Provision",
	).Request(t.Context(), route53zone.HostedZoneSpec{
		Account: integrationAccountName,
		Tags:    map[string]string{},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, route53zone.ServiceName, zoneName, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
