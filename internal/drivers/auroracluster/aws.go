package auroracluster

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"

	"github.com/praxiscloud/praxis/internal/infra/ratelimit"
)

type AuroraClusterAPI interface {
	CreateDBCluster(ctx context.Context, spec AuroraClusterSpec) (string, error)
	DescribeDBCluster(ctx context.Context, clusterIdentifier string) (ObservedState, error)
	ModifyDBCluster(ctx context.Context, spec AuroraClusterSpec, applyImmediately bool) error
	DeleteDBCluster(ctx context.Context, clusterIdentifier string, skipFinalSnapshot bool) error
	WaitUntilAvailable(ctx context.Context, clusterIdentifier string) error
	WaitUntilDeleted(ctx context.Context, clusterIdentifier string) error
	UpdateTags(ctx context.Context, arn string, tags map[string]string) error
	ListTags(ctx context.Context, arn string) (map[string]string, error)
}

type realAuroraClusterAPI struct {
	client  *rdssdk.Client
	limiter *ratelimit.Limiter
}

func NewAuroraClusterAPI(client *rdssdk.Client) AuroraClusterAPI {
	return &realAuroraClusterAPI{client: client, limiter: ratelimit.New("rds", 15, 8)}
}

func (r *realAuroraClusterAPI) CreateDBCluster(ctx context.Context, spec AuroraClusterSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	input := &rdssdk.CreateDBClusterInput{
		DBClusterIdentifier:         aws.String(spec.ClusterIdentifier),
		Engine:                      aws.String(spec.Engine),
		EngineVersion:               aws.String(spec.EngineVersion),
		MasterUsername:              aws.String(spec.MasterUsername),
		MasterUserPassword:          aws.String(spec.MasterUserPassword),
		StorageEncrypted:            aws.Bool(spec.StorageEncrypted),
		BackupRetentionPeriod:       aws.Int32(spec.BackupRetentionPeriod),
		DeletionProtection:          aws.Bool(spec.DeletionProtection),
		EnableCloudwatchLogsExports: spec.EnabledCloudwatchLogsExports,
		Tags:                        toRDSTags(spec.Tags),
	}
	if spec.DatabaseName != "" {
		input.DatabaseName = aws.String(spec.DatabaseName)
	}
	if spec.Port > 0 {
		input.Port = aws.Int32(spec.Port)
	}
	if spec.DBSubnetGroupName != "" {
		input.DBSubnetGroupName = aws.String(spec.DBSubnetGroupName)
	}
	if spec.DBClusterParameterGroupName != "" {
		input.DBClusterParameterGroupName = aws.String(spec.DBClusterParameterGroupName)
	}
	if len(spec.VpcSecurityGroupIds) > 0 {
		input.VpcSecurityGroupIds = spec.VpcSecurityGroupIds
	}
	if spec.KMSKeyId != "" {
		input.KmsKeyId = aws.String(spec.KMSKeyId)
	}
	if spec.PreferredBackupWindow != "" {
		input.PreferredBackupWindow = aws.String(spec.PreferredBackupWindow)
	}
	if spec.PreferredMaintenanceWindow != "" {
		input.PreferredMaintenanceWindow = aws.String(spec.PreferredMaintenanceWindow)
	}
	out, err := r.client.CreateDBCluster(ctx, input)
	if err != nil {
		return "", err
	}
	if out.DBCluster == nil {
		return "", errors.New("CreateDBCluster returned nil cluster")
	}
	return aws.ToString(out.DBCluster.DBClusterArn), nil
}

func (r *realAuroraClusterAPI) DescribeDBCluster(ctx context.Context, clusterIdentifier string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeDBClusters(ctx, &rdssdk.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterIdentifier)})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.DBClusters) == 0 {
		return ObservedState{}, errors.New("db cluster not found")
	}
	cluster := out.DBClusters[0]
	observed := ObservedState{
		ClusterIdentifier:            aws.ToString(cluster.DBClusterIdentifier),
		ClusterResourceId:            aws.ToString(cluster.DbClusterResourceId),
		ARN:                          aws.ToString(cluster.DBClusterArn),
		Engine:                       aws.ToString(cluster.Engine),
		EngineVersion:                aws.ToString(cluster.EngineVersion),
		MasterUsername:               aws.ToString(cluster.MasterUsername),
		DatabaseName:                 aws.ToString(cluster.DatabaseName),
		Port:                         aws.ToInt32(cluster.Port),
		DBSubnetGroupName:            aws.ToString(cluster.DBSubnetGroup),
		DBClusterParameterGroupName:  aws.ToString(cluster.DBClusterParameterGroup),
		StorageEncrypted:             aws.ToBool(cluster.StorageEncrypted),
		KMSKeyId:                     aws.ToString(cluster.KmsKeyId),
		BackupRetentionPeriod:        aws.ToInt32(cluster.BackupRetentionPeriod),
		PreferredBackupWindow:        aws.ToString(cluster.PreferredBackupWindow),
		PreferredMaintenanceWindow:   aws.ToString(cluster.PreferredMaintenanceWindow),
		DeletionProtection:           aws.ToBool(cluster.DeletionProtection),
		EnabledCloudwatchLogsExports: normalizeStrings(cluster.EnabledCloudwatchLogsExports),
		Endpoint:                     aws.ToString(cluster.Endpoint),
		ReaderEndpoint:               aws.ToString(cluster.ReaderEndpoint),
		Status:                       aws.ToString(cluster.Status),
		Tags:                         map[string]string{},
	}
	for _, group := range cluster.VpcSecurityGroups {
		observed.VpcSecurityGroupIds = append(observed.VpcSecurityGroupIds, aws.ToString(group.VpcSecurityGroupId))
	}
	sort.Strings(observed.VpcSecurityGroupIds)
	if observed.ARN != "" {
		tags, tagErr := r.ListTags(ctx, observed.ARN)
		if tagErr != nil {
			return ObservedState{}, tagErr
		}
		observed.Tags = tags
	}
	return observed, nil
}

