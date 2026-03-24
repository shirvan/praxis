package vpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// VPCAPI abstracts the AWS EC2 SDK operations that the VPC driver uses.
type VPCAPI interface {
	CreateVpc(ctx context.Context, spec VPCSpec) (string, error)
	DescribeVpc(ctx context.Context, vpcId string) (ObservedState, error)
	DeleteVpc(ctx context.Context, vpcId string) error
	WaitUntilAvailable(ctx context.Context, vpcId string) error
	ModifyDnsHostnames(ctx context.Context, vpcId string, enabled bool) error
	ModifyDnsSupport(ctx context.Context, vpcId string, enabled bool) error
	UpdateTags(ctx context.Context, vpcId string, tags map[string]string) error
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
	FindByTags(ctx context.Context, tags map[string]string) (string, error)
}

type realVPCAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

// NewVPCAPI returns a real implementation of VPCAPI backed by the EC2 SDK client.
func NewVPCAPI(client *ec2sdk.Client) VPCAPI {
	return &realVPCAPI{
		client:  client,
		limiter: ratelimit.New("vpc", 20, 10),
	}
}

func (r *realVPCAPI) CreateVpc(ctx context.Context, spec VPCSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	input := &ec2sdk.CreateVpcInput{
		CidrBlock: aws.String(spec.CidrBlock),
	}

	if spec.InstanceTenancy != "" && spec.InstanceTenancy != "default" {
		input.InstanceTenancy = ec2types.Tenancy(spec.InstanceTenancy)
	}

	ec2Tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for k, v := range spec.Tags {
		ec2Tags = append(ec2Tags, ec2types.Tag{
			Key: aws.String(k), Value: aws.String(v),
		})
	}
	input.TagSpecifications = []ec2types.TagSpecification{{
		ResourceType: ec2types.ResourceTypeVpc,
		Tags:         ec2Tags,
	}}

	out, err := r.client.CreateVpc(ctx, input)
	if err != nil {
		return "", err
	}
	if out.Vpc == nil {
		return "", fmt.Errorf("CreateVpc returned nil VPC")
	}
	return aws.ToString(out.Vpc.VpcId), nil
}

func (r *realVPCAPI) DescribeVpc(ctx context.Context, vpcId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{
		VpcIds: []string{vpcId},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Vpcs) == 0 {
		return ObservedState{}, fmt.Errorf("VPC %s not found", vpcId)
	}

	v := out.Vpcs[0]

	obs := ObservedState{
		VpcId:           aws.ToString(v.VpcId),
		CidrBlock:       aws.ToString(v.CidrBlock),
		State:           string(v.State),
		InstanceTenancy: string(v.InstanceTenancy),
		OwnerId:         aws.ToString(v.OwnerId),
		IsDefault:       aws.ToBool(v.IsDefault),
		DhcpOptionsId:   aws.ToString(v.DhcpOptionsId),
		Tags:            make(map[string]string, len(v.Tags)),
	}

	for _, tag := range v.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	// DNS settings require separate DescribeVpcAttribute calls.
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	dnsHostnames, err := r.client.DescribeVpcAttribute(ctx, &ec2sdk.DescribeVpcAttributeInput{
		VpcId:     aws.String(vpcId),
		Attribute: ec2types.VpcAttributeNameEnableDnsHostnames,
	})
	if err != nil {
		return ObservedState{}, fmt.Errorf("describe DNS hostnames for VPC %s: %w", vpcId, err)
	}
	if dnsHostnames.EnableDnsHostnames != nil {
		obs.EnableDnsHostnames = aws.ToBool(dnsHostnames.EnableDnsHostnames.Value)
	}

	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	dnsSupport, err := r.client.DescribeVpcAttribute(ctx, &ec2sdk.DescribeVpcAttributeInput{
		VpcId:     aws.String(vpcId),
		Attribute: ec2types.VpcAttributeNameEnableDnsSupport,
	})
	if err != nil {
		return ObservedState{}, fmt.Errorf("describe DNS support for VPC %s: %w", vpcId, err)
	}
	if dnsSupport.EnableDnsSupport != nil {
		obs.EnableDnsSupport = aws.ToBool(dnsSupport.EnableDnsSupport.Value)
	}

	return obs, nil
}

