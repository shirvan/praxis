// Package alb – aws.go
//
// This file contains the AWS API abstraction layer for AWS Application Load Balancer (ALB).
// It defines the ALBAPI interface (used for testing with mocks)
// and the real implementation that calls Elastic Load Balancing v2 through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package alb

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2sdk "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// ALBAPI abstracts all Elastic Load Balancing v2 SDK operations needed
// to manage a AWS Application Load Balancer (ALB). The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type ALBAPI interface {
	CreateALB(ctx context.Context, spec ALBSpec) (arn, dnsName, hostedZoneId, vpcId string, err error)
	DescribeALB(ctx context.Context, id string) (ObservedState, error)
	DeleteALB(ctx context.Context, arn string) error
	SetSubnets(ctx context.Context, arn string, subnets []SubnetMapping) error
	SetSecurityGroups(ctx context.Context, arn string, securityGroups []string) error
	SetIpAddressType(ctx context.Context, arn string, ipAddressType string) error
	ModifyAttributes(ctx context.Context, arn string, attrs map[string]string) error
	UpdateTags(ctx context.Context, arn string, desired map[string]string) error
}

type realALBAPI struct {
	client  *elbv2sdk.Client
	limiter *ratelimit.Limiter
}

// NewALBAPI constructs a production ALBAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewALBAPI(client *elbv2sdk.Client) ALBAPI {
	return &realALBAPI{client: client, limiter: ratelimit.New("alb", 15, 8)}
}

// CreateALB calls Elastic Load Balancing v2 to create a new AWS Application Load Balancer (ALB) from the given spec.
func (r *realALBAPI) CreateALB(ctx context.Context, spec ALBSpec) (string, string, string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", "", "", err
	}
	input := &elbv2sdk.CreateLoadBalancerInput{
		Name:           aws.String(spec.Name),
		Type:           elbv2types.LoadBalancerTypeEnumApplication,
		Scheme:         elbv2types.LoadBalancerSchemeEnum(spec.Scheme),
		IpAddressType:  elbv2types.IpAddressType(spec.IpAddressType),
		SecurityGroups: spec.SecurityGroups,
	}
	if len(spec.SubnetMappings) > 0 {
		for _, sm := range spec.SubnetMappings {
			mapping := elbv2types.SubnetMapping{SubnetId: aws.String(sm.SubnetId)}
			if sm.AllocationId != "" {
				mapping.AllocationId = aws.String(sm.AllocationId)
			}
			input.SubnetMappings = append(input.SubnetMappings, mapping)
		}
	} else {
		input.Subnets = spec.Subnets
	}
	if len(spec.Tags) > 0 {
		input.Tags = toELBTags(spec.Tags)
	}
	out, err := r.client.CreateLoadBalancer(ctx, input)
	if err != nil {
		return "", "", "", "", err
	}
	if len(out.LoadBalancers) == 0 {
		return "", "", "", "", fmt.Errorf("create ALB %q returned no load balancers", spec.Name)
	}
	lb := out.LoadBalancers[0]
	arn := aws.ToString(lb.LoadBalancerArn)
	// Set attributes after creation
	attrs := buildAttributeMap(spec)
	if len(attrs) > 0 {
		if err := r.ModifyAttributes(ctx, arn, attrs); err != nil {
			return "", "", "", "", err
		}
	}
	return arn,
		aws.ToString(lb.DNSName),
		aws.ToString(lb.CanonicalHostedZoneId),
		aws.ToString(lb.VpcId),
		nil
}

