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
	route53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/praxiscloud/praxis/internal/drivers/route53record"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func setupRoute53RecordDriver(t *testing.T) (*ingress.Client, *route53sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	r53Client := awsclient.NewRoute53Client(awsCfg)
	ensureRoute53Enabled(t, r53Client)
	driver := route53record.NewDNSRecordDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), r53Client
}

func createTestHostedZone(t *testing.T, r53Client *route53sdk.Client) string {
	t.Helper()
	zoneName := fmt.Sprintf("record-test-%d.example.com", time.Now().UnixNano()%1000000)
	out, err := r53Client.CreateHostedZone(context.Background(), &route53sdk.CreateHostedZoneInput{
		CallerReference: aws.String(fmt.Sprintf("test-%d", time.Now().UnixNano())),
		Name:            aws.String(zoneName),
	})
	require.NoError(t, err)
	return strings.TrimPrefix(aws.ToString(out.HostedZone.Id), "/hostedzone/")
}

func TestRoute53RecordProvision_CreatesARecord(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("web-%d.record-test.example.com", time.Now().UnixNano()%100000)
	key := fmt.Sprintf("%s~%s~A", zoneID, recordName)

	outputs, err := ingress.Object[route53record.RecordSpec, route53record.RecordOutputs](
		client, route53record.ServiceName, key, "Provision",
	).Request(t.Context(), route53record.RecordSpec{
		Account:         integrationAccountName,
		HostedZoneId:    zoneID,
		Name:            recordName,
		Type:            "A",
		TTL:             300,
		ResourceRecords: []string{"1.2.3.4"},
	})
	require.NoError(t, err)
	assert.Equal(t, zoneID, outputs.HostedZoneId)
	assert.Equal(t, "A", outputs.Type)

	// Verify record exists
	listOut, err := r53Client.ListResourceRecordSets(context.Background(), &route53sdk.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
	})
	require.NoError(t, err)
	found := false
	for _, rr := range listOut.ResourceRecordSets {
		if strings.TrimSuffix(aws.ToString(rr.Name), ".") == recordName && rr.Type == route53types.RRTypeA {
			found = true
			require.Len(t, rr.ResourceRecords, 1)
			assert.Equal(t, "1.2.3.4", aws.ToString(rr.ResourceRecords[0].Value))
		}
	}
	assert.True(t, found, "A record should exist in hosted zone")
}

func TestRoute53RecordProvision_UpdatesTTL(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("ttl-%d.record-test.example.com", time.Now().UnixNano()%100000)
	key := fmt.Sprintf("%s~%s~A", zoneID, recordName)
	spec := route53record.RecordSpec{
		Account:         integrationAccountName,
		HostedZoneId:    zoneID,
		Name:            recordName,
		Type:            "A",
		TTL:             300,
		ResourceRecords: []string{"1.2.3.4"},
	}

	_, err := ingress.Object[route53record.RecordSpec, route53record.RecordOutputs](
		client, route53record.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	// Update TTL
	spec.TTL = 60
	_, err = ingress.Object[route53record.RecordSpec, route53record.RecordOutputs](
		client, route53record.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	// Verify TTL
	listOut, err := r53Client.ListResourceRecordSets(context.Background(), &route53sdk.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
	})
	require.NoError(t, err)
	for _, rr := range listOut.ResourceRecordSets {
		if strings.TrimSuffix(aws.ToString(rr.Name), ".") == recordName && rr.Type == route53types.RRTypeA {
			assert.Equal(t, int64(60), aws.ToInt64(rr.TTL))
		}
	}
}

func TestRoute53RecordDelete_RemovesRecord(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("del-%d.record-test.example.com", time.Now().UnixNano()%100000)
	key := fmt.Sprintf("%s~%s~A", zoneID, recordName)

	_, err := ingress.Object[route53record.RecordSpec, route53record.RecordOutputs](
		client, route53record.ServiceName, key, "Provision",
	).Request(t.Context(), route53record.RecordSpec{
		Account:         integrationAccountName,
		HostedZoneId:    zoneID,
		Name:            recordName,
		Type:            "A",
		TTL:             300,
		ResourceRecords: []string{"1.2.3.4"},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, route53record.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify record is gone
	listOut, err := r53Client.ListResourceRecordSets(context.Background(), &route53sdk.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
	})
	require.NoError(t, err)
	for _, rr := range listOut.ResourceRecordSets {
		if strings.TrimSuffix(aws.ToString(rr.Name), ".") == recordName && rr.Type == route53types.RRTypeA {
			t.Fatal("A record should have been deleted")
		}
	}
}

func TestRoute53RecordGetStatus_ReturnsReady(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("status-%d.record-test.example.com", time.Now().UnixNano()%100000)
	key := fmt.Sprintf("%s~%s~A", zoneID, recordName)

	_, err := ingress.Object[route53record.RecordSpec, route53record.RecordOutputs](
		client, route53record.ServiceName, key, "Provision",
	).Request(t.Context(), route53record.RecordSpec{
		Account:         integrationAccountName,
		HostedZoneId:    zoneID,
		Name:            recordName,
		Type:            "A",
		TTL:             300,
		ResourceRecords: []string{"1.2.3.4"},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, route53record.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
