package dbparametergroup

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"

	"github.com/aws/aws-sdk-go-v2/aws"
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// DBParameterGroupAPI abstracts AWS RDS SDK operations for both DB and Cluster
// Parameter Group management.
type DBParameterGroupAPI interface {
	// CreateParameterGroup creates a DB or Cluster parameter group based on spec.Type.
	CreateParameterGroup(ctx context.Context, spec DBParameterGroupSpec) (string, error)

	// DescribeParameterGroup returns observed state including only user-modified parameters.
	DescribeParameterGroup(ctx context.Context, groupName string, groupType string) (ObservedState, error)

	// UpdateParameters sets changed parameters and resets removed ones in batches of 20.
	UpdateParameters(ctx context.Context, spec DBParameterGroupSpec, observed ObservedState) error

	// DeleteParameterGroup deletes the parameter group.
	DeleteParameterGroup(ctx context.Context, groupName string, groupType string) error

	// UpdateTags performs diff-based tag updates on the parameter group.
	UpdateTags(ctx context.Context, arn string, tags map[string]string) error
}

// realDBParameterGroupAPI implements DBParameterGroupAPI using the AWS SDK v2 RDS client.
type realDBParameterGroupAPI struct {
	client  *rdssdk.Client
	limiter *ratelimit.Limiter
}

// NewDBParameterGroupAPI creates a new API backed by the given RDS client.
// Rate limited to 15 req/s with burst of 8 for the "rds" category.
func NewDBParameterGroupAPI(client *rdssdk.Client) DBParameterGroupAPI {
	return &realDBParameterGroupAPI{client: client, limiter: ratelimit.New("rds", 15, 8)}
}

// CreateParameterGroup calls rds:CreateDBParameterGroup or rds:CreateDBClusterParameterGroup.
func (r *realDBParameterGroupAPI) CreateParameterGroup(ctx context.Context, spec DBParameterGroupSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	if spec.Type == TypeCluster {
		out, err := r.client.CreateDBClusterParameterGroup(ctx, &rdssdk.CreateDBClusterParameterGroupInput{
			DBClusterParameterGroupName: aws.String(spec.GroupName),
			DBParameterGroupFamily:      aws.String(spec.Family),
			Description:                 aws.String(spec.Description),
			Tags:                        toRDSTags(spec.Tags),
		})
		if err != nil {
			return "", err
		}
		if out.DBClusterParameterGroup == nil {
			return "", errors.New("CreateDBClusterParameterGroup returned nil parameter group")
		}
		return aws.ToString(out.DBClusterParameterGroup.DBClusterParameterGroupArn), nil
	}
	out, err := r.client.CreateDBParameterGroup(ctx, &rdssdk.CreateDBParameterGroupInput{
		DBParameterGroupName:   aws.String(spec.GroupName),
		DBParameterGroupFamily: aws.String(spec.Family),
		Description:            aws.String(spec.Description),
		Tags:                   toRDSTags(spec.Tags),
	})
	if err != nil {
		return "", err
	}
	if out.DBParameterGroup == nil {
		return "", errors.New("CreateDBParameterGroup returned nil parameter group")
	}
	return aws.ToString(out.DBParameterGroup.DBParameterGroupArn), nil
}