func (r *realAuroraClusterAPI) ModifyDBCluster(ctx context.Context, spec AuroraClusterSpec, applyImmediately bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &rdssdk.ModifyDBClusterInput{
		DBClusterIdentifier:               aws.String(spec.ClusterIdentifier),
		EngineVersion:                     aws.String(spec.EngineVersion),
		ApplyImmediately:                  aws.Bool(applyImmediately),
		Port:                              aws.Int32(spec.Port),
		DBClusterParameterGroupName:       aws.String(spec.DBClusterParameterGroupName),
		VpcSecurityGroupIds:               spec.VpcSecurityGroupIds,
		BackupRetentionPeriod:             aws.Int32(spec.BackupRetentionPeriod),
		PreferredBackupWindow:             aws.String(spec.PreferredBackupWindow),
		PreferredMaintenanceWindow:        aws.String(spec.PreferredMaintenanceWindow),
		DeletionProtection:                aws.Bool(spec.DeletionProtection),
		CloudwatchLogsExportConfiguration: &rdstypes.CloudwatchLogsExportConfiguration{EnableLogTypes: spec.EnabledCloudwatchLogsExports},
	}
	if spec.MasterUserPassword != "" {
		input.MasterUserPassword = aws.String(spec.MasterUserPassword)
	}
	_, err := r.client.ModifyDBCluster(ctx, input)
	return err
}

func (r *realAuroraClusterAPI) DeleteDBCluster(ctx context.Context, clusterIdentifier string, skipFinalSnapshot bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteDBCluster(ctx, &rdssdk.DeleteDBClusterInput{DBClusterIdentifier: aws.String(clusterIdentifier), SkipFinalSnapshot: aws.Bool(skipFinalSnapshot)})
	return err
}

func (r *realAuroraClusterAPI) WaitUntilAvailable(ctx context.Context, clusterIdentifier string) error {
	waiter := rdssdk.NewDBClusterAvailableWaiter(r.client)
	return waiter.Wait(ctx, &rdssdk.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterIdentifier)}, 30*time.Minute)
}

func (r *realAuroraClusterAPI) WaitUntilDeleted(ctx context.Context, clusterIdentifier string) error {
	waiter := rdssdk.NewDBClusterDeletedWaiter(r.client)
	return waiter.Wait(ctx, &rdssdk.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterIdentifier)}, 30*time.Minute)
}

func (r *realAuroraClusterAPI) UpdateTags(ctx context.Context, arn string, tags map[string]string) error {
	current, err := r.ListTags(ctx, arn)
	if err != nil {
		return err
	}
	desired := filterPraxisTags(tags)
	var remove []string
	for key := range current {
		if _, ok := desired[key]; !ok {
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
	add := toRDSTags(desired)
	if len(add) == 0 {
		return nil
	}
	var changed []rdstypes.Tag
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

func (r *realAuroraClusterAPI) ListTags(ctx context.Context, arn string) (map[string]string, error) {
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
		return apiErr.ErrorCode() == "DBClusterNotFoundFault"
	}
	return strings.Contains(err.Error(), "DBClusterNotFoundFault")
}

func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "DBClusterAlreadyExistsFault"
	}
	return strings.Contains(err.Error(), "DBClusterAlreadyExistsFault")
}

func IsInvalidState(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "InvalidDBClusterStateFault" || code == "InvalidDBInstanceState"
	}
	text := err.Error()
	return strings.Contains(text, "InvalidDBClusterStateFault") || strings.Contains(text, "InvalidDBInstanceState")
}

func IsInvalidParam(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "InvalidParameterValue" || code == "InvalidParameterCombination"
	}
	text := err.Error()
	return strings.Contains(text, "InvalidParameterValue") || strings.Contains(text, "InvalidParameterCombination")
}
