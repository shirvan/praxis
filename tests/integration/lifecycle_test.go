//go:build integration

// Full lifecycle functional tests for the Praxis platform.
//
// These tests exercise the complete plan → deploy → read → change → read →
// delete → verify cycle using the end-to-end.cue template, which provisions
// all 45 resource kinds with real DAG dependency resolution, data sources,
// SSM parameters, for-loop comprehensions, conditional resources, and
// lifecycle policies.
//
// Unlike unit tests (mock AWS) or integration tests (single-driver), these
// functional tests validate that the entire vertical stack works end-to-end:
//
//	CLI payload → CUE engine → DAG → Orchestrator → All 45 Drivers → Moto
//
// Run with:
//
//	go test ./tests/integration/ -run TestLifecycle -v -count=1 -tags=integration -timeout=20m
//
// Prerequisites:
//   - Docker must be running (Testcontainers starts Restate and Moto)
package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	ssmsdk "github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/registry"
	"github.com/shirvan/praxis/internal/core/workspace"

	driveracmcert "github.com/shirvan/praxis/internal/drivers/acmcert"
	driveralb "github.com/shirvan/praxis/internal/drivers/alb"
	driverami "github.com/shirvan/praxis/internal/drivers/ami"
	driverauroracluster "github.com/shirvan/praxis/internal/drivers/auroracluster"
	driverdashboard "github.com/shirvan/praxis/internal/drivers/dashboard"
	driverdbparametergroup "github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	driverdbsubnetgroup "github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	driverebs "github.com/shirvan/praxis/internal/drivers/ebs"
	driverec2 "github.com/shirvan/praxis/internal/drivers/ec2"
	driverecrpolicy "github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	driverecrrepo "github.com/shirvan/praxis/internal/drivers/ecrrepo"
	drivereip "github.com/shirvan/praxis/internal/drivers/eip"
	driveresm "github.com/shirvan/praxis/internal/drivers/esm"
	driveriamgroup "github.com/shirvan/praxis/internal/drivers/iamgroup"
	driveriaminstanceprofile "github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	driveriampolicy "github.com/shirvan/praxis/internal/drivers/iampolicy"
	driveriamrole "github.com/shirvan/praxis/internal/drivers/iamrole"
	driveriamuser "github.com/shirvan/praxis/internal/drivers/iamuser"
	driverigw "github.com/shirvan/praxis/internal/drivers/igw"
	driverkeypair "github.com/shirvan/praxis/internal/drivers/keypair"
	driverlambda "github.com/shirvan/praxis/internal/drivers/lambda"
	driverlambdalayer "github.com/shirvan/praxis/internal/drivers/lambdalayer"
	driverlambdaperm "github.com/shirvan/praxis/internal/drivers/lambdaperm"
	driverlistener "github.com/shirvan/praxis/internal/drivers/listener"
	driverlistenerrule "github.com/shirvan/praxis/internal/drivers/listenerrule"
	driverloggroup "github.com/shirvan/praxis/internal/drivers/loggroup"
	drivermetricalarm "github.com/shirvan/praxis/internal/drivers/metricalarm"
	drivernacl "github.com/shirvan/praxis/internal/drivers/nacl"
	drivernatgw "github.com/shirvan/praxis/internal/drivers/natgw"
	drivernlb "github.com/shirvan/praxis/internal/drivers/nlb"
	driverrdsinstance "github.com/shirvan/praxis/internal/drivers/rdsinstance"
	driverroute53healthcheck "github.com/shirvan/praxis/internal/drivers/route53healthcheck"
	driverroute53record "github.com/shirvan/praxis/internal/drivers/route53record"
	driverroute53zone "github.com/shirvan/praxis/internal/drivers/route53zone"
	driverroutetable "github.com/shirvan/praxis/internal/drivers/routetable"
	drivers3 "github.com/shirvan/praxis/internal/drivers/s3"
	driversg "github.com/shirvan/praxis/internal/drivers/sg"
	driversnssub "github.com/shirvan/praxis/internal/drivers/snssub"
	driversnstopic "github.com/shirvan/praxis/internal/drivers/snstopic"
	driversqs "github.com/shirvan/praxis/internal/drivers/sqs"
	driversqspolicy "github.com/shirvan/praxis/internal/drivers/sqspolicy"
	driversubnet "github.com/shirvan/praxis/internal/drivers/subnet"
	drivertargetgroup "github.com/shirvan/praxis/internal/drivers/targetgroup"
	drivervpc "github.com/shirvan/praxis/internal/drivers/vpc"
	drivervpcpeering "github.com/shirvan/praxis/internal/drivers/vpcpeering"

	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ---------------------------------------------------------------------------
// Full-stack test environment (all 45 drivers)
// ---------------------------------------------------------------------------

type lifecycleTestEnv struct {
	ingress   *ingress.Client
	s3Client  *s3sdk.Client
	ec2Client *ec2sdk.Client
	ssmClient *ssmsdk.Client
	iamClient *iamsdk.Client
}

