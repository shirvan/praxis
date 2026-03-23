//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/rdsinstance"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueDBInstanceName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupRDSInstanceDriver(t *testing.T) (*ingress.Client, *rdssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	rdsClient := awsclient.NewRDSClient(awsCfg)
	driver := rdsinstance.NewRDSInstanceDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), rdsClient
}

func TestRDSInstanceProvision_Creates(t *testing.T) {
	client, rdsClient := setupRDSInstanceDriver(t)
	name := uniqueDBInstanceName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs](
		client, "RDSInstance", key, "Provision",
	).Request(t.Context(), rdsinstance.RDSInstanceSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		DBIdentifier:       name,
		Engine:             "mysql",
		EngineVersion:      "8.0",
		InstanceClass:      "db.t3.micro",
		AllocatedStorage:   20,
		StorageType:        "gp3",
		MasterUsername:     "admin",
		MasterUserPassword: "TestPass1234!",
		Tags:               map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.DBIdentifier)
	assert.NotEmpty(t, outputs.ARN)

	desc, err := rdsClient.DescribeDBInstances(context.Background(), &rdssdk.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBInstances, 1)
	assert.Equal(t, name, aws.ToString(desc.DBInstances[0].DBInstanceIdentifier))
}

func TestRDSInstanceProvision_Idempotent(t *testing.T) {
	client, _ := setupRDSInstanceDriver(t)
	name := uniqueDBInstanceName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := rdsinstance.RDSInstanceSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		DBIdentifier:       name,
		Engine:             "mysql",
		EngineVersion:      "8.0",
		InstanceClass:      "db.t3.micro",
		AllocatedStorage:   20,
		StorageType:        "gp3",
		MasterUsername:     "admin",
		MasterUserPassword: "TestPass1234!",
		Tags:               map[string]string{"env": "test"},
	}

	out1, err := ingress.Object[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs](
		client, "RDSInstance", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs](
		client, "RDSInstance", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.ARN, out2.ARN, "re-provision should return same ARN")
}

func TestRDSInstanceImport_ExistingInstance(t *testing.T) {
	client, rdsClient := setupRDSInstanceDriver(t)
	name := uniqueDBInstanceName(t)

	_, err := rdsClient.CreateDBInstance(context.Background(), &rdssdk.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(name),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("mysql"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("TestPass1234!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, rdsinstance.RDSInstanceOutputs](
		client, "RDSInstance", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.DBIdentifier)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "RDSInstance", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestRDSInstanceDelete_Removes(t *testing.T) {
	client, rdsClient := setupRDSInstanceDriver(t)
	name := uniqueDBInstanceName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs](
		client, "RDSInstance", key, "Provision",
	).Request(t.Context(), rdsinstance.RDSInstanceSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		DBIdentifier:       name,
		Engine:             "mysql",
		EngineVersion:      "8.0",
		InstanceClass:      "db.t3.micro",
		AllocatedStorage:   20,
		StorageType:        "gp3",
		MasterUsername:     "admin",
		MasterUserPassword: "TestPass1234!",
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "RDSInstance", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = rdsClient.DescribeDBInstances(context.Background(), &rdssdk.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(name),
	})
	require.Error(t, err, "instance should be gone")
}

func TestRDSInstanceGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupRDSInstanceDriver(t)
	name := uniqueDBInstanceName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs](
		client, "RDSInstance", key, "Provision",
	).Request(t.Context(), rdsinstance.RDSInstanceSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		DBIdentifier:       name,
		Engine:             "mysql",
		EngineVersion:      "8.0",
		InstanceClass:      "db.t3.micro",
		AllocatedStorage:   20,
		StorageType:        "gp3",
		MasterUsername:     "admin",
		MasterUserPassword: "TestPass1234!",
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "RDSInstance", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