// DescribeParameterGroup fetches the group metadata and only user-modified parameters.
// Uses paginated APIs (rds:DescribeDBParameters / rds:DescribeDBClusterParameters)
// with Source="user" to skip thousands of engine-default parameters.
func (r *realDBParameterGroupAPI) DescribeParameterGroup(ctx context.Context, groupName string, groupType string) (ObservedState, error) {
	observed := ObservedState{GroupName: groupName, Type: groupType, Parameters: map[string]string{}, Tags: map[string]string{}}
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	if groupType == TypeCluster {
		out, err := r.client.DescribeDBClusterParameterGroups(ctx, &rdssdk.DescribeDBClusterParameterGroupsInput{DBClusterParameterGroupName: aws.String(groupName)})
		if err != nil {
			return ObservedState{}, err
		}
		if len(out.DBClusterParameterGroups) == 0 {
			return ObservedState{}, awserr.NotFound("cluster parameter group not found")
		}
		group := out.DBClusterParameterGroups[0]
		observed.ARN = aws.ToString(group.DBClusterParameterGroupArn)
		observed.Family = aws.ToString(group.DBParameterGroupFamily)
		observed.Description = aws.ToString(group.Description)
		params, err := r.describeClusterParameters(ctx, groupName)
		if err != nil {
			return ObservedState{}, err
		}
		observed.Parameters = params
	} else {
		out, err := r.client.DescribeDBParameterGroups(ctx, &rdssdk.DescribeDBParameterGroupsInput{DBParameterGroupName: aws.String(groupName)})
		if err != nil {
			return ObservedState{}, err
		}
		if len(out.DBParameterGroups) == 0 {
			return ObservedState{}, awserr.NotFound("parameter group not found")
		}
		group := out.DBParameterGroups[0]
		observed.ARN = aws.ToString(group.DBParameterGroupArn)
		observed.Family = aws.ToString(group.DBParameterGroupFamily)
		observed.Description = aws.ToString(group.Description)
		params, err := r.describeParameters(ctx, groupName)
		if err != nil {
			return ObservedState{}, err
		}
		observed.Parameters = params
	}
	if observed.ARN != "" {
		tags, err := r.listTags(ctx, observed.ARN)
		if err != nil {
			return ObservedState{}, err
		}
		observed.Tags = tags
	}
	return observed, nil
}