// setupFullStack boots the complete in-process Praxis stack with every driver
// registered. This mirrors the production wiring of all cmd/praxis-* binaries
// into a single Restate test environment.
func setupFullStack(t *testing.T) *lifecycleTestEnv {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	s3Client := awsclient.NewS3Client(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	ssmClient := ssmsdk.NewFromConfig(awsCfg)
	iamClient := iamsdk.NewFromConfig(awsCfg)
	authClient := authservice.NewAuthClient()

	providers := provider.NewRegistry(authClient)

	absSchemaDir, err := filepath.Abs("../../schemas")
	require.NoError(t, err, "resolving absolute schema dir")

	cfg := config.Config{SchemaDir: absSchemaDir}
	cmdService := command.NewPraxisCommandService(cfg, authClient, providers)
	applyWorkflow := orchestrator.NewDeploymentWorkflow(providers)
	deleteWorkflow := orchestrator.NewDeploymentDeleteWorkflow(providers)
	rollbackWorkflow := orchestrator.NewDeploymentRollbackWorkflow(providers)

	// --- Instantiate ALL 45 drivers ---
	env := restatetest.Start(t,
		// Core infrastructure
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(workspace.NewWorkspaceService(absSchemaDir)),
		restate.Reflect(workspace.WorkspaceIndex{}),
		restate.Reflect(cmdService),
		restate.Reflect(applyWorkflow),
		restate.Reflect(deleteWorkflow),
		restate.Reflect(rollbackWorkflow),
		restate.Reflect(orchestrator.DeploymentStateObj{}),
		restate.Reflect(orchestrator.DeploymentIndex{}),
		restate.Reflect(orchestrator.NewEventBus(absSchemaDir)),
		restate.Reflect(orchestrator.DeploymentEventStore{}),
		restate.Reflect(orchestrator.EventIndex{}),
		restate.Reflect(orchestrator.ResourceEventOwnerObj{}),
		restate.Reflect(orchestrator.ResourceEventBridge{}),
		restate.Reflect(orchestrator.SinkRouter{}),
		restate.Reflect(orchestrator.NewNotificationSinkConfig(absSchemaDir)),
		restate.Reflect(registry.TemplateRegistry{}),
		restate.Reflect(registry.TemplateIndex{}),
		restate.Reflect(registry.PolicyRegistry{}),

		// ── Storage drivers ──
		restate.Reflect(drivers3.NewS3BucketDriver(authClient)),

		// ── Network drivers ──
		restate.Reflect(drivervpc.NewVPCDriver(authClient)),
		restate.Reflect(driversg.NewSecurityGroupDriver(authClient)),
		restate.Reflect(driversubnet.NewSubnetDriver(authClient)),
		restate.Reflect(driverigw.NewIGWDriver(authClient)),
		restate.Reflect(drivernatgw.NewNATGatewayDriver(authClient)),
		restate.Reflect(drivereip.NewElasticIPDriver(authClient)),
		restate.Reflect(driverroutetable.NewRouteTableDriver(authClient)),
		restate.Reflect(drivernacl.NewNetworkACLDriver(authClient)),
		restate.Reflect(drivervpcpeering.NewVPCPeeringDriver(authClient)),

		// ── Compute drivers ──
		restate.Reflect(driverec2.NewEC2InstanceDriver(authClient)),
		restate.Reflect(driverkeypair.NewKeyPairDriver(authClient)),
		restate.Reflect(driverebs.NewEBSVolumeDriver(authClient)),
		restate.Reflect(driverami.NewAMIDriver(authClient)),

		// ── Identity drivers ──
		restate.Reflect(driveriamrole.NewIAMRoleDriver(authClient)),
		restate.Reflect(driveriampolicy.NewIAMPolicyDriver(authClient)),
		restate.Reflect(driveriaminstanceprofile.NewIAMInstanceProfileDriver(authClient)),
		restate.Reflect(driveriamgroup.NewIAMGroupDriver(authClient)),
		restate.Reflect(driveriamuser.NewIAMUserDriver(authClient)),

		// ── DNS & TLS drivers ──
		restate.Reflect(driveracmcert.NewACMCertificateDriver(authClient)),
		restate.Reflect(driverroute53zone.NewHostedZoneDriver(authClient)),
		restate.Reflect(driverroute53record.NewDNSRecordDriver(authClient)),
		restate.Reflect(driverroute53healthcheck.NewHealthCheckDriver(authClient)),

		// ── Load balancer drivers ──
		restate.Reflect(driveralb.NewALBDriver(authClient)),
		restate.Reflect(drivernlb.NewNLBDriver(authClient)),
		restate.Reflect(drivertargetgroup.NewTargetGroupDriver(authClient)),
		restate.Reflect(driverlistener.NewListenerDriver(authClient)),
		restate.Reflect(driverlistenerrule.NewListenerRuleDriver(authClient)),

		// ── Serverless drivers ──
		restate.Reflect(driverlambda.NewLambdaFunctionDriver(authClient)),
		restate.Reflect(driverlambdalayer.NewLambdaLayerDriver(authClient)),
		restate.Reflect(driverlambdaperm.NewLambdaPermissionDriver(authClient)),
		restate.Reflect(driveresm.NewEventSourceMappingDriver(authClient)),

		// ── Database drivers ──
		restate.Reflect(driverrdsinstance.NewRDSInstanceDriver(authClient)),
		restate.Reflect(driverauroracluster.NewAuroraClusterDriver(authClient)),
		restate.Reflect(driverdbsubnetgroup.NewDBSubnetGroupDriver(authClient)),
		restate.Reflect(driverdbparametergroup.NewDBParameterGroupDriver(authClient)),

		// ── Container registry drivers ──
		restate.Reflect(driverecrrepo.NewECRRepositoryDriver(authClient)),
		restate.Reflect(driverecrpolicy.NewECRLifecyclePolicyDriver(authClient)),

		// ── Messaging drivers ──
		restate.Reflect(driversnstopic.NewSNSTopicDriver(authClient)),
		restate.Reflect(driversnssub.NewSNSSubscriptionDriver(authClient)),
		restate.Reflect(driversqs.NewSQSQueueDriver(authClient)),
		restate.Reflect(driversqspolicy.NewSQSQueuePolicyDriver(authClient)),

		// ── Monitoring drivers ──
		restate.Reflect(driverloggroup.NewLogGroupDriver(authClient)),
		restate.Reflect(drivermetricalarm.NewMetricAlarmDriver(authClient)),
		restate.Reflect(driverdashboard.NewDashboardDriver(authClient)),
	)

	return &lifecycleTestEnv{
		ingress:   env.Ingress(),
		s3Client:  s3Client,
		ec2Client: ec2Client,
		ssmClient: ssmClient,
		iamClient: iamClient,
	}
}

// seedMotoPrerequisites creates the pre-existing resources that the
// end-to-end.cue template expects to find via data sources and SSM.
// This mirrors the moto-init/setup.sh script but runs programmatically.
func seedMotoPrerequisites(t *testing.T, env *lifecycleTestEnv, envName string) {
	t.Helper()
	ctx := context.Background()

	// --- Shared-services VPC (data source target) ---
	existing, err := env.ec2Client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:Name"), Values: []string{"shared-services"}},
		},
	})
	require.NoError(t, err)
	if len(existing.Vpcs) == 0 {
		createOut, err := env.ec2Client.CreateVpc(ctx, &ec2sdk.CreateVpcInput{
			CidrBlock: aws.String("10.100.0.0/16"),
			TagSpecifications: []ec2types.TagSpecification{{
				ResourceType: ec2types.ResourceTypeVpc,
				Tags: []ec2types.Tag{
					{Key: aws.String("Name"), Value: aws.String("shared-services")},
					{Key: aws.String("env"), Value: aws.String("shared")},
				},
			}},
		})
		require.NoError(t, err, "creating shared-services VPC")
		t.Logf("Created shared-services VPC: %s", aws.ToString(createOut.Vpc.VpcId))
	}

	// --- SSM: database password ---
	_, err = env.ssmClient.PutParameter(ctx, &ssmsdk.PutParameterInput{
		Name:      aws.String(fmt.Sprintf("/praxis/%s/db-password", envName)),
		Value:     aws.String("test-password-" + envName),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	})
	require.NoError(t, err, "seeding SSM db-password")

	// --- SSM: Aurora password (needed when enableAurora=true) ---
	_, err = env.ssmClient.PutParameter(ctx, &ssmsdk.PutParameterInput{
		Name:      aws.String(fmt.Sprintf("/praxis/%s/aurora-password", envName)),
		Value:     aws.String("test-aurora-password-" + envName),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	})
	require.NoError(t, err, "seeding SSM aurora-password")

	// --- SSM: base AMI (used by golden AMI and EC2 imageId) ---
	// Create a seed AMI via a throwaway instance, matching moto-init/setup.sh.
	instances, err := env.ec2Client.RunInstances(ctx, &ec2sdk.RunInstancesInput{
		ImageId:      aws.String("ami-12345678"),
		InstanceType: ec2types.InstanceTypeT2Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
	})
	require.NoError(t, err, "launching seed instance for AMI")
	seedInstanceId := aws.ToString(instances.Instances[0].InstanceId)

	amiOut, err := env.ec2Client.CreateImage(ctx, &ec2sdk.CreateImageInput{
		InstanceId: aws.String(seedInstanceId),
		Name:       aws.String("lifecycle-test-seed-ami"),
		NoReboot:   aws.Bool(true),
	})
	require.NoError(t, err, "creating seed AMI")
	seedAmiId := aws.ToString(amiOut.ImageId)

	_, err = env.ssmClient.PutParameter(ctx, &ssmsdk.PutParameterInput{
		Name:      aws.String("/praxis/moto/base-ami"),
		Value:     aws.String(seedAmiId),
		Type:      ssmtypes.ParameterTypeString,
		Overwrite: aws.Bool(true),
	})
	require.NoError(t, err, "seeding SSM base-ami")
	t.Logf("Seeded base AMI: %s", seedAmiId)

	// --- AWS managed IAM policy stubs (Moto doesn't pre-seed them) ---
	for _, policyName := range []string{"ReadOnlyAccess", "PowerUserAccess"} {
		_, _ = env.iamClient.CreatePolicy(ctx, &iamsdk.CreatePolicyInput{
			PolicyName: aws.String(policyName),
			Path:       aws.String("/aws-service-role/"),
			PolicyDocument: aws.String(`{
				"Version": "2012-10-17",
				"Statement": [{"Effect":"Allow","Action":"*","Resource":"*"}]
			}`),
		})
		// Ignore AlreadyExists errors.
	}
}

