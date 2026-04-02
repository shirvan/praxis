package snssub

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	snssdk "github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// SubscriptionAPI abstracts the AWS SNS SDK operations for subscription management.
type SubscriptionAPI interface {
	Subscribe(ctx context.Context, spec SNSSubscriptionSpec) (string, error)
	GetSubscriptionAttributes(ctx context.Context, subscriptionArn string) (ObservedState, error)
	SetSubscriptionAttribute(ctx context.Context, subscriptionArn, attrName, attrValue string) error
	Unsubscribe(ctx context.Context, subscriptionArn string) error
	FindByTopicProtocolEndpoint(ctx context.Context, topicArn, protocol, endpoint string) (string, error)
}

// SubscriptionSummary is a lightweight subscription record returned by list operations.
type SubscriptionSummary struct {
	SubscriptionArn string
	TopicArn        string
	Protocol        string
	Endpoint        string
	Owner           string
}

type realSubscriptionAPI struct {
	client  *snssdk.Client
	limiter *ratelimit.Limiter
}

// NewSubscriptionAPI returns a real AWS-backed SubscriptionAPI.
func NewSubscriptionAPI(client *snssdk.Client) SubscriptionAPI {
	return &realSubscriptionAPI{
		client:  client,
		limiter: ratelimit.New("sns-subscription", 30, 10),
	}
}

func (r *realSubscriptionAPI) Subscribe(ctx context.Context, spec SNSSubscriptionSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	attrs := make(map[string]string)
	if spec.FilterPolicy != "" {
		attrs["FilterPolicy"] = spec.FilterPolicy
	}
	if spec.FilterPolicyScope != "" {
		attrs["FilterPolicyScope"] = spec.FilterPolicyScope
	}
	if spec.RawMessageDelivery {
		attrs["RawMessageDelivery"] = "true"
	}
	if spec.DeliveryPolicy != "" {
		attrs["DeliveryPolicy"] = spec.DeliveryPolicy
	}
	if spec.RedrivePolicy != "" {
		attrs["RedrivePolicy"] = spec.RedrivePolicy
	}
	if spec.SubscriptionRoleArn != "" {
		attrs["SubscriptionRoleArn"] = spec.SubscriptionRoleArn
	}

	input := &snssdk.SubscribeInput{
		TopicArn:              aws.String(spec.TopicArn),
		Protocol:              aws.String(spec.Protocol),
		Endpoint:              aws.String(spec.Endpoint),
		ReturnSubscriptionArn: true,
	}
	if len(attrs) > 0 {
		input.Attributes = attrs
	}

	out, err := r.client.Subscribe(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.SubscriptionArn), nil
}

func (r *realSubscriptionAPI) GetSubscriptionAttributes(ctx context.Context, subscriptionArn string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.GetSubscriptionAttributes(ctx, &snssdk.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subscriptionArn),
	})
	if err != nil {
		return ObservedState{}, err
	}

	attrs := out.Attributes
	obs := ObservedState{
		SubscriptionArn: subscriptionArn,
		TopicArn:        attrs["TopicArn"],
		Protocol:        attrs["Protocol"],
		Endpoint:        attrs["Endpoint"],
		Owner:           attrs["Owner"],
	}

	if v, ok := attrs["FilterPolicy"]; ok && v != "" {
		obs.FilterPolicy = v
	}
	if v, ok := attrs["FilterPolicyScope"]; ok && v != "" {
		obs.FilterPolicyScope = v
	}
	if v, ok := attrs["RawMessageDelivery"]; ok {
		obs.RawMessageDelivery = v == "true"
	}
	if v, ok := attrs["DeliveryPolicy"]; ok && v != "" {
		obs.DeliveryPolicy = v
	}
	if v, ok := attrs["RedrivePolicy"]; ok && v != "" {
		obs.RedrivePolicy = v
	}
	if v, ok := attrs["SubscriptionRoleArn"]; ok && v != "" {
		obs.SubscriptionRoleArn = v
	}
	if v, ok := attrs["PendingConfirmation"]; ok {
		obs.PendingConfirmation = v == "true"
	}
	if obs.PendingConfirmation {
		obs.ConfirmationStatus = "pending"
	} else {
		obs.ConfirmationStatus = "confirmed"
	}

	return obs, nil
}

func (r *realSubscriptionAPI) SetSubscriptionAttribute(ctx context.Context, subscriptionArn, attrName, attrValue string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetSubscriptionAttributes(ctx, &snssdk.SetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subscriptionArn),
		AttributeName:   aws.String(attrName),
		AttributeValue:  aws.String(attrValue),
	})
	return err
}

func (r *realSubscriptionAPI) Unsubscribe(ctx context.Context, subscriptionArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.Unsubscribe(ctx, &snssdk.UnsubscribeInput{
		SubscriptionArn: aws.String(subscriptionArn),
	})
	return err
}

func (r *realSubscriptionAPI) FindByTopicProtocolEndpoint(ctx context.Context, topicArn, protocol, endpoint string) (string, error) {
	subs, err := r.listSubscriptionsByTopic(ctx, topicArn)
	if err != nil {
		return "", err
	}
	for _, s := range subs {
		if s.Protocol == protocol && s.Endpoint == endpoint {
			return s.SubscriptionArn, nil
		}
	}
	return "", nil
}

func (r *realSubscriptionAPI) listSubscriptionsByTopic(ctx context.Context, topicArn string) ([]SubscriptionSummary, error) {
	var subs []SubscriptionSummary
	var nextToken *string
	for {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		out, err := r.client.ListSubscriptionsByTopic(ctx, &snssdk.ListSubscriptionsByTopicInput{
			TopicArn:  aws.String(topicArn),
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, s := range out.Subscriptions {
			arn := aws.ToString(s.SubscriptionArn)
			if arn == "PendingConfirmation" || arn == "Deleted" {
				continue
			}
			subs = append(subs, SubscriptionSummary{
				SubscriptionArn: arn,
				TopicArn:        aws.ToString(s.TopicArn),
				Protocol:        aws.ToString(s.Protocol),
				Endpoint:        aws.ToString(s.Endpoint),
				Owner:           aws.ToString(s.Owner),
			})
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return subs, nil
}

// IsNotFound returns true if the error indicates the subscription does not exist.
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

// isSubscriptionLimitExceeded returns true if the subscription limit was exceeded.
func isSubscriptionLimitExceeded(err error) bool {
	if err == nil {
		return false
	}
	var sle *snstypes.SubscriptionLimitExceededException
	if errors.As(err, &sle) {
		return true
	}
	return strings.Contains(err.Error(), "SubscriptionLimitExceeded")
}
