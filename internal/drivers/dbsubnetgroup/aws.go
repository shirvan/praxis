package dbsubnetgroup

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"

	"github.com/praxiscloud/praxis/internal/infra/ratelimit"
)

type DBSubnetGroupAPI interface {
	CreateDBSubnetGroup(ctx context.Context, spec DBSubnetGroupSpec) (string, error)
	DescribeDBSubnetGroup(ctx context.Context, groupName string) (ObservedState, error)
	ModifyDBSubnetGroup(ctx context.Context, spec DBSubnetGroupSpec) error
	DeleteDBSubnetGroup(ctx context.Context, groupName string) error
	UpdateTags(ctx context.Context, arn string, tags map[string]string) error
}

type realDBSubnetGroupAPI struct {
	client  *rdssdk.Client
	limiter *ratelimit.Limiter
}

func NewDBSubnetGroupAPI(client *rdssdk.Client) DBSubnetGroupAPI {
	return &realDBSubnetGroupAPI{client: client, limiter: ratelimit.New("rds", 15, 8)}
}

func (r *realDBSubnetGroupAPI) CreateDBSubnetGroup(ctx context.Context, spec DBSubnetGroupSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.CreateDBSubnetGroup(ctx, &rdssdk.CreateDBSubnetGroupInput{
		DBSubnetGroupDescription: aws.String(spec.Description),
		DBSubnetGroupName:        aws.String(spec.GroupName),
		SubnetIds:                append([]string(nil), spec.SubnetIds...),
		Tags:                     toRDSTags(spec.Tags),
	})
	if err != nil {
		return "", err
	}
	if out.DBSubnetGroup == nil {
		return "", errors.New("CreateDBSubnetGroup returned nil subnet group")
	}
	return aws.ToString(out.DBSubnetGroup.DBSubnetGroupArn), nil
}

func (r *realDBSubnetGroupAPI) DescribeDBSubnetGroup(ctx context.Context, groupName string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeDBSubnetGroups(ctx, &rdssdk.DescribeDBSubnetGroupsInput{DBSubnetGroupName: aws.String(groupName)})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.DBSubnetGroups) == 0 {
		return ObservedState{}, errors.New("db subnet group not found")
	}
	group := out.DBSubnetGroups[0]
	observed := ObservedState{
		GroupName:         aws.ToString(group.DBSubnetGroupName),
		ARN:               aws.ToString(group.DBSubnetGroupArn),
		Description:       aws.ToString(group.DBSubnetGroupDescription),
		VpcId:             aws.ToString(group.VpcId),
		Status:            aws.ToString(group.SubnetGroupStatus),
		Tags:              map[string]string{},
		SubnetIds:         make([]string, 0, len(group.Subnets)),
		AvailabilityZones: make([]string, 0, len(group.Subnets)),
	}
	availabilityZones := make(map[string]struct{}, len(group.Subnets))
	for _, subnet := range group.Subnets {
		if subnet.SubnetIdentifier != nil {
			observed.SubnetIds = append(observed.SubnetIds, aws.ToString(subnet.SubnetIdentifier))
		}
		if subnet.SubnetAvailabilityZone != nil && subnet.SubnetAvailabilityZone.Name != nil {
			availabilityZones[aws.ToString(subnet.SubnetAvailabilityZone.Name)] = struct{}{}
		}
	}
	sort.Strings(observed.SubnetIds)
	for zone := range availabilityZones {
		observed.AvailabilityZones = append(observed.AvailabilityZones, zone)
	}
	sort.Strings(observed.AvailabilityZones)
	if observed.ARN != "" {
		tags, tagErr := r.listTags(ctx, observed.ARN)
		if tagErr != nil {
			return ObservedState{}, tagErr
		}
		observed.Tags = tags
	}
	return observed, nil
}

func (r *realDBSubnetGroupAPI) ModifyDBSubnetGroup(ctx context.Context, spec DBSubnetGroupSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ModifyDBSubnetGroup(ctx, &rdssdk.ModifyDBSubnetGroupInput{
		DBSubnetGroupDescription: aws.String(spec.Description),
		DBSubnetGroupName:        aws.String(spec.GroupName),
		SubnetIds:                append([]string(nil), spec.SubnetIds...),
	})
	return err
}

func (r *realDBSubnetGroupAPI) DeleteDBSubnetGroup(ctx context.Context, groupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteDBSubnetGroup(ctx, &rdssdk.DeleteDBSubnetGroupInput{DBSubnetGroupName: aws.String(groupName)})
	return err
}

func (r *realDBSubnetGroupAPI) UpdateTags(ctx context.Context, arn string, tags map[string]string) error {
	current, err := r.listTags(ctx, arn)
	if err != nil {
		return err
	}
	filteredDesired := filterPraxisTags(tags)
	var remove []string
	for key := range current {
		if _, ok := filteredDesired[key]; !ok {
			remove = append(remove, key)
		}
	}
	if len(remove) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err = r.client.RemoveTagsFromResource(ctx, &rdssdk.RemoveTagsFromResourceInput{ResourceName: aws.String(arn), TagKeys: remove})
		if err != nil {
			return err
		}
	}
	var add []rdstypes.Tag
	for key, value := range filteredDesired {
		if currentValue, ok := current[key]; !ok || currentValue != value {
			add = append(add, rdstypes.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
	}
	if len(add) == 0 {
		return nil
	}
	sort.Slice(add, func(i, j int) bool {
		return aws.ToString(add[i].Key) < aws.ToString(add[j].Key)
	})
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.AddTagsToResource(ctx, &rdssdk.AddTagsToResourceInput{ResourceName: aws.String(arn), Tags: add})
	return err
}

func (r *realDBSubnetGroupAPI) listTags(ctx context.Context, arn string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListTagsForResource(ctx, &rdssdk.ListTagsForResourceInput{ResourceName: aws.String(arn)})
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for _, tag := range out.TagList {
		key := aws.ToString(tag.Key)
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		tags[key] = aws.ToString(tag.Value)
	}
	return tags, nil
}

func toRDSTags(tags map[string]string) []rdstypes.Tag {
	filtered := filterPraxisTags(tags)
	if len(filtered) == 0 {
		return nil
	}
	out := make([]rdstypes.Tag, 0, len(filtered))
	for key, value := range filtered {
		out = append(out, rdstypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	sort.Slice(out, func(i, j int) bool {
		return aws.ToString(out[i].Key) < aws.ToString(out[j].Key)
	})
	return out
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "DBSubnetGroupNotFoundFault"
	}
	return strings.Contains(err.Error(), "DBSubnetGroupNotFoundFault")
}

func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "DBSubnetGroupAlreadyExistsFault"
	}
	return strings.Contains(err.Error(), "DBSubnetGroupAlreadyExistsFault")
}

func IsInvalidState(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidDBSubnetGroupStateFault"
	}
	return strings.Contains(err.Error(), "InvalidDBSubnetGroupStateFault")
}

func IsInvalidParam(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "InvalidSubnet" || code == "DBSubnetGroupDoesNotCoverEnoughAZs" || code == "SubnetAlreadyInUse" || code == "InvalidParameterValue" || code == "InvalidParameterCombination"
	}
	errText := err.Error()
	return strings.Contains(errText, "InvalidSubnet") || strings.Contains(errText, "DBSubnetGroupDoesNotCoverEnoughAZs") || strings.Contains(errText, "SubnetAlreadyInUse") || strings.Contains(errText, "InvalidParameterValue") || strings.Contains(errText, "InvalidParameterCombination")
}