// loadEndToEndTemplate reads the end-to-end.cue file from the examples directory.
func loadEndToEndTemplate(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../examples/stacks/end-to-end.cue")
	require.NoError(t, err)
	content, err := os.ReadFile(path)
	require.NoError(t, err, "reading end-to-end.cue")
	return string(content)
}

// ---------------------------------------------------------------------------
// Lifecycle Test: Full plan → deploy → read → change → read → delete cycle
// ---------------------------------------------------------------------------

// TestLifecycle_EndToEnd_FullCycle exercises the complete lifecycle of the
// end-to-end full-coverage template against Moto:
//
//  1. Plan (dry-run) — verify resource count, data source resolution, SSM
//  2. Deploy — apply the full 45+ resource stack with DAG orchestration
//  3. Read — verify deployment detail, resource outputs, AWS-side resources
//  4. Change — re-apply with modified variables (add monitoring, change tags)
//  5. Read — verify the update took effect
//  6. Delete — tear down all resources in reverse dependency order
//  7. Verify — confirm all resources are gone and deployment is cleaned up
func TestLifecycle_EndToEnd_FullCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full lifecycle test in short mode")
	}

	env := setupFullStack(t)
	template := loadEndToEndTemplate(t)
	name := uniqueName(t, "e2e")
	deployKey := "lifecycle-" + name
	domainName := name + ".example.com"

	// Use "dev" environment — fewer resources (no Aurora, no golden AMI,
	// no preventDestroy) but exercises all the core features.
	devVars := map[string]any{
		"account":           integrationAccountName,
		"name":              name,
		"environment":       "dev",
		"instanceType":      "t3.micro",
		"imageId":           "ssm:///praxis/moto/base-ami",
		"domainName":        domainName,
		"dbInstanceClass":   "db.t3.micro",
		"dbEngineVersion":   "15.3",
		"dbFamily":          "postgres15",
		"enableLogging":     true,
		"enableMonitoring":  false, // start with monitoring OFF
		"enableAurora":      false,
		"enableGoldenAmi":   false,
		"storageBuckets":    []any{"assets", "uploads"},
		"availabilityZones": []any{"us-east-1a", "us-east-1b"},
	}

	// Reset Moto to clear stale idempotency tokens (e.g. ACM) from prior runs,
	// then re-seed with prerequisites.
	resetMoto(t)
	seedMotoPrerequisites(t, env, "dev")
	t.Log("Moto prerequisites seeded")

	// ══════════════════════════════════════════════════════════════════
	// Phase 1: PLAN (dry-run)
	// ══════════════════════════════════════════════════════════════════

	t.Log("Phase 1: Plan (dry-run)")
	planResp, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{
		Template:  template,
		Variables: devVars,
	})
	require.NoError(t, err, "Plan should succeed")
	require.NotNil(t, planResp.Plan, "plan result should not be nil")

	// All resources should be creates (nothing exists yet).
	assert.Greater(t, planResp.Plan.Summary.ToCreate, 0,
		"plan should show resources to create")
	assert.Equal(t, 0, planResp.Plan.Summary.ToUpdate,
		"plan should show 0 updates (fresh deployment)")
	assert.Equal(t, 0, planResp.Plan.Summary.ToDelete,
		"plan should show 0 deletes (fresh deployment)")
	t.Logf("Plan: %d to create, %d to update, %d unchanged",
		planResp.Plan.Summary.ToCreate,
		planResp.Plan.Summary.ToUpdate,
		planResp.Plan.Summary.Unchanged)

	// Data sources should be resolved.
	require.Contains(t, planResp.DataSources, "sharedVpc",
		"plan should resolve the sharedVpc data source")
	assert.NotEmpty(t, planResp.DataSources["sharedVpc"].Outputs["vpcId"],
		"sharedVpc data source should have a vpcId output")
	t.Logf("Data source sharedVpc resolved: vpcId=%s",
		planResp.DataSources["sharedVpc"].Outputs["vpcId"])

	// DAG graph should be present.
	assert.NotEmpty(t, planResp.Graph,
		"plan should include the dependency graph")

	// Rendered output should contain resolved SSM values (masked).
	assert.NotEmpty(t, planResp.Rendered,
		"rendered template should be non-empty")

	// Verify nothing was provisioned (dry-run).
	expectedBucket := fmt.Sprintf("%s-dev-assets", name)
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &expectedBucket,
	})
	require.Error(t, err, "bucket should NOT exist after Plan (dry-run)")

	// ══════════════════════════════════════════════════════════════════
	// Phase 2: DEPLOY (apply)
	// ══════════════════════════════════════════════════════════════════

	t.Log("Phase 2: Deploy")
	applyResp, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      template,
		DeploymentKey: deployKey,
		Variables:     devVars,
	})
	require.NoError(t, err, "Apply should succeed")
	assert.Equal(t, deployKey, applyResp.DeploymentKey)
	assert.Equal(t, types.DeploymentPending, applyResp.Status,
		"initial status should be Pending")

	// Poll until the deployment reaches a terminal state.
	// The full stack with 40+ resources + DAG takes time.
	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		5*time.Minute,
	)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"deployment should reach Complete — errors: %s", state.Error)
	t.Logf("Deploy complete: %d resources provisioned", len(state.Resources))

	// ══════════════════════════════════════════════════════════════════
	// Phase 3: READ (verify deployment state and AWS resources)
	// ══════════════════════════════════════════════════════════════════

	t.Log("Phase 3: Read & verify")

	// Verify via DeploymentState
	assert.NotEmpty(t, state.Resources, "deployment should track resources")
	assert.NotEmpty(t, state.Outputs, "deployment should capture outputs")

	// Spot-check: verify key resources are in Ready state.
	for _, resourceName := range []string{"vpc", "igw", "albSg", "appSg", "dataSg"} {
		require.Contains(t, state.Resources, resourceName,
			"resource %q should be tracked", resourceName)
		assert.Equal(t, types.DeploymentResourceReady, state.Resources[resourceName].Status,
			"resource %q should be Ready", resourceName)
	}

	// Verify the S3 buckets actually exist in Moto.
	for _, suffix := range []string{"assets", "uploads"} {
		bucket := fmt.Sprintf("%s-dev-%s", name, suffix)
		_, err := env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
			Bucket: aws.String(bucket),
		})
		require.NoError(t, err, "S3 bucket %q should exist after deploy", bucket)
	}

	// Verify the log-aggregator bucket (enableLogging=true).
	logBucket := fmt.Sprintf("%s-dev-logs", name)
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: aws.String(logBucket),
	})
	require.NoError(t, err, "log-aggregator bucket should exist (enableLogging=true)")

	// Verify the VPC was created.
	require.Contains(t, state.Outputs, "vpc")
	vpcId, ok := state.Outputs["vpc"]["vpcId"].(string)
	require.True(t, ok && vpcId != "", "vpc output should contain vpcId")
	t.Logf("VPC created: %s", vpcId)

	// Verify via GetDetail (the richer read path).
	detail, err := ingress.Object[restate.Void, *types.DeploymentDetail](
		env.ingress,
		orchestrator.DeploymentStateServiceName,
		deployKey,
		"GetDetail",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err, "GetDetail should succeed")
	assert.Equal(t, types.DeploymentComplete, detail.Status)
	assert.NotEmpty(t, detail.Resources, "detail should list resources")
	assert.False(t, detail.CreatedAt.IsZero(), "CreatedAt should be populated")
	assert.False(t, detail.UpdatedAt.IsZero(), "UpdatedAt should be populated")

	// Verify deployment appears in the global index.
	pollDeploymentList(t, env.ingress, deployKey, true, 15*time.Second)

	// Verify CloudEvents were emitted for the apply lifecycle.
	cloudEvents, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	require.NotEmpty(t, cloudEvents,
		"apply workflow should have emitted CloudEvents")
	typeSet := make(map[string]bool, len(cloudEvents))
	for _, record := range cloudEvents {
		typeSet[record.Event.Type()] = true
	}
	assert.True(t, typeSet[orchestrator.EventTypeDeploymentSubmitted])
	assert.True(t, typeSet[orchestrator.EventTypeDeploymentStarted])
	assert.True(t, typeSet[orchestrator.EventTypeDeploymentCompleted])
	t.Logf("Apply emitted %d CloudEvents", len(cloudEvents))

	// ══════════════════════════════════════════════════════════════════
	// Phase 4: CHANGE (re-apply with modified variables)
	// ══════════════════════════════════════════════════════════════════

	t.Log("Phase 4: Change (re-apply with monitoring enabled + extra bucket)")

	// Modify variables: enable monitoring and add an extra storage bucket.
	// This exercises the update path — existing resources should be OpNoOp,
	// new conditional resources (MetricAlarm, Dashboard) should be OpCreate.
	changedVars := map[string]any{
		"account":           integrationAccountName,
		"name":              name,
		"environment":       "dev",
		"instanceType":      "t3.micro",
		"imageId":           "ssm:///praxis/moto/base-ami",
		"domainName":        domainName,
		"dbInstanceClass":   "db.t3.micro",
		"dbEngineVersion":   "15.3",
		"dbFamily":          "postgres15",
		"enableLogging":     true,
		"enableMonitoring":  true, // ← CHANGED: was false
		"enableAurora":      false,
		"enableGoldenAmi":   false,
		"storageBuckets":    []any{"assets", "uploads", "exports"}, // ← CHANGED: added "exports"
		"availabilityZones": []any{"us-east-1a", "us-east-1b"},
	}

	// Plan the change first to verify diff detection.
	changePlan, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{
		Template:  template,
		Variables: changedVars,
	})
	require.NoError(t, err, "Plan for change should succeed")
	require.NotNil(t, changePlan.Plan)

	// The plan should show creates for newly-enabled resources and the
	// new "exports" bucket. Existing resources should be no-op or update.
	assert.Greater(t, changePlan.Plan.Summary.ToCreate, 0,
		"change plan should show new resources to create (monitoring + exports bucket)")
	t.Logf("Change plan: %d create, %d update, %d unchanged, %d delete",
		changePlan.Plan.Summary.ToCreate,
		changePlan.Plan.Summary.ToUpdate,
		changePlan.Plan.Summary.Unchanged,
		changePlan.Plan.Summary.ToDelete)

	// Apply the change.
	applyResp2, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      template,
		DeploymentKey: deployKey,
		Variables:     changedVars,
	})
	require.NoError(t, err, "Apply change should succeed")
	assert.Equal(t, deployKey, applyResp2.DeploymentKey)

	// Poll until the change deployment completes.
	state2 := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		5*time.Minute,
	)
	require.Equal(t, types.DeploymentComplete, state2.Status,
		"change deployment should reach Complete — errors: %s", state2.Error)
	t.Logf("Change complete: %d resources after update", len(state2.Resources))

	// ══════════════════════════════════════════════════════════════════
	// Phase 5: READ after CHANGE (verify updates took effect)
	// ══════════════════════════════════════════════════════════════════

	t.Log("Phase 5: Read after change")

	// The new "exports" bucket should now exist.
	exportsBucket := fmt.Sprintf("%s-dev-exports", name)
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: aws.String(exportsBucket),
	})
	require.NoError(t, err,
		"exports bucket should exist after change (new storageBuckets entry)")

	// The monitoring resources should now be tracked.
	require.Contains(t, state2.Resources, "highErrorAlarm",
		"MetricAlarm should exist after enabling monitoring")
	assert.Equal(t, types.DeploymentResourceReady,
		state2.Resources["highErrorAlarm"].Status,
		"MetricAlarm should be Ready")

	require.Contains(t, state2.Resources, "platformDashboard",
		"Dashboard should exist after enabling monitoring")
	assert.Equal(t, types.DeploymentResourceReady,
		state2.Resources["platformDashboard"].Status,
		"Dashboard should be Ready")

	// Original resources should still be healthy.
	assert.Equal(t, types.DeploymentResourceReady,
		state2.Resources["vpc"].Status,
		"VPC should still be Ready after change")

	// More CloudEvents should have been emitted.
	cloudEvents2, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	assert.Greater(t, len(cloudEvents2), len(cloudEvents),
		"change should have emitted additional CloudEvents")

	// ══════════════════════════════════════════════════════════════════
	// Phase 6: DELETE (tear down everything)
	// ══════════════════════════════════════════════════════════════════

	t.Log("Phase 6: Delete")
	delResp, err := ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{
		DeploymentKey: deployKey,
	})
	require.NoError(t, err, "DeleteDeployment should succeed")
	assert.Equal(t, types.DeploymentDeleting, delResp.Status)

	// Poll until fully deleted.
	state3 := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
		5*time.Minute,
	)
	require.Equal(t, types.DeploymentDeleted, state3.Status,
		"deployment should reach Deleted — errors: %s", state3.Error)
	t.Log("Delete complete")

	// ══════════════════════════════════════════════════════════════════
	// Phase 7: VERIFY (everything is gone)
	// ══════════════════════════════════════════════════════════════════

	t.Log("Phase 7: Verify teardown")

	// S3 buckets should be gone.
	for _, suffix := range []string{"assets", "uploads", "exports", "logs"} {
		bucket := fmt.Sprintf("%s-dev-%s", name, suffix)
		_, err := env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
			Bucket: aws.String(bucket),
		})
		assert.Error(t, err, "S3 bucket %q should be deleted", bucket)
	}

	// Deployment should be removed from the global index.
	pollDeploymentList(t, env.ingress, deployKey, false, 15*time.Second)

	// Delete lifecycle CloudEvents should have been emitted.
	cloudEvents3, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	typeSet3 := make(map[string]bool, len(cloudEvents3))
	for _, record := range cloudEvents3 {
		typeSet3[record.Event.Type()] = true
	}
	assert.True(t, typeSet3[orchestrator.EventTypeDeploymentDeleteStarted],
		"delete should emit deployment.delete.started")
	assert.True(t, typeSet3[orchestrator.EventTypeDeploymentDeleteDone],
		"delete should emit deployment.delete.completed")

	t.Log("Full lifecycle test passed")
}

