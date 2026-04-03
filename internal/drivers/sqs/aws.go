// Package sqs – aws.go
//
// This file contains the AWS API abstraction layer for AWS SQS Queue.
// It defines the SQSQueueAPI interface (used for testing with mocks)
// and the real implementation that calls Amazon Simple Queue Service (SQS) through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package sqs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// QueueAPI abstracts all Amazon Simple Queue Service (SQS) SDK operations needed
// to manage a AWS SQS Queue. The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type QueueAPI interface {
	CreateQueue(ctx context.Context, spec SQSQueueSpec) (string, error)
	GetQueueUrl(ctx context.Context, queueName string) (string, error)
	GetQueueAttributes(ctx context.Context, queueURL string) (ObservedState, error)
	SetQueueAttributes(ctx context.Context, queueURL string, attrs map[string]string) error
	DeleteQueue(ctx context.Context, queueURL string) error
	UpdateTags(ctx context.Context, queueURL string, tags map[string]string) error
	GetTags(ctx context.Context, queueURL string) (map[string]string, error)
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

type realQueueAPI struct {
	client  *sqssdk.Client
	limiter *ratelimit.Limiter
}

// NewQueueAPI constructs a production SQSQueueAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewQueueAPI(client *sqssdk.Client) QueueAPI {
	return &realQueueAPI{client: client, limiter: ratelimit.New("sqs", 50, 20)}
}

// CreateQueue calls Amazon Simple Queue Service (SQS) to create a new AWS SQS Queue from the given spec.
func (r *realQueueAPI) CreateQueue(ctx context.Context, spec SQSQueueSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	attrs := map[string]string{
		"VisibilityTimeout":             strconv.Itoa(spec.VisibilityTimeout),
		"MessageRetentionPeriod":        strconv.Itoa(spec.MessageRetentionPeriod),
		"MaximumMessageSize":            strconv.Itoa(spec.MaximumMessageSize),
		"DelaySeconds":                  strconv.Itoa(spec.DelaySeconds),
		"ReceiveMessageWaitTimeSeconds": strconv.Itoa(spec.ReceiveMessageWaitTimeSeconds),
	}

	if spec.KmsMasterKeyId != "" {
		attrs["KmsMasterKeyId"] = spec.KmsMasterKeyId
		attrs["KmsDataKeyReusePeriodSeconds"] = strconv.Itoa(spec.KmsDataKeyReusePeriodSeconds)
		attrs["SqsManagedSseEnabled"] = "false"
	} else {
		attrs["SqsManagedSseEnabled"] = strconv.FormatBool(spec.SqsManagedSseEnabled)
	}

	if spec.RedrivePolicy != nil {
		payload, err := json.Marshal(spec.RedrivePolicy)
		if err != nil {
			return "", err
		}
		attrs["RedrivePolicy"] = string(payload)
	}

	if spec.FifoQueue {
		attrs["FifoQueue"] = "true"
		attrs["ContentBasedDeduplication"] = strconv.FormatBool(spec.ContentBasedDeduplication)
		if spec.DeduplicationScope != "" {
			attrs["DeduplicationScope"] = spec.DeduplicationScope
		}
		if spec.FifoThroughputLimit != "" {
			attrs["FifoThroughputLimit"] = spec.FifoThroughputLimit
		}
	}

	input := &sqssdk.CreateQueueInput{QueueName: aws.String(spec.QueueName), Attributes: attrs}
	if len(spec.Tags) > 0 {
		input.Tags = spec.Tags
	}

	out, err := r.client.CreateQueue(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.QueueUrl), nil
}

// GetQueueUrl reads the current state of the AWS SQS Queue from Amazon Simple Queue Service (SQS).
func (r *realQueueAPI) GetQueueUrl(ctx context.Context, queueName string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.GetQueueUrl(ctx, &sqssdk.GetQueueUrlInput{QueueName: aws.String(queueName)})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.QueueUrl), nil
}

// GetQueueAttributes reads the current state of the AWS SQS Queue from Amazon Simple Queue Service (SQS).
func (r *realQueueAPI) GetQueueAttributes(ctx context.Context, queueURL string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetQueueAttributes(ctx, &sqssdk.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameAll},
	})
	if err != nil {
		return ObservedState{}, err
	}

	attrs := out.Attributes
	obs := ObservedState{
		QueueUrl:              queueURL,
		QueueArn:              attrs[string(sqstypes.QueueAttributeNameQueueArn)],
		QueueName:             extractQueueName(queueURL),
		KmsMasterKeyId:        attrs[string(sqstypes.QueueAttributeNameKmsMasterKeyId)],
		DeduplicationScope:    attrs[string(sqstypes.QueueAttributeNameDeduplicationScope)],
		FifoThroughputLimit:   attrs[string(sqstypes.QueueAttributeNameFifoThroughputLimit)],
		CreatedTimestamp:      attrs[string(sqstypes.QueueAttributeNameCreatedTimestamp)],
		LastModifiedTimestamp: attrs[string(sqstypes.QueueAttributeNameLastModifiedTimestamp)],
	}

	obs.VisibilityTimeout, _ = strconv.Atoi(attrs[string(sqstypes.QueueAttributeNameVisibilityTimeout)])
	obs.MessageRetentionPeriod, _ = strconv.Atoi(attrs[string(sqstypes.QueueAttributeNameMessageRetentionPeriod)])
	obs.MaximumMessageSize, _ = strconv.Atoi(attrs[string(sqstypes.QueueAttributeNameMaximumMessageSize)])
	obs.DelaySeconds, _ = strconv.Atoi(attrs[string(sqstypes.QueueAttributeNameDelaySeconds)])
	obs.ReceiveMessageWaitTimeSeconds, _ = strconv.Atoi(attrs[string(sqstypes.QueueAttributeNameReceiveMessageWaitTimeSeconds)])
	obs.KmsDataKeyReusePeriodSeconds, _ = strconv.Atoi(attrs[string(sqstypes.QueueAttributeNameKmsDataKeyReusePeriodSeconds)])
	obs.ApproximateNumberOfMessages, _ = strconv.ParseInt(attrs[string(sqstypes.QueueAttributeNameApproximateNumberOfMessages)], 10, 64)

	obs.FifoQueue = attrs[string(sqstypes.QueueAttributeNameFifoQueue)] == "true"
	obs.ContentBasedDeduplication = attrs[string(sqstypes.QueueAttributeNameContentBasedDeduplication)] == "true"
	obs.SqsManagedSseEnabled = attrs[string(sqstypes.QueueAttributeNameSqsManagedSseEnabled)] == "true"

	if payload := attrs[string(sqstypes.QueueAttributeNameRedrivePolicy)]; payload != "" {
		var policy RedrivePolicy
		if err := json.Unmarshal([]byte(payload), &policy); err == nil {
			obs.RedrivePolicy = &policy
		}
	}

	tags, err := r.GetTags(ctx, queueURL)
	if err != nil {
		return ObservedState{}, fmt.Errorf("get tags for queue %s: %w", queueURL, err)
	}
	obs.Tags = tags
	return obs, nil
}