// DescribeALB reads the current state of the AWS Application Load Balancer (ALB) from Elastic Load Balancing v2.
func (r *realALBAPI) DescribeALB(ctx context.Context, id string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	input := &elbv2sdk.DescribeLoadBalancersInput{}
	if strings.HasPrefix(id, "arn:") {
		input.LoadBalancerArns = []string{id}
	} else {
		input.Names = []string{id}
	}
	out, err := r.client.DescribeLoadBalancers(ctx, input)
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.LoadBalancers) == 0 {
		return ObservedState{}, awserr.NotFound(fmt.Sprintf("ALB %s not found", id))
	}
	lb := out.LoadBalancers[0]
	arn := aws.ToString(lb.LoadBalancerArn)

	// Get attributes
	attrState, err := r.describeAttributes(ctx, arn)
	if err != nil {
		return ObservedState{}, err
	}
	// Get tags
	tags, err := r.describeTags(ctx, arn)
	if err != nil {
		return ObservedState{}, err
	}
	// Extract subnets and security groups
	subnets := make([]string, 0, len(lb.AvailabilityZones))
	for _, az := range lb.AvailabilityZones {
		subnets = append(subnets, aws.ToString(az.SubnetId))
	}
	sort.Strings(subnets)
	sgs := make([]string, len(lb.SecurityGroups))
	copy(sgs, lb.SecurityGroups)
	sort.Strings(sgs)

	return ObservedState{
		LoadBalancerArn:    arn,
		DnsName:            aws.ToString(lb.DNSName),
		HostedZoneId:       aws.ToString(lb.CanonicalHostedZoneId),
		Name:               aws.ToString(lb.LoadBalancerName),
		Scheme:             string(lb.Scheme),
		VpcId:              aws.ToString(lb.VpcId),
		IpAddressType:      string(lb.IpAddressType),
		Subnets:            subnets,
		SecurityGroups:     sgs,
		AccessLogs:         attrState.AccessLogs,
		DeletionProtection: attrState.DeletionProtection,
		IdleTimeout:        attrState.IdleTimeout,
		State:              string(lb.State.Code),
		Tags:               tags,
	}, nil
}

// DeleteALB removes the AWS Application Load Balancer (ALB) from AWS via Elastic Load Balancing v2.
func (r *realALBAPI) DeleteALB(ctx context.Context, arn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteLoadBalancer(ctx, &elbv2sdk.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(arn)})
	return err
}

// SetSubnets updates mutable properties of the AWS Application Load Balancer (ALB) via Elastic Load Balancing v2.
func (r *realALBAPI) SetSubnets(ctx context.Context, arn string, subnets []SubnetMapping) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &elbv2sdk.SetSubnetsInput{LoadBalancerArn: aws.String(arn)}
	for _, sm := range subnets {
		mapping := elbv2types.SubnetMapping{SubnetId: aws.String(sm.SubnetId)}
		if sm.AllocationId != "" {
			mapping.AllocationId = aws.String(sm.AllocationId)
		}
		input.SubnetMappings = append(input.SubnetMappings, mapping)
	}
	_, err := r.client.SetSubnets(ctx, input)
	return err
}

// SetSecurityGroups updates mutable properties of the AWS Application Load Balancer (ALB) via Elastic Load Balancing v2.
func (r *realALBAPI) SetSecurityGroups(ctx context.Context, arn string, securityGroups []string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetSecurityGroups(ctx, &elbv2sdk.SetSecurityGroupsInput{
		LoadBalancerArn: aws.String(arn),
		SecurityGroups:  securityGroups,
	})
	return err
}

// SetIpAddressType updates mutable properties of the AWS Application Load Balancer (ALB) via Elastic Load Balancing v2.
func (r *realALBAPI) SetIpAddressType(ctx context.Context, arn string, ipAddressType string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetIpAddressType(ctx, &elbv2sdk.SetIpAddressTypeInput{
		LoadBalancerArn: aws.String(arn),
		IpAddressType:   elbv2types.IpAddressType(ipAddressType),
	})
	return err
}

// ModifyAttributes updates mutable properties of the AWS Application Load Balancer (ALB) via Elastic Load Balancing v2.
func (r *realALBAPI) ModifyAttributes(ctx context.Context, arn string, attrs map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	var attributes []elbv2types.LoadBalancerAttribute
	for key, value := range attrs {
		attributes = append(attributes, elbv2types.LoadBalancerAttribute{Key: aws.String(key), Value: aws.String(value)})
	}
	_, err := r.client.ModifyLoadBalancerAttributes(ctx, &elbv2sdk.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(arn),
		Attributes:      attributes,
	})
	return err
}

// UpdateTags updates mutable properties of the AWS Application Load Balancer (ALB) via Elastic Load Balancing v2.
func (r *realALBAPI) UpdateTags(ctx context.Context, arn string, desired map[string]string) error {
	existing, err := r.describeTags(ctx, arn)
	if err != nil {
		return err
	}
	filteredExisting := drivers.FilterPraxisTags(existing)
	filteredDesired := drivers.FilterPraxisTags(desired)
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
	_, err = r.client.AddTags(ctx, &elbv2sdk.AddTagsInput{ResourceArns: []string{arn}, Tags: toELBTags(filteredDesired)})
	return err
}

