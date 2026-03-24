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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/registry"
)

type dataSourcePlanEnv struct {
	ingress   *ingress.Client
	s3Client  *s3sdk.Client
	ec2Client *ec2sdk.Client
}

func setupDataSourcePlanStack(t *testing.T) *dataSourcePlanEnv {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	authClient := authservice.NewAuthClient()
	providers := provider.NewRegistry(authClient)
	absSchemaDir, err := filepath.Abs("../../schemas")
	require.NoError(t, err)

	cmdService := command.NewPraxisCommandService(config.Config{SchemaDir: absSchemaDir}, authClient, providers)
	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(cmdService),
		restate.Reflect(registry.PolicyRegistry{}),
	)

	return &dataSourcePlanEnv{
		ingress:   env.Ingress(),
		s3Client:  s3sdk.NewFromConfig(awsCfg),
		ec2Client: ec2sdk.NewFromConfig(awsCfg),
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
	).Request(t.Context(), command.PlanRequest{Template: `
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
		apiVersion: "praxis.io/v1"
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
	).Request(t.Context(), command.PlanRequest{Template: `
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
		apiVersion: "praxis.io/v1"
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

func TestDataSourcePlan_NotFound_Integration(t *testing.T) {
	env := setupDataSourcePlanStack(t)
	_, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{Template: `
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
		apiVersion: "praxis.io/v1"
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
