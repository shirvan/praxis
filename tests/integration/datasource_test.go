//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	secretssdk "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	ssmsdk "github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/shirvan/praxis/internal/restatetest"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/registry"
	s3driver "github.com/shirvan/praxis/internal/drivers/s3"
	sgdriver "github.com/shirvan/praxis/internal/drivers/sg"
)

type dataSourcePlanEnv struct {
	ingress      *ingress.Client
	s3Client     *s3sdk.Client
	ec2Client    *ec2sdk.Client
	secretClient *secretssdk.Client
	ssmClient    *ssmsdk.Client
}

func setupDataSourcePlanStack(t *testing.T) *dataSourcePlanEnv {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	authClient := authservice.NewAuthClient()
	providers := provider.NewRegistry(authClient)
	absSchemaDir, err := filepath.Abs("../../schemas")
	require.NoError(t, err)

	cmdService := command.NewPraxisCommandService(config.Config{SchemaDir: absSchemaDir}, authClient, providers)
	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(cmdService),
		restate.Reflect(registry.PolicyRegistry{}),
		// Plan consults prior deployment state for diff computation and
		// collects live outputs from the drivers of expression-bearing resources.
		restate.Reflect(orchestrator.DeploymentStateObj{}),
		restate.Reflect(s3driver.NewGenericS3BucketDriver(authClient)),
		restate.Reflect(sgdriver.NewGenericSecurityGroupDriver(authClient)),
	)

	return &dataSourcePlanEnv{
		ingress:      env.Ingress(),
		s3Client:     s3sdk.NewFromConfig(awsCfg),
		ec2Client:    ec2sdk.NewFromConfig(awsCfg),
		secretClient: secretssdk.NewFromConfig(awsCfg),
		ssmClient:    ssmsdk.NewFromConfig(awsCfg),
	}
}

func TestDataSourcePlan_VPCLookup_Integration(t *testing.T) {
	env := setupDataSourcePlanStack(t)
	vpcName := uniqueVpcName(t)
	createOut, err := env.ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{
		CidrBlock: aws.String(uniqueCidr()),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeVpc,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(vpcName)}},
		}},
	})
	require.NoError(t, err)
	vpcID := aws.ToString(createOut.Vpc.VpcId)

	resp, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{Account: integrationAccountName, Template: `
data: {
	existingVpc: {
		kind: "VPC"
		region: "us-east-1"
		filter: {
			name: "` + vpcName + `"
		}
	}
}
resources: {
	webSG: {
		apiVersion: "praxis.io/alpha"
		kind: "SecurityGroup"
		metadata: name: "web-sg"
		spec: {
			groupName: "web-sg"
			description: "Web SG"
			vpcId: "${data.existingVpc.outputs.vpcId}"
		}
	}
}`})
	require.NoError(t, err)
	require.NotNil(t, resp.Plan)
	assert.Equal(t, 1, resp.Plan.Summary.ToCreate)
	require.Contains(t, resp.DataSources, "existingVpc")
	assert.Equal(t, vpcID, resp.DataSources["existingVpc"].Outputs["vpcId"])
	assert.Contains(t, resp.Rendered, vpcID)
}

func TestDataSourcePlan_S3Lookup_Integration(t *testing.T) {
	env := setupDataSourcePlanStack(t)
	bucket := uniqueBucket(t)
	_, err := env.s3Client.CreateBucket(context.Background(), &s3sdk.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	resp, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{Account: integrationAccountName, Template: `
data: {
	existingBucket: {
		kind: "S3Bucket"
		filter: {
			name: "` + bucket + `"
		}
	}
}
resources: {
	copyBucket: {
		apiVersion: "praxis.io/alpha"
		kind: "S3Bucket"
		metadata: name: "` + uniqueBucket(t) + `"
		spec: {
			region: "us-east-1"
			tags: {
				sourceBucket: "${data.existingBucket.outputs.bucketName}"
			}
		}
	}
}`})
	require.NoError(t, err)
	require.Contains(t, resp.DataSources, "existingBucket")
	assert.Equal(t, bucket, resp.DataSources["existingBucket"].Outputs["bucketName"])
	assert.Contains(t, resp.Rendered, bucket)
}