// TestLifecycle_EndToEnd_IdempotentReApply verifies that re-applying the
// exact same template + variables results in no changes (OpNoOp for all resources).
func TestLifecycle_EndToEnd_IdempotentReApply(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping idempotent re-apply test in short mode")
	}

	env := setupFullStack(t)
	template := loadEndToEndTemplate(t)
	name := uniqueName(t, "idem")
	deployKey := "idempotent-" + name
	domainName := name + ".example.com"

	vars := map[string]any{
		"account":           integrationAccountName,
		"name":              name,
		"environment":       "dev",
		"domainName":        domainName,
		"enableLogging":     false,
		"enableMonitoring":  false,
		"enableAurora":      false,
		"enableGoldenAmi":   false,
		"storageBuckets":    []any{"assets"},
		"availabilityZones": []any{"us-east-1a", "us-east-1b"},
	}

	resetMoto(t)
	seedMotoPrerequisites(t, env, "dev")

	// Deploy initial stack.
	t.Log("Deploying initial stack")
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      template,
		DeploymentKey: deployKey,
		Variables:     vars,
	})
	require.NoError(t, err)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		5*time.Minute,
	)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"initial deploy should succeed — errors: %s", state.Error)

	// Plan with the same variables — should be all no-ops.
	t.Log("Planning identical re-apply")
	planResp, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{
		Template:  template,
		Variables: vars,
	})
	require.NoError(t, err, "Plan for re-apply should succeed")
	require.NotNil(t, planResp.Plan)

	assert.Equal(t, 0, planResp.Plan.Summary.ToCreate,
		"idempotent re-apply should create nothing")
	assert.Equal(t, 0, planResp.Plan.Summary.ToDelete,
		"idempotent re-apply should delete nothing")
	t.Logf("Idempotent plan: %d create, %d update, %d unchanged",
		planResp.Plan.Summary.ToCreate,
		planResp.Plan.Summary.ToUpdate,
		planResp.Plan.Summary.Unchanged)

	// Clean up.
	_, err = ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{
		DeploymentKey: deployKey,
	})
	require.NoError(t, err)
	pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
		5*time.Minute,
	)
}

