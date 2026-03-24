package targetgroup

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2sdk "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type TargetGroupAPI interface {
	CreateTargetGroup(ctx context.Context, spec TargetGroupSpec) (TargetGroupOutputs, error)
	DescribeTargetGroup(ctx context.Context, id string) (ObservedState, error)
	DeleteTargetGroup(ctx context.Context, arn string) error
	ModifyTargetGroup(ctx context.Context, arn string, spec TargetGroupSpec) error
	UpdateAttributes(ctx context.Context, arn string, spec TargetGroupSpec) error
	UpdateTargets(ctx context.Context, arn string, desired []Target, observed []Target) error
	UpdateTags(ctx context.Context, arn string, desired map[string]string) error
}

type realTargetGroupAPI struct {
	client  *elbv2sdk.Client
	limiter *ratelimit.Limiter
}

func NewTargetGroupAPI(client *elbv2sdk.Client) TargetGroupAPI {
	return &realTargetGroupAPI{client: client, limiter: ratelimit.New("target-group", 15, 8)}
}

func (r *realTargetGroupAPI) CreateTargetGroup(ctx context.Context, spec TargetGroupSpec) (TargetGroupOutputs, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return TargetGroupOutputs{}, err
	}
	input := &elbv2sdk.CreateTargetGroupInput{
		Name:       aws.String(spec.Name),
		Port:       aws.Int32(int32(spec.Port)),
		Protocol:   elbv2types.ProtocolEnum(spec.Protocol),
		TargetType: elbv2types.TargetTypeEnum(spec.TargetType),
	}
	if spec.TargetType != "lambda" {
		input.VpcId = aws.String(spec.VpcId)
	}
	if spec.ProtocolVersion != "" {
		input.ProtocolVersion = aws.String(spec.ProtocolVersion)
	}
	applyHealthCheckInput(input, spec.HealthCheck)
	out, err := r.client.CreateTargetGroup(ctx, input)
	if err != nil {
		return TargetGroupOutputs{}, err
	}
	if len(out.TargetGroups) == 0 {
		return TargetGroupOutputs{}, fmt.Errorf("create target group %q returned no target groups", spec.Name)
	}
	group := out.TargetGroups[0]
	arn := aws.ToString(group.TargetGroupArn)
	if err := r.UpdateAttributes(ctx, arn, spec); err != nil {
		return TargetGroupOutputs{}, err
	}
	if len(spec.Tags) > 0 {
		if err := r.UpdateTags(ctx, arn, spec.Tags); err != nil {
			return TargetGroupOutputs{}, err
		}
	}
	if len(spec.Targets) > 0 {
		if err := r.UpdateTargets(ctx, arn, spec.Targets, nil); err != nil {
			return TargetGroupOutputs{}, err
		}
	}
	return TargetGroupOutputs{TargetGroupArn: arn, TargetGroupName: aws.ToString(group.TargetGroupName)}, nil
}

func (r *realTargetGroupAPI) DescribeTargetGroup(ctx context.Context, id string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	input := &elbv2sdk.DescribeTargetGroupsInput{}
	if strings.HasPrefix(id, "arn:") {
		input.TargetGroupArns = []string{id}
	} else {
		input.Names = []string{id}
	}
	out, err := r.client.DescribeTargetGroups(ctx, input)
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.TargetGroups) == 0 {
		return ObservedState{}, fmt.Errorf("target group %s not found", id)
	}
	group := out.TargetGroups[0]
	attrState, err := r.describeAttributeState(ctx, aws.ToString(group.TargetGroupArn))
	if err != nil {
		return ObservedState{}, err
	}
	tags, err := r.describeTags(ctx, aws.ToString(group.TargetGroupArn))
	if err != nil {
		return ObservedState{}, err
	}
	targets, err := r.describeTargets(ctx, aws.ToString(group.TargetGroupArn))
	if err != nil {
		return ObservedState{}, err
	}
	return ObservedState{
		TargetGroupArn:      aws.ToString(group.TargetGroupArn),
		Name:                aws.ToString(group.TargetGroupName),
		Protocol:            string(group.Protocol),
		Port:                int(aws.ToInt32(group.Port)),
		VpcId:               aws.ToString(group.VpcId),
		TargetType:          string(group.TargetType),
		ProtocolVersion:     aws.ToString(group.ProtocolVersion),
		HealthCheck:         healthCheckFromTargetGroup(group),
		DeregistrationDelay: attrState.DeregistrationDelay,
		Stickiness:          attrState.Stickiness,
		Targets:             targets,
		Tags:                tags,
	}, nil
}

