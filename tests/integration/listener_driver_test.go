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
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/listener"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueListenerName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 24 {
		name = name[:24]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

// listenerPrereqs creates an ALB and target group required for listener tests.
func listenerPrereqs(t *testing.T, elbClient *elbv2sdk.Client, ec2Client *ec2sdk.Client) (lbArn, tgArn string) {
	t.Helper()
	name := uniqueListenerName(t)

	// Get subnets
	subOut, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("default-for-az"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(subOut.Subnets), 2)
	subnets := []string{aws.ToString(subOut.Subnets[0].SubnetId), aws.ToString(subOut.Subnets[1].SubnetId)}

	// Get default SG
	sgOut, err := ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-name"), Values: []string{"default"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, sgOut.SecurityGroups)

	// Get default VPC
	vpcOut, err := ec2Client.DescribeVpcs(context.Background(), &ec2sdk.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("isDefault"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, vpcOut.Vpcs)

	// Create ALB
	lbOut, err := elbClient.CreateLoadBalancer(context.Background(), &elbv2sdk.CreateLoadBalancerInput{
		Name:           aws.String(name + "-lb"),
		Subnets:        subnets,
		SecurityGroups: []string{aws.ToString(sgOut.SecurityGroups[0].GroupId)},
		Scheme:         elbv2types.LoadBalancerSchemeEnumInternetFacing,
		Type:           elbv2types.LoadBalancerTypeEnumApplication,
	})
	require.NoError(t, err)
	require.Len(t, lbOut.LoadBalancers, 1)
	lbArn = aws.ToString(lbOut.LoadBalancers[0].LoadBalancerArn)

	// Create Target Group
	tgOut, err := elbClient.CreateTargetGroup(context.Background(), &elbv2sdk.CreateTargetGroupInput{
		Name:       aws.String(name + "-tg"),
		Protocol:   elbv2types.ProtocolEnum("HTTP"),
		Port:       aws.Int32(80),
		VpcId:      vpcOut.Vpcs[0].VpcId,
		TargetType: elbv2types.TargetTypeEnumInstance,
	})
	require.NoError(t, err)
	require.Len(t, tgOut.TargetGroups, 1)
	tgArn = aws.ToString(tgOut.TargetGroups[0].TargetGroupArn)

	t.Cleanup(func() {
		elbClient.DeleteLoadBalancer(context.Background(), &elbv2sdk.DeleteLoadBalancerInput{
			LoadBalancerArn: aws.String(lbArn),
		})
		elbClient.DeleteTargetGroup(context.Background(), &elbv2sdk.DeleteTargetGroupInput{
			TargetGroupArn: aws.String(tgArn),
		})
	})
	return lbArn, tgArn
}

func setupListenerDriver(t *testing.T) (*ingress.Client, *elbv2sdk.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)
	skipIfELBv2Unavailable(t)

	awsCfg := localstackAWSConfig(t)
	elbClient := awsclient.NewELBv2Client(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := listener.NewListenerDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), elbClient, ec2Client
}

func TestListenerProvision(t *testing.T) {
	client, elbClient, ec2Client := setupListenerDriver(t)
	lbArn, tgArn := listenerPrereqs(t, elbClient, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", uniqueListenerName(t))

	outputs, err := ingress.Object[listener.ListenerSpec, listener.ListenerOutputs](
		client, "Listener", key, "Provision",
	).Request(t.Context(), listener.ListenerSpec{
		Account:         integrationAccountName,
		LoadBalancerArn: lbArn,
		Port:            80,
		Protocol:        "HTTP",
		DefaultActions: []listener.ListenerAction{{
			Type:           "forward",
			TargetGroupArn: tgArn,
		}},
		Tags: map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.ListenerArn)
	assert.Equal(t, 80, outputs.Port)

	desc, err := elbClient.DescribeListeners(context.Background(), &elbv2sdk.DescribeListenersInput{
		ListenerArns: []string{outputs.ListenerArn},
	})
	require.NoError(t, err)
	require.Len(t, desc.Listeners, 1)
	assert.Equal(t, int32(80), aws.ToInt32(desc.Listeners[0].Port))
}

func TestListenerDelete(t *testing.T) {
	client, elbClient, ec2Client := setupListenerDriver(t)
	lbArn, tgArn := listenerPrereqs(t, elbClient, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", uniqueListenerName(t))

	out, err := ingress.Object[listener.ListenerSpec, listener.ListenerOutputs](
		client, "Listener", key, "Provision",
	).Request(t.Context(), listener.ListenerSpec{
		Account:         integrationAccountName,
		LoadBalancerArn: lbArn,
		Port:            80,
		Protocol:        "HTTP",
		DefaultActions: []listener.ListenerAction{{
			Type:           "forward",
			TargetGroupArn: tgArn,
		}},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "Listener", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, descErr := elbClient.DescribeListeners(context.Background(), &elbv2sdk.DescribeListenersInput{
		ListenerArns: []string{out.ListenerArn},
	})
	assert.Error(t, descErr)
}

func TestListenerGetStatus(t *testing.T) {
	client, elbClient, ec2Client := setupListenerDriver(t)
	lbArn, tgArn := listenerPrereqs(t, elbClient, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", uniqueListenerName(t))

	_, err := ingress.Object[listener.ListenerSpec, listener.ListenerOutputs](
		client, "Listener", key, "Provision",
	).Request(t.Context(), listener.ListenerSpec{
		Account:         integrationAccountName,
		LoadBalancerArn: lbArn,
		Port:            80,
		Protocol:        "HTTP",
		DefaultActions: []listener.ListenerAction{{
			Type:           "forward",
			TargetGroupArn: tgArn,
		}},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "Listener", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