// UpdateParameters applies parameter changes in batches of 20 (AWS limit).
// New/changed parameters are set via Modify; removed parameters are reset to engine defaults.
func (r *realDBParameterGroupAPI) UpdateParameters(ctx context.Context, spec DBParameterGroupSpec, observed ObservedState) error {
	toSet := make([]rdstypes.Parameter, 0)
	for key, value := range spec.Parameters {
		if current, ok := observed.Parameters[key]; ok && current == value {
			continue
		}
		toSet = append(toSet, rdstypes.Parameter{ParameterName: aws.String(key), ParameterValue: aws.String(value), ApplyMethod: rdstypes.ApplyMethodPendingReboot})
	}
	toReset := make([]rdstypes.Parameter, 0)
	for key := range observed.Parameters {
		if _, ok := spec.Parameters[key]; !ok {
			toReset = append(toReset, rdstypes.Parameter{ParameterName: aws.String(key), ApplyMethod: rdstypes.ApplyMethodPendingReboot})
		}
	}
	for start := 0; start < len(toSet); start += 20 {
		end := min(start+20, len(toSet))
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if spec.Type == TypeCluster {
			_, err := r.client.ModifyDBClusterParameterGroup(ctx, &rdssdk.ModifyDBClusterParameterGroupInput{DBClusterParameterGroupName: aws.String(spec.GroupName), Parameters: toSet[start:end]})
			if err != nil {
				return err
			}
		} else {
			_, err := r.client.ModifyDBParameterGroup(ctx, &rdssdk.ModifyDBParameterGroupInput{DBParameterGroupName: aws.String(spec.GroupName), Parameters: toSet[start:end]})
			if err != nil {
				return err
			}
		}
	}
	for start := 0; start < len(toReset); start += 20 {
		end := min(start+20, len(toReset))
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if spec.Type == TypeCluster {
			_, err := r.client.ResetDBClusterParameterGroup(ctx, &rdssdk.ResetDBClusterParameterGroupInput{DBClusterParameterGroupName: aws.String(spec.GroupName), Parameters: toReset[start:end]})
			if err != nil {
				return err
			}
		} else {
			_, err := r.client.ResetDBParameterGroup(ctx, &rdssdk.ResetDBParameterGroupInput{DBParameterGroupName: aws.String(spec.GroupName), Parameters: toReset[start:end]})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteParameterGroup calls rds:DeleteDBParameterGroup or rds:DeleteDBClusterParameterGroup.
func (r *realDBParameterGroupAPI) DeleteParameterGroup(ctx context.Context, groupName string, groupType string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	if groupType == TypeCluster {
		_, err := r.client.DeleteDBClusterParameterGroup(ctx, &rdssdk.DeleteDBClusterParameterGroupInput{DBClusterParameterGroupName: aws.String(groupName)})
		return err
	}
	_, err := r.client.DeleteDBParameterGroup(ctx, &rdssdk.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String(groupName)})
	return err
}

// UpdateTags performs diff-based tag updates: removes stale, adds new/changed.
func (r *realDBParameterGroupAPI) UpdateTags(ctx context.Context, arn string, tags map[string]string) error {
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
	add := toRDSTags(filteredDesired)
	if len(add) == 0 {
		return nil
	}
	changed := make([]rdstypes.Tag, 0, len(add))
	for _, tag := range add {
		key := aws.ToString(tag.Key)
		if currentValue, ok := current[key]; !ok || currentValue != aws.ToString(tag.Value) {
			changed = append(changed, tag)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.AddTagsToResource(ctx, &rdssdk.AddTagsToResourceInput{ResourceName: aws.String(arn), Tags: changed})
	return err
}

// describeParameters paginates rds:DescribeDBParameters with Source="user".
func (r *realDBParameterGroupAPI) describeParameters(ctx context.Context, groupName string) (map[string]string, error) {
	paginator := rdssdk.NewDescribeDBParametersPaginator(r.client, &rdssdk.DescribeDBParametersInput{DBParameterGroupName: aws.String(groupName), Source: aws.String("user")})
	params := map[string]string{}
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, param := range page.Parameters {
			name := aws.ToString(param.ParameterName)
			if name == "" {
				continue
			}
			params[name] = aws.ToString(param.ParameterValue)
		}
	}
	return params, nil
}

// describeClusterParameters paginates rds:DescribeDBClusterParameters with Source="user".
func (r *realDBParameterGroupAPI) describeClusterParameters(ctx context.Context, groupName string) (map[string]string, error) {
	paginator := rdssdk.NewDescribeDBClusterParametersPaginator(r.client, &rdssdk.DescribeDBClusterParametersInput{DBClusterParameterGroupName: aws.String(groupName), Source: aws.String("user")})
	params := map[string]string{}
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, param := range page.Parameters {
			name := aws.ToString(param.ParameterName)
			if name == "" {
				continue
			}
			params[name] = aws.ToString(param.ParameterValue)
		}
	}
	return params, nil
}

// listTags returns all non-praxis tags on the resource.
func (r *realDBParameterGroupAPI) listTags(ctx context.Context, arn string) (map[string]string, error) {
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
	out := make([]rdstypes.Tag, 0, len(filtered))
	for key, value := range filtered {
		out = append(out, rdstypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	sort.Slice(out, func(i, j int) bool {
		return aws.ToString(out[i].Key) < aws.ToString(out[j].Key)
	})
	return out
}

// IsNotFound returns true if the parameter group does not exist (DB or Cluster).
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "DBParameterGroupNotFoundFault", "DBClusterParameterGroupNotFoundFault") || awserr.IsNotFoundErr(err)
}

// IsAlreadyExists returns true if a parameter group with the same name exists.
func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "DBParameterGroupAlreadyExistsFault", "DBParameterGroupQuotaExceededFault", "DBParameterGroupAlreadyExists", "DBClusterParameterGroupAlreadyExistsFault")
}

// IsInvalidState returns true if the parameter group is in a state preventing the operation.
func IsInvalidState(err error) bool {
	return awserr.HasCode(err, "InvalidDBParameterGroupStateFault", "InvalidDBClusterParameterGroupStateFault")
}

// IsInvalidParam returns true if the error indicates invalid API parameters.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination")
}
