//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbv2sdk "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/alb"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueALBName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 15 {
		name = name[:15]
	}
	return fmt.Sprintf("%s-%x", name, uint64(time.Now().UnixNano()))
}

func albSubnets(t *testing.T, ec2Client *ec2sdk.Client) []string {
	t.Helper()
	out, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("default-for-az"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(out.Subnets), 2, "need at least 2 subnets for ALB")
	var ids []string
	for i := range out.Subnets[:2] {
		ids = append(ids, aws.ToString(out.Subnets[i].SubnetId))
	}
	return ids
}

func albDefaultSgId(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	out, err := ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-name"), Values: []string{"default"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.SecurityGroups)
	return aws.ToString(out.SecurityGroups[0].GroupId)
}

func setupALBDriver(t *testing.T) (*ingress.Client, *elbv2sdk.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)
	skipIfELBv2Unavailable(t)

	awsCfg := motoAWSConfig(t)
	elbClient := awsclient.NewELBv2Client(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := alb.NewGenericALBDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, elbClient, ec2Client
}

func TestALBProvision(t *testing.T) {
	client, elbClient, ec2Client := setupALBDriver(t)
	name := uniqueALBName(t)
	subnets := albSubnets(t, ec2Client)
	sgId := albDefaultSgId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[types.ProvisionRequest, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), provisionRequest(t, alb.ALBSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		Name:           name,
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        subnets,
		SecurityGroups: []string{sgId},
		IdleTimeout:    60,
		Tags:           map[string]string{"Name": name, "env": "test"},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.LoadBalancerArn)
	assert.NotEmpty(t, outputs.DnsName)

	desc, err := elbClient.DescribeLoadBalancers(context.Background(), &elbv2sdk.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{outputs.LoadBalancerArn},
	})
	require.NoError(t, err)
	require.Len(t, desc.LoadBalancers, 1)
	assert.Equal(t, name, aws.ToString(desc.LoadBalancers[0].LoadBalancerName))
}

func TestALBProvisionIdempotent(t *testing.T) {
	client, _, ec2Client := setupALBDriver(t)
	name := uniqueALBName(t)
	subnets := albSubnets(t, ec2Client)
	sgId := albDefaultSgId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := alb.ALBSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		Name:           name,
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        subnets,
		SecurityGroups: []string{sgId},
		IdleTimeout:    60,
		Tags:           map[string]string{"Name": name},
	}

	out1, err := ingress.Object[types.ProvisionRequest, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)

	out2, err := ingress.Object[types.ProvisionRequest, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, out1.LoadBalancerArn, out2.LoadBalancerArn)
}

func TestALBDelete(t *testing.T) {
	client, elbClient, ec2Client := setupALBDriver(t)
	name := uniqueALBName(t)
	subnets := albSubnets(t, ec2Client)
	sgId := albDefaultSgId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[types.ProvisionRequest, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), provisionRequest(t, alb.ALBSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		Name:           name,
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        subnets,
		SecurityGroups: []string{sgId},
		IdleTimeout:    60,
		Tags:           map[string]string{"Name": name},
	}))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "ALB", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, descErr := elbClient.DescribeLoadBalancers(context.Background(), &elbv2sdk.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{out.LoadBalancerArn},
	})
	assert.Error(t, descErr)
}

// elbTagValue returns the value of the named ELBv2 tag on the resource, or "" when absent.
func elbTagValue(t *testing.T, elbClient *elbv2sdk.Client, arn, key string) string {
	t.Helper()
	out, err := elbClient.DescribeTags(context.Background(), &elbv2sdk.DescribeTagsInput{
		ResourceArns: []string{arn},
	})
	require.NoError(t, err)
	for _, desc := range out.TagDescriptions {
		for _, tag := range desc.Tags {
			if aws.ToString(tag.Key) == key {
				return aws.ToString(tag.Value)
			}
		}
	}
	return ""
}

func TestALBReconcile_DetectsTagDrift(t *testing.T) {
	client, elbClient, ec2Client := setupALBDriver(t)
	name := uniqueALBName(t)
	subnets := albSubnets(t, ec2Client)
	sgId := albDefaultSgId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[types.ProvisionRequest, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), provisionRequest(t, alb.ALBSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		Name:           name,
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        subnets,
		SecurityGroups: []string{sgId},
		IdleTimeout:    60,
		Tags:           map[string]string{"Name": name, "env": "test"},
	}))
	require.NoError(t, err)

	// Externally overwrite a tag value to introduce drift.
	_, err = elbClient.AddTags(context.Background(), &elbv2sdk.AddTagsInput{
		ResourceArns: []string{outputs.LoadBalancerArn},
		Tags:         []elbv2types.Tag{{Key: aws.String("env"), Value: aws.String("hacked")}},
	})
	require.NoError(t, err)

	// Verify the external mutation landed before reconciling; otherwise there
	// is no observable drift and the scenario can only run against real AWS.
	if elbTagValue(t, elbClient, outputs.LoadBalancerArn, "env") != "hacked" {
		t.Skip("Moto does not apply AddTags to load balancers")
	}

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "ALB", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")

	assert.Equal(t, "test", elbTagValue(t, elbClient, outputs.LoadBalancerArn, "env"), "tag should be restored to desired value")
}

func TestALBGetStatus(t *testing.T) {
	client, _, ec2Client := setupALBDriver(t)
	name := uniqueALBName(t)
	subnets := albSubnets(t, ec2Client)
	sgId := albDefaultSgId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[types.ProvisionRequest, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), provisionRequest(t, alb.ALBSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		Name:           name,
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        subnets,
		SecurityGroups: []string{sgId},
		IdleTimeout:    60,
		Tags:           map[string]string{"Name": name},
	}))
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "ALB", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