func (r *realVPCAPI) DeleteVpc(ctx context.Context, vpcId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteVpc(ctx, &ec2sdk.DeleteVpcInput{
		VpcId: aws.String(vpcId),
	})
	return err
}

func (r *realVPCAPI) WaitUntilAvailable(ctx context.Context, vpcId string) error {
	waiter := ec2sdk.NewVpcAvailableWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeVpcsInput{
		VpcIds: []string{vpcId},
	}, 2*time.Minute)
}

func (r *realVPCAPI) ModifyDnsHostnames(ctx context.Context, vpcId string, enabled bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ModifyVpcAttribute(ctx, &ec2sdk.ModifyVpcAttributeInput{
		VpcId: aws.String(vpcId),
		EnableDnsHostnames: &ec2types.AttributeBooleanValue{
			Value: aws.Bool(enabled),
		},
	})
	return err
}

func (r *realVPCAPI) ModifyDnsSupport(ctx context.Context, vpcId string, enabled bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ModifyVpcAttribute(ctx, &ec2sdk.ModifyVpcAttributeInput{
		VpcId: aws.String(vpcId),
		EnableDnsSupport: &ec2types.AttributeBooleanValue{
			Value: aws.Bool(enabled),
		},
	})
	return err
}

func (r *realVPCAPI) UpdateTags(ctx context.Context, vpcId string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}

	out, err := r.client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{
		VpcIds: []string{vpcId},
	})
	if err != nil {
		return err
	}
	if len(out.Vpcs) > 0 {
		vpc := out.Vpcs[0]
		if len(vpc.Tags) > 0 {
			var oldTags []ec2types.Tag
			for _, t := range vpc.Tags {
				key := aws.ToString(t.Key)
				if strings.HasPrefix(key, "praxis:") {
					continue
				}
				oldTags = append(oldTags, ec2types.Tag{Key: t.Key})
			}
			if len(oldTags) > 0 {
				_, _ = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
					Resources: []string{vpcId},
					Tags:      oldTags,
				})
			}
		}
	}

	if len(tags) > 0 {
		var ec2Tags []ec2types.Tag
		for k, v := range tags {
			if strings.HasPrefix(k, "praxis:") {
				continue
			}
			ec2Tags = append(ec2Tags, ec2types.Tag{
				Key: aws.String(k), Value: aws.String(v),
			})
		}
		if len(ec2Tags) > 0 {
			_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{
				Resources: []string{vpcId},
				Tags:      ec2Tags,
			})
			return err
		}
	}
	return nil
}

func (r *realVPCAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	out, err := r.client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:praxis:managed-key"), Values: []string{managedKey}},
		},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for _, v := range out.Vpcs {
		if id := aws.ToString(v.VpcId); id != "" {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf(
			"ownership corruption: %d VPCs claim managed-key %q: %v; "+
				"manual intervention required",
			len(matches), managedKey, matches,
		)
	}
}

func (r *realVPCAPI) FindByTags(ctx context.Context, tags map[string]string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	filters := make([]ec2types.Filter, 0, len(tags))
	for key, value := range tags {
		filters = append(filters, ec2types.Filter{Name: aws.String("tag:" + key), Values: []string{value}})
	}
	out, err := r.client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{Filters: filters})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, item := range out.Vpcs {
		if id := aws.ToString(item.VpcId); id != "" {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous lookup: %d VPCs match the given tag filters", len(matches))
	}
}

// ---------------------------------------------------------------------------
// Error classification helpers
// ---------------------------------------------------------------------------

// IsNotFound returns true if the AWS error indicates the VPC does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidVpcID.NotFound", "InvalidVpcID.Malformed")
}

// IsDependencyViolation returns true if the VPC cannot be deleted because it
// has dependent resources.
func IsDependencyViolation(err error) bool {
	return awserr.HasCode(err, "DependencyViolation")
}

// IsInvalidParam returns true if the error indicates an invalid parameter.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination", "InvalidVpcRange", "VpcLimitExceeded")
}

// IsCidrConflict returns true if the requested CIDR block conflicts with an
// existing VPC or overlaps with reserved ranges.
func IsCidrConflict(err error) bool {
	return awserr.HasCode(err, "CidrConflict", "InvalidVpc.Range")
}
