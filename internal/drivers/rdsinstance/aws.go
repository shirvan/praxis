package rdsinstance

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/shirvan/praxis/internal/drivers"

	"github.com/aws/aws-sdk-go-v2/aws"
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// RDSInstanceAPI abstracts the AWS RDS SDK operations for instance management.
// In production, this is realRDSInstanceAPI (backed by the real SDK client).
// In unit tests, this is mockRDSInstanceAPI (backed by testify/mock).
type RDSInstanceAPI interface {
	// CreateDBInstance creates a new RDS instance and returns its ARN.
	// For Aurora cluster members, storage/backup/master fields are omitted.
	CreateDBInstance(ctx context.Context, spec RDSInstanceSpec) (string, error)

	// DescribeDBInstance returns the observed state including endpoint, tags, and status.
	DescribeDBInstance(ctx context.Context, dbIdentifier string) (ObservedState, error)

	// ModifyDBInstance modifies mutable instance attributes. applyImmediately=true
	// applies changes without waiting for the next maintenance window.
	ModifyDBInstance(ctx context.Context, spec RDSInstanceSpec, applyImmediately bool) error

	// DeleteDBInstance deletes the instance. skipFinalSnapshot=true skips the final snapshot.
	DeleteDBInstance(ctx context.Context, dbIdentifier string, skipFinalSnapshot bool) error

	// WaitUntilAvailable polls until the instance reaches "available" status (up to 30 min).
	WaitUntilAvailable(ctx context.Context, dbIdentifier string) error

	// WaitUntilDeleted polls until the instance is fully deleted (up to 30 min).
	WaitUntilDeleted(ctx context.Context, dbIdentifier string) error

	// UpdateTags performs diff-based tag updates (add/remove/change).
	UpdateTags(ctx context.Context, arn string, tags map[string]string) error

	// ListTags returns all non-praxis tags on the resource.
	ListTags(ctx context.Context, arn string) (map[string]string, error)
}

// realRDSInstanceAPI implements RDSInstanceAPI using the actual AWS SDK v2 RDS client.
type realRDSInstanceAPI struct {
	client  *rdssdk.Client
	limiter *ratelimit.Limiter
}

// NewRDSInstanceAPI creates a new RDSInstanceAPI backed by the given RDS SDK client.
// Rate limited to 15 req/s with burst of 8 for the "rds" category.
func NewRDSInstanceAPI(client *rdssdk.Client) RDSInstanceAPI {
	return &realRDSInstanceAPI{client: client, limiter: ratelimit.New("rds", 15, 8)}
}

// CreateDBInstance calls rds:CreateDBInstance. For Aurora cluster members
// (DBClusterIdentifier set), storage and master credentials are omitted
// since the cluster manages them.
func (r *realRDSInstanceAPI) CreateDBInstance(ctx context.Context, spec RDSInstanceSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	input := &rdssdk.CreateDBInstanceInput{
		DBInstanceIdentifier:      aws.String(spec.DBIdentifier),
		DBInstanceClass:           aws.String(spec.InstanceClass),
		Engine:                    aws.String(spec.Engine),
		EngineVersion:             aws.String(spec.EngineVersion),
		StorageType:               aws.String(spec.StorageType),
		StorageEncrypted:          aws.Bool(spec.StorageEncrypted),
		PubliclyAccessible:        aws.Bool(spec.PubliclyAccessible),
		DeletionProtection:        aws.Bool(spec.DeletionProtection),
		AutoMinorVersionUpgrade:   aws.Bool(spec.AutoMinorVersionUpgrade),
		EnablePerformanceInsights: aws.Bool(spec.PerformanceInsightsEnabled),
		Tags:                      toRDSTags(spec.Tags),
	}
	if spec.DBClusterIdentifier == "" {
		input.AllocatedStorage = aws.Int32(spec.AllocatedStorage)
		input.MasterUsername = aws.String(spec.MasterUsername)
		input.MasterUserPassword = aws.String(spec.MasterUserPassword)
		input.BackupRetentionPeriod = aws.Int32(spec.BackupRetentionPeriod)
		input.MultiAZ = aws.Bool(spec.MultiAZ)
	} else {
		input.DBClusterIdentifier = aws.String(spec.DBClusterIdentifier)
	}
	if spec.IOPS > 0 {
		input.Iops = aws.Int32(spec.IOPS)
	}
	if spec.StorageThroughput > 0 {
		input.StorageThroughput = aws.Int32(spec.StorageThroughput)
	}
	if spec.KMSKeyId != "" {
		input.KmsKeyId = aws.String(spec.KMSKeyId)
	}
	if spec.DBSubnetGroupName != "" {
		input.DBSubnetGroupName = aws.String(spec.DBSubnetGroupName)
	}
	if spec.ParameterGroupName != "" {
		input.DBParameterGroupName = aws.String(spec.ParameterGroupName)
	}
	if len(spec.VpcSecurityGroupIds) > 0 {
		input.VpcSecurityGroupIds = spec.VpcSecurityGroupIds
	}
	if spec.PreferredBackupWindow != "" {
		input.PreferredBackupWindow = aws.String(spec.PreferredBackupWindow)
	}
	if spec.PreferredMaintenanceWindow != "" {
		input.PreferredMaintenanceWindow = aws.String(spec.PreferredMaintenanceWindow)
	}
	if spec.MonitoringInterval > 0 {
		input.MonitoringInterval = aws.Int32(spec.MonitoringInterval)
		if spec.MonitoringRoleArn != "" {
			input.MonitoringRoleArn = aws.String(spec.MonitoringRoleArn)
		}
	}
	out, err := r.client.CreateDBInstance(ctx, input)
	if err != nil {
		return "", err
	}
	if out.DBInstance == nil {
		return "", errors.New("CreateDBInstance returned nil instance")
	}
	return aws.ToString(out.DBInstance.DBInstanceArn), nil
}

