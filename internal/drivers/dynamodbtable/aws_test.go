package dynamodbtable

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestKeySchemaAndAttributeDefinitions_HashOnly(t *testing.T) {
	spec := DynamoDBTableSpec{HashKey: "pk", HashKeyType: "S"}
	schema := keySchema(spec)
	assert.Len(t, schema, 1)
	assert.Equal(t, "pk", aws.ToString(schema[0].AttributeName))
	assert.Equal(t, ddbtypes.KeyTypeHash, schema[0].KeyType)

	defs := attributeDefinitions(spec)
	assert.Len(t, defs, 1)
	assert.Equal(t, ddbtypes.ScalarAttributeTypeS, defs[0].AttributeType)
}

func TestKeySchemaAndAttributeDefinitions_HashAndRange(t *testing.T) {
	spec := DynamoDBTableSpec{HashKey: "pk", HashKeyType: "S", RangeKey: "sk", RangeKeyType: "N"}
	schema := keySchema(spec)
	assert.Len(t, schema, 2)
	assert.Equal(t, ddbtypes.KeyTypeRange, schema[1].KeyType)

	defs := attributeDefinitions(spec)
	assert.Len(t, defs, 2)
	assert.Equal(t, ddbtypes.ScalarAttributeTypeN, defs[1].AttributeType)
}

func TestProvisionedThroughput_DefaultsToMinimum(t *testing.T) {
	pt := provisionedThroughput(DynamoDBTableSpec{})
	assert.Equal(t, int64(1), aws.ToInt64(pt.ReadCapacityUnits))
	assert.Equal(t, int64(1), aws.ToInt64(pt.WriteCapacityUnits))

	pt = provisionedThroughput(DynamoDBTableSpec{ReadCapacity: 10, WriteCapacity: 20})
	assert.Equal(t, int64(10), aws.ToInt64(pt.ReadCapacityUnits))
	assert.Equal(t, int64(20), aws.ToInt64(pt.WriteCapacityUnits))
}

func TestManagedTags(t *testing.T) {
	out := managedTags(map[string]string{"env": "prod"}, "us-east-1~prod")
	got := map[string]string{}
	for _, tag := range out {
		got[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, "prod", got["env"])
	assert.Equal(t, "us-east-1~prod", got["praxis:managed-key"])

	noKey := managedTags(map[string]string{"env": "prod"}, "")
	for _, tag := range noKey {
		assert.NotEqual(t, "praxis:managed-key", aws.ToString(tag.Key))
	}
}

func TestTableToObserved_PayPerRequest(t *testing.T) {
	obs := tableToObserved(&ddbtypes.TableDescription{
		TableArn:    aws.String("arn:aws:dynamodb:us-east-1:123456789012:table/prod"),
		TableName:   aws.String("prod"),
		TableStatus: ddbtypes.TableStatusActive,
		ItemCount:   aws.Int64(7),
		BillingModeSummary: &ddbtypes.BillingModeSummary{
			BillingMode: ddbtypes.BillingModePayPerRequest,
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: ddbtypes.KeyTypeRange},
		},
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: ddbtypes.ScalarAttributeTypeN},
		},
	}, map[string]string{"env": "prod"})

	assert.Equal(t, "prod", obs.Name)
	assert.Equal(t, "ACTIVE", obs.Status)
	assert.Equal(t, int64(7), obs.ItemCount)
	assert.Equal(t, BillingModePayPerRequest, obs.BillingMode)
	assert.Equal(t, "pk", obs.HashKey)
	assert.Equal(t, "S", obs.HashKeyType)
	assert.Equal(t, "sk", obs.RangeKey)
	assert.Equal(t, "N", obs.RangeKeyType)
	assert.Zero(t, obs.ReadCapacity, "PAY_PER_REQUEST omits provisioned throughput")
	assert.Equal(t, "prod", obs.Tags["env"])
}

func TestTableToObserved_Provisioned(t *testing.T) {
	obs := tableToObserved(&ddbtypes.TableDescription{
		TableName:   aws.String("prod"),
		TableStatus: ddbtypes.TableStatusActive,
		BillingModeSummary: &ddbtypes.BillingModeSummary{
			BillingMode: ddbtypes.BillingModeProvisioned,
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash},
		},
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		ProvisionedThroughput: &ddbtypes.ProvisionedThroughputDescription{
			ReadCapacityUnits:  aws.Int64(5),
			WriteCapacityUnits: aws.Int64(9),
		},
	}, nil)

	assert.Equal(t, BillingModeProvisioned, obs.BillingMode)
	assert.Equal(t, int64(5), obs.ReadCapacity)
	assert.Equal(t, int64(9), obs.WriteCapacity)
}

func TestObservedBillingMode_NoSummaryInfersFromThroughput(t *testing.T) {
	prov := observedBillingMode(&ddbtypes.TableDescription{
		ProvisionedThroughput: &ddbtypes.ProvisionedThroughputDescription{ReadCapacityUnits: aws.Int64(5)},
	})
	assert.Equal(t, BillingModeProvisioned, prov)

	ppr := observedBillingMode(&ddbtypes.TableDescription{})
	assert.Equal(t, BillingModePayPerRequest, ppr)
}

func TestHelpersDefaults(t *testing.T) {
	assert.Equal(t, BillingModePayPerRequest, billingModeOrDefault(""))
	assert.Equal(t, BillingModeProvisioned, billingModeOrDefault(BillingModeProvisioned))
	assert.True(t, isProvisioned(BillingModeProvisioned))
	assert.False(t, isProvisioned(""))
	assert.Equal(t, "S", keyTypeOrDefault(""))
	assert.Equal(t, "N", keyTypeOrDefault("N"))
	assert.Equal(t, int64(1), capacityOrDefault(0))
	assert.Equal(t, int64(3), capacityOrDefault(3))
}

func TestErrorClassifiers(t *testing.T) {
	notFound := &smithy.GenericAPIError{Code: "ResourceNotFoundException"}
	inUse := &smithy.GenericAPIError{Code: "ResourceInUseException"}
	invalid := &smithy.GenericAPIError{Code: "ValidationException"}
	limit := &smithy.GenericAPIError{Code: "LimitExceededException"}

	assert.True(t, IsNotFound(notFound))
	assert.False(t, IsNotFound(inUse))
	assert.True(t, IsConflict(inUse))
	assert.True(t, IsInvalidParam(invalid))
	assert.True(t, IsLimitExceeded(limit))

	// String fallback: Restate wraps errors and loses the typed code, so the
	// classifiers must still match on the wrapped message.
	wrapped := errors.New("operation error DynamoDB: DescribeTable, ResourceNotFoundException: Requested resource not found")
	assert.True(t, IsNotFound(wrapped), "classifier must survive error wrapping via string fallback")
}