func TestDataSourcePlan_EC2LookupByNameAndTag_Integration(t *testing.T) {
	env := setupDataSourcePlanStack(t)
	instanceName := uniqueInstanceName(t)
	runOut, err := env.ec2Client.RunInstances(context.Background(), &ec2sdk.RunInstancesInput{
		ImageId:      aws.String("ami-0123456789abcdef0"),
		InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags: []ec2types.Tag{
				{Key: aws.String("Name"), Value: aws.String(instanceName)},
				{Key: aws.String("environment"), Value: aws.String("integration")},
			},
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, runOut.Instances)
	instanceID := aws.ToString(runOut.Instances[0].InstanceId)

	resp, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{Account: integrationAccountName, Template: `
data: {
	existingInstance: {
		kind: "EC2Instance"
		region: "us-east-1"
		filter: {
			name: "` + instanceName + `"
			tag: environment: "integration"
		}
	}
}
resources: {
	bucket: {
		apiVersion: "praxis.io/alpha"
		kind: "S3Bucket"
		metadata: {name: "` + uniqueBucket(t) + `", labels: {}}
		spec: {
			region: "us-east-1"
			tags: sourceInstance: "${data.existingInstance.outputs.instanceId}"
		}
	}
}`})
	require.NoError(t, err)
	require.Contains(t, resp.DataSources, "existingInstance")
	assert.Equal(t, instanceID, resp.DataSources["existingInstance"].Outputs["instanceId"])
	assert.Contains(t, resp.Rendered, instanceID)
}

func TestDataSourcePlan_SecretAndSSMMetadataLookup_Integration(t *testing.T) {
	env := setupDataSourcePlanStack(t)
	secretName := "datasource-secret-" + uniqueBucket(t)
	secretOut, err := env.secretClient.CreateSecret(context.Background(), &secretssdk.CreateSecretInput{
		Name: aws.String(secretName), SecretString: aws.String("must-not-appear"),
	})
	require.NoError(t, err)
	parameterName := "/praxis/datasource/" + uniqueBucket(t)
	_, err = env.ssmClient.PutParameter(context.Background(), &ssmsdk.PutParameterInput{
		Name: aws.String(parameterName), Value: aws.String("must-not-appear"), Type: ssmtypes.ParameterTypeSecureString,
	})
	require.NoError(t, err)

	resp, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{Account: integrationAccountName, Template: `
data: {
	secret: {
		kind: "SecretsManagerSecret"
		region: "us-east-1"
		filter: name: "` + secretName + `"
	}
	parameter: {
		kind: "SSMParameter"
		region: "us-east-1"
		filter: name: "` + parameterName + `"
	}
}
resources: {
	bucket: {
		apiVersion: "praxis.io/alpha"
		kind: "S3Bucket"
		metadata: {name: "` + uniqueBucket(t) + `", labels: {}}
		spec: {
			region: "us-east-1"
			tags: {
				secretName: "${data.secret.outputs.name}"
				parameterName: "${data.parameter.outputs.parameterName}"
			}
		}
	}
}`})
	require.NoError(t, err)
	assert.Equal(t, secretName, resp.DataSources["secret"].Outputs["name"])
	assert.Equal(t, aws.ToString(secretOut.VersionId), resp.DataSources["secret"].Outputs["versionId"])
	assert.NotContains(t, resp.DataSources["secret"].Outputs, "secretString")
	assert.Equal(t, parameterName, resp.DataSources["parameter"].Outputs["parameterName"])
	assert.NotContains(t, resp.DataSources["parameter"].Outputs, "value")
	assert.NotContains(t, resp.Rendered, "must-not-appear")
}

func TestDataSourcePlan_NotFound_Integration(t *testing.T) {
	env := setupDataSourcePlanStack(t)
	_, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{Account: integrationAccountName, Template: `
data: {
	missingBucket: {
		kind: "S3Bucket"
		filter: {
			name: "definitely-missing-bucket"
		}
	}
}
resources: {
	bucket: {
		apiVersion: "praxis.io/alpha"
		kind: "S3Bucket"
		metadata: name: "placeholder-bucket"
		spec: {
			region: "us-east-1"
			tags: {
				source: "${data.missingBucket.outputs.bucketName}"
			}
		}
	}
}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
