// Package listener – aws.go
//
// This file contains the AWS API abstraction layer for AWS ELBv2 Listener.
// It defines the ListenerAPI interface (used for testing with mocks)
// and the real implementation that calls Elastic Load Balancing v2 through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package listener

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2sdk "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// ListenerAPI abstracts all Elastic Load Balancing v2 SDK operations needed
// to manage a AWS ELBv2 Listener. The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type ListenerAPI interface {
	CreateListener(ctx context.Context, spec ListenerSpec) (arn string, err error)
	DescribeListener(ctx context.Context, arn string) (ObservedState, error)
	FindListenerByPort(ctx context.Context, lbArn string, port int) (ObservedState, error)
	DeleteListener(ctx context.Context, arn string) error
	ModifyListener(ctx context.Context, arn string, spec ListenerSpec) error
	UpdateTags(ctx context.Context, arn string, desired map[string]string) error
}

type realListenerAPI struct {
	client  *elbv2sdk.Client
	limiter *ratelimit.Limiter
}

// NewListenerAPI constructs a production ListenerAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewListenerAPI(client *elbv2sdk.Client) ListenerAPI {
	return &realListenerAPI{client: client, limiter: ratelimit.New("listener", 15, 8)}
}

// CreateListener calls Elastic Load Balancing v2 to create a new AWS ELBv2 Listener from the given spec.
func (r *realListenerAPI) CreateListener(ctx context.Context, spec ListenerSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	input := &elbv2sdk.CreateListenerInput{
		LoadBalancerArn: aws.String(spec.LoadBalancerArn),
		Port:            aws.Int32(int32(spec.Port)), //nolint:gosec // G115: listener port is bounded to valid TCP range
		Protocol:        elbv2types.ProtocolEnum(spec.Protocol),
		DefaultActions:  toAWSActions(spec.DefaultActions),
	}
	if spec.SslPolicy != "" {
		input.SslPolicy = aws.String(spec.SslPolicy)
	}
	if spec.CertificateArn != "" {
		input.Certificates = []elbv2types.Certificate{{CertificateArn: aws.String(spec.CertificateArn)}}
	}
	if spec.AlpnPolicy != "" {
		input.AlpnPolicy = []string{spec.AlpnPolicy}
	}
	if len(spec.Tags) > 0 {
		input.Tags = toELBTags(spec.Tags)
	}
	out, err := r.client.CreateListener(ctx, input)
	if err != nil {
		return "", err
	}
	if len(out.Listeners) == 0 {
		return "", fmt.Errorf("CreateListener returned no listeners")
	}
	return aws.ToString(out.Listeners[0].ListenerArn), nil
}

// DescribeListener reads the current state of the AWS ELBv2 Listener from Elastic Load Balancing v2.
func (r *realListenerAPI) DescribeListener(ctx context.Context, arn string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeListeners(ctx, &elbv2sdk.DescribeListenersInput{ListenerArns: []string{arn}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Listeners) == 0 {
		return ObservedState{}, fmt.Errorf("ListenerNotFound: %s", arn)
	}
	return r.buildObservedState(ctx, out.Listeners[0])
}

// FindListenerByPort searches for the AWS ELBv2 Listener using alternative identifiers.
func (r *realListenerAPI) FindListenerByPort(ctx context.Context, lbArn string, port int) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeListeners(ctx, &elbv2sdk.DescribeListenersInput{LoadBalancerArn: aws.String(lbArn)})
	if err != nil {
		return ObservedState{}, err
	}
	for i := range out.Listeners {
		if aws.ToInt32(out.Listeners[i].Port) == int32(port) { //nolint:gosec // G115: listener port is bounded to valid TCP range
			return r.buildObservedState(ctx, out.Listeners[i])
		}
	}
	return ObservedState{}, fmt.Errorf("ListenerNotFound: no listener on port %d for %s", port, lbArn)
}

