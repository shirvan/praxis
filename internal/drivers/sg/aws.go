package sg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// SGAPI abstracts the AWS EC2 SDK operations that the driver uses.
// In production and integration tests, this is realSGAPI (backed by the real SDK client).
// In unit tests, this is a mock.
//
// All methods receive a plain context.Context, NOT a restate.RunContext.
// The caller in driver.go wraps these calls inside restate.Run().
type SGAPI interface {
	DescribeSecurityGroup(ctx context.Context, groupId string) (ObservedState, error)
	FindSecurityGroup(ctx context.Context, groupName, vpcId string) (ObservedState, error)
	FindByTags(ctx context.Context, tags map[string]string) (string, error)
	CreateSecurityGroup(ctx context.Context, spec SecurityGroupSpec) (string, error) // returns groupId
	DeleteSecurityGroup(ctx context.Context, groupId string) error
	AuthorizeIngress(ctx context.Context, groupId string, rules []NormalizedRule) error
	AuthorizeEgress(ctx context.Context, groupId string, rules []NormalizedRule) error
	RevokeIngress(ctx context.Context, groupId string, rules []NormalizedRule) error
	RevokeEgress(ctx context.Context, groupId string, rules []NormalizedRule) error
	UpdateTags(ctx context.Context, groupId string, tags map[string]string) error
}

// realSGAPI implements SGAPI using the actual AWS SDK v2 EC2 client.
type realSGAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

// NewSGAPI creates a new SGAPI backed by the given EC2 SDK client.
func NewSGAPI(client *ec2sdk.Client) SGAPI {
	return &realSGAPI{
		client:  client,
		limiter: ratelimit.New("ec2", 20, 10),
	}
}

// DescribeSecurityGroup returns the observed state of a security group.
func (r *realSGAPI) DescribeSecurityGroup(ctx context.Context, groupId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeSecurityGroups(ctx, &ec2sdk.DescribeSecurityGroupsInput{
		GroupIds: []string{groupId},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.SecurityGroups) == 0 {
		return ObservedState{}, fmt.Errorf("security group %s not found", groupId)
	}

	sg := out.SecurityGroups[0]

	obs := ObservedState{
		GroupId:     aws.ToString(sg.GroupId),
		GroupName:   aws.ToString(sg.GroupName),
		Description: aws.ToString(sg.Description),
		VpcId:       aws.ToString(sg.VpcId),
		OwnerId:     aws.ToString(sg.OwnerId),
		Tags:        make(map[string]string, len(sg.Tags)),
	}
	for _, tag := range sg.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	// Normalize ingress rules
	for _, perm := range sg.IpPermissions {
		for _, cidr := range perm.IpRanges {
			obs.IngressRules = append(obs.IngressRules, NormalizedRule{
				Direction: "ingress",
				Protocol:  normalizeProtocol(aws.ToString(perm.IpProtocol)),
				FromPort:  aws.ToInt32(perm.FromPort),
				ToPort:    aws.ToInt32(perm.ToPort),
				Target:    "cidr:" + aws.ToString(cidr.CidrIp),
			})
		}
	}

	// Normalize egress rules
	for _, perm := range sg.IpPermissionsEgress {
		for _, cidr := range perm.IpRanges {
			obs.EgressRules = append(obs.EgressRules, NormalizedRule{
				Direction: "egress",
				Protocol:  normalizeProtocol(aws.ToString(perm.IpProtocol)),
				FromPort:  aws.ToInt32(perm.FromPort),
				ToPort:    aws.ToInt32(perm.ToPort),
				Target:    "cidr:" + aws.ToString(cidr.CidrIp),
			})
		}
	}

	return obs, nil
}

// FindSecurityGroup locates a security group by its declarative identity.
//
// Planning needs this lookup path because desired templates know the group name
// and VPC, while the driver itself usually operates on the AWS-assigned group
// ID once provisioning has completed.
func (r *realSGAPI) FindSecurityGroup(ctx context.Context, groupName, vpcId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeSecurityGroups(ctx, &ec2sdk.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-name"), Values: []string{groupName}},
			{Name: aws.String("vpc-id"), Values: []string{vpcId}},
		},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.SecurityGroups) == 0 {
		return ObservedState{}, fmt.Errorf("security group %s in VPC %s not found", groupName, vpcId)
	}

	return r.DescribeSecurityGroup(ctx, aws.ToString(out.SecurityGroups[0].GroupId))
}

