package route53healthcheck

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	route53sdk "github.com/aws/aws-sdk-go-v2/service/route53"
	route53types "github.com/aws/aws-sdk-go-v2/service/route53/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// HealthCheckAPI defines the interface for all Route53 health check operations.
// Includes creation, describe, update (with version-based optimistic concurrency), tag management, and deletion.
type HealthCheckAPI interface {
	CreateHealthCheck(ctx context.Context, spec HealthCheckSpec) (string, error)
	DescribeHealthCheck(ctx context.Context, healthCheckID string) (ObservedState, error)
	UpdateHealthCheck(ctx context.Context, healthCheckID string, observed ObservedState, desired HealthCheckSpec) error
	UpdateTags(ctx context.Context, healthCheckID string, tags map[string]string) error
	DeleteHealthCheck(ctx context.Context, healthCheckID string) error
}

type realHealthCheckAPI struct {
	client  *route53sdk.Client
	limiter *ratelimit.Limiter
}

// NewHealthCheckAPI constructs a production HealthCheckAPI with Route53 rate limiting.
func NewHealthCheckAPI(client *route53sdk.Client) HealthCheckAPI {
	return &realHealthCheckAPI{client: client, limiter: ratelimit.New("route53", 5, 3)}
}

func (r *realHealthCheckAPI) CreateHealthCheck(ctx context.Context, spec HealthCheckSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	callerReference := spec.ManagedKey
	if callerReference == "" {
		callerReference = spec.CloudWatchAlarmName
	}
	if callerReference == "" {
		callerReference = strings.ToLower(spec.Type)
	}
	out, err := r.client.CreateHealthCheck(ctx, &route53sdk.CreateHealthCheckInput{
		CallerReference:   aws.String(callerReference),
		HealthCheckConfig: buildHealthCheckConfig(spec),
	})
	if err != nil {
		return "", err
	}
	healthCheckID := aws.ToString(out.HealthCheck.Id)
	if err := r.UpdateTags(ctx, healthCheckID, spec.Tags); err != nil {
		return "", err
	}
	return healthCheckID, nil
}

func (r *realHealthCheckAPI) DescribeHealthCheck(ctx context.Context, healthCheckID string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetHealthCheck(ctx, &route53sdk.GetHealthCheckInput{HealthCheckId: aws.String(healthCheckID)})
	if err != nil {
		return ObservedState{}, err
	}
	tags, err := r.listTags(ctx, healthCheckID)
	if err != nil {
		return ObservedState{}, err
	}
	observed := fromHealthCheck(out.HealthCheck)
	observed.Tags = tags
	return normalizeObservedState(observed), nil
}

// UpdateHealthCheck calls the Route53 UpdateHealthCheck API using version-based optimistic
// concurrency (observed.Version). Computes resetElements to clear fields no longer set.
func (r *realHealthCheckAPI) UpdateHealthCheck(ctx context.Context, healthCheckID string, observed ObservedState, desired HealthCheckSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &route53sdk.UpdateHealthCheckInput{
		HealthCheckId:      aws.String(healthCheckID),
		HealthCheckVersion: aws.Int64(observed.Version),
		Disabled:           aws.Bool(desired.Disabled),
		EnableSNI:          aws.Bool(desired.EnableSNI),
		FailureThreshold:   aws.Int32(desired.FailureThreshold),
		HealthThreshold:    aws.Int32(desired.HealthThreshold),
		Inverted:           aws.Bool(desired.InvertHealthCheck),
		Port:               aws.Int32(desired.Port),
	}
	if desired.IPAddress != "" {
		input.IPAddress = aws.String(desired.IPAddress)
	}
	if desired.FQDN != "" {
		input.FullyQualifiedDomainName = aws.String(desired.FQDN)
	}
	if desired.ResourcePath != "" {
		input.ResourcePath = aws.String(desired.ResourcePath)
	}
	if desired.SearchString != "" {
		input.SearchString = aws.String(desired.SearchString)
	}
	if len(desired.ChildHealthChecks) > 0 {
		input.ChildHealthChecks = append([]string(nil), desired.ChildHealthChecks...)
	}
	if len(desired.Regions) > 0 {
		input.Regions = toHealthCheckRegions(desired.Regions)
	}
	if desired.CloudWatchAlarmName != "" && desired.CloudWatchAlarmRegion != "" {
		input.AlarmIdentifier = &route53types.AlarmIdentifier{Name: aws.String(desired.CloudWatchAlarmName), Region: route53types.CloudWatchRegion(desired.CloudWatchAlarmRegion)}
	}
	if desired.InsufficientDataHealthStatus != "" {
		input.InsufficientDataHealthStatus = route53types.InsufficientDataHealthStatus(desired.InsufficientDataHealthStatus)
	}
	input.ResetElements = resetElements(observed, desired)
	_, err := r.client.UpdateHealthCheck(ctx, input)
	return err
}

