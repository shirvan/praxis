// Package nlb – aws.go
//
// This file contains the AWS API abstraction layer for AWS Network Load Balancer (NLB).
// It defines the NLBAPI interface (used for testing with mocks)
// and the real implementation that calls Elastic Load Balancing v2 through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package nlb

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

// NLBAPI abstracts all Elastic Load Balancing v2 SDK operations needed
// to manage a AWS Network Load Balancer (NLB). The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type NLBAPI interface {
	CreateNLB(ctx context.Context, spec NLBSpec) (arn, dnsName, hostedZoneId, vpcId string, err error)
	DescribeNLB(ctx context.Context, id string) (ObservedState, error)
	DeleteNLB(ctx context.Context, arn string) error
	SetSubnets(ctx context.Context, arn string, subnets []SubnetMapping) error
	SetIpAddressType(ctx context.Context, arn string, ipAddressType string) error
	ModifyAttributes(ctx context.Context, arn string, attrs map[string]string) error
	UpdateTags(ctx context.Context, arn string, desired map[string]string) error
}

type realNLBAPI struct {
	client  *elbv2sdk.Client
	limiter *ratelimit.Limiter
}

// NewNLBAPI constructs a production NLBAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewNLBAPI(client *elbv2sdk.Client) NLBAPI {
	return &realNLBAPI{client: client, limiter: ratelimit.New("nlb", 15, 8)}
}

// CreateNLB calls Elastic Load Balancing v2 to create a new AWS Network Load Balancer (NLB) from the given spec.
func (r *realNLBAPI) CreateNLB(ctx context.Context, spec NLBSpec) (string, string, string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", "", "", err
	}
	input := &elbv2sdk.CreateLoadBalancerInput{
		Name:          aws.String(spec.Name),
		Type:          elbv2types.LoadBalancerTypeEnumNetwork,
		Scheme:        elbv2types.LoadBalancerSchemeEnum(spec.Scheme),
		IpAddressType: elbv2types.IpAddressType(spec.IpAddressType),
		Tags:          toELBTags(spec.Tags),
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
	out, err := r.client.CreateLoadBalancer(ctx, input)
	if err != nil {
		return "", "", "", "", err
	}
	if len(out.LoadBalancers) == 0 {
		return "", "", "", "", fmt.Errorf("NLB creation returned no load balancers")
	}
	lb := out.LoadBalancers[0]
	arn := aws.ToString(lb.LoadBalancerArn)

	attrs := buildAttributeMap(spec)
	if len(attrs) > 0 {
		if modErr := r.ModifyAttributes(ctx, arn, attrs); modErr != nil {
			return "", "", "", "", fmt.Errorf("set NLB attributes after creation: %w", modErr)
		}
	}

	return arn, aws.ToString(lb.DNSName), aws.ToString(lb.CanonicalHostedZoneId), aws.ToString(lb.VpcId), nil
}

// DescribeNLB reads the current state of the AWS Network Load Balancer (NLB) from Elastic Load Balancing v2.
func (r *realNLBAPI) DescribeNLB(ctx context.Context, id string) (ObservedState, error) {
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
		return ObservedState{}, fmt.Errorf("NLB %s not found: LoadBalancerNotFound", id)
	}
	lb := out.LoadBalancers[0]
	if lb.Type != elbv2types.LoadBalancerTypeEnumNetwork {
		return ObservedState{}, fmt.Errorf("NLB %s is not a network load balancer", id)
	}
	arn := aws.ToString(lb.LoadBalancerArn)
	attrs, err := r.describeAttributes(ctx, arn)
	if err != nil {
		return ObservedState{}, err
	}
	tags, err := r.describeTags(ctx, arn)
	if err != nil {
		return ObservedState{}, err
	}
	subnets := make([]string, 0, len(lb.AvailabilityZones))
	for _, az := range lb.AvailabilityZones {
		subnets = append(subnets, aws.ToString(az.SubnetId))
	}
	sort.Strings(subnets)
	return ObservedState{
		LoadBalancerArn:        arn,
		DnsName:                aws.ToString(lb.DNSName),
		HostedZoneId:           aws.ToString(lb.CanonicalHostedZoneId),
		Name:                   aws.ToString(lb.LoadBalancerName),
		Scheme:                 string(lb.Scheme),
		VpcId:                  aws.ToString(lb.VpcId),
		IpAddressType:          string(lb.IpAddressType),
		Subnets:                subnets,
		CrossZoneLoadBalancing: attrs.CrossZone,
		DeletionProtection:     attrs.DeletionProtection,
		Tags:                   tags,
		State:                  string(lb.State.Code),
	}, nil
}