func (r *realSGAPI) FindByTags(ctx context.Context, tags map[string]string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	filters := make([]ec2types.Filter, 0, len(tags))
	for key, value := range tags {
		filters = append(filters, ec2types.Filter{Name: aws.String("tag:" + key), Values: []string{value}})
	}
	out, err := r.client.DescribeSecurityGroups(ctx, &ec2sdk.DescribeSecurityGroupsInput{Filters: filters})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, item := range out.SecurityGroups {
		if id := aws.ToString(item.GroupId); id != "" {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous lookup: %d security groups match the given tag filters", len(matches))
	}
}

// CreateSecurityGroup creates a new EC2 security group and returns its group ID.
func (r *realSGAPI) CreateSecurityGroup(ctx context.Context, spec SecurityGroupSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.CreateSecurityGroup(ctx, &ec2sdk.CreateSecurityGroupInput{
		GroupName:   aws.String(spec.GroupName),
		Description: aws.String(spec.Description),
		VpcId:       aws.String(spec.VpcId),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.GroupId), nil
}

// DeleteSecurityGroup removes a security group.
func (r *realSGAPI) DeleteSecurityGroup(ctx context.Context, groupId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteSecurityGroup(ctx, &ec2sdk.DeleteSecurityGroupInput{
		GroupId: aws.String(groupId),
	})
	return err
}

// AuthorizeIngress adds ingress rules to a security group.
func (r *realSGAPI) AuthorizeIngress(ctx context.Context, groupId string, rules []NormalizedRule) error {
	if len(rules) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AuthorizeSecurityGroupIngress(ctx, &ec2sdk.AuthorizeSecurityGroupIngressInput{
		GroupId:       aws.String(groupId),
		IpPermissions: rulesToIpPermissions(rules),
	})
	// AWS returns InvalidPermission.Duplicate when the rule already exists.
	// Treat as success for idempotent provisioning.
	if err != nil && isDuplicatePermission(err) {
		return nil
	}
	return err
}

// AuthorizeEgress adds egress rules to a security group.
func (r *realSGAPI) AuthorizeEgress(ctx context.Context, groupId string, rules []NormalizedRule) error {
	if len(rules) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AuthorizeSecurityGroupEgress(ctx, &ec2sdk.AuthorizeSecurityGroupEgressInput{
		GroupId:       aws.String(groupId),
		IpPermissions: rulesToIpPermissions(rules),
	})
	// AWS returns InvalidPermission.Duplicate when the rule already exists.
	// Treat as success for idempotent provisioning.
	if err != nil && isDuplicatePermission(err) {
		return nil
	}
	return err
}

// RevokeIngress removes ingress rules from a security group.
func (r *realSGAPI) RevokeIngress(ctx context.Context, groupId string, rules []NormalizedRule) error {
	if len(rules) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RevokeSecurityGroupIngress(ctx, &ec2sdk.RevokeSecurityGroupIngressInput{
		GroupId:       aws.String(groupId),
		IpPermissions: rulesToIpPermissions(rules),
	})
	return err
}

// RevokeEgress removes egress rules from a security group.
func (r *realSGAPI) RevokeEgress(ctx context.Context, groupId string, rules []NormalizedRule) error {
	if len(rules) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RevokeSecurityGroupEgress(ctx, &ec2sdk.RevokeSecurityGroupEgressInput{
		GroupId:       aws.String(groupId),
		IpPermissions: rulesToIpPermissions(rules),
	})
	return err
}

// UpdateTags replaces all tags on a security group.
func (r *realSGAPI) UpdateTags(ctx context.Context, groupId string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	// First, describe to get current tags so we can remove old ones
	out, err := r.client.DescribeSecurityGroups(ctx, &ec2sdk.DescribeSecurityGroupsInput{
		GroupIds: []string{groupId},
	})
	if err != nil {
		return err
	}
	if len(out.SecurityGroups) == 0 {
		return fmt.Errorf("security group %s not found", groupId)
	}

	// Delete existing tags
	if len(out.SecurityGroups[0].Tags) > 0 {
		var tagKeys []ec2types.Tag
		for _, t := range out.SecurityGroups[0].Tags {
			tagKeys = append(tagKeys, ec2types.Tag{Key: t.Key})
		}
		_, err = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
			Resources: []string{groupId},
			Tags:      tagKeys,
		})
		if err != nil {
			return fmt.Errorf("delete tags: %w", err)
		}
	}

	// Set new tags
	if len(tags) > 0 {
		var ec2Tags []ec2types.Tag
		for k, v := range tags {
			ec2Tags = append(ec2Tags, ec2types.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}
		_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{
			Resources: []string{groupId},
			Tags:      ec2Tags,
		})
		if err != nil {
			return fmt.Errorf("create tags: %w", err)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// rulesToIpPermissions converts normalized rules to EC2 IpPermission structs.
func rulesToIpPermissions(rules []NormalizedRule) []ec2types.IpPermission {
	// Group rules by (protocol, fromPort, toPort) to batch CIDRs.
	type permKey struct {
		Protocol string
		FromPort int32
		ToPort   int32
	}
	grouped := make(map[permKey][]ec2types.IpRange)
	order := make([]permKey, 0)
	for _, r := range rules {
		k := permKey{Protocol: denormalizeProtocol(r.Protocol), FromPort: r.FromPort, ToPort: r.ToPort}
		if _, exists := grouped[k]; !exists {
			order = append(order, k)
		}
		cidr := strings.TrimPrefix(r.Target, "cidr:")
		grouped[k] = append(grouped[k], ec2types.IpRange{CidrIp: aws.String(cidr)})
	}

	perms := make([]ec2types.IpPermission, 0, len(grouped))
	for _, k := range order {
		perms = append(perms, ec2types.IpPermission{
			IpProtocol: aws.String(k.Protocol),
			FromPort:   aws.Int32(k.FromPort),
			ToPort:     aws.Int32(k.ToPort),
			IpRanges:   grouped[k],
		})
	}
	return perms
}

// normalizeProtocol lowercases and normalizes "-1" to "all".
func normalizeProtocol(p string) string {
	p = strings.ToLower(p)
	if p == "-1" {
		return "all"
	}
	return p
}

// denormalizeProtocol converts "all" back to "-1" for AWS API calls.
func denormalizeProtocol(p string) string {
	if p == "all" {
		return "-1"
	}
	return p
}

// ---------------------------------------------------------------------------
// Error Classification Helpers
// ---------------------------------------------------------------------------

// IsNotFound returns true if the AWS error indicates the security group does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidGroup.NotFound", "InvalidGroupId.Malformed")
}

// IsDuplicate returns true if the error indicates a duplicate security group name.
func IsDuplicate(err error) bool {
	return awserr.HasCode(err, "InvalidGroup.Duplicate")
}

// isDuplicatePermission returns true if the error indicates a duplicate rule
// (e.g., the rule already exists on the security group). This happens when AWS
// adds a default rule during SG creation and we try to add the same one.
func isDuplicatePermission(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidPermission.Duplicate"
	}
	return false
}

// IsInvalidParam returns true if the error indicates an invalid parameter
// (e.g., malformed CIDR, invalid port range). These should be terminal errors.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidPermission.Malformed")
}

// IsDependencyViolation returns true if deletion failed because the security group
// is still referenced by other resources (e.g., ENIs, other SG rules).
func IsDependencyViolation(err error) bool {
	return awserr.HasCode(err, "DependencyViolation")
}
