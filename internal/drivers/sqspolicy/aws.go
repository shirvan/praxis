package sqspolicy

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type PolicyAPI interface {
	GetQueueUrl(ctx context.Context, queueName string) (string, error)
	GetQueuePolicy(ctx context.Context, queueURL string) (ObservedState, error)
	SetQueuePolicy(ctx context.Context, queueURL, policy string) error
	RemoveQueuePolicy(ctx context.Context, queueURL string) error
}

type realPolicyAPI struct {
	client  *sqssdk.Client
	limiter *ratelimit.Limiter
}

func NewPolicyAPI(client *sqssdk.Client) PolicyAPI {
	return &realPolicyAPI{client: client, limiter: ratelimit.New("sqs", 50, 20)}
}

func (r *realPolicyAPI) GetQueueUrl(ctx context.Context, queueName string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.GetQueueUrl(ctx, &sqssdk.GetQueueUrlInput{QueueName: aws.String(queueName)})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.QueueUrl), nil
}

func (r *realPolicyAPI) GetQueuePolicy(ctx context.Context, queueURL string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetQueueAttributes(ctx, &sqssdk.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNamePolicy,
			sqstypes.QueueAttributeNameQueueArn,
		},
	})
	if err != nil {
		return ObservedState{}, err
	}
	return ObservedState{
		QueueUrl: queueURL,
		QueueArn: out.Attributes[string(sqstypes.QueueAttributeNameQueueArn)],
		Policy:   out.Attributes[string(sqstypes.QueueAttributeNamePolicy)],
	}, nil
}

func (r *realPolicyAPI) SetQueuePolicy(ctx context.Context, queueURL, policy string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetQueueAttributes(ctx, &sqssdk.SetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		Attributes: map[string]string{
			string(sqstypes.QueueAttributeNamePolicy): policy,
		},
	})
	return err
}

func (r *realPolicyAPI) RemoveQueuePolicy(ctx context.Context, queueURL string) error {
	return r.SetQueuePolicy(ctx, queueURL, "")
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	return awserr.HasCode(err, "QueueDoesNotExist", "AWS.SimpleQueueService.NonExistentQueue") ||
		strings.Contains(err.Error(), "QueueDoesNotExist") ||
		strings.Contains(err.Error(), "NonExistentQueue")
}

func IsInvalidInput(err error) bool {
	if err == nil {
		return false
	}
	return awserr.HasCode(err, "InvalidAttributeName", "InvalidAttributeValue", "InvalidParameterValue") ||
		strings.Contains(err.Error(), "InvalidAttribute") ||
		strings.Contains(err.Error(), "InvalidParameterValue")
}