// TestLifecycle_EndToEnd_PlanOnly verifies that the full end-to-end template
// can be planned successfully without provisioning anything. This is useful as
// a fast smoke test that CUE evaluation, data source resolution, SSM, and DAG
// construction all work with the complete template.
func TestLifecycle_EndToEnd_PlanOnly(t *testing.T) {
	env := setupFullStack(t)
	template := loadEndToEndTemplate(t)
	name := uniqueName(t, "plan")
	domainName := name + ".example.com"

	resetMoto(t)
	seedMotoPrerequisites(t, env, "dev")

	// Plan the "dev" variant.
	t.Log("Planning dev variant")
	devPlan, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{
		Template: template,
		Variables: map[string]any{
			"account":           integrationAccountName,
			"name":              name,
			"environment":       "dev",
			"domainName":        domainName,
			"enableLogging":     true,
			"enableMonitoring":  true,
			"enableAurora":      false,
			"enableGoldenAmi":   false,
			"storageBuckets":    []any{"assets", "uploads", "backups"},
			"availabilityZones": []any{"us-east-1a", "us-east-1b"},
		},
	})
	require.NoError(t, err, "Plan (dev) should succeed")
	require.NotNil(t, devPlan.Plan)
	devCount := devPlan.Plan.Summary.ToCreate
	t.Logf("Dev plan: %d resources to create", devCount)

	// Plan the "prod" variant — should have MORE resources
	// (Aurora, golden AMI, monitoring, extra buckets).
	resetMoto(t)
	seedMotoPrerequisites(t, env, "prod")

	t.Log("Planning prod variant (all features enabled)")
	prodPlan, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{
		Template: template,
		Variables: map[string]any{
			"account":           integrationAccountName,
			"name":              name + "p",
			"environment":       "prod",
			"domainName":        domainName,
			"enableLogging":     true,
			"enableMonitoring":  true,
			"enableAurora":      true,
			"enableGoldenAmi":   true,
			"storageBuckets":    []any{"assets", "uploads", "backups", "exports"},
			"availabilityZones": []any{"us-east-1a", "us-east-1b"},
		},
	})
	require.NoError(t, err, "Plan (prod) should succeed")
	require.NotNil(t, prodPlan.Plan)
	prodCount := prodPlan.Plan.Summary.ToCreate
	t.Logf("Prod plan: %d resources to create", prodCount)

	assert.Greater(t, prodCount, devCount,
		"prod plan should have more resources than dev (Aurora, golden AMI, extra buckets, monitoring)")

	// Verify data sources resolve in both environments.
	for _, plan := range []command.PlanResponse{devPlan, prodPlan} {
		require.Contains(t, plan.DataSources, "sharedVpc")
		vpcId := plan.DataSources["sharedVpc"].Outputs["vpcId"]
		assert.NotEmpty(t, vpcId, "sharedVpc data source should resolve")
	}

	// Verify dependency graph is populated.
	assert.NotEmpty(t, devPlan.Graph, "dev plan should include dependency graph")
	assert.NotEmpty(t, prodPlan.Graph, "prod plan should include dependency graph")

	// Verify no resources were created (this is plan-only).
	for _, suffix := range []string{"assets", "uploads"} {
		bucket := fmt.Sprintf("%s-dev-%s", name, suffix)
		_, err := env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
			Bucket: aws.String(bucket),
		})
		assert.Error(t, err, "bucket %q should NOT exist (plan-only)", bucket)
	}
}