func (r *realTargetGroupAPI) DeleteTargetGroup(ctx context.Context, arn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteTargetGroup(ctx, &elbv2sdk.DeleteTargetGroupInput{TargetGroupArn: aws.String(arn)})
	return err
}

func (r *realTargetGroupAPI) ModifyTargetGroup(ctx context.Context, arn string, spec TargetGroupSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &elbv2sdk.ModifyTargetGroupInput{TargetGroupArn: aws.String(arn)}
	applyHealthCheckModifyInput(input, spec.HealthCheck)
	_, err := r.client.ModifyTargetGroup(ctx, input)
	return err
}

func (r *realTargetGroupAPI) UpdateAttributes(ctx context.Context, arn string, spec TargetGroupSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	attributes := []elbv2types.TargetGroupAttribute{{Key: aws.String("deregistration_delay.timeout_seconds"), Value: aws.String(strconv.Itoa(spec.DeregistrationDelay))}}
	stickiness := spec.Stickiness
	if stickiness == nil {
		stickiness = &Stickiness{Enabled: false, Type: "lb_cookie", Duration: 86400}
	}
	attributes = append(attributes,
		elbv2types.TargetGroupAttribute{Key: aws.String("stickiness.enabled"), Value: aws.String(strconv.FormatBool(stickiness.Enabled))},
		elbv2types.TargetGroupAttribute{Key: aws.String("stickiness.type"), Value: aws.String(stickiness.Type)},
		elbv2types.TargetGroupAttribute{Key: aws.String("stickiness.lb_cookie.duration_seconds"), Value: aws.String(strconv.Itoa(stickiness.Duration))},
	)
	_, err := r.client.ModifyTargetGroupAttributes(ctx, &elbv2sdk.ModifyTargetGroupAttributesInput{TargetGroupArn: aws.String(arn), Attributes: attributes})
	return err
}

