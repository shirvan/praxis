// Package snstopic – aws.go
//
// This file contains the AWS API abstraction layer for AWS SNS Topic.
// It defines the SNSTopicAPI interface (used for testing with mocks)
// and the real implementation that calls Amazon Simple Notification Service (SNS) through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package snstopic

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	snssdk "github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// TopicAPI abstracts the AWS SNS SDK operations for SNS topic management.
type TopicAPI interface {
	CreateTopic(ctx context.Context, spec SNSTopicSpec) (string, error)
	GetTopicAttributes(ctx context.Context, topicArn string) (ObservedState, error)
	SetTopicAttribute(ctx context.Context, topicArn, attrName, attrValue string) error
	DeleteTopic(ctx context.Context, topicArn string) error
	UpdateTags(ctx context.Context, topicArn string, tags map[string]string) error
	FindByName(ctx context.Context, topicName string) (string, error)
}

type realTopicAPI struct {
	client  *snssdk.Client
	limiter *ratelimit.Limiter
}

// NewTopicAPI returns a real AWS-backed TopicAPI.
func NewTopicAPI(client *snssdk.Client) TopicAPI {
	return &realTopicAPI{
		client:  client,
		limiter: ratelimit.New("sns-topic", 30, 10),
	}
}

// CreateTopic calls Amazon Simple Notification Service (SNS) to create a new AWS SNS Topic from the given spec.
func (r *realTopicAPI) CreateTopic(ctx context.Context, spec SNSTopicSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	attrs := make(map[string]string)
	if spec.DisplayName != "" {
		attrs["DisplayName"] = spec.DisplayName
	}
	if spec.FifoTopic {
		attrs["FifoTopic"] = "true"
	}
	if spec.ContentBasedDeduplication {
		attrs["ContentBasedDeduplication"] = "true"
	}
	if spec.KmsMasterKeyId != "" {
		attrs["KmsMasterKeyId"] = spec.KmsMasterKeyId
	}
	if spec.Policy != "" {
		attrs["Policy"] = spec.Policy
	}
	if spec.DeliveryPolicy != "" {
		attrs["DeliveryPolicy"] = spec.DeliveryPolicy
	}

	input := &snssdk.CreateTopicInput{
		Name: aws.String(spec.TopicName),
	}
	if len(attrs) > 0 {
		input.Attributes = attrs
	}

	out, err := r.client.CreateTopic(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.TopicArn), nil
}

// GetTopicAttributes reads the current state of the AWS SNS Topic from Amazon Simple Notification Service (SNS).
func (r *realTopicAPI) GetTopicAttributes(ctx context.Context, topicArn string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.GetTopicAttributes(ctx, &snssdk.GetTopicAttributesInput{
		TopicArn: aws.String(topicArn),
	})
	if err != nil {
		return ObservedState{}, err
	}

	attrs := out.Attributes
	obs := ObservedState{
		TopicArn:    topicArn,
		TopicName:   extractTopicName(topicArn),
		DisplayName: attrs["DisplayName"],
		Owner:       attrs["Owner"],
	}
	obs.FifoTopic = attrs["FifoTopic"] == "true"
	obs.ContentBasedDeduplication = attrs["ContentBasedDeduplication"] == "true"
	if v, ok := attrs["Policy"]; ok {
		obs.Policy = v
	}
	if v, ok := attrs["DeliveryPolicy"]; ok {
		obs.DeliveryPolicy = v
	}
	if v, ok := attrs["KmsMasterKeyId"]; ok {
		obs.KmsMasterKeyId = v
	}

	// Fetch tags separately (SNS tags use a separate API)
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	tagOut, err := r.client.ListTagsForResource(ctx, &snssdk.ListTagsForResourceInput{
		ResourceArn: aws.String(topicArn),
	})
	if err != nil {
		return ObservedState{}, fmt.Errorf("list tags for topic %s: %w", topicArn, err)
	}
	obs.Tags = make(map[string]string)
	for _, tag := range tagOut.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	return obs, nil
}

// SetTopicAttribute updates mutable properties of the AWS SNS Topic via Amazon Simple Notification Service (SNS).
func (r *realTopicAPI) SetTopicAttribute(ctx context.Context, topicArn, attrName, attrValue string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetTopicAttributes(ctx, &snssdk.SetTopicAttributesInput{
		TopicArn:       aws.String(topicArn),
		AttributeName:  aws.String(attrName),
		AttributeValue: aws.String(attrValue),
	})
	return err
}

// DeleteTopic removes the AWS SNS Topic from AWS via Amazon Simple Notification Service (SNS).
func (r *realTopicAPI) DeleteTopic(ctx context.Context, topicArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteTopic(ctx, &snssdk.DeleteTopicInput{
		TopicArn: aws.String(topicArn),
	})
	return err
}

// UpdateTags updates mutable properties of the AWS SNS Topic via Amazon Simple Notification Service (SNS).
func (r *realTopicAPI) UpdateTags(ctx context.Context, topicArn string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	tagOut, err := r.client.ListTagsForResource(ctx, &snssdk.ListTagsForResourceInput{
		ResourceArn: aws.String(topicArn),
	})
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}

	var removeKeys []string
	for _, tag := range tagOut.Tags {
		key := aws.ToString(tag.Key)
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
		if _, err := r.client.UntagResource(ctx, &snssdk.UntagResourceInput{
			ResourceArn: aws.String(topicArn),
			TagKeys:     removeKeys,
		}); err != nil {
			return fmt.Errorf("untag: %w", err)
		}
	}

	if len(tags) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		snsTags := make([]snstypes.Tag, 0, len(tags))
		for k, v := range tags {
			snsTags = append(snsTags, snstypes.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}
		if _, err := r.client.TagResource(ctx, &snssdk.TagResourceInput{
			ResourceArn: aws.String(topicArn),
			Tags:        snsTags,
		}); err != nil {
			return fmt.Errorf("tag: %w", err)
		}
	}

	return nil
}

// FindByName searches for the AWS SNS Topic using alternative identifiers.
func (r *realTopicAPI) FindByName(ctx context.Context, topicName string) (string, error) {
	var nextToken *string
	for {
		if err := r.limiter.Wait(ctx); err != nil {
			return "", err
		}
		out, err := r.client.ListTopics(ctx, &snssdk.ListTopicsInput{
			NextToken: nextToken,
		})
		if err != nil {
			return "", err
		}
		for _, topic := range out.Topics {
			arn := aws.ToString(topic.TopicArn)
			if extractTopicName(arn) == topicName {
				return arn, nil
			}
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return "", nil
}

// extractTopicName extracts the topic name from a topic ARN.
// ARN format: arn:aws:sns:<region>:<account>:<topicName>
func extractTopicName(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 6 {
		return parts[5]
	}
	return arn
}

// IsNotFound returns true if the error indicates the topic does not exist.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nfe *snstypes.NotFoundException
	if errors.As(err, &nfe) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "NotFoundException") || strings.Contains(msg, "NotFound")
}

// IsInvalidParameter returns true if the error indicates an invalid parameter.
func IsInvalidParameter(err error) bool {
	if err == nil {
		return false
	}
	var ipe *snstypes.InvalidParameterException
	if errors.As(err, &ipe) {
		return true
	}
	var ipve *snstypes.InvalidParameterValueException
	if errors.As(err, &ipve) {
		return true
	}
	return strings.Contains(err.Error(), "InvalidParameter")
}

// isAuthError returns true if the error is an authorization error.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	var aee *snstypes.AuthorizationErrorException
	if errors.As(err, &aee) {
		return true
	}
	return strings.Contains(err.Error(), "AuthorizationError")
}
