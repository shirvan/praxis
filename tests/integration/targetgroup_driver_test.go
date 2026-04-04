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

	"github.com/shirvan/praxis/internal/drivers/targetgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueTGName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 24 {
		name = name[:24]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupTargetGroupDriver(t *testing.T) (*ingress.Client, *elbv2sdk.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)
	skipIfELBv2Unavailable(t)

	awsCfg := localstackAWSConfig(t)
	elbClient := awsclient.NewELBv2Client(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := targetgroup.NewTargetGroupDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), elbClient, ec2Client
}

// skipIfELBv2Unavailable skips the test when the ELBv2 service is not
// reachable (e.g., mock server not running).
func skipIfELBv2Unavailable(t *testing.T) {
	t.Helper()
	awsCfg := localstackAWSConfig(t)
	elbClient := awsclient.NewELBv2Client(awsCfg)
	_, err := elbClient.DescribeTargetGroups(context.Background(), &elbv2sdk.DescribeTargetGroupsInput{})
	if err != nil {
		t.Skipf("ELBv2 service not available — skipping: %v", err)
	}
}

func tgDefaultVpcId(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	out, err := ec2Client.DescribeVpcs(context.Background(), &ec2sdk.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("isDefault"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Vpcs, "Moto should have a default VPC")
	return aws.ToString(out.Vpcs[0].VpcId)
}

func TestTargetGroupProvision(t *testing.T) {
	client, elbClient, ec2Client := setupTargetGroupDriver(t)
	name := uniqueTGName(t)
	vpcId := tgDefaultVpcId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Provision",
	).Request(t.Context(), targetgroup.TargetGroupSpec{
		Account:  integrationAccountName,
		Region:   "us-east-1",
		Name:     name,
		Protocol: "HTTP",
		Port:     8080,
		VpcId:    vpcId,
		HealthCheck: targetgroup.HealthCheck{
			Protocol:           "HTTP",
			Path:               "/health",
			Port:               "traffic-port",
			HealthyThreshold:   3,
			UnhealthyThreshold: 2,
			Interval:           30,
			Timeout:            5,
		},
		DeregistrationDelay: 60,
		Tags:                map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.TargetGroupArn)
	assert.Equal(t, name, outputs.TargetGroupName)

	desc, err := elbClient.DescribeTargetGroups(context.Background(), &elbv2sdk.DescribeTargetGroupsInput{
		TargetGroupArns: []string{outputs.TargetGroupArn},
	})
	require.NoError(t, err)
	require.Len(t, desc.TargetGroups, 1)
	assert.Equal(t, name, aws.ToString(desc.TargetGroups[0].TargetGroupName))
}

func TestTargetGroupProvisionIdempotent(t *testing.T) {
	client, _, ec2Client := setupTargetGroupDriver(t)
	name := uniqueTGName(t)
	vpcId := tgDefaultVpcId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := targetgroup.TargetGroupSpec{
		Account:  integrationAccountName,
		Region:   "us-east-1",
		Name:     name,
		Protocol: "HTTP",
		Port:     8080,
		VpcId:    vpcId,
		Tags:     map[string]string{"Name": name},
	}

	out1, err := ingress.Object[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.NotEmpty(t, out1.TargetGroupArn)

	out2, err := ingress.Object[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.TargetGroupArn, out2.TargetGroupArn, "re-provision should reuse same target group")
}

func TestTargetGroupImport(t *testing.T) {
	client, elbClient, ec2Client := setupTargetGroupDriver(t)
	name := uniqueTGName(t)
	vpcId := tgDefaultVpcId(t, ec2Client)

	createOut, err := elbClient.CreateTargetGroup(context.Background(), &elbv2sdk.CreateTargetGroupInput{
		Name:       aws.String(name),
		Protocol:   "HTTP",
		Port:       aws.Int32(80),
		VpcId:      aws.String(vpcId),
		TargetType: "instance",
	})
	require.NoError(t, err)
	require.Len(t, createOut.TargetGroups, 1)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.TargetGroupArn)
	assert.Equal(t, name, outputs.TargetGroupName)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "TargetGroup", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestTargetGroupUpdateHealthCheck(t *testing.T) {
	client, elbClient, ec2Client := setupTargetGroupDriver(t)
	name := uniqueTGName(t)
	vpcId := tgDefaultVpcId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := targetgroup.TargetGroupSpec{
		Account:  integrationAccountName,
		Region:   "us-east-1",
		Name:     name,
		Protocol: "HTTP",
		Port:     8080,
		VpcId:    vpcId,
		HealthCheck: targetgroup.HealthCheck{
			Protocol:           "HTTP",
			Path:               "/health",
			Port:               "traffic-port",
			HealthyThreshold:   3,
			UnhealthyThreshold: 2,
			Interval:           30,
			Timeout:            5,
		},
		Tags: map[string]string{"Name": name},
	}
	out, err := ingress.Object[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	spec.HealthCheck.Path = "/ready"
	spec.HealthCheck.Interval = 15
	_, err = ingress.Object[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	desc, err := elbClient.DescribeTargetGroups(context.Background(), &elbv2sdk.DescribeTargetGroupsInput{
		TargetGroupArns: []string{out.TargetGroupArn},
	})
	require.NoError(t, err)
	require.Len(t, desc.TargetGroups, 1)
	assert.Equal(t, "/ready", aws.ToString(desc.TargetGroups[0].HealthCheckPath))
}

func TestTargetGroupDelete(t *testing.T) {
	client, elbClient, ec2Client := setupTargetGroupDriver(t)
	name := uniqueTGName(t)
	vpcId := tgDefaultVpcId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Provision",
	).Request(t.Context(), targetgroup.TargetGroupSpec{
		Account:  integrationAccountName,
		Region:   "us-east-1",
		Name:     name,
		Protocol: "HTTP",
		Port:     8080,
		VpcId:    vpcId,
		Tags:     map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "TargetGroup", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, descErr := elbClient.DescribeTargetGroups(context.Background(), &elbv2sdk.DescribeTargetGroupsInput{
		TargetGroupArns: []string{out.TargetGroupArn},
	})
	assert.Error(t, descErr, "target group should not exist after deletion")
}

func TestTargetGroupDeleteObservedBlocked(t *testing.T) {
	client, elbClient, ec2Client := setupTargetGroupDriver(t)
	name := uniqueTGName(t)
	vpcId := tgDefaultVpcId(t, ec2Client)

	_, err := elbClient.CreateTargetGroup(context.Background(), &elbv2sdk.CreateTargetGroupInput{
		Name:       aws.String(name),
		Protocol:   "HTTP",
		Port:       aws.Int32(80),
		VpcId:      aws.String(vpcId),
		TargetType: "instance",
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	_, err = ingress.Object[types.ImportRef, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: name, Account: integrationAccountName})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "TargetGroup", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestTargetGroupGetStatus(t *testing.T) {
	client, _, ec2Client := setupTargetGroupDriver(t)
	name := uniqueTGName(t)
	vpcId := tgDefaultVpcId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](
		client, "TargetGroup", key, "Provision",
	).Request(t.Context(), targetgroup.TargetGroupSpec{
		Account:  integrationAccountName,
		Region:   "us-east-1",
		Name:     name,
		Protocol: "HTTP",
		Port:     8080,
		VpcId:    vpcId,
		Tags:     map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "TargetGroup", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