func (r *realTargetGroupAPI) UpdateTargets(ctx context.Context, arn string, desired []Target, observed []Target) error {
	add, remove := diffTargets(desired, observed)
	if len(add) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err := r.client.RegisterTargets(ctx, &elbv2sdk.RegisterTargetsInput{TargetGroupArn: aws.String(arn), Targets: encodeTargets(add)})
		if err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err := r.client.DeregisterTargets(ctx, &elbv2sdk.DeregisterTargetsInput{TargetGroupArn: aws.String(arn), Targets: encodeTargets(remove)})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *realTargetGroupAPI) UpdateTags(ctx context.Context, arn string, desired map[string]string) error {
	existing, err := r.describeTags(ctx, arn)
	if err != nil {
		return err
	}
	filteredExisting := filterPraxisTags(existing)
	filteredDesired := filterPraxisTags(desired)
	var removeKeys []string
	for key := range filteredExisting {
		if _, ok := filteredDesired[key]; !ok {
			removeKeys = append(removeKeys, key)
		}
	}
	if len(removeKeys) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err := r.client.RemoveTags(ctx, &elbv2sdk.RemoveTagsInput{ResourceArns: []string{arn}, TagKeys: removeKeys})
		if err != nil {
			return err
		}
	}
	if len(filteredDesired) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	tags := make([]elbv2types.Tag, 0, len(filteredDesired))
	for key, value := range filteredDesired {
		tags = append(tags, elbv2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	_, err = r.client.AddTags(ctx, &elbv2sdk.AddTagsInput{ResourceArns: []string{arn}, Tags: tags})
	return err
}

func (r *realTargetGroupAPI) describeAttributeState(ctx context.Context, arn string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeTargetGroupAttributes(ctx, &elbv2sdk.DescribeTargetGroupAttributesInput{TargetGroupArn: aws.String(arn)})
	if err != nil {
		return ObservedState{}, err
	}
	state := ObservedState{DeregistrationDelay: 300}
	for _, attr := range out.Attributes {
		key := aws.ToString(attr.Key)
		value := aws.ToString(attr.Value)
		switch key {
		case "deregistration_delay.timeout_seconds":
			if parsed, parseErr := strconv.Atoi(value); parseErr == nil {
				state.DeregistrationDelay = parsed
			}
		case "stickiness.enabled":
			if state.Stickiness == nil {
				state.Stickiness = &Stickiness{}
			}
			state.Stickiness.Enabled = value == "true"
		case "stickiness.type":
			if state.Stickiness == nil {
				state.Stickiness = &Stickiness{}
			}
			state.Stickiness.Type = value
		case "stickiness.lb_cookie.duration_seconds":
			if state.Stickiness == nil {
				state.Stickiness = &Stickiness{}
			}
			if parsed, parseErr := strconv.Atoi(value); parseErr == nil {
				state.Stickiness.Duration = parsed
			}
		}
	}
	if state.Stickiness != nil && state.Stickiness.Type == "" {
		state.Stickiness.Type = "lb_cookie"
	}
	return state, nil
}

func (r *realTargetGroupAPI) describeTags(ctx context.Context, arn string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.DescribeTags(ctx, &elbv2sdk.DescribeTagsInput{ResourceArns: []string{arn}})
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for _, desc := range out.TagDescriptions {
		for _, tag := range desc.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}
	return tags, nil
}

func (r *realTargetGroupAPI) describeTargets(ctx context.Context, arn string) ([]Target, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.DescribeTargetHealth(ctx, &elbv2sdk.DescribeTargetHealthInput{TargetGroupArn: aws.String(arn)})
	if err != nil {
		return nil, err
	}
	targets := make([]Target, 0, len(out.TargetHealthDescriptions))
	for _, desc := range out.TargetHealthDescriptions {
		targets = append(targets, Target{ID: aws.ToString(desc.Target.Id), Port: int(aws.ToInt32(desc.Target.Port)), AvailabilityZone: aws.ToString(desc.Target.AvailabilityZone)})
	}
	sort.Slice(targets, func(i, j int) bool { return targetKey(targets[i]) < targetKey(targets[j]) })
	return targets, nil
}

func applyHealthCheckInput(input *elbv2sdk.CreateTargetGroupInput, hc HealthCheck) {
	hc = healthCheckWithDefaults(hc)
	input.HealthCheckEnabled = aws.Bool(true)
	input.HealthCheckProtocol = elbv2types.ProtocolEnum(hc.Protocol)
	input.HealthCheckPort = aws.String(hc.Port)
	if hc.Path != "" {
		input.HealthCheckPath = aws.String(hc.Path)
	}
	input.HealthyThresholdCount = aws.Int32(hc.HealthyThreshold)
	input.UnhealthyThresholdCount = aws.Int32(hc.UnhealthyThreshold)
	input.HealthCheckIntervalSeconds = aws.Int32(hc.Interval)
	input.HealthCheckTimeoutSeconds = aws.Int32(hc.Timeout)
	if hc.Matcher != "" {
		input.Matcher = &elbv2types.Matcher{HttpCode: aws.String(hc.Matcher)}
	}
}

func applyHealthCheckModifyInput(input *elbv2sdk.ModifyTargetGroupInput, hc HealthCheck) {
	hc = healthCheckWithDefaults(hc)
	input.HealthCheckProtocol = elbv2types.ProtocolEnum(hc.Protocol)
	input.HealthCheckPort = aws.String(hc.Port)
	if hc.Path != "" {
		input.HealthCheckPath = aws.String(hc.Path)
	}
	input.HealthyThresholdCount = aws.Int32(hc.HealthyThreshold)
	input.UnhealthyThresholdCount = aws.Int32(hc.UnhealthyThreshold)
	input.HealthCheckIntervalSeconds = aws.Int32(hc.Interval)
	input.HealthCheckTimeoutSeconds = aws.Int32(hc.Timeout)
	if hc.Matcher != "" {
		input.Matcher = &elbv2types.Matcher{HttpCode: aws.String(hc.Matcher)}
	}
}

func healthCheckFromTargetGroup(group elbv2types.TargetGroup) HealthCheck {
	hc := HealthCheck{
		Protocol:           string(group.HealthCheckProtocol),
		Path:               aws.ToString(group.HealthCheckPath),
		Port:               aws.ToString(group.HealthCheckPort),
		HealthyThreshold:   aws.ToInt32(group.HealthyThresholdCount),
		UnhealthyThreshold: aws.ToInt32(group.UnhealthyThresholdCount),
		Interval:           aws.ToInt32(group.HealthCheckIntervalSeconds),
		Timeout:            aws.ToInt32(group.HealthCheckTimeoutSeconds),
	}
	if group.Matcher != nil {
		hc.Matcher = aws.ToString(group.Matcher.HttpCode)
	}
	return healthCheckWithDefaults(hc)
}

func healthCheckWithDefaults(hc HealthCheck) HealthCheck {
	if hc.Protocol == "" {
		hc.Protocol = "HTTP"
	}
	if hc.Port == "" {
		hc.Port = "traffic-port"
	}
	if hc.HealthyThreshold == 0 {
		hc.HealthyThreshold = 5
	}
	if hc.UnhealthyThreshold == 0 {
		hc.UnhealthyThreshold = 2
	}
	if hc.Interval == 0 {
		hc.Interval = 30
	}
	if hc.Timeout == 0 {
		hc.Timeout = 5
	}
	if (hc.Protocol == "HTTP" || hc.Protocol == "HTTPS") && hc.Path == "" {
		hc.Path = "/"
	}
	return hc
}

func encodeTargets(targets []Target) []elbv2types.TargetDescription {
	out := make([]elbv2types.TargetDescription, 0, len(targets))
	for _, target := range targets {
		desc := elbv2types.TargetDescription{Id: aws.String(target.ID)}
		if target.Port != 0 {
			desc.Port = aws.Int32(int32(target.Port))
		}
		if target.AvailabilityZone != "" {
			desc.AvailabilityZone = aws.String(target.AvailabilityZone)
		}
		out = append(out, desc)
	}
	return out
}

func diffTargets(desired, observed []Target) (add []Target, remove []Target) {
	desiredSet := targetSet(desired)
	observedSet := targetSet(observed)
	for key, target := range desiredSet {
		if _, ok := observedSet[key]; !ok {
			add = append(add, target)
		}
	}
	for key, target := range observedSet {
		if _, ok := desiredSet[key]; !ok {
			remove = append(remove, target)
		}
	}
	sort.Slice(add, func(i, j int) bool { return targetKey(add[i]) < targetKey(add[j]) })
	sort.Slice(remove, func(i, j int) bool { return targetKey(remove[i]) < targetKey(remove[j]) })
	return add, remove
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "TargetGroupNotFound")
}

func IsDuplicate(err error) bool {
	return awserr.HasCode(err, "DuplicateTargetGroupName")
}

func IsResourceInUse(err error) bool {
	return awserr.HasCode(err, "ResourceInUse")
}

func IsTooMany(err error) bool {
	return awserr.HasCode(err, "TooManyTargetGroups")
}

func IsInvalidConfiguration(err error) bool {
	return awserr.HasCode(err, "InvalidTarget", "ValidationError", "InvalidConfigurationRequest")
}