// DescribeDBInstance calls rds:DescribeDBInstances and maps to ObservedState.
// Also fetches tags via ListTagsForResource and sorts VPC security group IDs
// for deterministic comparison.
func (r *realRDSInstanceAPI) DescribeDBInstance(ctx context.Context, dbIdentifier string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeDBInstances(ctx, &rdssdk.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(dbIdentifier)})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.DBInstances) == 0 {
		return ObservedState{}, awserr.NotFound("db instance not found")
	}
	instance := out.DBInstances[0]
	observed := ObservedState{
		DBIdentifier:               aws.ToString(instance.DBInstanceIdentifier),
		DbiResourceId:              aws.ToString(instance.DbiResourceId),
		ARN:                        aws.ToString(instance.DBInstanceArn),
		Engine:                     aws.ToString(instance.Engine),
		EngineVersion:              aws.ToString(instance.EngineVersion),
		InstanceClass:              aws.ToString(instance.DBInstanceClass),
		AllocatedStorage:           aws.ToInt32(instance.AllocatedStorage),
		StorageType:                aws.ToString(instance.StorageType),
		IOPS:                       aws.ToInt32(instance.Iops),
		StorageThroughput:          aws.ToInt32(instance.StorageThroughput),
		StorageEncrypted:           aws.ToBool(instance.StorageEncrypted),
		KMSKeyId:                   aws.ToString(instance.KmsKeyId),
		MasterUsername:             aws.ToString(instance.MasterUsername),
		DBClusterIdentifier:        aws.ToString(instance.DBClusterIdentifier),
		MultiAZ:                    aws.ToBool(instance.MultiAZ),
		PubliclyAccessible:         aws.ToBool(instance.PubliclyAccessible),
		BackupRetentionPeriod:      aws.ToInt32(instance.BackupRetentionPeriod),
		PreferredBackupWindow:      aws.ToString(instance.PreferredBackupWindow),
		PreferredMaintenanceWindow: aws.ToString(instance.PreferredMaintenanceWindow),
		DeletionProtection:         aws.ToBool(instance.DeletionProtection),
		AutoMinorVersionUpgrade:    aws.ToBool(instance.AutoMinorVersionUpgrade),
		MonitoringInterval:         aws.ToInt32(instance.MonitoringInterval),
		MonitoringRoleArn:          aws.ToString(instance.MonitoringRoleArn),
		PerformanceInsightsEnabled: aws.ToBool(instance.PerformanceInsightsEnabled),
		Port:                       aws.ToInt32(instance.DbInstancePort),
		Status:                     aws.ToString(instance.DBInstanceStatus),
		Tags:                       map[string]string{},
	}
	if instance.Endpoint != nil {
		observed.Endpoint = aws.ToString(instance.Endpoint.Address)
		if instance.Endpoint.Port != nil {
			observed.Port = aws.ToInt32(instance.Endpoint.Port)
		}
	}
	if instance.DBSubnetGroup != nil {
		observed.DBSubnetGroupName = aws.ToString(instance.DBSubnetGroup.DBSubnetGroupName)
	}
	if len(instance.DBParameterGroups) > 0 {
		observed.ParameterGroupName = aws.ToString(instance.DBParameterGroups[0].DBParameterGroupName)
	}
	for _, group := range instance.VpcSecurityGroups {
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

// ModifyDBInstance calls rds:ModifyDBInstance with the given spec.
// applyImmediately=true applies changes without waiting for the maintenance window.
func (r *realRDSInstanceAPI) ModifyDBInstance(ctx context.Context, spec RDSInstanceSpec, applyImmediately bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &rdssdk.ModifyDBInstanceInput{
		DBInstanceIdentifier:       aws.String(spec.DBIdentifier),
		DBInstanceClass:            aws.String(spec.InstanceClass),
		EngineVersion:              aws.String(spec.EngineVersion),
		ApplyImmediately:           aws.Bool(applyImmediately),
		StorageType:                aws.String(spec.StorageType),
		PubliclyAccessible:         aws.Bool(spec.PubliclyAccessible),
		DeletionProtection:         aws.Bool(spec.DeletionProtection),
		AutoMinorVersionUpgrade:    aws.Bool(spec.AutoMinorVersionUpgrade),
		EnablePerformanceInsights:  aws.Bool(spec.PerformanceInsightsEnabled),
		BackupRetentionPeriod:      aws.Int32(spec.BackupRetentionPeriod),
		PreferredBackupWindow:      aws.String(spec.PreferredBackupWindow),
		PreferredMaintenanceWindow: aws.String(spec.PreferredMaintenanceWindow),
		MonitoringInterval:         aws.Int32(spec.MonitoringInterval),
		MonitoringRoleArn:          aws.String(spec.MonitoringRoleArn),
		MultiAZ:                    aws.Bool(spec.MultiAZ),
	}
	if spec.AllocatedStorage > 0 {
		input.AllocatedStorage = aws.Int32(spec.AllocatedStorage)
	}
	if spec.IOPS > 0 {
		input.Iops = aws.Int32(spec.IOPS)
	}
	if spec.StorageThroughput > 0 {
		input.StorageThroughput = aws.Int32(spec.StorageThroughput)
	}
	if spec.ParameterGroupName != "" {
		input.DBParameterGroupName = aws.String(spec.ParameterGroupName)
	}
	if len(spec.VpcSecurityGroupIds) > 0 {
		input.VpcSecurityGroupIds = spec.VpcSecurityGroupIds
	}
	if spec.MasterUserPassword != "" {
		input.MasterUserPassword = aws.String(spec.MasterUserPassword)
	}
	_, err := r.client.ModifyDBInstance(ctx, input)
	return err
}

// DeleteDBInstance calls rds:DeleteDBInstance. The driver always uses
// skipFinalSnapshot=true for simplicity — operators should configure
// automated backups via BackupRetentionPeriod instead.
func (r *realRDSInstanceAPI) DeleteDBInstance(ctx context.Context, dbIdentifier string, skipFinalSnapshot bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteDBInstance(ctx, &rdssdk.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(dbIdentifier), SkipFinalSnapshot: aws.Bool(skipFinalSnapshot)})
	return err
}

