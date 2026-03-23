package listenerrule

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2sdk "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type ListenerRuleAPI interface {
	CreateRule(ctx context.Context, listenerArn string, spec ListenerRuleSpec) (string, error)
	DescribeRule(ctx context.Context, ruleArn string) (ObservedState, error)
	FindRuleByPriority(ctx context.Context, listenerArn string, priority int) (ObservedState, error)
	ListRules(ctx context.Context, listenerArn string) ([]ObservedState, error)
	DeleteRule(ctx context.Context, ruleArn string) error
	ModifyRule(ctx context.Context, ruleArn string, conditions []RuleCondition, actions []RuleAction) error
	SetRulePriorities(ctx context.Context, ruleArn string, priority int) error
	UpdateTags(ctx context.Context, ruleArn string, desired map[string]string) error
}

type realListenerRuleAPI struct {
	client  *elbv2sdk.Client
	limiter *ratelimit.Limiter
}

func NewListenerRuleAPI(client *elbv2sdk.Client) ListenerRuleAPI {
	return &realListenerRuleAPI{client: client, limiter: ratelimit.New("listener-rule", 15, 8)}
}

func (r *realListenerRuleAPI) CreateRule(ctx context.Context, listenerArn string, spec ListenerRuleSpec) (string, error) {
	r.limiter.Wait(ctx)
	input := &elbv2sdk.CreateRuleInput{
		ListenerArn: aws.String(listenerArn),
		Priority:    aws.Int32(int32(spec.Priority)),
		Conditions:  toAWSConditions(spec.Conditions),
		Actions:     toAWSActions(spec.Actions),
	}
	if len(spec.Tags) > 0 {
		input.Tags = toELBTags(spec.Tags)
	}
	out, err := r.client.CreateRule(ctx, input)
	if err != nil {
		return "", err
	}
	if len(out.Rules) == 0 {
		return "", fmt.Errorf("CreateRule returned no rules")
	}
	return aws.ToString(out.Rules[0].RuleArn), nil
}

func (r *realListenerRuleAPI) DescribeRule(ctx context.Context, ruleArn string) (ObservedState, error) {
	r.limiter.Wait(ctx)
	out, err := r.client.DescribeRules(ctx, &elbv2sdk.DescribeRulesInput{RuleArns: []string{ruleArn}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Rules) == 0 {
		return ObservedState{}, fmt.Errorf("RuleNotFound: %s", ruleArn)
	}
	return r.buildObservedState(ctx, out.Rules[0])
}

func (r *realListenerRuleAPI) FindRuleByPriority(ctx context.Context, listenerArn string, priority int) (ObservedState, error) {
	rules, err := r.ListRules(ctx, listenerArn)
	if err != nil {
		return ObservedState{}, err
	}
	for _, rule := range rules {
		if rule.Priority == priority {
			return rule, nil
		}
	}
	return ObservedState{}, fmt.Errorf("RuleNotFound: no rule with priority %d on listener %s", priority, listenerArn)
}

func (r *realListenerRuleAPI) ListRules(ctx context.Context, listenerArn string) ([]ObservedState, error) {
	r.limiter.Wait(ctx)
	out, err := r.client.DescribeRules(ctx, &elbv2sdk.DescribeRulesInput{ListenerArn: aws.String(listenerArn)})
	if err != nil {
		return nil, err
	}
	var rules []ObservedState
	for _, rule := range out.Rules {
		obs, buildErr := r.buildObservedState(ctx, rule)
		if buildErr != nil {
			return nil, buildErr
		}
		rules = append(rules, obs)
	}
	return rules, nil
}

func (r *realListenerRuleAPI) DeleteRule(ctx context.Context, ruleArn string) error {
	r.limiter.Wait(ctx)
	_, err := r.client.DeleteRule(ctx, &elbv2sdk.DeleteRuleInput{RuleArn: aws.String(ruleArn)})
	return err
}

func (r *realListenerRuleAPI) ModifyRule(ctx context.Context, ruleArn string, conditions []RuleCondition, actions []RuleAction) error {
	r.limiter.Wait(ctx)
	input := &elbv2sdk.ModifyRuleInput{
		RuleArn:    aws.String(ruleArn),
		Conditions: toAWSConditions(conditions),
		Actions:    toAWSActions(actions),
	}
	_, err := r.client.ModifyRule(ctx, input)
	return err
}

func (r *realListenerRuleAPI) SetRulePriorities(ctx context.Context, ruleArn string, priority int) error {
	r.limiter.Wait(ctx)
	_, err := r.client.SetRulePriorities(ctx, &elbv2sdk.SetRulePrioritiesInput{
		RulePriorities: []elbv2types.RulePriorityPair{
			{RuleArn: aws.String(ruleArn), Priority: aws.Int32(int32(priority))},
		},
	})
	return err
}

func (r *realListenerRuleAPI) UpdateTags(ctx context.Context, ruleArn string, desired map[string]string) error {
	r.limiter.Wait(ctx)
	existing, err := r.describeTags(ctx, ruleArn)
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
			ResourceArns: []string{ruleArn}, TagKeys: keysToRemove,
		}); removeErr != nil {
			return removeErr
		}
	}
	if len(desired) > 0 {
		r.limiter.Wait(ctx)
		if _, addErr := r.client.AddTags(ctx, &elbv2sdk.AddTagsInput{
			ResourceArns: []string{ruleArn}, Tags: toELBTags(desired),
		}); addErr != nil {
			return addErr
		}
	}
	return nil
}

