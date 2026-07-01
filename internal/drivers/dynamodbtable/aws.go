// Package dynamodbtable – aws.go
//
// This file contains the AWS API abstraction layer for DynamoDB tables.
// It defines the DynamoDBTableAPI interface (used for testing with mocks)
// and the real implementation that calls AWS DynamoDB through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package dynamodbtable

import (
	"context"
	"maps"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// DynamoDBTableAPI abstracts all AWS DynamoDB SDK operations needed to manage a
// table. The real implementation calls AWS; tests supply a mock to verify driver
// logic without network calls.
type DynamoDBTableAPI interface {
	CreateTable(ctx context.Context, spec DynamoDBTableSpec) (ObservedState, error)
	DescribeTable(ctx context.Context, name string) (ObservedState, bool, error)
	UpdateTable(ctx context.Context, spec DynamoDBTableSpec) error
	DeleteTable(ctx context.Context, name string) error
	TagResource(ctx context.Context, arn string, tags map[string]string) error
	UntagResource(ctx context.Context, arn string, tagKeys []string) error
}

type realDynamoDBTableAPI struct {
	client  *dynamodb.Client
	limiter *ratelimit.Limiter
}

// NewDynamoDBTableAPI constructs a production DynamoDBTableAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewDynamoDBTableAPI(client *dynamodb.Client) DynamoDBTableAPI {
	return &realDynamoDBTableAPI{
		client:  client,
		limiter: ratelimit.Shared("dynamodb-table", 10, 5),
	}
}

// CreateTable provisions a new table from the given spec and returns the observed
// state read back from the create response.
func (r *realDynamoDBTableAPI) CreateTable(ctx context.Context, spec DynamoDBTableSpec) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	input := &dynamodb.CreateTableInput{
		TableName:            aws.String(spec.Name),
		AttributeDefinitions: attributeDefinitions(spec),
		KeySchema:            keySchema(spec),
		BillingMode:          ddbtypes.BillingMode(billingModeOrDefault(spec.BillingMode)),
		Tags:                 managedTags(spec.Tags, spec.ManagedKey),
	}
	if isProvisioned(spec.BillingMode) {
		input.ProvisionedThroughput = provisionedThroughput(spec)
	}
	out, err := r.client.CreateTable(ctx, input)
	if err != nil {
		return ObservedState{}, err
	}
	return tableToObserved(out.TableDescription, nil), nil
}

// DescribeTable reads the current state of the table from AWS. The second return
// value is false when the table does not exist. Tags are read via a follow-up
// ListTagsOfResource call because DescribeTable does not return them.
func (r *realDynamoDBTableAPI) DescribeTable(ctx context.Context, name string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	out, err := r.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	tags, err := r.listTags(ctx, aws.ToString(out.Table.TableArn))
	if err != nil {
		return ObservedState{}, false, err
	}
	return tableToObserved(out.Table, tags), true, nil
}

// listTags reads all tags on the table identified by ARN.
func (r *realDynamoDBTableAPI) listTags(ctx context.Context, arn string) (map[string]string, error) {
	if arn == "" {
		return map[string]string{}, nil
	}
	tags := map[string]string{}
	var next *string
	for {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		out, err := r.client.ListTagsOfResource(ctx, &dynamodb.ListTagsOfResourceInput{
			ResourceArn: aws.String(arn),
			NextToken:   next,
		})
		if err != nil {
			return nil, err
		}
		for _, t := range out.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		if out.NextToken == nil {
			break
		}
		next = out.NextToken
	}
	return tags, nil
}

// UpdateTable converges the mutable billing mode and provisioned throughput for
// an existing table.
func (r *realDynamoDBTableAPI) UpdateTable(ctx context.Context, spec DynamoDBTableSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &dynamodb.UpdateTableInput{
		TableName:   aws.String(spec.Name),
		BillingMode: ddbtypes.BillingMode(billingModeOrDefault(spec.BillingMode)),
	}
	if isProvisioned(spec.BillingMode) {
		input.ProvisionedThroughput = provisionedThroughput(spec)
	}
	_, err := r.client.UpdateTable(ctx, input)
	return err
}

// DeleteTable removes the table from AWS.
func (r *realDynamoDBTableAPI) DeleteTable(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(name)})
	return err
}

// TagResource attaches or overwrites tags on the table identified by ARN.
func (r *realDynamoDBTableAPI) TagResource(ctx context.Context, arn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TagResource(ctx, &dynamodb.TagResourceInput{
		ResourceArn: aws.String(arn),
		Tags:        tagList(tags),
	})
	return err
}