func (r *realHealthCheckAPI) UpdateTags(ctx context.Context, healthCheckID string, tags map[string]string) error {
	current, err := r.listTags(ctx, healthCheckID)
	if err != nil {
		return err
	}
	addTags := make([]route53types.Tag, 0, len(tags))
	for key, value := range tags {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		if currentValue, ok := current[key]; !ok || currentValue != value {
			addTags = append(addTags, route53types.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
	}
	removeKeys := make([]string, 0)
	for key := range current {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		if _, ok := tags[key]; !ok {
			removeKeys = append(removeKeys, key)
		}
	}
	if len(addTags) == 0 && len(removeKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.ChangeTagsForResource(ctx, &route53sdk.ChangeTagsForResourceInput{
		ResourceId:    aws.String(healthCheckID),
		ResourceType:  route53types.TagResourceTypeHealthcheck,
		AddTags:       addTags,
		RemoveTagKeys: removeKeys,
	})
	return err
}

func (r *realHealthCheckAPI) DeleteHealthCheck(ctx context.Context, healthCheckID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteHealthCheck(ctx, &route53sdk.DeleteHealthCheckInput{HealthCheckId: aws.String(healthCheckID)})
	return err
}

func (r *realHealthCheckAPI) listTags(ctx context.Context, healthCheckID string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListTagsForResource(ctx, &route53sdk.ListTagsForResourceInput{
		ResourceId:   aws.String(healthCheckID),
		ResourceType: route53types.TagResourceTypeHealthcheck,
	})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string, len(out.ResourceTagSet.Tags))
	for _, tag := range out.ResourceTagSet.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags, nil
}

func buildHealthCheckConfig(spec HealthCheckSpec) *route53types.HealthCheckConfig {
	config := &route53types.HealthCheckConfig{
		Type:             route53types.HealthCheckType(spec.Type),
		Disabled:         aws.Bool(spec.Disabled),
		EnableSNI:        aws.Bool(spec.EnableSNI),
		FailureThreshold: aws.Int32(spec.FailureThreshold),
		Inverted:         aws.Bool(spec.InvertHealthCheck),
	}
	switch spec.Type {
	case "HTTP", "HTTPS", "HTTP_STR_MATCH", "HTTPS_STR_MATCH", "TCP":
		config.Port = aws.Int32(spec.Port)
		config.RequestInterval = aws.Int32(spec.RequestInterval)
		if spec.IPAddress != "" {
			config.IPAddress = aws.String(spec.IPAddress)
		}
		if spec.FQDN != "" {
			config.FullyQualifiedDomainName = aws.String(spec.FQDN)
		}
		if spec.ResourcePath != "" {
			config.ResourcePath = aws.String(spec.ResourcePath)
		}
		if spec.SearchString != "" {
			config.SearchString = aws.String(spec.SearchString)
		}
		if len(spec.Regions) > 0 {
			config.Regions = toHealthCheckRegions(spec.Regions)
		}
	case "CALCULATED":
		config.ChildHealthChecks = append([]string(nil), spec.ChildHealthChecks...)
		config.HealthThreshold = aws.Int32(spec.HealthThreshold)
	case "CLOUDWATCH_METRIC":
		config.AlarmIdentifier = &route53types.AlarmIdentifier{Name: aws.String(spec.CloudWatchAlarmName), Region: route53types.CloudWatchRegion(spec.CloudWatchAlarmRegion)}
		config.InsufficientDataHealthStatus = route53types.InsufficientDataHealthStatus(spec.InsufficientDataHealthStatus)
	}
	return config
}

func fromHealthCheck(check *route53types.HealthCheck) ObservedState {
	if check == nil || check.HealthCheckConfig == nil {
		return ObservedState{}
	}
	config := check.HealthCheckConfig
	observed := ObservedState{
		HealthCheckId:                aws.ToString(check.Id),
		CallerReference:              aws.ToString(check.CallerReference),
		Version:                      aws.ToInt64(check.HealthCheckVersion),
		Type:                         string(config.Type),
		IPAddress:                    aws.ToString(config.IPAddress),
		Port:                         aws.ToInt32(config.Port),
		ResourcePath:                 aws.ToString(config.ResourcePath),
		FQDN:                         aws.ToString(config.FullyQualifiedDomainName),
		SearchString:                 aws.ToString(config.SearchString),
		RequestInterval:              aws.ToInt32(config.RequestInterval),
		FailureThreshold:             aws.ToInt32(config.FailureThreshold),
		ChildHealthChecks:            append([]string(nil), config.ChildHealthChecks...),
		HealthThreshold:              aws.ToInt32(config.HealthThreshold),
		Disabled:                     aws.ToBool(config.Disabled),
		InvertHealthCheck:            aws.ToBool(config.Inverted),
		EnableSNI:                    aws.ToBool(config.EnableSNI),
		Regions:                      fromHealthCheckRegions(config.Regions),
		InsufficientDataHealthStatus: string(config.InsufficientDataHealthStatus),
	}
	if config.AlarmIdentifier != nil {
		observed.CloudWatchAlarmName = aws.ToString(config.AlarmIdentifier.Name)
		observed.CloudWatchAlarmRegion = string(config.AlarmIdentifier.Region)
	}
	return observed
}

func toHealthCheckRegions(values []string) []route53types.HealthCheckRegion {
	out := make([]route53types.HealthCheckRegion, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, route53types.HealthCheckRegion(trimmed))
		}
	}
	return out
}

func fromHealthCheckRegions(values []route53types.HealthCheckRegion) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(string(value)); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func resetElements(observed ObservedState, desired HealthCheckSpec) []route53types.ResettableElementName {
	var elements []route53types.ResettableElementName
	if observed.ResourcePath != "" && desired.ResourcePath == "" {
		elements = append(elements, route53types.ResettableElementNameResourcePath)
	}
	if len(observed.Regions) > 0 && len(desired.Regions) == 0 {
		elements = append(elements, route53types.ResettableElementNameRegions)
	}
	if observed.FQDN != "" && desired.FQDN == "" {
		elements = append(elements, route53types.ResettableElementNameFullyQualifiedDomainName)
	}
	if len(observed.ChildHealthChecks) > 0 && len(desired.ChildHealthChecks) == 0 {
		elements = append(elements, route53types.ResettableElementNameChildHealthChecks)
	}
	return elements
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "NoSuchHealthCheck")
}

func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "HealthCheckAlreadyExists")
}

func IsConflict(err error) bool {
	return awserr.HasCode(err, "PriorRequestNotComplete", "HealthCheckVersionMismatch")
}

func IsInvalidInput(err error) bool {
	return awserr.HasCode(err, "InvalidInput")
}