func (r *realListenerAPI) buildObservedState(ctx context.Context, l elbv2types.Listener) (ObservedState, error) {
	arn := aws.ToString(l.ListenerArn)
	tags, err := r.describeTags(ctx, arn)
	if err != nil {
		return ObservedState{}, err
	}
	var certArn string
	if len(l.Certificates) > 0 {
		certArn = aws.ToString(l.Certificates[0].CertificateArn)
	}
	var alpnPolicy string
	if len(l.AlpnPolicy) > 0 {
		alpnPolicy = l.AlpnPolicy[0]
	}
	return ObservedState{
		ListenerArn:     arn,
		LoadBalancerArn: aws.ToString(l.LoadBalancerArn),
		Port:            int(aws.ToInt32(l.Port)),
		Protocol:        string(l.Protocol),
		SslPolicy:       aws.ToString(l.SslPolicy),
		CertificateArn:  certArn,
		AlpnPolicy:      alpnPolicy,
		DefaultActions:  fromAWSActions(l.DefaultActions),
		Tags:            tags,
	}, nil
}

// DeleteListener removes the AWS ELBv2 Listener from AWS via Elastic Load Balancing v2.
func (r *realListenerAPI) DeleteListener(ctx context.Context, arn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteListener(ctx, &elbv2sdk.DeleteListenerInput{ListenerArn: aws.String(arn)})
	return err
}

// ModifyListener updates mutable properties of the AWS ELBv2 Listener via Elastic Load Balancing v2.
func (r *realListenerAPI) ModifyListener(ctx context.Context, arn string, spec ListenerSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &elbv2sdk.ModifyListenerInput{
		ListenerArn:    aws.String(arn),
		Port:           aws.Int32(int32(spec.Port)), //nolint:gosec // G115: listener port is bounded to valid TCP range
		Protocol:       elbv2types.ProtocolEnum(spec.Protocol),
		DefaultActions: toAWSActions(spec.DefaultActions),
	}
	if spec.SslPolicy != "" {
		input.SslPolicy = aws.String(spec.SslPolicy)
	}
	if spec.CertificateArn != "" {
		input.Certificates = []elbv2types.Certificate{{CertificateArn: aws.String(spec.CertificateArn)}}
	}
	if spec.AlpnPolicy != "" {
		input.AlpnPolicy = []string{spec.AlpnPolicy}
	}
	_, err := r.client.ModifyListener(ctx, input)
	return err
}

// UpdateTags updates mutable properties of the AWS ELBv2 Listener via Elastic Load Balancing v2.
func (r *realListenerAPI) UpdateTags(ctx context.Context, arn string, desired map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	existing, err := r.describeTags(ctx, arn)
	if err != nil {
		return err
	}
	var keysToRemove []string
	for key := range existing {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		if _, ok := desired[key]; !ok {
			keysToRemove = append(keysToRemove, key)
		}
	}
	if len(keysToRemove) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, removeErr := r.client.RemoveTags(ctx, &elbv2sdk.RemoveTagsInput{
			ResourceArns: []string{arn}, TagKeys: keysToRemove,
		}); removeErr != nil {
			return removeErr
		}
	}
	if len(desired) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, addErr := r.client.AddTags(ctx, &elbv2sdk.AddTagsInput{
			ResourceArns: []string{arn}, Tags: toELBTags(desired),
		}); addErr != nil {
			return addErr
		}
	}
	return nil
}

func (r *realListenerAPI) describeTags(ctx context.Context, arn string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.DescribeTags(ctx, &elbv2sdk.DescribeTagsInput{ResourceArns: []string{arn}})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string)
	for _, desc := range out.TagDescriptions {
		for _, tag := range desc.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}
	return tags, nil
}

