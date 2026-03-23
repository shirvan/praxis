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

	"github.com/praxiscloud/praxis/internal/drivers/listenerrule"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueRuleName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 24 {
		name = name[:24]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

// rulePrereqs creates an ALB, target group, and listener required for rule tests.
func rulePrereqs(t *testing.T, elbClient *elbv2sdk.Client, ec2Client *ec2sdk.Client) (listenerArn, tgArn string) {
	t.Helper()
	name := uniqueRuleName(t)

	subOut, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("default-for-az"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(subOut.Subnets), 2)
	subnets := []string{aws.ToString(subOut.Subnets[0].SubnetId), aws.ToString(subOut.Subnets[1].SubnetId)}

	sgOut, err := ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-name"), Values: []string{"default"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, sgOut.SecurityGroups)

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
	lbArn := aws.ToString(lbOut.LoadBalancers[0].LoadBalancerArn)

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

	// Create Listener
	lnOut, err := elbClient.CreateListener(context.Background(), &elbv2sdk.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Port:            aws.Int32(80),
		Protocol:        elbv2types.ProtocolEnumHttp,
		DefaultActions: []elbv2types.Action{{
			Type:           elbv2types.ActionTypeEnumForward,
			TargetGroupArn: aws.String(tgArn),
		}},
	})
	require.NoError(t, err)
	require.Len(t, lnOut.Listeners, 1)
	listenerArn = aws.ToString(lnOut.Listeners[0].ListenerArn)

	t.Cleanup(func() {
		elbClient.DeleteListener(context.Background(), &elbv2sdk.DeleteListenerInput{
			ListenerArn: aws.String(listenerArn),
		})
		elbClient.DeleteLoadBalancer(context.Background(), &elbv2sdk.DeleteLoadBalancerInput{
			LoadBalancerArn: aws.String(lbArn),
		})
		elbClient.DeleteTargetGroup(context.Background(), &elbv2sdk.DeleteTargetGroupInput{
			TargetGroupArn: aws.String(tgArn),
		})
	})
	return listenerArn, tgArn
}

func setupListenerRuleDriver(t *testing.T) (*ingress.Client, *elbv2sdk.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)
	skipIfELBv2Unavailable(t)

	awsCfg := localstackAWSConfig(t)
	elbClient := awsclient.NewELBv2Client(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := listenerrule.NewListenerRuleDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), elbClient, ec2Client
}

func TestListenerRuleProvision(t *testing.T) {
	client, elbClient, ec2Client := setupListenerRuleDriver(t)
	listenerArn, tgArn := rulePrereqs(t, elbClient, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", uniqueRuleName(t))

	outputs, err := ingress.Object[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs](
		client, "ListenerRule", key, "Provision",
	).Request(t.Context(), listenerrule.ListenerRuleSpec{
		Account:     integrationAccountName,
		ListenerArn: listenerArn,
		Priority:    100,
		Conditions: []listenerrule.RuleCondition{{
			Field:  "path-pattern",
			Values: []string{"/api/*"},
		}},
		Actions: []listenerrule.RuleAction{{
			Type:           "forward",
			TargetGroupArn: tgArn,
		}},
		Tags: map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.RuleArn)
	assert.Equal(t, 100, outputs.Priority)

	desc, err := elbClient.DescribeRules(context.Background(), &elbv2sdk.DescribeRulesInput{
		RuleArns: []string{outputs.RuleArn},
	})
	require.NoError(t, err)
	require.Len(t, desc.Rules, 1)
	assert.Equal(t, "100", aws.ToString(desc.Rules[0].Priority))
}

func TestListenerRuleDelete(t *testing.T) {
	client, elbClient, ec2Client := setupListenerRuleDriver(t)
	listenerArn, tgArn := rulePrereqs(t, elbClient, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", uniqueRuleName(t))

	out, err := ingress.Object[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs](
		client, "ListenerRule", key, "Provision",
	).Request(t.Context(), listenerrule.ListenerRuleSpec{
		Account:     integrationAccountName,
		ListenerArn: listenerArn,
		Priority:    200,
		Conditions: []listenerrule.RuleCondition{{
			Field:  "path-pattern",
			Values: []string{"/health"},
		}},
		Actions: []listenerrule.RuleAction{{
			Type:           "forward",
			TargetGroupArn: tgArn,
		}},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "ListenerRule", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, descErr := elbClient.DescribeRules(context.Background(), &elbv2sdk.DescribeRulesInput{
		RuleArns: []string{out.RuleArn},
	})
	assert.Error(t, descErr)
}

func TestListenerRuleGetStatus(t *testing.T) {
	client, elbClient, ec2Client := setupListenerRuleDriver(t)
	listenerArn, tgArn := rulePrereqs(t, elbClient, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", uniqueRuleName(t))

	_, err := ingress.Object[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs](
		client, "ListenerRule", key, "Provision",
	).Request(t.Context(), listenerrule.ListenerRuleSpec{
		Account:     integrationAccountName,
		ListenerArn: listenerArn,
		Priority:    300,
		Conditions: []listenerrule.RuleCondition{{
			Field:  "path-pattern",
			Values: []string{"/status"},
		}},
		Actions: []listenerrule.RuleAction{{
			Type:           "forward",
			TargetGroupArn: tgArn,
		}},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "ListenerRule", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
