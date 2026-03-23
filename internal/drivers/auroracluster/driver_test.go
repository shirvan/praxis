package auroracluster

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewAuroraClusterDriver(nil)
	assert.Equal(t, "AuroraCluster", drv.ServiceName())
}

func TestValidateSpec_Valid(t *testing.T) {
	err := validateSpec(AuroraClusterSpec{
		Region: "us-east-1", ClusterIdentifier: "my-cluster", Engine: "aurora-mysql",
		EngineVersion: "8.0.mysql_aurora.3.04.0", MasterUsername: "admin", MasterUserPassword: "secret123",
	})
	assert.NoError(t, err)
}

func TestValidateSpec_MissingRegion(t *testing.T) {
	err := validateSpec(AuroraClusterSpec{ClusterIdentifier: "c", Engine: "aurora-mysql", EngineVersion: "8.0", MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "region is required")
}

func TestValidateSpec_MissingClusterIdentifier(t *testing.T) {
	err := validateSpec(AuroraClusterSpec{Region: "us-east-1", Engine: "aurora-mysql", EngineVersion: "8.0", MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "clusterIdentifier is required")
}

func TestValidateSpec_MissingEngine(t *testing.T) {
	err := validateSpec(AuroraClusterSpec{Region: "us-east-1", ClusterIdentifier: "c", EngineVersion: "8.0", MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "engine is required")
}

func TestValidateSpec_MissingEngineVersion(t *testing.T) {
	err := validateSpec(AuroraClusterSpec{Region: "us-east-1", ClusterIdentifier: "c", Engine: "aurora-mysql", MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "engineVersion is required")
}

func TestValidateSpec_MissingMasterUsername(t *testing.T) {
	err := validateSpec(AuroraClusterSpec{Region: "us-east-1", ClusterIdentifier: "c", Engine: "aurora-mysql", EngineVersion: "8.0", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "masterUsername is required")
}

func TestValidateSpec_MissingMasterUserPassword(t *testing.T) {
	err := validateSpec(AuroraClusterSpec{Region: "us-east-1", ClusterIdentifier: "c", Engine: "aurora-mysql", EngineVersion: "8.0", MasterUsername: "admin"})
	assert.ErrorContains(t, err, "masterUserPassword is required")
}

func TestValidateExisting_Valid(t *testing.T) {
	err := validateExisting(
		AuroraClusterSpec{ClusterIdentifier: "c", Engine: "aurora-mysql", MasterUsername: "admin"},
		ObservedState{ClusterIdentifier: "c", Engine: "aurora-mysql", MasterUsername: "admin"},
	)
	assert.NoError(t, err)
}

func TestValidateExisting_ImmutableClusterIdentifier(t *testing.T) {
	err := validateExisting(
		AuroraClusterSpec{ClusterIdentifier: "new-c"},
		ObservedState{ClusterIdentifier: "old-c"},
	)
	assert.ErrorContains(t, err, "clusterIdentifier is immutable")
}

func TestValidateExisting_ImmutableEngine(t *testing.T) {
	err := validateExisting(
		AuroraClusterSpec{ClusterIdentifier: "c", Engine: "aurora-postgresql"},
		ObservedState{ClusterIdentifier: "c", Engine: "aurora-mysql"},
	)
	assert.ErrorContains(t, err, "engine is immutable")
}

func TestValidateExisting_ImmutableMasterUsername(t *testing.T) {
	err := validateExisting(
		AuroraClusterSpec{ClusterIdentifier: "c", Engine: "aurora-mysql", MasterUsername: "newuser"},
		ObservedState{ClusterIdentifier: "c", Engine: "aurora-mysql", MasterUsername: "admin"},
	)
	assert.ErrorContains(t, err, "masterUsername is immutable")
}

func TestValidateExisting_ImmutableDatabaseName(t *testing.T) {
	err := validateExisting(
		AuroraClusterSpec{ClusterIdentifier: "c", Engine: "aurora-mysql", DatabaseName: "newdb"},
		ObservedState{ClusterIdentifier: "c", Engine: "aurora-mysql", DatabaseName: "olddb"},
	)
	assert.ErrorContains(t, err, "databaseName is immutable")
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		ClusterIdentifier: "my-cluster", Engine: "aurora-mysql", EngineVersion: "8.0",
		MasterUsername: "admin", DatabaseName: "mydb", Port: 3306,
		DBSubnetGroupName: "my-subnet-group", DBClusterParameterGroupName: "my-param-group",
		VpcSecurityGroupIds: []string{"sg-111"}, StorageEncrypted: true,
		BackupRetentionPeriod: 7, DeletionProtection: true,
		EnabledCloudwatchLogsExports: []string{"audit"},
		Tags:                         map[string]string{"env": "prod", "praxis:key": "val"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "my-cluster", spec.ClusterIdentifier)
	assert.Equal(t, "aurora-mysql", spec.Engine)
	assert.Equal(t, "8.0", spec.EngineVersion)
	assert.Equal(t, "admin", spec.MasterUsername)
	assert.Equal(t, "mydb", spec.DatabaseName)
	assert.Equal(t, int32(3306), spec.Port)
	assert.Equal(t, "my-subnet-group", spec.DBSubnetGroupName)
	assert.Equal(t, "my-param-group", spec.DBClusterParameterGroupName)
	assert.True(t, spec.StorageEncrypted)
	assert.True(t, spec.DeletionProtection)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		ClusterIdentifier: "my-cluster", ClusterResourceId: "cluster-abc",
		ARN:            "arn:aws:rds:us-east-1:123:cluster:my-cluster",
		Endpoint:       "my-cluster.abc.us-east-1.rds.amazonaws.com",
		ReaderEndpoint: "my-cluster-ro.abc.us-east-1.rds.amazonaws.com",
		Port:           3306, Engine: "aurora-mysql", EngineVersion: "8.0", Status: "available",
	}
	out := outputsFromObserved(obs)
	assert.Equal(t, "my-cluster", out.ClusterIdentifier)
	assert.Equal(t, "cluster-abc", out.ClusterResourceId)
	assert.Equal(t, "arn:aws:rds:us-east-1:123:cluster:my-cluster", out.ARN)
	assert.Equal(t, "my-cluster.abc.us-east-1.rds.amazonaws.com", out.Endpoint)
	assert.Equal(t, "my-cluster-ro.abc.us-east-1.rds.amazonaws.com", out.ReaderEndpoint)
	assert.Equal(t, int32(3306), out.Port)
	assert.Equal(t, "aurora-mysql", out.Engine)
	assert.Equal(t, "available", out.Status)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(AuroraClusterSpec{})
	assert.Equal(t, int32(7), spec.BackupRetentionPeriod)
	assert.NotNil(t, spec.VpcSecurityGroupIds)
	assert.NotNil(t, spec.EnabledCloudwatchLogsExports)
	assert.NotNil(t, spec.Tags)
}