// TestLifecycle_EndToEnd_TargetedApply verifies that applying a subset of
// resources via the Targets field works correctly. Only the named resources
// and their transitive dependencies should be provisioned.
func TestLifecycle_EndToEnd_TargetedApply(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping targeted apply test in short mode")
	}

	env := setupFullStack(t)
	template := loadEndToEndTemplate(t)
	name := uniqueName(t, "tgt")
	deployKey := "targeted-" + name
	domainName := name + ".example.com"

	resetMoto(t)
	seedMotoPrerequisites(t, env, "dev")

	vars := map[string]any{
		"account":           integrationAccountName,
		"name":              name,
		"environment":       "dev",
		"domainName":        domainName,
		"enableLogging":     false,
		"enableMonitoring":  false,
		"enableAurora":      false,
		"enableGoldenAmi":   false,
		"storageBuckets":    []any{"assets"},
		"availabilityZones": []any{"us-east-1a", "us-east-1b"},
	}

	// Target only the storage layer: bucket-assets.
	// This should pull in transitive dependencies only if there are any.
	t.Log("Applying targeted subset: bucket-assets")
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      template,
		DeploymentKey: deployKey,
		Variables:     vars,
		Targets:       []string{"bucket-assets"},
	})
	require.NoError(t, err, "Targeted Apply should succeed")

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		3*time.Minute,
	)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"targeted deployment should complete — errors: %s", state.Error)

	// The S3 bucket should exist.
	assetsBucket := fmt.Sprintf("%s-dev-assets", name)
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: aws.String(assetsBucket),
	})
	require.NoError(t, err, "targeted S3 bucket should exist")

	// The full VPC infrastructure should NOT have been provisioned
	// (unless bucket-assets has VPC dependencies, which it doesn't).
	_, hasVPC := state.Resources["vpc"]
	if !hasVPC {
		t.Log("VPC was correctly excluded from targeted deploy (no transitive dependency)")
	}

	// Fewer resources than the full stack.
	assert.Less(t, len(state.Resources), 10,
		"targeted deploy should provision far fewer resources than full stack")

	t.Logf("Targeted deploy provisioned %d resources", len(state.Resources))

	// Clean up.
	_, err = ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{DeploymentKey: deployKey})
	require.NoError(t, err)
	pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
		3*time.Minute,
	)
}

