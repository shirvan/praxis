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

	"github.com/praxiscloud/praxis/internal/drivers/nlb"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueNLBName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 24 {
		name = name[:24]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func nlbSubnets(t *testing.T, ec2Client *ec2sdk.Client) []string {
	t.Helper()
	out, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("default-for-az"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(out.Subnets), 2, "need at least 2 subnets for NLB")
	var ids []string
	for _, s := range out.Subnets[:2] {
		ids = append(ids, aws.ToString(s.SubnetId))
	}
	return ids
}

func setupNLBDriver(t *testing.T) (*ingress.Client, *elbv2sdk.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)
	skipIfELBv2Unavailable(t)

	awsCfg := localstackAWSConfig(t)
	elbClient := awsclient.NewELBv2Client(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := nlb.NewNLBDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), elbClient, ec2Client
}

func TestNLBProvision(t *testing.T) {
	client, elbClient, ec2Client := setupNLBDriver(t)
	name := uniqueNLBName(t)
	subnets := nlbSubnets(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[nlb.NLBSpec, nlb.NLBOutputs](
		client, "NLB", key, "Provision",
	).Request(t.Context(), nlb.NLBSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		Name:          name,
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       subnets,
		Tags:          map[string]string{"Name": name, "env": "test"},
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

func TestNLBProvisionIdempotent(t *testing.T) {
	client, _, ec2Client := setupNLBDriver(t)
	name := uniqueNLBName(t)
	subnets := nlbSubnets(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := nlb.NLBSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		Name:          name,
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       subnets,
		Tags:          map[string]string{"Name": name},
	}

	out1, err := ingress.Object[nlb.NLBSpec, nlb.NLBOutputs](
		client, "NLB", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[nlb.NLBSpec, nlb.NLBOutputs](
		client, "NLB", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.LoadBalancerArn, out2.LoadBalancerArn)
}

func TestNLBDelete(t *testing.T) {
	client, elbClient, ec2Client := setupNLBDriver(t)
	name := uniqueNLBName(t)
	subnets := nlbSubnets(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[nlb.NLBSpec, nlb.NLBOutputs](
		client, "NLB", key, "Provision",
	).Request(t.Context(), nlb.NLBSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		Name:          name,
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       subnets,
		Tags:          map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "NLB", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, descErr := elbClient.DescribeLoadBalancers(context.Background(), &elbv2sdk.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{out.LoadBalancerArn},
	})
	assert.Error(t, descErr)
}

func TestNLBGetStatus(t *testing.T) {
	client, _, ec2Client := setupNLBDriver(t)
	name := uniqueNLBName(t)
	subnets := nlbSubnets(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[nlb.NLBSpec, nlb.NLBOutputs](
		client, "NLB", key, "Provision",
	).Request(t.Context(), nlb.NLBSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		Name:          name,
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       subnets,
		Tags:          map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "NLB", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
