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
	ddbsdk "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/dynamodbtable"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// uniqueDynamoDBTableName derives a Moto-safe, collision-free table name from the
// test name plus a nanosecond suffix. Moto keeps no cross-test state guarantees,
// so every test provisions under its own name.
func uniqueDynamoDBTableName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 200 {
		name = name[:200]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupDynamoDBTableDriver(t *testing.T) (*ingress.Client, *ddbsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	ddbClient := awsclient.NewDynamoDBClient(awsCfg)
	driver := dynamodbtable.NewGenericDynamoDBTableDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, ddbClient
}

// baseDynamoDBTableSpec builds a spec Moto accepts: an on-demand table with a
// composite primary key.
func baseDynamoDBTableSpec(name string) dynamodbtable.DynamoDBTableSpec {
	return dynamodbtable.DynamoDBTableSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		Name:         name,
		BillingMode:  dynamodbtable.BillingModePayPerRequest,
		HashKey:      "pk",
		HashKeyType:  "S",
		RangeKey:     "sk",
		RangeKeyType: "N",
		Tags:         map[string]string{"env": "test"},
	}
}

func provisionDynamoDBTable(t *testing.T, client *ingress.Client, key string, spec dynamodbtable.DynamoDBTableSpec) dynamodbtable.DynamoDBTableOutputs {
	t.Helper()
	out, err := ingress.Object[dynamodbtable.DynamoDBTableSpec, dynamodbtable.DynamoDBTableOutputs](
		client, dynamodbtable.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	return out
}

func TestDynamoDBTableProvision_CreatesTable(t *testing.T) {
	client, ddbClient := setupDynamoDBTableDriver(t)
	name := uniqueDynamoDBTableName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	outputs := provisionDynamoDBTable(t, client, key, baseDynamoDBTableSpec(name))
	assert.Equal(t, name, outputs.Name)
	assert.Equal(t, "ACTIVE", outputs.Status)
	assert.NotEmpty(t, outputs.ARN)

	got, err := ddbClient.DescribeTable(context.Background(), &ddbsdk.DescribeTableInput{TableName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(got.Table.TableName))
	assert.Equal(t, ddbtypes.BillingModePayPerRequest, got.Table.BillingModeSummary.BillingMode)

	tags, err := ddbClient.ListTagsOfResource(context.Background(), &ddbsdk.ListTagsOfResourceInput{ResourceArn: got.Table.TableArn})
	require.NoError(t, err)
	tagMap := map[string]string{}
	for _, tg := range tags.Tags {
		tagMap[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Equal(t, "test", tagMap["env"])
	assert.Contains(t, tagMap, "praxis:managed-key", "provisioning should stamp the managed-key marker")
}

func TestDynamoDBTableProvision_Idempotent(t *testing.T) {
	client, _ := setupDynamoDBTableDriver(t)
	name := uniqueDynamoDBTableName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	spec := baseDynamoDBTableSpec(name)

	out1 := provisionDynamoDBTable(t, client, key, spec)
	out2 := provisionDynamoDBTable(t, client, key, spec)
	assert.Equal(t, out1.ARN, out2.ARN, "re-provisioning an in-sync table must be a no-op")
}

func TestDynamoDBTableImport_ExistingTable(t *testing.T) {
	client, ddbClient := setupDynamoDBTableDriver(t)
	name := uniqueDynamoDBTableName(t)

	_, err := ddbClient.CreateTable(context.Background(), &ddbsdk.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash},
		},
		Tags: []ddbtypes.Tag{{Key: aws.String("env"), Value: aws.String("preexisting")}},
	})
	require.NoError(t, err)

	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	outputs, err := ingress.Object[types.ImportRef, dynamodbtable.DynamoDBTableOutputs](
		client, dynamodbtable.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: name, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.Name)
	assert.NotEmpty(t, outputs.ARN)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, dynamodbtable.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode, "default import mode is Observed")
}

func TestDynamoDBTableDelete_RemovesTable(t *testing.T) {
	client, ddbClient := setupDynamoDBTableDriver(t)
	name := uniqueDynamoDBTableName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionDynamoDBTable(t, client, key, baseDynamoDBTableSpec(name))

	_, err := ingress.Object[restate.Void, restate.Void](
		client, dynamodbtable.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = ddbClient.DescribeTable(context.Background(), &ddbsdk.DescribeTableInput{TableName: aws.String(name)})
	require.Error(t, err, "table should be deleted from AWS")
}

func TestDynamoDBTableReconcile_DetectsAndCorrectsTagDrift(t *testing.T) {
	client, ddbClient := setupDynamoDBTableDriver(t)
	name := uniqueDynamoDBTableName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	out := provisionDynamoDBTable(t, client, key, baseDynamoDBTableSpec(name))

	// Externally mutate the tag set to introduce drift.
	_, err := ddbClient.TagResource(context.Background(), &ddbsdk.TagResourceInput{
		ResourceArn: aws.String(out.ARN),
		Tags:        []ddbtypes.Tag{{Key: aws.String("env"), Value: aws.String("hijacked")}, {Key: aws.String("rogue"), Value: aws.String("1")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, dynamodbtable.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "tag drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct tag drift")

	tags, err := ddbClient.ListTagsOfResource(context.Background(), &ddbsdk.ListTagsOfResourceInput{ResourceArn: aws.String(out.ARN)})
	require.NoError(t, err)
	tagMap := map[string]string{}
	for _, tg := range tags.Tags {
		tagMap[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Equal(t, "test", tagMap["env"], "reconcile should restore the desired tag value")
	assert.NotContains(t, tagMap, "rogue", "reconcile should remove externally-added tags")
}

func TestDynamoDBTableReconcile_DetectsAndCorrectsThroughputDrift(t *testing.T) {
	client, ddbClient := setupDynamoDBTableDriver(t)
	name := uniqueDynamoDBTableName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	spec := baseDynamoDBTableSpec(name)
	spec.BillingMode = dynamodbtable.BillingModeProvisioned
	spec.ReadCapacity = 5
	spec.WriteCapacity = 5
	provisionDynamoDBTable(t, client, key, spec)

	// Externally change provisioned throughput to introduce drift.
	_, err := ddbClient.UpdateTable(context.Background(), &ddbsdk.UpdateTableInput{
		TableName:             aws.String(name),
		ProvisionedThroughput: &ddbtypes.ProvisionedThroughput{ReadCapacityUnits: aws.Int64(50), WriteCapacityUnits: aws.Int64(60)},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, dynamodbtable.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "throughput drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct throughput drift")

	got, err := ddbClient.DescribeTable(context.Background(), &ddbsdk.DescribeTableInput{TableName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, int64(5), aws.ToInt64(got.Table.ProvisionedThroughput.ReadCapacityUnits), "read capacity should be restored to desired")
	assert.Equal(t, int64(5), aws.ToInt64(got.Table.ProvisionedThroughput.WriteCapacityUnits), "write capacity should be restored to desired")
}

func TestDynamoDBTableReconcile_DetectsExternalDelete(t *testing.T) {
	client, ddbClient := setupDynamoDBTableDriver(t)
	name := uniqueDynamoDBTableName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionDynamoDBTable(t, client, key, baseDynamoDBTableSpec(name))

	_, err := ddbClient.DeleteTable(context.Background(), &ddbsdk.DeleteTableInput{TableName: aws.String(name)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, dynamodbtable.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally", "external deletion should be surfaced as an error")

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, dynamodbtable.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusError, status.Status)
}
