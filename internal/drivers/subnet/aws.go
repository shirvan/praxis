package subnet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type SubnetAPI interface {
	CreateSubnet(ctx context.Context, spec SubnetSpec) (string, error)
	DescribeSubnet(ctx context.Context, subnetId string) (ObservedState, error)
	DeleteSubnet(ctx context.Context, subnetId string) error
	WaitUntilAvailable(ctx context.Context, subnetId string) error
	ModifyMapPublicIp(ctx context.Context, subnetId string, enabled bool) error
	UpdateTags(ctx context.Context, subnetId string, tags map[string]string) error
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
	FindByTags(ctx context.Context, tags map[string]string) (string, error)
}

type realSubnetAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

func NewSubnetAPI(client *ec2sdk.Client) SubnetAPI {
	return &realSubnetAPI{
		client:  client,
		limiter: ratelimit.New("subnet", 20, 10),
	}
}

func (r *realSubnetAPI) CreateSubnet(ctx context.Context, spec SubnetSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	ec2Tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for key, value := range spec.Tags {
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}

	out, err := r.client.CreateSubnet(ctx, &ec2sdk.CreateSubnetInput{
		VpcId:            aws.String(spec.VpcId),
		CidrBlock:        aws.String(spec.CidrBlock),
		AvailabilityZone: aws.String(spec.AvailabilityZone),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSubnet,
			Tags:         ec2Tags,
		}},
	})
	if err != nil {
		return "", err
	}
	if out.Subnet == nil {
		return "", fmt.Errorf("CreateSubnet returned nil Subnet")
	}
	return aws.ToString(out.Subnet.SubnetId), nil
}

func (r *realSubnetAPI) DescribeSubnet(ctx context.Context, subnetId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeSubnets(ctx, &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{subnetId}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Subnets) == 0 {
		return ObservedState{}, fmt.Errorf("subnet %s not found", subnetId)
	}

	sub := out.Subnets[0]
	obs := ObservedState{
		SubnetId:            aws.ToString(sub.SubnetId),
		VpcId:               aws.ToString(sub.VpcId),
		CidrBlock:           aws.ToString(sub.CidrBlock),
		AvailabilityZone:    aws.ToString(sub.AvailabilityZone),
		AvailabilityZoneId:  aws.ToString(sub.AvailabilityZoneId),
		MapPublicIpOnLaunch: aws.ToBool(sub.MapPublicIpOnLaunch),
		State:               string(sub.State),
		OwnerId:             aws.ToString(sub.OwnerId),
		AvailableIpCount:    int(aws.ToInt32(sub.AvailableIpAddressCount)),
		Tags:                make(map[string]string, len(sub.Tags)),
	}
	for _, tag := range sub.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return obs, nil
}

func (r *realSubnetAPI) DeleteSubnet(ctx context.Context, subnetId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteSubnet(ctx, &ec2sdk.DeleteSubnetInput{SubnetId: aws.String(subnetId)})
	return err
}

func (r *realSubnetAPI) WaitUntilAvailable(ctx context.Context, subnetId string) error {
	waiter := ec2sdk.NewSubnetAvailableWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{subnetId}}, 2*time.Minute)
}

func (r *realSubnetAPI) ModifyMapPublicIp(ctx context.Context, subnetId string, enabled bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ModifySubnetAttribute(ctx, &ec2sdk.ModifySubnetAttributeInput{
		SubnetId: aws.String(subnetId),
		MapPublicIpOnLaunch: &ec2types.AttributeBooleanValue{
			Value: aws.Bool(enabled),
		},
	})
	return err
}

func (r *realSubnetAPI) UpdateTags(ctx context.Context, subnetId string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}

	out, err := r.client.DescribeSubnets(ctx, &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{subnetId}})
	if err != nil {
		return err
	}
	if len(out.Subnets) > 0 {
		sub := out.Subnets[0]
		var oldTags []ec2types.Tag
		for _, tag := range sub.Tags {
			key := aws.ToString(tag.Key)
			if strings.HasPrefix(key, "praxis:") {
				continue
			}
			oldTags = append(oldTags, ec2types.Tag{Key: tag.Key})
		}
		if len(oldTags) > 0 {
			_, _ = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{Resources: []string{subnetId}, Tags: oldTags})
		}
	}

	if len(tags) == 0 {
		return nil
	}

	var ec2Tags []ec2types.Tag
	for key, value := range tags {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	if len(ec2Tags) == 0 {
		return nil
	}
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{Resources: []string{subnetId}, Tags: ec2Tags})
	return err
}

func (r *realSubnetAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	out, err := r.client.DescribeSubnets(ctx, &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("tag:praxis:managed-key"), Values: []string{managedKey}}},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for i := range out.Subnets {
		if id := aws.ToString(out.Subnets[i].SubnetId); id != "" {
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
			"ownership corruption: %d subnets claim managed-key %q: %v; manual intervention required",
			len(matches), managedKey, matches,
		)
	}
}

func (r *realSubnetAPI) FindByTags(ctx context.Context, tags map[string]string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	filters := make([]ec2types.Filter, 0, len(tags))
	for key, value := range tags {
		filters = append(filters, ec2types.Filter{Name: aws.String("tag:" + key), Values: []string{value}})
	}
	out, err := r.client.DescribeSubnets(ctx, &ec2sdk.DescribeSubnetsInput{Filters: filters})
	if err != nil {
		return "", err
	}
	var matches []string
	for i := range out.Subnets {
		if id := aws.ToString(out.Subnets[i].SubnetId); id != "" {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous lookup: %d subnets match the given tag filters", len(matches))
	}
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidSubnetID.NotFound", "InvalidSubnetID.Malformed")
}

func IsDependencyViolation(err error) bool {
	return awserr.HasCode(err, "DependencyViolation")
}

func IsInvalidParam(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidParameterValue", "InvalidParameterCombination", "InvalidSubnet.Range", "SubnetLimitExceeded":
			return true
		}
	}
	return false
}

func IsCidrConflict(err error) bool {
	return awserr.HasCode(err, "InvalidSubnet.Conflict")
}