type nlbAttributes struct {
	DeletionProtection bool
	CrossZone          bool
}

func (r *realNLBAPI) describeAttributes(ctx context.Context, arn string) (nlbAttributes, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nlbAttributes{}, err
	}
	out, err := r.client.DescribeLoadBalancerAttributes(ctx, &elbv2sdk.DescribeLoadBalancerAttributesInput{LoadBalancerArn: aws.String(arn)})
	if err != nil {
		return nlbAttributes{}, err
	}
	var result nlbAttributes
	for _, attr := range out.Attributes {
		switch aws.ToString(attr.Key) {
		case "deletion_protection.enabled":
			result.DeletionProtection, _ = strconv.ParseBool(aws.ToString(attr.Value))
		case "load_balancing.cross_zone.enabled":
			result.CrossZone, _ = strconv.ParseBool(aws.ToString(attr.Value))
		}
	}
	return result, nil
}

func (r *realNLBAPI) describeTags(ctx context.Context, arn string) (map[string]string, error) {
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

// DeleteNLB removes the AWS Network Load Balancer (NLB) from AWS via Elastic Load Balancing v2.
func (r *realNLBAPI) DeleteNLB(ctx context.Context, arn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteLoadBalancer(ctx, &elbv2sdk.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(arn)})
	return err
}

// SetSubnets updates mutable properties of the AWS Network Load Balancer (NLB) via Elastic Load Balancing v2.
func (r *realNLBAPI) SetSubnets(ctx context.Context, arn string, subnets []SubnetMapping) error {
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

// SetIpAddressType updates mutable properties of the AWS Network Load Balancer (NLB) via Elastic Load Balancing v2.
func (r *realNLBAPI) SetIpAddressType(ctx context.Context, arn string, ipAddressType string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetIpAddressType(ctx, &elbv2sdk.SetIpAddressTypeInput{
		LoadBalancerArn: aws.String(arn),
		IpAddressType:   elbv2types.IpAddressType(ipAddressType),
	})
	return err
}

// ModifyAttributes updates mutable properties of the AWS Network Load Balancer (NLB) via Elastic Load Balancing v2.
func (r *realNLBAPI) ModifyAttributes(ctx context.Context, arn string, attrs map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	var elbAttrs []elbv2types.LoadBalancerAttribute
	for key, value := range attrs {
		elbAttrs = append(elbAttrs, elbv2types.LoadBalancerAttribute{Key: aws.String(key), Value: aws.String(value)})
	}
	_, err := r.client.ModifyLoadBalancerAttributes(ctx, &elbv2sdk.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(arn),
		Attributes:      elbAttrs,
	})
	return err
}

// UpdateTags updates mutable properties of the AWS Network Load Balancer (NLB) via Elastic Load Balancing v2.
func (r *realNLBAPI) UpdateTags(ctx context.Context, arn string, desired map[string]string) error {
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
			ResourceArns: []string{arn},
			TagKeys:      keysToRemove,
		}); removeErr != nil {
			return removeErr
		}
	}
	if len(desired) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, addErr := r.client.AddTags(ctx, &elbv2sdk.AddTagsInput{
			ResourceArns: []string{arn},
			Tags:         toELBTags(desired),
		}); addErr != nil {
			return addErr
		}
	}
	return nil
}

func buildAttributeMap(spec NLBSpec) map[string]string {
	return map[string]string{
		"deletion_protection.enabled":       strconv.FormatBool(spec.DeletionProtection),
		"load_balancing.cross_zone.enabled": strconv.FormatBool(spec.CrossZoneLoadBalancing),
	}
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

func normalizeSubnets(mappings []SubnetMapping) []string {
	out := make([]string, len(mappings))
	for i, m := range mappings {
		out[i] = m.SubnetId
	}
	sort.Strings(out)
	return out
}

func subnetsToMappings(subnets []string) []SubnetMapping {
	out := make([]SubnetMapping, len(subnets))
	for i, s := range subnets {
		out[i] = SubnetMapping{SubnetId: s}
	}
	return out
}

func resolveSubnetMappings(spec NLBSpec) []SubnetMapping {
	if len(spec.SubnetMappings) > 0 {
		return spec.SubnetMappings
	}
	return subnetsToMappings(spec.Subnets)
}

func resolveSubnets(spec NLBSpec) []string {
	if len(spec.SubnetMappings) > 0 {
		return normalizeSubnets(spec.SubnetMappings)
	}
	out := make([]string, len(spec.Subnets))
	copy(out, spec.Subnets)
	sort.Strings(out)
	return out
}

// IsNotFound returns true if the AWS error indicates the AWS Network Load Balancer (NLB) does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "LoadBalancerNotFound")
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