// SetQueueAttributes updates mutable properties of the AWS SQS Queue via Amazon Simple Queue Service (SQS).
func (r *realQueueAPI) SetQueueAttributes(ctx context.Context, queueURL string, attrs map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetQueueAttributes(ctx, &sqssdk.SetQueueAttributesInput{QueueUrl: aws.String(queueURL), Attributes: attrs})
	return err
}

// DeleteQueue removes the AWS SQS Queue from AWS via Amazon Simple Queue Service (SQS).
func (r *realQueueAPI) DeleteQueue(ctx context.Context, queueURL string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteQueue(ctx, &sqssdk.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	return err
}

// UpdateTags updates mutable properties of the AWS SQS Queue via Amazon Simple Queue Service (SQS).
func (r *realQueueAPI) UpdateTags(ctx context.Context, queueURL string, tags map[string]string) error {
	current, err := r.GetTags(ctx, queueURL)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}

	var removeKeys []string
	for key := range current {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		if _, keep := tags[key]; !keep {
			removeKeys = append(removeKeys, key)
		}
	}

	if len(removeKeys) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, err := r.client.UntagQueue(ctx, &sqssdk.UntagQueueInput{QueueUrl: aws.String(queueURL), TagKeys: removeKeys}); err != nil {
			return fmt.Errorf("untag: %w", err)
		}
	}

	if len(tags) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, err := r.client.TagQueue(ctx, &sqssdk.TagQueueInput{QueueUrl: aws.String(queueURL), Tags: tags}); err != nil {
			return fmt.Errorf("tag: %w", err)
		}
	}

	return nil
}

// GetTags reads the current state of the AWS SQS Queue from Amazon Simple Queue Service (SQS).
func (r *realQueueAPI) GetTags(ctx context.Context, queueURL string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListQueueTags(ctx, &sqssdk.ListQueueTagsInput{QueueUrl: aws.String(queueURL)})
	if err != nil {
		return nil, err
	}
	if out.Tags == nil {
		return map[string]string{}, nil
	}
	return out.Tags, nil
}

// FindByManagedKey searches for the AWS SQS Queue using alternative identifiers.
func (r *realQueueAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	var nextToken *string
	for {
		if err := r.limiter.Wait(ctx); err != nil {
			return "", err
		}
		out, err := r.client.ListQueues(ctx, &sqssdk.ListQueuesInput{NextToken: nextToken})
		if err != nil {
			return "", err
		}
		for _, queueURL := range out.QueueUrls {
			tags, tagErr := r.GetTags(ctx, queueURL)
			if tagErr != nil {
				continue
			}
			if tags["praxis:managed-key"] == managedKey {
				return queueURL, nil
			}
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return "", nil
}

func extractQueueName(queueURL string) string {
	parts := strings.Split(queueURL, "/")
	if len(parts) == 0 {
		return queueURL
	}
	return parts[len(parts)-1]
}

// IsNotFound returns true if the AWS error indicates the AWS SQS Queue does not exist.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	return awserr.HasCode(err, "QueueDoesNotExist", "AWS.SimpleQueueService.NonExistentQueue") ||
		strings.Contains(err.Error(), "QueueDoesNotExist") ||
		strings.Contains(err.Error(), "NonExistentQueue")
}

func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return awserr.HasCode(err, "QueueAlreadyExists", "QueueNameExists") ||
		strings.Contains(err.Error(), "QueueNameExists") ||
		strings.Contains(err.Error(), "QueueAlreadyExists")
}

func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	return awserr.HasCode(err, "QueueDeletedRecently") ||
		strings.Contains(err.Error(), "QueueDeletedRecently") ||
		strings.Contains(err.Error(), "You must wait 60 seconds")
}

func IsInvalidInput(err error) bool {
	if err == nil {
		return false
	}
	return awserr.HasCode(err, "InvalidAttributeName", "InvalidAttributeValue", "InvalidParameterValue") ||
		strings.Contains(err.Error(), "InvalidAttribute")
}
