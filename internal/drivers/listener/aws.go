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

func NewListenerAPI(client *elbv2sdk.Client) ListenerAPI {
	return &realListenerAPI{client: client, limiter: ratelimit.New("listener", 15, 8)}
}

func (r *realListenerAPI) CreateListener(ctx context.Context, spec ListenerSpec) (string, error) {
	r.limiter.Wait(ctx)
	input := &elbv2sdk.CreateListenerInput{
		LoadBalancerArn: aws.String(spec.LoadBalancerArn),
		Port:            aws.Int32(int32(spec.Port)),
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

func (r *realListenerAPI) DescribeListener(ctx context.Context, arn string) (ObservedState, error) {
	r.limiter.Wait(ctx)
	out, err := r.client.DescribeListeners(ctx, &elbv2sdk.DescribeListenersInput{ListenerArns: []string{arn}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Listeners) == 0 {
		return ObservedState{}, fmt.Errorf("ListenerNotFound: %s", arn)
	}
	return r.buildObservedState(ctx, out.Listeners[0])
}

func (r *realListenerAPI) FindListenerByPort(ctx context.Context, lbArn string, port int) (ObservedState, error) {
	r.limiter.Wait(ctx)
	out, err := r.client.DescribeListeners(ctx, &elbv2sdk.DescribeListenersInput{LoadBalancerArn: aws.String(lbArn)})
	if err != nil {
		return ObservedState{}, err
	}
	for _, l := range out.Listeners {
		if aws.ToInt32(l.Port) == int32(port) {
			return r.buildObservedState(ctx, l)
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

func (r *realListenerAPI) DeleteListener(ctx context.Context, arn string) error {
	r.limiter.Wait(ctx)
	_, err := r.client.DeleteListener(ctx, &elbv2sdk.DeleteListenerInput{ListenerArn: aws.String(arn)})
	return err
}

func (r *realListenerAPI) ModifyListener(ctx context.Context, arn string, spec ListenerSpec) error {
	r.limiter.Wait(ctx)
	input := &elbv2sdk.ModifyListenerInput{
		ListenerArn:    aws.String(arn),
		Port:           aws.Int32(int32(spec.Port)),
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

func (r *realListenerAPI) UpdateTags(ctx context.Context, arn string, desired map[string]string) error {
	r.limiter.Wait(ctx)
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
		r.limiter.Wait(ctx)
		if _, removeErr := r.client.RemoveTags(ctx, &elbv2sdk.RemoveTagsInput{
			ResourceArns: []string{arn}, TagKeys: keysToRemove,
		}); removeErr != nil {
			return removeErr
		}
	}
	if len(desired) > 0 {
		r.limiter.Wait(ctx)
		if _, addErr := r.client.AddTags(ctx, &elbv2sdk.AddTagsInput{
			ResourceArns: []string{arn}, Tags: toELBTags(desired),
		}); addErr != nil {
			return addErr
		}
	}
	return nil
}

func (r *realListenerAPI) describeTags(ctx context.Context, arn string) (map[string]string, error) {
	r.limiter.Wait(ctx)
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

func filterPraxisTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		if !strings.HasPrefix(key, "praxis:") {
			out[key] = value
		}
	}
	return out
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ListenerNotFound")
}

func IsDuplicate(err error) bool {
	return awserr.HasCode(err, "DuplicateListener")
}

func IsTooMany(err error) bool {
	return awserr.HasCode(err, "TooManyListeners")
}

func IsTargetGroupNotFound(err error) bool {
	return awserr.HasCode(err, "TargetGroupNotFound")
}

func IsInvalidConfig(err error) bool {
	return awserr.HasCode(err, "InvalidConfigurationRequest")
}

func IsCertificateNotFound(err error) bool {
	return awserr.HasCode(err, "CertificateNotFound")
}
