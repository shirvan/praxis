//go:build acceptance

package acceptance

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func (env *topology) requireCrossPackLifecycle(t *testing.T) {
	suffix := acceptanceSuffix()
	name := "praxis-graph-" + suffix
	bucketName := name + "-assets"
	deploymentKey := "graph-" + suffix
	template := fmt.Sprintf(`resources: {
	vpc: {
		apiVersion: "praxis.io/alpha"
		kind:       "VPC"
		metadata: name: %q
		spec: {
			region:             "us-east-1"
			cidrBlock:          "10.42.0.0/16"
			enableDnsHostnames: true
			enableDnsSupport:   true
			tags: acceptance: %q
		}
	}
	subnet: {
		apiVersion: "praxis.io/alpha"
		kind:       "Subnet"
		metadata: name: %q
		spec: {
			region:              "us-east-1"
			vpcId:               "${resources.vpc.outputs.vpcId}"
			cidrBlock:           "10.42.1.0/24"
			availabilityZone:    "us-east-1a"
			mapPublicIpOnLaunch: true
			tags: acceptance: %q
		}
	}
	securityGroup: {
		apiVersion: "praxis.io/alpha"
		kind:       "SecurityGroup"
		metadata: name: %q
		spec: {
			groupName:   %q
			description: "Praxis production-topology acceptance"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [{
				protocol: "tcp", fromPort: 443, toPort: 443, cidrBlock: "10.42.0.0/16"
			}]
			tags: acceptance: %q
		}
	}
	server: {
		apiVersion: "praxis.io/alpha"
		kind:       "EC2Instance"
		metadata: name: %q
		spec: {
			region:           "us-east-1"
			imageId:          "ssm:///praxis/moto/base-ami"
			instanceType:     "t3.micro"
			subnetId:         "${resources.subnet.outputs.subnetId}"
			securityGroupIds: ["${resources.securityGroup.outputs.groupId}"]
			monitoring:       false
			tags: acceptance: %q
		}
	}
	assets: {
		apiVersion: "praxis.io/alpha"
		kind:       "S3Bucket"
		metadata: name: %q
		spec: {
			region:     "us-east-1"
			versioning: false
			acl:        "private"
			encryption: {enabled: true, algorithm: "AES256"}
			tags: {
				acceptance: %q
				instanceId: "${resources.server.outputs.instanceId}"
			}
		}
	}
}
`, name, suffix, name+"-subnet", suffix, name+"-sg", name+"-sg", suffix, name+"-server", suffix, bucketName, suffix)

	resources := map[string]string{
		"vpc":           "VPC",
		"subnet":        "Subnet",
		"securityGroup": "SecurityGroup",
		"server":        "EC2Instance",
		"assets":        "S3Bucket",
	}
	dependencies := map[string][]string{
		"subnet":        {"vpc"},
		"securityGroup": {"vpc"},
		"server":        {"securityGroup", "subnet"},
		"assets":        {"server"},
	}

	env.runManagedDeploymentScenario(t, managedDeploymentScenario{
		DeploymentKey: deploymentKey,
		Template:      template,
		Resources:     resources,
		Dependencies:  dependencies,
		AssertAbsent: func(t *testing.T) {
			env.assertCrossPackAbsent(t, suffix, bucketName)
		},
		AssertPresent: func(t *testing.T, detail types.DeploymentDetail) {
			env.assertCrossPackPresent(t, detail, bucketName)
		},
	})
}

