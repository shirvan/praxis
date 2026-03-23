package rdsinstance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewRDSInstanceDriver(nil)
	assert.Equal(t, "RDSInstance", drv.ServiceName())
}

func TestValidateSpec_Valid(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{
		Region: "us-east-1", DBIdentifier: "mydb", Engine: "mysql", EngineVersion: "8.0",
		InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUsername: "admin", MasterUserPassword: "secret123",
	})
	assert.NoError(t, err)
}

func TestValidateSpec_AuroraInstance(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{
		Region: "us-east-1", DBIdentifier: "mydb", Engine: "aurora-mysql", EngineVersion: "8.0",
		InstanceClass: "db.r5.large", DBClusterIdentifier: "my-cluster",
	})
	assert.NoError(t, err)
}

func TestValidateSpec_MissingRegion(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{DBIdentifier: "mydb", Engine: "mysql", EngineVersion: "8.0", InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "region is required")
}

func TestValidateSpec_MissingDBIdentifier(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{Region: "us-east-1", Engine: "mysql", EngineVersion: "8.0", InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "dbIdentifier is required")
}

func TestValidateSpec_MissingEngine(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{Region: "us-east-1", DBIdentifier: "mydb", EngineVersion: "8.0", InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "engine is required")
}

func TestValidateSpec_MissingEngineVersion(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{Region: "us-east-1", DBIdentifier: "mydb", Engine: "mysql", InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "engineVersion is required")
}

func TestValidateSpec_MissingInstanceClass(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{Region: "us-east-1", DBIdentifier: "mydb", Engine: "mysql", EngineVersion: "8.0", AllocatedStorage: 20, MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "instanceClass is required")
}

func TestValidateSpec_MissingStorageForNonAurora(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{Region: "us-east-1", DBIdentifier: "mydb", Engine: "mysql", EngineVersion: "8.0", InstanceClass: "db.t3.micro", MasterUsername: "admin", MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "allocatedStorage is required")
}

func TestValidateSpec_MissingMasterUsernameForNonAurora(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{Region: "us-east-1", DBIdentifier: "mydb", Engine: "mysql", EngineVersion: "8.0", InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUserPassword: "secret"})
	assert.ErrorContains(t, err, "masterUsername is required")
}

func TestValidateSpec_MissingMasterPasswordForNonAurora(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{Region: "us-east-1", DBIdentifier: "mydb", Engine: "mysql", EngineVersion: "8.0", InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUsername: "admin"})
	assert.ErrorContains(t, err, "masterUserPassword is required")
}

func TestValidateSpec_MonitoringIntervalWithoutRole(t *testing.T) {
	err := validateSpec(RDSInstanceSpec{Region: "us-east-1", DBIdentifier: "mydb", Engine: "mysql", EngineVersion: "8.0", InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUsername: "admin", MasterUserPassword: "secret", MonitoringInterval: 60})
	assert.ErrorContains(t, err, "monitoringRoleArn is required")
}

func TestValidateExisting_Valid(t *testing.T) {
	err := validateExisting(
		RDSInstanceSpec{DBIdentifier: "mydb", Engine: "mysql", MasterUsername: "admin"},
		ObservedState{DBIdentifier: "mydb", Engine: "mysql", MasterUsername: "admin"},
	)
	assert.NoError(t, err)
}

func TestValidateExisting_ImmutableEngine(t *testing.T) {
	err := validateExisting(
		RDSInstanceSpec{DBIdentifier: "mydb", Engine: "postgres"},
		ObservedState{DBIdentifier: "mydb", Engine: "mysql"},
	)
	assert.ErrorContains(t, err, "engine is immutable")
}

func TestValidateExisting_ImmutableDBIdentifier(t *testing.T) {
	err := validateExisting(
		RDSInstanceSpec{DBIdentifier: "newdb"},
		ObservedState{DBIdentifier: "olddb"},
	)
	assert.ErrorContains(t, err, "dbIdentifier is immutable")
}

func TestValidateExisting_ImmutableMasterUsername(t *testing.T) {
	err := validateExisting(
		RDSInstanceSpec{DBIdentifier: "mydb", Engine: "mysql", MasterUsername: "newuser"},
		ObservedState{DBIdentifier: "mydb", Engine: "mysql", MasterUsername: "admin"},
	)
	assert.ErrorContains(t, err, "masterUsername is immutable")
}

func TestValidateExisting_ImmutableDBClusterIdentifier(t *testing.T) {
	err := validateExisting(
		RDSInstanceSpec{DBIdentifier: "mydb", Engine: "mysql", DBClusterIdentifier: "new-cluster"},
		ObservedState{DBIdentifier: "mydb", Engine: "mysql", DBClusterIdentifier: "old-cluster"},
	)
	assert.ErrorContains(t, err, "dbClusterIdentifier is immutable")
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		DBIdentifier: "mydb", Engine: "mysql", EngineVersion: "8.0", InstanceClass: "db.t3.micro",
		AllocatedStorage: 20, StorageType: "gp3", MultiAZ: true, PubliclyAccessible: false,
		BackupRetentionPeriod: 7, DeletionProtection: true, StorageEncrypted: true,
		Tags: map[string]string{"env": "prod", "praxis:managed-key": "key"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "mydb", spec.DBIdentifier)
	assert.Equal(t, "mysql", spec.Engine)
	assert.Equal(t, "8.0", spec.EngineVersion)
	assert.Equal(t, "db.t3.micro", spec.InstanceClass)
	assert.Equal(t, int32(20), spec.AllocatedStorage)
	assert.Equal(t, "gp3", spec.StorageType)
	assert.True(t, spec.MultiAZ)
	assert.True(t, spec.DeletionProtection)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		DBIdentifier: "mydb", DbiResourceId: "db-abc123", ARN: "arn:aws:rds:us-east-1:123456:db:mydb",
		Endpoint: "mydb.abc.us-east-1.rds.amazonaws.com", Port: 3306, Engine: "mysql", EngineVersion: "8.0", Status: "available",
	}
	out := outputsFromObserved(obs)
	assert.Equal(t, "mydb", out.DBIdentifier)
	assert.Equal(t, "db-abc123", out.DbiResourceId)
	assert.Equal(t, "arn:aws:rds:us-east-1:123456:db:mydb", out.ARN)
	assert.Equal(t, "mydb.abc.us-east-1.rds.amazonaws.com", out.Endpoint)
	assert.Equal(t, int32(3306), out.Port)
	assert.Equal(t, "mysql", out.Engine)
	assert.Equal(t, "available", out.Status)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(RDSInstanceSpec{})
	assert.Equal(t, "gp3", spec.StorageType)
	assert.Equal(t, int32(7), spec.BackupRetentionPeriod)
	assert.NotNil(t, spec.Tags)
	assert.NotNil(t, spec.VpcSecurityGroupIds)
}

func TestApplyDefaults_AuroraSkipsBackupDefault(t *testing.T) {
	spec := applyDefaults(RDSInstanceSpec{DBClusterIdentifier: "my-cluster"})
	assert.Equal(t, int32(0), spec.BackupRetentionPeriod)
}