// UntagResource removes the given tag keys from the table identified by ARN.
func (r *realDynamoDBTableAPI) UntagResource(ctx context.Context, arn string, tagKeys []string) error {
	if len(tagKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UntagResource(ctx, &dynamodb.UntagResourceInput{
		ResourceArn: aws.String(arn),
		TagKeys:     tagKeys,
	})
	return err
}

// attributeDefinitions builds the AttributeDefinitions for the primary key from
// the hash key and (optional) range key.
func attributeDefinitions(spec DynamoDBTableSpec) []ddbtypes.AttributeDefinition {
	defs := []ddbtypes.AttributeDefinition{{
		AttributeName: aws.String(spec.HashKey),
		AttributeType: ddbtypes.ScalarAttributeType(keyTypeOrDefault(spec.HashKeyType)),
	}}
	if spec.RangeKey != "" {
		defs = append(defs, ddbtypes.AttributeDefinition{
			AttributeName: aws.String(spec.RangeKey),
			AttributeType: ddbtypes.ScalarAttributeType(keyTypeOrDefault(spec.RangeKeyType)),
		})
	}
	return defs
}

// keySchema builds the KeySchema for the primary key from the hash key and
// (optional) range key.
func keySchema(spec DynamoDBTableSpec) []ddbtypes.KeySchemaElement {
	schema := []ddbtypes.KeySchemaElement{{
		AttributeName: aws.String(spec.HashKey),
		KeyType:       ddbtypes.KeyTypeHash,
	}}
	if spec.RangeKey != "" {
		schema = append(schema, ddbtypes.KeySchemaElement{
			AttributeName: aws.String(spec.RangeKey),
			KeyType:       ddbtypes.KeyTypeRange,
		})
	}
	return schema
}

// provisionedThroughput builds the throughput request for PROVISIONED tables,
// defaulting unset capacities to 1 (the AWS minimum) so the request is valid.
func provisionedThroughput(spec DynamoDBTableSpec) *ddbtypes.ProvisionedThroughput {
	return &ddbtypes.ProvisionedThroughput{
		ReadCapacityUnits:  aws.Int64(capacityOrDefault(spec.ReadCapacity)),
		WriteCapacityUnits: aws.Int64(capacityOrDefault(spec.WriteCapacity)),
	}
}

// tableToObserved projects a DynamoDB SDK TableDescription and its tag set into
// the driver's ObservedState.
func tableToObserved(t *ddbtypes.TableDescription, tags map[string]string) ObservedState {
	obs := ObservedState{Tags: map[string]string{}}
	if t == nil {
		if tags != nil {
			obs.Tags = tags
		}
		return obs
	}
	obs.ARN = aws.ToString(t.TableArn)
	obs.Name = aws.ToString(t.TableName)
	obs.Status = string(t.TableStatus)
	obs.ItemCount = aws.ToInt64(t.ItemCount)
	obs.BillingMode = observedBillingMode(t)
	for _, el := range t.KeySchema {
		switch el.KeyType {
		case ddbtypes.KeyTypeHash:
			obs.HashKey = aws.ToString(el.AttributeName)
		case ddbtypes.KeyTypeRange:
			obs.RangeKey = aws.ToString(el.AttributeName)
		}
	}
	for _, def := range t.AttributeDefinitions {
		name := aws.ToString(def.AttributeName)
		if name == obs.HashKey {
			obs.HashKeyType = string(def.AttributeType)
		}
		if name == obs.RangeKey {
			obs.RangeKeyType = string(def.AttributeType)
		}
	}
	if obs.BillingMode == BillingModeProvisioned && t.ProvisionedThroughput != nil {
		obs.ReadCapacity = aws.ToInt64(t.ProvisionedThroughput.ReadCapacityUnits)
		obs.WriteCapacity = aws.ToInt64(t.ProvisionedThroughput.WriteCapacityUnits)
	}
	if tags != nil {
		obs.Tags = tags
	}
	return obs
}

// observedBillingMode reads the effective billing mode from a table description,
// defaulting to PAY_PER_REQUEST only when the summary is entirely absent (older
// AWS responses omit BillingModeSummary for on-demand tables).
func observedBillingMode(t *ddbtypes.TableDescription) string {
	if t.BillingModeSummary != nil && t.BillingModeSummary.BillingMode != "" {
		return string(t.BillingModeSummary.BillingMode)
	}
	// No summary: a table with provisioned throughput reported is PROVISIONED,
	// otherwise treat it as on-demand.
	if t.ProvisionedThroughput != nil && aws.ToInt64(t.ProvisionedThroughput.ReadCapacityUnits) > 0 {
		return BillingModeProvisioned
	}
	return BillingModePayPerRequest
}

// tagList converts a tag map into the SDK's tag slice form.
func tagList(tags map[string]string) []ddbtypes.Tag {
	out := make([]ddbtypes.Tag, 0, len(tags))
	for k, v := range tags {
		out = append(out, ddbtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return out
}

// managedTags merges the user's tags with the praxis managed-key marker and
// returns them in the SDK's tag slice form.
func managedTags(tags map[string]string, managedKey string) []ddbtypes.Tag {
	merged := make(map[string]string, len(tags)+1)
	maps.Copy(merged, tags)
	if managedKey != "" {
		merged["praxis:managed-key"] = managedKey
	}
	return tagList(merged)
}

// billingModeOrDefault returns the configured billing mode, defaulting to
// PAY_PER_REQUEST when unset.
func billingModeOrDefault(mode string) string {
	if mode == "" {
		return BillingModePayPerRequest
	}
	return mode
}

// isProvisioned reports whether the given billing mode is PROVISIONED.
func isProvisioned(mode string) bool {
	return billingModeOrDefault(mode) == BillingModeProvisioned
}

// keyTypeOrDefault returns the configured scalar key type, defaulting to "S".
func keyTypeOrDefault(t string) string {
	if t == "" {
		return "S"
	}
	return t
}

// capacityOrDefault returns the configured capacity, defaulting to 1 (the AWS
// minimum) for non-positive values.
func capacityOrDefault(c int64) int64 {
	if c < 1 {
		return 1
	}
	return c
}

// IsNotFound reports whether the AWS error indicates the table does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException")
}

// IsConflict reports whether the AWS error indicates the table already exists or
// is otherwise in use (e.g. concurrent create/update/delete).
func IsConflict(err error) bool {
	return awserr.HasCode(err, "ResourceInUseException")
}

// IsInvalidParam reports whether the AWS error indicates an invalid request that
// a retry cannot fix.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "ValidationException", "InvalidParameterException")
}

// IsLimitExceeded reports whether the AWS error indicates a service quota was hit.
func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "LimitExceededException")
}
