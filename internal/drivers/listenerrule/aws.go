// Package listenerrule – aws.go
//
// This file contains the AWS API abstraction layer for AWS ELBv2 Listener Rule.
// It defines the ListenerRuleAPI interface (used for testing with mocks)
// and the real implementation that calls Elastic Load Balancing v2 through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package listenerrule

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

// ListenerRuleAPI abstracts all Elastic Load Balancing v2 SDK operations needed
// to manage a AWS ELBv2 Listener Rule. The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
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

// NewListenerRuleAPI constructs a production ListenerRuleAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewListenerRuleAPI(client *elbv2sdk.Client) ListenerRuleAPI {
	return &realListenerRuleAPI{client: client, limiter: ratelimit.New("listener-rule", 15, 8)}
}

// CreateRule calls Elastic Load Balancing v2 to create a new AWS ELBv2 Listener Rule from the given spec.
func (r *realListenerRuleAPI) CreateRule(ctx context.Context, listenerArn string, spec ListenerRuleSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	input := &elbv2sdk.CreateRuleInput{
		ListenerArn: aws.String(listenerArn),
		Priority:    aws.Int32(int32(spec.Priority)), //nolint:gosec // G115: listener rule priority is bounded to valid range
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

// DescribeRule reads the current state of the AWS ELBv2 Listener Rule from Elastic Load Balancing v2.
func (r *realListenerRuleAPI) DescribeRule(ctx context.Context, ruleArn string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeRules(ctx, &elbv2sdk.DescribeRulesInput{RuleArns: []string{ruleArn}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Rules) == 0 {
		return ObservedState{}, fmt.Errorf("RuleNotFound: %s", ruleArn)
	}
	return r.buildObservedState(ctx, out.Rules[0])
}

// FindRuleByPriority searches for the AWS ELBv2 Listener Rule using alternative identifiers.
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

// ListRules enumerates AWS ELBv2 Listener Rule resources from Elastic Load Balancing v2.
func (r *realListenerRuleAPI) ListRules(ctx context.Context, listenerArn string) ([]ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
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

// DeleteRule removes the AWS ELBv2 Listener Rule from AWS via Elastic Load Balancing v2.
func (r *realListenerRuleAPI) DeleteRule(ctx context.Context, ruleArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRule(ctx, &elbv2sdk.DeleteRuleInput{RuleArn: aws.String(ruleArn)})
	return err
}

// ModifyRule updates mutable properties of the AWS ELBv2 Listener Rule via Elastic Load Balancing v2.
func (r *realListenerRuleAPI) ModifyRule(ctx context.Context, ruleArn string, conditions []RuleCondition, actions []RuleAction) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &elbv2sdk.ModifyRuleInput{
		RuleArn:    aws.String(ruleArn),
		Conditions: toAWSConditions(conditions),
		Actions:    toAWSActions(actions),
	}
	_, err := r.client.ModifyRule(ctx, input)
	return err
}

// SetRulePriorities updates mutable properties of the AWS ELBv2 Listener Rule via Elastic Load Balancing v2.
func (r *realListenerRuleAPI) SetRulePriorities(ctx context.Context, ruleArn string, priority int) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetRulePriorities(ctx, &elbv2sdk.SetRulePrioritiesInput{
		RulePriorities: []elbv2types.RulePriorityPair{
			{RuleArn: aws.String(ruleArn), Priority: aws.Int32(int32(priority))}, //nolint:gosec // G115: priority is bounded to valid range
		},
	})
	return err
}

// UpdateTags updates mutable properties of the AWS ELBv2 Listener Rule via Elastic Load Balancing v2.
func (r *realListenerRuleAPI) UpdateTags(ctx context.Context, ruleArn string, desired map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
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
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, removeErr := r.client.RemoveTags(ctx, &elbv2sdk.RemoveTagsInput{
			ResourceArns: []string{ruleArn}, TagKeys: keysToRemove,
		}); removeErr != nil {
			return removeErr
		}
	}
	if len(desired) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
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
						Weight:         aws.Int32(int32(tg.Weight)), //nolint:gosec // G115: weight is bounded to valid range
					})
				}
				if a.ForwardConfig.Stickiness != nil {
					fc.TargetGroupStickinessConfig = &elbv2types.TargetGroupStickinessConfig{
						Enabled:         aws.Bool(a.ForwardConfig.Stickiness.Enabled),
						DurationSeconds: aws.Int32(int32(a.ForwardConfig.Stickiness.Duration)), //nolint:gosec // G115: stickiness duration is bounded to valid range
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
	_, _ = fmt.Sscanf(s, "%d", &p)
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

// IsNotFound returns true if the AWS error indicates the AWS ELBv2 Listener Rule does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "RuleNotFound")
}

// IsPriorityInUse returns true if a listener rule with the same priority already exists.
func IsPriorityInUse(err error) bool {
	return awserr.HasCode(err, "PriorityInUse")
}

// IsTooMany returns true if the AWS error indicates a service quota has been reached.
func IsTooMany(err error) bool {
	return awserr.HasCode(err, "TooManyRules")
}

// IsTooManyConditions returns true if the rule exceeds the condition value limit.
func IsTooManyConditions(err error) bool {
	return awserr.HasCode(err, "TooManyConditionValues")
}

// IsTargetGroupNotFound returns true if a referenced target group does not exist.
func IsTargetGroupNotFound(err error) bool {
	return awserr.HasCode(err, "TargetGroupNotFound")
}

// IsInvalidConfig returns true if the AWS error indicates an invalid configuration.
func IsInvalidConfig(err error) bool {
	return awserr.HasCode(err, "InvalidConfigurationRequest")
}
