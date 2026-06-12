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
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/route53record"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func setupRoute53RecordDriver(t *testing.T) (*ingress.Client, *route53sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	r53Client := awsclient.NewRoute53Client(awsCfg)
	ensureRoute53Enabled(t, r53Client)
	driver := route53record.NewDNSRecordDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, r53Client
}

func createTestHostedZone(t *testing.T, r53Client *route53sdk.Client) (string, string) {
	t.Helper()
	zoneName := fmt.Sprintf("record-test-%d.example.com", time.Now().UnixNano()%1000000)
	out, err := r53Client.CreateHostedZone(context.Background(), &route53sdk.CreateHostedZoneInput{
		CallerReference: aws.String(fmt.Sprintf("test-%d", time.Now().UnixNano())),
		Name:            aws.String(zoneName),
	})
	require.NoError(t, err)
	return strings.TrimPrefix(aws.ToString(out.HostedZone.Id), "/hostedzone/"), zoneName
}

func TestRoute53RecordProvision_CreatesARecord(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID, zoneName := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("web-%d.%s", time.Now().UnixNano()%100000, zoneName)
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
	zoneID, zoneName := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("ttl-%d.%s", time.Now().UnixNano()%100000, zoneName)
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
	found := false
	for _, rr := range listOut.ResourceRecordSets {
		if strings.TrimSuffix(aws.ToString(rr.Name), ".") == recordName && rr.Type == route53types.RRTypeA {
			found = true
			assert.Equal(t, int64(60), aws.ToInt64(rr.TTL))
		}
	}
	assert.True(t, found, "A record should exist in hosted zone")
}

func TestRoute53RecordImport_ExistingRecord(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID, zoneName := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("import-%d.%s", time.Now().UnixNano()%100000, zoneName)

	// Create the record directly via the raw Route53 client
	_, err := r53Client.ChangeResourceRecordSets(context.Background(), &route53sdk.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &route53types.ChangeBatch{
			Changes: []route53types.Change{{
				Action: route53types.ChangeActionCreate,
				ResourceRecordSet: &route53types.ResourceRecordSet{
					Name:            aws.String(recordName),
					Type:            route53types.RRTypeA,
					TTL:             aws.Int64(300),
					ResourceRecords: []route53types.ResourceRecord{{Value: aws.String("5.6.7.8")}},
				},
			}},
		},
	})
	require.NoError(t, err)

	key := fmt.Sprintf("%s~%s~A", zoneID, recordName)
	outputs, err := ingress.Object[types.ImportRef, route53record.RecordOutputs](
		client, route53record.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: fmt.Sprintf("%s~%s~A", zoneID, recordName),
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, zoneID, outputs.HostedZoneId)
	assert.Equal(t, recordName, outputs.FQDN)
	assert.Equal(t, "A", outputs.Type)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, route53record.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestRoute53RecordDelete_RemovesRecord(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID, zoneName := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("del-%d.%s", time.Now().UnixNano()%100000, zoneName)
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

func TestRoute53RecordReconcile_DetectsTTLDrift(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID, zoneName := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("drift-%d.%s", time.Now().UnixNano()%100000, zoneName)
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

	// Externally change the TTL to introduce drift.
	_, err = r53Client.ChangeResourceRecordSets(context.Background(), &route53sdk.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &route53types.ChangeBatch{
			Changes: []route53types.Change{{
				Action: route53types.ChangeActionUpsert,
				ResourceRecordSet: &route53types.ResourceRecordSet{
					Name:            aws.String(recordName),
					Type:            route53types.RRTypeA,
					TTL:             aws.Int64(600),
					ResourceRecords: []route53types.ResourceRecord{{Value: aws.String("1.2.3.4")}},
				},
			}},
		},
	})
	require.NoError(t, err)

	// Verify the external mutation landed before reconciling; otherwise there
	// is no observable drift and the scenario can only run against real AWS.
	if route53RecordTTL(t, r53Client, zoneID, recordName) != 600 {
		t.Skip("Moto does not apply ChangeResourceRecordSets UPSERT TTL")
	}

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, route53record.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")

	assert.Equal(t, int64(300), route53RecordTTL(t, r53Client, zoneID, recordName), "TTL should be restored to desired value")
}