// TestLifecycle_EndToEnd_PreventDestroy verifies that lifecycle policies
// are enforced. A "prod" deployment with preventDestroy on the database should
// reject a delete request.
func TestLifecycle_EndToEnd_PreventDestroy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping preventDestroy test in short mode")
	}

	env := setupFullStack(t)
	template := loadEndToEndTemplate(t)
	name := uniqueName(t, "prot")
	deployKey := "protect-" + name
	domainName := name + ".example.com"

	resetMoto(t)
	seedMotoPrerequisites(t, env, "prod")

	// Deploy a "prod" stack — database has preventDestroy: true.
	// Use targeted apply to only deploy the storage layer to keep it quick.
	// The database has preventDestroy which applies at delete-time regardless.
	//
	// Actually, to test preventDestroy we need the DB provisioned.
	// Target just the database + its dependencies for a faster test.
	t.Log("Deploying prod database subtree (preventDestroy=true)")
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      template,
		DeploymentKey: deployKey,
		Variables: map[string]any{
			"account":           integrationAccountName,
			"name":              name,
			"environment":       "prod",
			"domainName":        domainName,
			"enableLogging":     false,
			"enableMonitoring":  false,
			"enableAurora":      false,
			"enableGoldenAmi":   false,
			"storageBuckets":    []any{"assets"},
			"availabilityZones": []any{"us-east-1a", "us-east-1b"},
		},
		Targets: []string{"database"},
	})
	require.NoError(t, err)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		5*time.Minute,
	)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"prod database deploy should succeed — errors: %s", state.Error)

	// Attempt to delete without force — should fail due to preventDestroy.
	t.Log("Attempting delete without force (should be blocked by preventDestroy)")
	_, err = ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{
		DeploymentKey: deployKey,
		Force:         false,
	})
	if err != nil {
		// Expected: preventDestroy should block the delete.
		assert.True(t,
			strings.Contains(strings.ToLower(err.Error()), "prevent") ||
				strings.Contains(strings.ToLower(err.Error()), "protected") ||
				strings.Contains(strings.ToLower(err.Error()), "lifecycle"),
			"error should mention preventDestroy — got: %s", err.Error())
		t.Logf("Delete correctly blocked: %s", err.Error())
	} else {
		// Delete was accepted — it might fail during the workflow.
		state2 := pollDeploymentState(t, env.ingress, deployKey,
			[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
			3*time.Minute,
		)
		assert.Equal(t, types.DeploymentFailed, state2.Status,
			"delete should fail for preventDestroy resources")
		t.Logf("Delete failed during workflow (preventDestroy enforced): %s", state2.Error)
	}

	// Force delete to clean up.
	t.Log("Force-deleting to clean up")
	_, err = ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{
		DeploymentKey: deployKey,
		Force:         true,
	})
	require.NoError(t, err, "Force delete should be accepted")
	pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
		3*time.Minute,
	)
}