func (r *realListenerRuleAPI) buildObservedState(ctx context.Context, rule elbv2types.Rule) (ObservedState, error) {
	arn := aws.ToString(rule.RuleArn)
	tags, err := r.describeTags(ctx, arn)
	if err != nil {
		return ObservedState{}, err
	}
	priority := parsePriority(aws.ToString(rule.Priority))
	// Extract listener ARN from rule ARN: arn:aws:...:listener-rule/app/name/lb-id/listener-id/rule-id
	// → arn:aws:...:listener/app/name/lb-id/listener-id
	listenerArn := extractListenerArnFromRuleArn(arn)
	return ObservedState{
		RuleArn:     arn,
		ListenerArn: listenerArn,
		Priority:    priority,
		IsDefault:   aws.ToBool(rule.IsDefault),
		Conditions:  fromAWSConditions(rule.Conditions),
		Actions:     fromAWSActions(rule.Actions),
		Tags:        tags,
	}, nil
}

func (r *realListenerRuleAPI) describeTags(ctx context.Context, arn string) (map[string]string, error) {
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

// toAWSConditions converts our condition types to AWS SDK types.
func toAWSConditions(conditions []RuleCondition) []elbv2types.RuleCondition {
	out := make([]elbv2types.RuleCondition, 0, len(conditions))
	for _, c := range conditions {
		rc := elbv2types.RuleCondition{Field: aws.String(c.Field)}
		switch c.Field {
		case "path-pattern":
			rc.PathPatternConfig = &elbv2types.PathPatternConditionConfig{Values: c.Values}
		case "host-header":
			rc.HostHeaderConfig = &elbv2types.HostHeaderConditionConfig{Values: c.Values}
		case "http-header":
			if c.HttpHeaderConfig != nil {
				rc.HttpHeaderConfig = &elbv2types.HttpHeaderConditionConfig{
					HttpHeaderName: aws.String(c.HttpHeaderConfig.Name),
					Values:         c.HttpHeaderConfig.Values,
				}
			}
		case "query-string":
			if c.QueryStringConfig != nil {
				kvs := make([]elbv2types.QueryStringKeyValuePair, 0, len(c.QueryStringConfig.Values))
				for _, kv := range c.QueryStringConfig.Values {
					kvs = append(kvs, elbv2types.QueryStringKeyValuePair{
						Key:   aws.String(kv.Key),
						Value: aws.String(kv.Value),
					})
				}
				rc.QueryStringConfig = &elbv2types.QueryStringConditionConfig{Values: kvs}
			}
		case "source-ip":
			rc.SourceIpConfig = &elbv2types.SourceIpConditionConfig{Values: c.Values}
		case "http-request-method":
			rc.HttpRequestMethodConfig = &elbv2types.HttpRequestMethodConditionConfig{Values: c.Values}
		}
		out = append(out, rc)
	}
	return out
}

// fromAWSConditions converts AWS SDK conditions back to our types.
func fromAWSConditions(conditions []elbv2types.RuleCondition) []RuleCondition {
	out := make([]RuleCondition, 0, len(conditions))
	for _, c := range conditions {
		rc := RuleCondition{Field: aws.ToString(c.Field)}
		switch rc.Field {
		case "path-pattern":
			if c.PathPatternConfig != nil {
				rc.Values = c.PathPatternConfig.Values
			}
		case "host-header":
			if c.HostHeaderConfig != nil {
				rc.Values = c.HostHeaderConfig.Values
			}
		case "http-header":
			if c.HttpHeaderConfig != nil {
				rc.HttpHeaderConfig = &HttpHeaderConfig{
					Name:   aws.ToString(c.HttpHeaderConfig.HttpHeaderName),
					Values: c.HttpHeaderConfig.Values,
				}
			}
		case "query-string":
			if c.QueryStringConfig != nil {
				kvs := make([]QueryStringKV, 0, len(c.QueryStringConfig.Values))
				for _, kv := range c.QueryStringConfig.Values {
					kvs = append(kvs, QueryStringKV{
						Key:   aws.ToString(kv.Key),
						Value: aws.ToString(kv.Value),
					})
				}
				rc.QueryStringConfig = &QueryStringConfig{Values: kvs}
			}
		case "source-ip":
			if c.SourceIpConfig != nil {
				rc.Values = c.SourceIpConfig.Values
			}
		case "http-request-method":
			if c.HttpRequestMethodConfig != nil {
				rc.Values = c.HttpRequestMethodConfig.Values
			}
		}
		out = append(out, rc)
	}
	return out
}

// toAWSActions converts our action types to AWS SDK types.
func toAWSActions(actions []RuleAction) []elbv2types.Action {
	out := make([]elbv2types.Action, 0, len(actions))
	for i, a := range actions {
		order := a.Order
		if order == 0 {
			order = i + 1
		}
		action := elbv2types.Action{
			Type:  elbv2types.ActionTypeEnum(a.Type),
			Order: aws.Int32(int32(order)),
		}
		switch a.Type {
		case "forward":
			if a.ForwardConfig != nil {
				fc := &elbv2types.ForwardActionConfig{}
				for _, tg := range a.ForwardConfig.TargetGroups {
					fc.TargetGroups = append(fc.TargetGroups, elbv2types.TargetGroupTuple{
						TargetGroupArn: aws.String(tg.TargetGroupArn),
						Weight:         aws.Int32(int32(tg.Weight)),
					})
				}
				if a.ForwardConfig.Stickiness != nil {
					fc.TargetGroupStickinessConfig = &elbv2types.TargetGroupStickinessConfig{
						Enabled:         aws.Bool(a.ForwardConfig.Stickiness.Enabled),
						DurationSeconds: aws.Int32(int32(a.ForwardConfig.Stickiness.Duration)),
					}
				}
				action.ForwardConfig = fc
			} else if a.TargetGroupArn != "" {
				action.TargetGroupArn = aws.String(a.TargetGroupArn)
			}
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

// fromAWSActions converts AWS SDK actions back to our types.
func fromAWSActions(actions []elbv2types.Action) []RuleAction {
	out := make([]RuleAction, 0, len(actions))
	for _, a := range actions {
		ra := RuleAction{
			Type:  string(a.Type),
			Order: int(aws.ToInt32(a.Order)),
		}
		switch a.Type {
		case elbv2types.ActionTypeEnumForward:
			if a.ForwardConfig != nil && len(a.ForwardConfig.TargetGroups) > 0 {
				fc := &ForwardConfig{}
				for _, tg := range a.ForwardConfig.TargetGroups {
					fc.TargetGroups = append(fc.TargetGroups, WeightedTargetGroup{
						TargetGroupArn: aws.ToString(tg.TargetGroupArn),
						Weight:         int(aws.ToInt32(tg.Weight)),
					})
				}
				if a.ForwardConfig.TargetGroupStickinessConfig != nil {
					fc.Stickiness = &ForwardStickiness{
						Enabled:  aws.ToBool(a.ForwardConfig.TargetGroupStickinessConfig.Enabled),
						Duration: int(aws.ToInt32(a.ForwardConfig.TargetGroupStickinessConfig.DurationSeconds)),
					}
				}
				ra.ForwardConfig = fc
			} else if a.TargetGroupArn != nil {
				ra.TargetGroupArn = aws.ToString(a.TargetGroupArn)
			}
		case elbv2types.ActionTypeEnumRedirect:
			if a.RedirectConfig != nil {
				ra.RedirectConfig = &RedirectConfig{
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
				ra.FixedResponseConfig = &FixedResponseConfig{
					StatusCode:  aws.ToString(a.FixedResponseConfig.StatusCode),
					ContentType: aws.ToString(a.FixedResponseConfig.ContentType),
					MessageBody: aws.ToString(a.FixedResponseConfig.MessageBody),
				}
			}
		}
		out = append(out, ra)
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

func parsePriority(s string) int {
	if s == "" || s == "default" {
		return 0
	}
	var p int
	fmt.Sscanf(s, "%d", &p)
	return p
}

func extractListenerArnFromRuleArn(ruleArn string) string {
	// Rule ARN format:  arn:aws:elasticloadbalancing:REGION:ACCOUNT:listener-rule/app/LB-NAME/LB-ID/LISTENER-ID/RULE-ID
	// Listener ARN format: arn:aws:elasticloadbalancing:REGION:ACCOUNT:listener/app/LB-NAME/LB-ID/LISTENER-ID
	return strings.Replace(
		ruleArn[:strings.LastIndex(ruleArn, "/")],
		":listener-rule/",
		":listener/",
		1,
	)
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "RuleNotFound"
	}
	return strings.Contains(err.Error(), "RuleNotFound")
}

func IsPriorityInUse(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "PriorityInUse"
	}
	return strings.Contains(err.Error(), "PriorityInUse")
}

func IsTooMany(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "TooManyRules"
	}
	return strings.Contains(err.Error(), "TooManyRules")
}

func IsTooManyConditions(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "TooManyConditionValues"
	}
	return strings.Contains(err.Error(), "TooManyConditionValues")
}

func IsTargetGroupNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "TargetGroupNotFound"
	}
	return strings.Contains(err.Error(), "TargetGroupNotFound")
}

func IsInvalidConfig(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidConfigurationRequest"
	}
	return strings.Contains(err.Error(), "InvalidConfigurationRequest")
}