func (env *topology) assertCrossPackPresent(t *testing.T, detail types.DeploymentDetail, bucketName string) {
	t.Helper()
	vpcID := outputString(t, deploymentResource(t, detail, "vpc"), "vpcId")
	subnetID := outputString(t, deploymentResource(t, detail, "subnet"), "subnetId")
	groupID := outputString(t, deploymentResource(t, detail, "securityGroup"), "groupId")
	instanceID := outputString(t, deploymentResource(t, detail, "server"), "instanceId")

	vpcs, err := env.ec2.DescribeVpcs(t.Context(), &ec2sdk.DescribeVpcsInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	require.Len(t, vpcs.Vpcs, 1)
	assert.Equal(t, vpcID, aws.ToString(vpcs.Vpcs[0].VpcId))

	subnets, err := env.ec2.DescribeSubnets(t.Context(), &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{subnetID}})
	require.NoError(t, err)
	require.Len(t, subnets.Subnets, 1)
	assert.Equal(t, vpcID, aws.ToString(subnets.Subnets[0].VpcId), "subnet must use hydrated VPC output")

	groups, err := env.ec2.DescribeSecurityGroups(t.Context(), &ec2sdk.DescribeSecurityGroupsInput{GroupIds: []string{groupID}})
	require.NoError(t, err)
	require.Len(t, groups.SecurityGroups, 1)
	assert.Equal(t, vpcID, aws.ToString(groups.SecurityGroups[0].VpcId), "security group must use hydrated VPC output")

	instances, err := env.ec2.DescribeInstances(t.Context(), &ec2sdk.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	require.Len(t, instances.Reservations, 1)
	require.Len(t, instances.Reservations[0].Instances, 1)
	instance := instances.Reservations[0].Instances[0]
	assert.Equal(t, subnetID, aws.ToString(instance.SubnetId), "instance must use hydrated subnet output")
	require.Len(t, instance.SecurityGroups, 1)
	assert.Equal(t, groupID, aws.ToString(instance.SecurityGroups[0].GroupId), "instance must use hydrated security-group output")
	assert.NotEqual(t, ec2types.InstanceStateNameTerminated, instance.State.Name)

	_, err = env.s3.HeadBucket(t.Context(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
	require.NoError(t, err)
	assert.Equal(t, instanceID, bucketTags(t, env.s3, bucketName)["instanceId"], "bucket tag must use hydrated EC2 output")
}

func (env *topology) assertCrossPackAbsent(t *testing.T, suffix, bucketName string) {
	t.Helper()
	assertBucketMissing(t, env.s3, bucketName)
	filters := []ec2types.Filter{{Name: aws.String("tag:acceptance"), Values: []string{suffix}}}

	vpcs, err := env.ec2.DescribeVpcs(t.Context(), &ec2sdk.DescribeVpcsInput{Filters: filters})
	require.NoError(t, err)
	assert.Empty(t, vpcs.Vpcs, "acceptance VPC must be absent")

	subnets, err := env.ec2.DescribeSubnets(t.Context(), &ec2sdk.DescribeSubnetsInput{Filters: filters})
	require.NoError(t, err)
	assert.Empty(t, subnets.Subnets, "acceptance subnet must be absent")

	groups, err := env.ec2.DescribeSecurityGroups(t.Context(), &ec2sdk.DescribeSecurityGroupsInput{Filters: filters})
	require.NoError(t, err)
	assert.Empty(t, groups.SecurityGroups, "acceptance security group must be absent")

	instances, err := env.ec2.DescribeInstances(t.Context(), &ec2sdk.DescribeInstancesInput{Filters: filters})
	require.NoError(t, err)
	for _, reservation := range instances.Reservations {
		for _, instance := range reservation.Instances {
			assert.Equal(t, ec2types.InstanceStateNameTerminated, instance.State.Name, "acceptance EC2 instance must be terminated")
		}
	}
}

func bucketTags(t *testing.T, client *s3sdk.Client, bucket string) map[string]string {
	t.Helper()
	result, err := client.GetBucketTagging(t.Context(), &s3sdk.GetBucketTaggingInput{Bucket: aws.String(bucket)})
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchTagSet" {
		return map[string]string{}
	}
	require.NoError(t, err)
	tags := make(map[string]string, len(result.TagSet))
	for _, tag := range result.TagSet {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags
}