type describeAttributeResult struct {
	AccessLogs         *AccessLogConfig
	DeletionProtection bool
	IdleTimeout        int
}

func (r *realALBAPI) describeAttributes(ctx context.Context, arn string) (describeAttributeResult, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return describeAttributeResult{}, err
	}
	out, err := r.client.DescribeLoadBalancerAttributes(ctx, &elbv2sdk.DescribeLoadBalancerAttributesInput{LoadBalancerArn: aws.String(arn)})
	if err != nil {
		return describeAttributeResult{}, err
	}
	result := describeAttributeResult{IdleTimeout: 60}
	var accessLogEnabled bool
	var accessLogBucket, accessLogPrefix string
	for _, attr := range out.Attributes {
		key := aws.ToString(attr.Key)
		value := aws.ToString(attr.Value)
		switch key {
		case "deletion_protection.enabled":
			result.DeletionProtection = value == "true"
		case "idle_timeout.timeout_seconds":
			if parsed, parseErr := strconv.Atoi(value); parseErr == nil {
				result.IdleTimeout = parsed
			}
		case "access_logs.s3.enabled":
			accessLogEnabled = value == "true"
		case "access_logs.s3.bucket":
			accessLogBucket = value
		case "access_logs.s3.prefix":
			accessLogPrefix = value
		}
	}
	if accessLogEnabled || accessLogBucket != "" {
		result.AccessLogs = &AccessLogConfig{
			Enabled: accessLogEnabled,
			Bucket:  accessLogBucket,
			Prefix:  accessLogPrefix,
		}
	}
	return result, nil
}

func (r *realALBAPI) describeTags(ctx context.Context, arn string) (map[string]string, error) {
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

// buildAttributeMap constructs the LB attribute key-value pairs from the spec.
func buildAttributeMap(spec ALBSpec) map[string]string {
	attrs := map[string]string{
		"deletion_protection.enabled":  strconv.FormatBool(spec.DeletionProtection),
		"idle_timeout.timeout_seconds": strconv.Itoa(spec.IdleTimeout),
	}
	if spec.AccessLogs != nil {
		attrs["access_logs.s3.enabled"] = strconv.FormatBool(spec.AccessLogs.Enabled)
		attrs["access_logs.s3.bucket"] = spec.AccessLogs.Bucket
		if spec.AccessLogs.Prefix != "" {
			attrs["access_logs.s3.prefix"] = spec.AccessLogs.Prefix
		}
	}
	return attrs
}

func toELBTags(tags map[string]string) []elbv2types.Tag {
	out := make([]elbv2types.Tag, 0, len(tags))
	for key, value := range tags {
		out = append(out, elbv2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return out
}

// normalizeSubnets converts subnet mappings to a sorted list of subnet IDs.
func normalizeSubnets(mappings []SubnetMapping) []string {
	out := make([]string, 0, len(mappings))
	for _, m := range mappings {
		out = append(out, m.SubnetId)
	}
	sort.Strings(out)
	return out
}

// subnetsToMappings converts a plain subnet slice to SubnetMapping slice.
func subnetsToMappings(subnets []string) []SubnetMapping {
	out := make([]SubnetMapping, 0, len(subnets))
	for _, s := range subnets {
		out = append(out, SubnetMapping{SubnetId: s})
	}
	return out
}

// Error classification

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "LoadBalancerNotFound") || awserr.IsNotFoundErr(err)
}

// IsDuplicate returns true if the AWS error indicates a naming conflict.
func IsDuplicate(err error) bool {
	return awserr.HasCode(err, "DuplicateLoadBalancerName")
}

// IsResourceInUse returns true if the resource cannot be deleted because it is still referenced.
func IsResourceInUse(err error) bool {
	return awserr.HasCode(err, "ResourceInUse", "OperationNotPermitted")
}

// IsTooMany returns true if the AWS error indicates a service quota has been reached.
func IsTooMany(err error) bool {
	return awserr.HasCode(err, "TooManyLoadBalancers")
}

// IsInvalidConfig returns true if the AWS error indicates an invalid configuration.
func IsInvalidConfig(err error) bool {
	return awserr.HasCode(err, "InvalidConfigurationRequest")
}
