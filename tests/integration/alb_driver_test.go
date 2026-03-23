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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/alb"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueALBName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 24 {
		name = name[:24]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
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
	for _, s := range out.Subnets[:2] {
		ids = append(ids, aws.ToString(s.SubnetId))
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

	awsCfg := localstackAWSConfig(t)
	elbClient := awsclient.NewELBv2Client(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := alb.NewALBDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), elbClient, ec2Client
}

func TestALBProvision(t *testing.T) {
	client, elbClient, ec2Client := setupALBDriver(t)
	name := uniqueALBName(t)
	subnets := albSubnets(t, ec2Client)
	sgId := albDefaultSgId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[alb.ALBSpec, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), alb.ALBSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		Name:           name,
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        subnets,
		SecurityGroups: []string{sgId},
		IdleTimeout:    60,
		Tags:           map[string]string{"Name": name, "env": "test"},
	})
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

	out1, err := ingress.Object[alb.ALBSpec, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[alb.ALBSpec, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.LoadBalancerArn, out2.LoadBalancerArn)
}

func TestALBDelete(t *testing.T) {
	client, elbClient, ec2Client := setupALBDriver(t)
	name := uniqueALBName(t)
	subnets := albSubnets(t, ec2Client)
	sgId := albDefaultSgId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[alb.ALBSpec, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), alb.ALBSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		Name:           name,
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        subnets,
		SecurityGroups: []string{sgId},
		IdleTimeout:    60,
		Tags:           map[string]string{"Name": name},
	})
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

func TestALBGetStatus(t *testing.T) {
	client, _, ec2Client := setupALBDriver(t)
	name := uniqueALBName(t)
	subnets := albSubnets(t, ec2Client)
	sgId := albDefaultSgId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[alb.ALBSpec, alb.ALBOutputs](
		client, "ALB", key, "Provision",
	).Request(t.Context(), alb.ALBSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		Name:           name,
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        subnets,
		SecurityGroups: []string{sgId},
		IdleTimeout:    60,
		Tags:           map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "ALB", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
