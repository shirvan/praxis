package dbsubnetgroup

import (
	"context"
	"errors"
	"github.com/shirvan/praxis/internal/drivers"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// DBSubnetGroupAPI abstracts AWS RDS SDK operations for DB Subnet Group management.
type DBSubnetGroupAPI interface {
	// CreateDBSubnetGroup creates a new subnet group and returns its ARN.
	CreateDBSubnetGroup(ctx context.Context, spec DBSubnetGroupSpec) (string, error)

	// DescribeDBSubnetGroup returns the observed state including subnets, AZs, and tags.
	DescribeDBSubnetGroup(ctx context.Context, groupName string) (ObservedState, error)

	// ModifyDBSubnetGroup updates description and subnet IDs.
	ModifyDBSubnetGroup(ctx context.Context, spec DBSubnetGroupSpec) error

	// DeleteDBSubnetGroup deletes the subnet group.
	DeleteDBSubnetGroup(ctx context.Context, groupName string) error

	// UpdateTags performs diff-based tag updates on the subnet group.
	UpdateTags(ctx context.Context, arn string, tags map[string]string) error
}

// realDBSubnetGroupAPI implements DBSubnetGroupAPI using the AWS SDK v2 RDS client.
type realDBSubnetGroupAPI struct {
	client  *rdssdk.Client
	limiter *ratelimit.Limiter
}

// NewDBSubnetGroupAPI creates a new API backed by the given RDS client.
// Rate limited to 15 req/s with burst of 8 for the "rds" category.
func NewDBSubnetGroupAPI(client *rdssdk.Client) DBSubnetGroupAPI {
	return &realDBSubnetGroupAPI{client: client, limiter: ratelimit.New("rds", 15, 8)}
}

// CreateDBSubnetGroup calls rds:CreateDBSubnetGroup.
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

// DescribeDBSubnetGroup calls rds:DescribeDBSubnetGroups and maps to ObservedState.
// Sorts subnet IDs and deduplicates availability zones for deterministic comparison.
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

// ModifyDBSubnetGroup calls rds:ModifyDBSubnetGroup to update description and subnets.
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

// DeleteDBSubnetGroup calls rds:DeleteDBSubnetGroup.
func (r *realDBSubnetGroupAPI) DeleteDBSubnetGroup(ctx context.Context, groupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteDBSubnetGroup(ctx, &rdssdk.DeleteDBSubnetGroupInput{DBSubnetGroupName: aws.String(groupName)})
	return err
}

// UpdateTags performs diff-based tag updates: removes stale, adds new/changed.
func (r *realDBSubnetGroupAPI) UpdateTags(ctx context.Context, arn string, tags map[string]string) error {
	current, err := r.listTags(ctx, arn)
	if err != nil {
		return err
	}
	filteredDesired := drivers.FilterPraxisTags(tags)
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

// listTags returns all non-praxis tags on the resource.
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

// toRDSTags converts a map to sorted RDS tag structs, filtering praxis: tags.
func toRDSTags(tags map[string]string) []rdstypes.Tag {
	filtered := drivers.FilterPraxisTags(tags)
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

// IsNotFound returns true if the DB subnet group does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "DBSubnetGroupNotFoundFault")
}

// IsAlreadyExists returns true if a subnet group with the same name exists.
func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "DBSubnetGroupAlreadyExistsFault")
}

// IsInvalidState returns true if the subnet group is in a state preventing the operation.
func IsInvalidState(err error) bool {
	return awserr.HasCode(err, "InvalidDBSubnetGroupStateFault")
}

// IsInvalidParam returns true if the error indicates invalid parameters (e.g. bad subnets).
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidSubnet", "DBSubnetGroupDoesNotCoverEnoughAZs", "SubnetAlreadyInUse", "InvalidParameterValue", "InvalidParameterCombination")
}
