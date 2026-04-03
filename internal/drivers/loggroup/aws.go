// Package loggroup – aws.go
//
// This file contains the AWS API abstraction layer for AWS CloudWatch Log Group.
// It defines the LogGroupAPI interface (used for testing with mocks)
// and the real implementation that calls Amazon CloudWatch Logs through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package loggroup

import (
	"context"
	"maps"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cloudwatchlogs "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// LogGroupAPI abstracts all Amazon CloudWatch Logs SDK operations needed
// to manage a AWS CloudWatch Log Group. The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type LogGroupAPI interface {
	CreateLogGroup(ctx context.Context, spec LogGroupSpec) error
	DescribeLogGroup(ctx context.Context, logGroupName string) (ObservedState, bool, error)
	PutRetentionPolicy(ctx context.Context, logGroupName string, retentionInDays int32) error
	DeleteRetentionPolicy(ctx context.Context, logGroupName string) error
	AssociateKmsKey(ctx context.Context, logGroupName, kmsKeyID string) error
	DisassociateKmsKey(ctx context.Context, logGroupName string) error
	DeleteLogGroup(ctx context.Context, logGroupName string) error
	TagResource(ctx context.Context, arn string, tags map[string]string) error
	UntagResource(ctx context.Context, arn string, tagKeys []string) error
	ListTagsForResource(ctx context.Context, arn string) (map[string]string, error)
}

type realLogGroupAPI struct {
	client  *cloudwatchlogs.Client
	limiter *ratelimit.Limiter
}

// NewLogGroupAPI constructs a production LogGroupAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewLogGroupAPI(client *cloudwatchlogs.Client) LogGroupAPI {
	return &realLogGroupAPI{
		client:  client,
		limiter: ratelimit.New("cloudwatch-log-group", 20, 10),
	}
}

// CreateLogGroup calls Amazon CloudWatch Logs to create a new AWS CloudWatch Log Group from the given spec.
func (r *realLogGroupAPI) CreateLogGroup(ctx context.Context, spec LogGroupSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(spec.LogGroupName),
		Tags:         managedTags(spec.Tags, spec.ManagedKey),
	}
	if spec.LogGroupClass != "" && spec.LogGroupClass != "STANDARD" {
		input.LogGroupClass = cwltypes.LogGroupClass(spec.LogGroupClass)
	}
	if spec.KmsKeyID != "" {
		input.KmsKeyId = aws.String(spec.KmsKeyID)
	}
	_, err := r.client.CreateLogGroup(ctx, input)
	return err
}

// DescribeLogGroup reads the current state of the AWS CloudWatch Log Group from Amazon CloudWatch Logs.
func (r *realLogGroupAPI) DescribeLogGroup(ctx context.Context, logGroupName string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	out, err := r.client.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroupName),
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	for i := range out.LogGroups {
		group := &out.LogGroups[i]
		if aws.ToString(group.LogGroupName) != logGroupName {
			continue
		}
		observed := ObservedState{
			ARN:           aws.ToString(group.Arn),
			LogGroupName:  aws.ToString(group.LogGroupName),
			LogGroupClass: string(group.LogGroupClass),
			KmsKeyID:      aws.ToString(group.KmsKeyId),
			CreationTime:  aws.ToInt64(group.CreationTime),
			StoredBytes:   aws.ToInt64(group.StoredBytes),
			Tags:          map[string]string{},
		}
		if group.RetentionInDays != nil {
			days := aws.ToInt32(group.RetentionInDays)
			if days > 0 {
				observed.RetentionInDays = aws.Int32(days)
			}
		}
		return observed, true, nil
	}
	return ObservedState{}, false, nil
}

func (r *realLogGroupAPI) PutRetentionPolicy(ctx context.Context, logGroupName string, retentionInDays int32) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(logGroupName),
		RetentionInDays: aws.Int32(retentionInDays),
	})
	return err
}

// DeleteRetentionPolicy removes the AWS CloudWatch Log Group from AWS via Amazon CloudWatch Logs.
func (r *realLogGroupAPI) DeleteRetentionPolicy(ctx context.Context, logGroupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRetentionPolicy(ctx, &cloudwatchlogs.DeleteRetentionPolicyInput{LogGroupName: aws.String(logGroupName)})
	return err
}

func (r *realLogGroupAPI) AssociateKmsKey(ctx context.Context, logGroupName, kmsKeyID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AssociateKmsKey(ctx, &cloudwatchlogs.AssociateKmsKeyInput{
		LogGroupName: aws.String(logGroupName),
		KmsKeyId:     aws.String(kmsKeyID),
	})
	return err
}

func (r *realLogGroupAPI) DisassociateKmsKey(ctx context.Context, logGroupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DisassociateKmsKey(ctx, &cloudwatchlogs.DisassociateKmsKeyInput{LogGroupName: aws.String(logGroupName)})
	return err
}

// DeleteLogGroup removes the AWS CloudWatch Log Group from AWS via Amazon CloudWatch Logs.
func (r *realLogGroupAPI) DeleteLogGroup(ctx context.Context, logGroupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
	return err
}

func (r *realLogGroupAPI) TagResource(ctx context.Context, arn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TagResource(ctx, &cloudwatchlogs.TagResourceInput{
		ResourceArn: aws.String(arn),
		Tags:        tags,
	})
	return err
}

func (r *realLogGroupAPI) UntagResource(ctx context.Context, arn string, tagKeys []string) error {
	if len(tagKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UntagResource(ctx, &cloudwatchlogs.UntagResourceInput{
		ResourceArn: aws.String(arn),
		TagKeys:     tagKeys,
	})
	return err
}

// ListTagsForResource enumerates AWS CloudWatch Log Group resources from Amazon CloudWatch Logs.
func (r *realLogGroupAPI) ListTagsForResource(ctx context.Context, arn string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string, len(out.Tags))
	maps.Copy(tags, out.Tags)
	return tags, nil
}

// IsNotFound returns true if the AWS error indicates the AWS CloudWatch Log Group does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException")
}

func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "ResourceAlreadyExistsException")
}

func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterException")
}

func IsConflict(err error) bool {
	return awserr.HasCode(err, "OperationAbortedException")
}

func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "LimitExceededException")
}

func IsServiceUnavailable(err error) bool {
	return awserr.HasCode(err, "ServiceUnavailableException")
}

func managedTags(tags map[string]string, managedKey string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, tags)
	if strings.TrimSpace(managedKey) != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

func tagDiff(desired, observed map[string]string, managedKey string) (map[string]string, []string) {
	want := managedTags(filterPraxisTags(desired), managedKey)
	have := managedTags(filterPraxisTags(observed), managedKey)
	toAdd := map[string]string{}
	for key, value := range want {
		if current, ok := have[key]; !ok || current != value {
			toAdd[key] = value
		}
	}
	var toRemove []string
	for key := range have {
		if _, ok := want[key]; !ok {
			toRemove = append(toRemove, key)
		}
	}
	sort.Strings(toRemove)
	return toAdd, toRemove
}