// route53RecordTTL returns the TTL of the named A record in the zone, or 0 when absent.
func route53RecordTTL(t *testing.T, r53Client *route53sdk.Client, zoneID, recordName string) int64 {
	t.Helper()
	listOut, err := r53Client.ListResourceRecordSets(context.Background(), &route53sdk.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
	})
	require.NoError(t, err)
	for _, rr := range listOut.ResourceRecordSets {
		if strings.TrimSuffix(aws.ToString(rr.Name), ".") == recordName && rr.Type == route53types.RRTypeA {
			return aws.ToInt64(rr.TTL)
		}
	}
	return 0
}

func TestRoute53RecordImport_FindsRecordBeyondFirstPage(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID, zoneName := createTestHostedZone(t, r53Client)

	// Fill the zone with 120 A records that all sort before the target so the
	// target lands beyond the driver's 100-item ListResourceRecordSets page.
	const fillerCount = 120
	const batchSize = 50
	for start := 0; start < fillerCount; start += batchSize {
		end := start + batchSize
		if end > fillerCount {
			end = fillerCount
		}
		changes := make([]route53types.Change, 0, end-start)
		for i := start; i < end; i++ {
			changes = append(changes, route53types.Change{
				Action: route53types.ChangeActionCreate,
				ResourceRecordSet: &route53types.ResourceRecordSet{
					Name:            aws.String(fmt.Sprintf("a-%03d.%s", i, zoneName)),
					Type:            route53types.RRTypeA,
					TTL:             aws.Int64(300),
					ResourceRecords: []route53types.ResourceRecord{{Value: aws.String("10.0.0.1")}},
				},
			})
		}
		_, err := r53Client.ChangeResourceRecordSets(context.Background(), &route53sdk.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zoneID),
			ChangeBatch:  &route53types.ChangeBatch{Changes: changes},
		})
		require.NoError(t, err)
	}

	// Create the target record whose name sorts after every filler record.
	targetName := fmt.Sprintf("zzz-last.%s", zoneName)
	_, err := r53Client.ChangeResourceRecordSets(context.Background(), &route53sdk.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &route53types.ChangeBatch{
			Changes: []route53types.Change{{
				Action: route53types.ChangeActionCreate,
				ResourceRecordSet: &route53types.ResourceRecordSet{
					Name:            aws.String(targetName),
					Type:            route53types.RRTypeA,
					TTL:             aws.Int64(300),
					ResourceRecords: []route53types.ResourceRecord{{Value: aws.String("9.9.9.9")}},
				},
			}},
		},
	})
	require.NoError(t, err)

	// Import observes via the driver's paginated DescribeRecord; it must find
	// the record even though it is not on the first 100-item page.
	key := fmt.Sprintf("%s~%s~A", zoneID, targetName)
	outputs, err := ingress.Object[types.ImportRef, route53record.RecordOutputs](
		client, route53record.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: fmt.Sprintf("%s~%s~A", zoneID, targetName),
		Account:    integrationAccountName,
	})
	require.NoError(t, err, "driver should find the record beyond the first ListResourceRecordSets page")
	assert.Equal(t, zoneID, outputs.HostedZoneId)
	assert.Equal(t, targetName, outputs.FQDN)
	assert.Equal(t, "A", outputs.Type)
}

func TestRoute53RecordGetStatus_ReturnsReady(t *testing.T) {
	client, r53Client := setupRoute53RecordDriver(t)
	zoneID, zoneName := createTestHostedZone(t, r53Client)
	recordName := fmt.Sprintf("status-%d.%s", time.Now().UnixNano()%100000, zoneName)
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
