package igw

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type IGWAPI interface {
	CreateInternetGateway(ctx context.Context, spec IGWSpec) (string, error)
	DescribeInternetGateway(ctx context.Context, internetGatewayID string) (ObservedState, error)
	DeleteInternetGateway(ctx context.Context, internetGatewayID string) error
	AttachToVpc(ctx context.Context, internetGatewayID string, vpcID string) error
	DetachFromVpc(ctx context.Context, internetGatewayID string, vpcID string) error
	UpdateTags(ctx context.Context, internetGatewayID string, tags map[string]string) error
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

type realIGWAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

func NewIGWAPI(client *ec2sdk.Client) IGWAPI {
	return &realIGWAPI{
		client:  client,
		limiter: ratelimit.New("internet-gateway", 20, 10),
	}
}

func (r *realIGWAPI) CreateInternetGateway(ctx context.Context, spec IGWSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for key, value := range spec.Tags {
		tags = append(tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}

	out, err := r.client.CreateInternetGateway(ctx, &ec2sdk.CreateInternetGatewayInput{
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInternetGateway,
			Tags:         tags,
		}},
	})
	if err != nil {
		return "", err
	}
	if out.InternetGateway == nil {
		return "", fmt.Errorf("CreateInternetGateway returned nil internet gateway")
	}
	return aws.ToString(out.InternetGateway.InternetGatewayId), nil
}

func (r *realIGWAPI) DescribeInternetGateway(ctx context.Context, internetGatewayID string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeInternetGateways(ctx, &ec2sdk.DescribeInternetGatewaysInput{
		InternetGatewayIds: []string{internetGatewayID},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.InternetGateways) == 0 {
		return ObservedState{}, fmt.Errorf("internet gateway %s not found", internetGatewayID)
	}

	gw := out.InternetGateways[0]
	observed := ObservedState{
		InternetGatewayId: aws.ToString(gw.InternetGatewayId),
		OwnerId:           aws.ToString(gw.OwnerId),
		Tags:              make(map[string]string, len(gw.Tags)),
	}
	for _, attachment := range gw.Attachments {
		if aws.ToString(attachment.VpcId) != "" && string(attachment.State) == "available" {
			observed.AttachedVpcId = aws.ToString(attachment.VpcId)
			break
		}
		if observed.AttachedVpcId == "" {
			observed.AttachedVpcId = aws.ToString(attachment.VpcId)
		}
	}
	for _, tag := range gw.Tags {
		observed.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return observed, nil
}

func (r *realIGWAPI) DeleteInternetGateway(ctx context.Context, internetGatewayID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteInternetGateway(ctx, &ec2sdk.DeleteInternetGatewayInput{
		InternetGatewayId: aws.String(internetGatewayID),
	})
	return err
}

func (r *realIGWAPI) AttachToVpc(ctx context.Context, internetGatewayID string, vpcID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AttachInternetGateway(ctx, &ec2sdk.AttachInternetGatewayInput{
		InternetGatewayId: aws.String(internetGatewayID),
		VpcId:             aws.String(vpcID),
	})
	return err
}

func (r *realIGWAPI) DetachFromVpc(ctx context.Context, internetGatewayID string, vpcID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DetachInternetGateway(ctx, &ec2sdk.DetachInternetGatewayInput{
		InternetGatewayId: aws.String(internetGatewayID),
		VpcId:             aws.String(vpcID),
	})
	return err
}

func (r *realIGWAPI) UpdateTags(ctx context.Context, internetGatewayID string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}

	out, err := r.client.DescribeInternetGateways(ctx, &ec2sdk.DescribeInternetGatewaysInput{
		InternetGatewayIds: []string{internetGatewayID},
	})
	if err != nil {
		return err
	}
	if len(out.InternetGateways) > 0 {
		gw := out.InternetGateways[0]
		var oldTags []ec2types.Tag
		for _, tag := range gw.Tags {
			key := aws.ToString(tag.Key)
			if strings.HasPrefix(key, "praxis:") {
				continue
			}
			oldTags = append(oldTags, ec2types.Tag{Key: tag.Key})
		}
		if len(oldTags) > 0 {
			if err := r.limiter.Wait(ctx); err != nil {
				return err
			}
			_, err = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
				Resources: []string{internetGatewayID},
				Tags:      oldTags,
			})
			if err != nil {
				return err
			}
		}
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
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{
		Resources: []string{internetGatewayID},
		Tags:      ec2Tags,
	})
	return err
}

func (r *realIGWAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	out, err := r.client.DescribeInternetGateways(ctx, &ec2sdk.DescribeInternetGatewaysInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("tag:praxis:managed-key"),
			Values: []string{managedKey},
		}},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for _, gw := range out.InternetGateways {
		if id := aws.ToString(gw.InternetGatewayId); id != "" {
			matches = append(matches, id)
		}
	}

	return singleManagedKeyMatch(managedKey, matches)
}

func singleManagedKeyMatch(managedKey string, matches []string) (string, error) {
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d internet gateways claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidInternetGatewayID.NotFound")
}

func IsDependencyViolation(err error) bool {
	return awserr.HasCode(err, "DependencyViolation")
}

func IsAlreadyAttached(err error) bool {
	return awserr.HasCode(err, "Resource.AlreadyAssociated")
}

func IsNotAttached(err error) bool {
	return awserr.HasCode(err, "Gateway.NotAttached")
}

func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination")
}