// WaitUntilAvailable uses the SDK's built-in waiter to poll until the instance
// reaches "available" status. Times out after 30 minutes.
func (r *realRDSInstanceAPI) WaitUntilAvailable(ctx context.Context, dbIdentifier string) error {
	waiter := rdssdk.NewDBInstanceAvailableWaiter(r.client)
	return waiter.Wait(ctx, &rdssdk.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(dbIdentifier)}, 30*time.Minute)
}

// WaitUntilDeleted uses the SDK's built-in waiter to poll until the instance
// is fully deleted. Times out after 30 minutes.
func (r *realRDSInstanceAPI) WaitUntilDeleted(ctx context.Context, dbIdentifier string) error {
	waiter := rdssdk.NewDBInstanceDeletedWaiter(r.client)
	return waiter.Wait(ctx, &rdssdk.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(dbIdentifier)}, 30*time.Minute)
}

// UpdateTags performs diff-based tag updates: removes tags no longer desired,
// adds/changes tags that differ. Filters out praxis:-prefixed internal tags.
func (r *realRDSInstanceAPI) UpdateTags(ctx context.Context, arn string, tags map[string]string) error {
	current, err := r.ListTags(ctx, arn)
	if err != nil {
		return err
	}
	desired := drivers.FilterPraxisTags(tags)
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

// ListTags calls rds:ListTagsForResource and returns non-praxis tags.
func (r *realRDSInstanceAPI) ListTags(ctx context.Context, arn string) (map[string]string, error) {
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

// IsNotFound returns true if the AWS error indicates the instance does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "DBInstanceNotFound") || awserr.IsNotFoundErr(err)
}

// IsAlreadyExists returns true if a CreateDBInstance call failed because
// an instance with the same identifier already exists.
func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "DBInstanceAlreadyExists")
}

// IsInvalidState returns true if the instance is in a state that prevents
// the requested operation (e.g., modifying while backing up).
func IsInvalidState(err error) bool {
	return awserr.HasCode(err, "InvalidDBInstanceState", "InvalidDBClusterStateFault")
}

// IsInvalidParam returns true if the error indicates invalid API parameters.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination")
}