func toAWSActions(actions []ListenerAction) []elbv2types.Action {
	out := make([]elbv2types.Action, 0, len(actions))
	for i, a := range actions {
		action := elbv2types.Action{
			Type:  elbv2types.ActionTypeEnum(a.Type),
			Order: aws.Int32(int32(i + 1)),
		}
		switch a.Type {
		case "forward":
			action.TargetGroupArn = aws.String(a.TargetGroupArn)
		case "redirect":
			if a.RedirectConfig != nil {
				action.RedirectConfig = &elbv2types.RedirectActionConfig{
					Protocol:   aws.String(a.RedirectConfig.Protocol),
					Host:       aws.String(a.RedirectConfig.Host),
					Port:       aws.String(a.RedirectConfig.Port),
					Path:       aws.String(a.RedirectConfig.Path),
					Query:      aws.String(a.RedirectConfig.Query),
					StatusCode: elbv2types.RedirectActionStatusCodeEnum(a.RedirectConfig.StatusCode),
				}
			}
		case "fixed-response":
			if a.FixedResponseConfig != nil {
				action.FixedResponseConfig = &elbv2types.FixedResponseActionConfig{
					StatusCode:  aws.String(a.FixedResponseConfig.StatusCode),
					ContentType: aws.String(a.FixedResponseConfig.ContentType),
					MessageBody: aws.String(a.FixedResponseConfig.MessageBody),
				}
			}
		}
		out = append(out, action)
	}
	return out
}

func fromAWSActions(actions []elbv2types.Action) []ListenerAction {
	out := make([]ListenerAction, 0, len(actions))
	for _, a := range actions {
		la := ListenerAction{Type: string(a.Type)}
		switch a.Type {
		case elbv2types.ActionTypeEnumForward:
			la.TargetGroupArn = aws.ToString(a.TargetGroupArn)
		case elbv2types.ActionTypeEnumRedirect:
			if a.RedirectConfig != nil {
				la.RedirectConfig = &RedirectConfig{
					Protocol:   aws.ToString(a.RedirectConfig.Protocol),
					Host:       aws.ToString(a.RedirectConfig.Host),
					Port:       aws.ToString(a.RedirectConfig.Port),
					Path:       aws.ToString(a.RedirectConfig.Path),
					Query:      aws.ToString(a.RedirectConfig.Query),
					StatusCode: string(a.RedirectConfig.StatusCode),
				}
			}
		case elbv2types.ActionTypeEnumFixedResponse:
			if a.FixedResponseConfig != nil {
				la.FixedResponseConfig = &FixedResponseConfig{
					StatusCode:  aws.ToString(a.FixedResponseConfig.StatusCode),
					ContentType: aws.ToString(a.FixedResponseConfig.ContentType),
					MessageBody: aws.ToString(a.FixedResponseConfig.MessageBody),
				}
			}
		}
		out = append(out, la)
	}
	return out
}

func toELBTags(tags map[string]string) []elbv2types.Tag {
	out := make([]elbv2types.Tag, 0, len(tags))
	for key, value := range tags {
		out = append(out, elbv2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return out
}

// IsNotFound returns true if the AWS error indicates the AWS ELBv2 Listener does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ListenerNotFound")
}

// IsDuplicate returns true if the AWS error indicates a naming conflict.
func IsDuplicate(err error) bool {
	return awserr.HasCode(err, "DuplicateListener")
}

// IsTooMany returns true if the AWS error indicates a service quota has been reached.
func IsTooMany(err error) bool {
	return awserr.HasCode(err, "TooManyListeners")
}

// IsTargetGroupNotFound returns true if a referenced target group does not exist.
func IsTargetGroupNotFound(err error) bool {
	return awserr.HasCode(err, "TargetGroupNotFound")
}

// IsInvalidConfig returns true if the AWS error indicates an invalid configuration.
func IsInvalidConfig(err error) bool {
	return awserr.HasCode(err, "InvalidConfigurationRequest")
}

// IsCertificateNotFound returns true if a referenced ACM certificate does not exist.
func IsCertificateNotFound(err error) bool {
	return awserr.HasCode(err, "CertificateNotFound")
}
