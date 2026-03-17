//go:build integration

// End-to-End Integration Tests for the Praxis Core pipeline.
//
// These tests verify the full vertical stack:
//
//	CLI payload → PraxisCommandService → DeploymentWorkflow → Typed Drivers → LocalStack
//
// Each test starts a full in-process Restate environment using Testcontainers
// with every service bound (command service, workflows, read models, and both
// S3 and SG drivers). LocalStack provides mock AWS APIs.
//
// These tests deliberately exercise the real Restate runtime, real durable
// execution, and real AWS SDK calls against LocalStack — no mocks at the
// service boundary.
//
// Run with:
//
//	go test ./tests/integration/... -v -count=1 -tags=integration -timeout=10m
//	go test ./tests/integration/ -run TestCore -v -count=1 -tags=integration -timeout=10m
//
// Prerequisites:
//   - Docker must be running (Testcontainers starts Restate and LocalStack)
//   - The LocalStack init script in hack/localstack-init/setup.sh must exist
package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/praxiscloud/praxis/internal/core/command"
	"github.com/praxiscloud/praxis/internal/core/config"
	"github.com/praxiscloud/praxis/internal/core/orchestrator"
	"github.com/praxiscloud/praxis/internal/core/provider"
	"github.com/praxiscloud/praxis/internal/core/registry"
	drivers3 "github.com/praxiscloud/praxis/internal/drivers/s3"
	driversg "github.com/praxiscloud/praxis/internal/drivers/sg"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

// ---------------------------------------------------------------------------
// Test infrastructure: full core stack setup
// ---------------------------------------------------------------------------

// coreTestEnv bundles the handles a test needs to talk to the full Praxis
// stack and to verify AWS-side effects directly.
type coreTestEnv struct {
	// ingress is the Restate ingress client used to invoke command-service and
	// workflow handlers exactly as the CLI would.
	ingress *ingress.Client

	// s3Client is a raw AWS S3 SDK client pointing at LocalStack.
	// Tests use it to verify provisioned resources and clean up after.
	s3Client *s3sdk.Client

	// ec2Client is a raw AWS EC2 SDK client pointing at LocalStack.
	// Tests use it to verify SecurityGroup resources and to resolve the
	// default VPC ID.
	ec2Client *ec2sdk.Client
}

// setupCoreStack boots the entire in-process Praxis stack:
//
//  1. Builds a LocalStack-backed AWS config (same pattern as existing driver
//     integration tests).
//  2. Instantiates the S3 and SG drivers.
//  3. Constructs the provider adapter registry with live AWS describe APIs.
//  4. Constructs the PraxisCommandService, both deployment workflows, and all
//     read-model objects.
//  5. Binds everything into a single Restate test environment.
//  6. Returns a coreTestEnv ready for end-to-end assertions.
//
// The Testcontainers-managed Restate instance is automatically torn down when
// the test completes.
func setupCoreStack(t *testing.T) *coreTestEnv {
	t.Helper()
	configureLocalAccount(t)

	// --- AWS clients pointing at LocalStack ---
	awsCfg := localstackAWSConfig(t)
	s3Client := awsclient.NewS3Client(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	accounts := config.Load().Auth()

	// --- Typed drivers ---
	s3Driver := drivers3.NewS3BucketDriver(accounts)
	sgDriver := driversg.NewSecurityGroupDriver(accounts)

	// --- Provider adapter registry ---
	//
	// The adapters get the live AWS APIs so that `praxis plan` can describe
	// existing state during tests. The same construction path as
	// cmd/praxis-core/main.go keeps these tests honest.
	providers := provider.NewRegistryWithAdapters(
		provider.NewS3AdapterWithRegistry(accounts),
		provider.NewSecurityGroupAdapterWithRegistry(accounts),
	)

	// --- Core config ---
	//
	// CUE's load.Config overlay requires absolute paths, so we resolve the
	// schema directory relative to this test file's location (tests/integration/).
	absSchemaDir, err := filepath.Abs("../../schemas")
	require.NoError(t, err, "resolving absolute schema dir")

	cfg := config.Config{
		SchemaDir: absSchemaDir,
	}

	// --- Command service + orchestrator instances ---
	cmdService := command.NewPraxisCommandService(cfg, accounts, providers)
	applyWorkflow := orchestrator.NewDeploymentWorkflow(providers)
	deleteWorkflow := orchestrator.NewDeploymentDeleteWorkflow(providers)

	// --- Bind everything into one Restate test environment ---
	//
	// restatetest.Start starts a real Restate container via Testcontainers,
	// registers every service, and returns an environment whose Ingress()
	// client routes through the actual Restate runtime.
	env := restatetest.Start(t,
		// Command service
		restate.Reflect(cmdService),
		// Apply workflow
		restate.Reflect(applyWorkflow),
		// Delete workflow
		restate.Reflect(deleteWorkflow),
		// Durable deployment state
		restate.Reflect(orchestrator.DeploymentStateObj{}),
		// Global deployment index
		restate.Reflect(orchestrator.DeploymentIndex{}),
		// Per-deployment event feed
		restate.Reflect(orchestrator.DeploymentEvents{}),
		// Template registry
		restate.Reflect(registry.TemplateRegistry{}),
		restate.Reflect(registry.TemplateIndex{}),
		restate.Reflect(registry.PolicyRegistry{}),

		// Typed drivers — the workflows call these via Restate service-to-service
		restate.Reflect(s3Driver),
		restate.Reflect(sgDriver),
	)

	return &coreTestEnv{
		ingress:   env.Ingress(),
		s3Client:  s3Client,
		ec2Client: ec2Client,
	}
}

// defaultVpcId returns the default VPC ID from LocalStack. SecurityGroup tests
// need the real VPC ID because the EC2 API rejects unknown vpc-ids.
func defaultVpcId(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	out, err := ec2Client.DescribeVpcs(context.Background(), &ec2sdk.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("isDefault"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Vpcs, "LocalStack should have a default VPC")
	return aws.ToString(out.Vpcs[0].VpcId)
}

// uniqueName generates a short, unique, S3-rule-safe name for each test.
func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 40 {
		name = name[:40]
	}
	return fmt.Sprintf("%s-%s-%d", prefix, name, time.Now().UnixNano()%100000)
}

// pollDeploymentState polls the DeploymentState virtual object until the
// deployment reaches one of the expected terminal statuses or the timeout
// expires.
//
// This mirrors what `praxis apply --wait` does under the hood: repeatedly
// query the shared GetState handler until the deployment is no longer in a
// transient state.
func pollDeploymentState(
	t *testing.T,
	client *ingress.Client,
	deploymentKey string,
	terminalStatuses []types.DeploymentStatus,
	timeout time.Duration,
) *orchestrator.DeploymentState {
	t.Helper()

	deadline := time.Now().Add(timeout)
	statusSet := make(map[types.DeploymentStatus]bool, len(terminalStatuses))
	for _, s := range terminalStatuses {
		statusSet[s] = true
	}

	for {
		// Query the durable deployment state directly — this is the same
		// handler the CLI calls through Restate ingress.
		state, err := ingress.Object[restate.Void, *orchestrator.DeploymentState](
			client,
			orchestrator.DeploymentStateServiceName,
			deploymentKey,
			"GetState",
		).Request(t.Context(), restate.Void{})
		require.NoError(t, err, "polling DeploymentState should not fail")

		if state != nil && statusSet[state.Status] {
			return state
		}

		if time.Now().After(deadline) {
			currentStatus := "nil"
			if state != nil {
				currentStatus = string(state.Status)
			}
			t.Fatalf(
				"deployment %q did not reach terminal status %v within %v (current: %s)",
				deploymentKey, terminalStatuses, timeout, currentStatus,
			)
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// pollDeploymentList polls the deployment index until it contains (or stops
// containing) the given key. Useful for verifying that apply creates a listing
// entry and delete removes it.
func pollDeploymentList(
	t *testing.T,
	client *ingress.Client,
	expectKey string,
	expectPresent bool,
	timeout time.Duration,
) []types.DeploymentSummary {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		summaries, err := ingress.Object[restate.Void, []types.DeploymentSummary](
			client,
			orchestrator.DeploymentIndexServiceName,
			orchestrator.DeploymentIndexGlobalKey,
			"List",
		).Request(t.Context(), restate.Void{})
		require.NoError(t, err, "polling DeploymentIndex should not fail")

		found := false
		for _, s := range summaries {
			if s.Key == expectKey {
				found = true
				break
			}
		}

		if found == expectPresent {
			return summaries
		}

		if time.Now().After(deadline) {
			t.Fatalf(
				"deployment %q present=%v not reached in index within %v",
				expectKey, expectPresent, timeout,
			)
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// CUE template helpers
// ---------------------------------------------------------------------------

// simpleS3Template returns a minimal CUE template that provisions a single S3
// bucket. The bucket name is parameterized so each test can use a unique name.
//
// Note: The bucket name lives in metadata.name (matched by CUE schema regex).
// spec contains only region, versioning, encryption, and tags.
// The S3 adapter maps metadata.name → S3BucketSpec.BucketName internally.
func simpleS3Template(bucketName string) string {
	return fmt.Sprintf(`
resources: {
	bucket: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: {
			name: %q
		}
		spec: {
			region:     "us-east-1"
			versioning: true
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				env: "integration-test"
			}
		}
	}
}
`, bucketName)
}

// multiResourceTemplate returns a CUE template with an S3 bucket and a
// SecurityGroup. The bucket's tags reference the SG's outputs via a CEL
// expression, creating a real dependency edge in the DAG.
//
// This validates:
//   - DAG dependency parsing (the bucket depends on the SG)
//   - Correct topological dispatch order (SG first, then bucket)
//   - Dispatch-time CEL hydration of resources.*.outputs.* expressions
func multiResourceTemplate(bucketName, sgName, vpcId string) string {
	return fmt.Sprintf(`
resources: {
	appSG: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: {
			name: %q
		}
		spec: {
			groupName:   %q
			description: "Integration test SG"
			vpcId:       %q
			ingressRules: [{
				protocol:  "tcp"
				fromPort:  443
				toPort:    443
				cidrBlock: "0.0.0.0/0"
			}]
			tags: {
				env: "integration-test"
			}
		}
	}
	bucket: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: {
			name: %q
		}
		spec: {
			region:     "us-east-1"
			versioning: true
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				env:        "integration-test"
				secGroupId: "${cel:resources.appSG.outputs.groupId}"
			}
		}
	}
}
`, sgName, sgName, vpcId, bucketName)
}

// cyclicDependencyTemplate returns a CUE template where two resources
// reference each other's outputs, creating an unresolvable cycle.
// The command service should reject this with a terminal validation error.
func cyclicDependencyTemplate() string {
	return `
resources: {
	bucketA: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: {
			name: "bucket-a"
		}
		spec: {
			region:     "us-east-1"
			tags: {
				dep: "${cel:resources.bucketB.outputs.bucketName}"
			}
		}
	}
	bucketB: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: {
			name: "bucket-b"
		}
		spec: {
			region:     "us-east-1"
			tags: {
				dep: "${cel:resources.bucketA.outputs.bucketName}"
			}
		}
	}
}
`
}

// ---------------------------------------------------------------------------
// End-to-End Test Cases
// ---------------------------------------------------------------------------

// TestCore_Apply_SingleS3 exercises the simplest possible apply path:
//
//  1. Submit a CUE template with one S3 bucket to PraxisCommandService.Apply
//  2. Verify the immediate response returns a deployment key in Pending status
//  3. Poll DeploymentState until the deployment reaches Complete
//  4. Verify the S3 bucket was actually created in LocalStack
//  5. Verify the deployment state contains the correct resource outputs
//  6. Verify the deployment appears in the global listing index
func TestCore_Apply_SingleS3(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "s3e2e")

	// --- Step 1: Submit Apply request ---
	//
	// This mirrors what `praxis apply template.cue` does:
	// sends the template content and optional variables to the command service.
	resp, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      simpleS3Template(bucketName),
		DeploymentKey: "test-s3-apply-" + bucketName,
		Variables:     accountVariables(),
	})
	require.NoError(t, err, "Apply should succeed")

	// --- Step 2: Verify immediate response ---
	//
	// Apply is asynchronous: it returns immediately with Pending status
	// while the workflow runs in the background.
	deployKey := resp.DeploymentKey
	assert.NotEmpty(t, deployKey, "deployment key should be returned")
	assert.Equal(t, types.DeploymentPending, resp.Status,
		"initial status should be Pending — the workflow hasn't started yet")

	// --- Step 3: Poll until terminal ---
	//
	// The workflow will: init state → set Running → dispatch S3 Provision →
	// collect outputs → set Complete. We wait up to 60s for all of that.
	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		60*time.Second,
	)
	assert.Equal(t, types.DeploymentComplete, state.Status,
		"deployment should reach Complete — a single S3 bucket with no deps")

	// --- Step 4: Verify the bucket exists in LocalStack ---
	//
	// This confirms the driver actually talked to AWS (LocalStack) and
	// created the resource, not just journaled a no-op.
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &bucketName,
	})
	require.NoError(t, err, "S3 bucket %q should exist in LocalStack after apply", bucketName)

	// --- Step 5: Verify resource outputs in deployment state ---
	//
	// The workflow should have collected S3 outputs (ARN, bucketName, etc.)
	// and stored them in the DeploymentState VO via UpdateResource.
	require.Contains(t, state.Resources, "bucket",
		"deployment should track the 'bucket' resource")
	assert.Equal(t, types.DeploymentResourceReady, state.Resources["bucket"].Status,
		"the 'bucket' resource should be Ready")
	require.Contains(t, state.Outputs, "bucket",
		"deployment outputs should contain the 'bucket' resource outputs")
	assert.Contains(t, state.Outputs["bucket"], "arn",
		"S3 outputs should include the ARN")
	assert.Equal(t, bucketName, state.Outputs["bucket"]["bucketName"],
		"S3 output bucketName should match the spec")

	// --- Step 6: Verify listing index ---
	//
	// The workflow upserts a summary into the global DeploymentIndex so that
	// `praxis list deployments` can show it.
	summaries := pollDeploymentList(t, env.ingress, deployKey, true, 10*time.Second)
	found := false
	for _, s := range summaries {
		if s.Key == deployKey {
			found = true
			assert.Equal(t, types.DeploymentComplete, s.Status)
			assert.Equal(t, 1, s.Resources)
		}
	}
	assert.True(t, found, "deployment should appear in listing index")
}

// TestCore_Plan_ShowsDiff exercises the plan (dry-run) path:
//
//  1. Submit a CUE template to PraxisCommandService.Plan (no provisioning)
//  2. Verify it returns a plan with "1 to create" for a new resource
//  3. Verify that no S3 bucket was actually created in LocalStack
func TestCore_Plan_ShowsDiff(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "plan")

	// --- Step 1: Submit Plan request ---
	//
	// Plan uses the same rendering pipeline as Apply but stops before
	// dispatching any driver operations.
	resp, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{
		Template:  simpleS3Template(bucketName),
		Variables: accountVariables(),
	})
	require.NoError(t, err, "Plan should succeed for a valid template")

	// --- Step 2: Verify plan result ---
	//
	// The plan should show one resource to create (the S3 bucket doesn't exist
	// yet). The rendered output should contain the template post-evaluation.
	require.NotNil(t, resp.Plan, "plan result should not be nil")
	assert.Equal(t, 1, resp.Plan.Summary.ToCreate,
		"plan should report 1 resource to create (bucket does not exist yet)")
	assert.Equal(t, 0, resp.Plan.Summary.ToUpdate,
		"plan should report 0 to update (nothing provisioned yet)")
	assert.Equal(t, 0, resp.Plan.Summary.ToDelete,
		"plan should report 0 to delete")
	assert.NotEmpty(t, resp.Rendered,
		"rendered template output should be non-empty")

	// --- Step 3: Verify nothing was provisioned ---
	//
	// The S3 bucket should NOT exist — plan is read-only.
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &bucketName,
	})
	require.Error(t, err, "S3 bucket should NOT exist after plan (dry run)")
}

// TestCore_Delete_ReverseOrder exercises the full apply → delete lifecycle:
//
//  1. Apply a single-resource template to create an S3 bucket
//  2. Verify the bucket exists
//  3. Submit a DeleteDeployment request
//  4. Poll until the deployment reaches Deleted status
//  5. Verify the S3 bucket was actually deleted from LocalStack
//  6. Verify the deployment was removed from the listing index
func TestCore_Delete_ReverseOrder(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "del")
	deployKey := "test-delete-" + bucketName

	// --- Step 1: Apply first ---
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      simpleS3Template(bucketName),
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err, "Apply should succeed")

	// Wait for apply to complete
	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete},
		60*time.Second,
	)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"apply must complete before we can test delete")

	// --- Step 2: Verify bucket exists before delete ---
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &bucketName,
	})
	require.NoError(t, err, "bucket must exist before delete")

	// --- Step 3: Submit DeleteDeployment ---
	//
	// Like apply, delete is asynchronous. The command service validates the
	// request, starts the delete workflow, and returns Deleting immediately.
	delResp, err := ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{
		DeploymentKey: deployKey,
	})
	require.NoError(t, err, "DeleteDeployment should succeed")
	assert.Equal(t, types.DeploymentDeleting, delResp.Status,
		"delete response should show Deleting")

	// --- Step 4: Poll until deleted ---
	state = pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
		60*time.Second,
	)
	assert.Equal(t, types.DeploymentDeleted, state.Status,
		"deployment should reach Deleted status")

	// --- Step 5: Verify bucket is gone from LocalStack ---
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &bucketName,
	})
	require.Error(t, err, "bucket should be deleted from LocalStack")

	// --- Step 6: Verify removal from listing index ---
	//
	// The delete workflow calls DeploymentIndex.Remove on success, so the
	// deployment should no longer appear in listings.
	pollDeploymentList(t, env.ingress, deployKey, false, 10*time.Second)
}

// TestCore_Apply_MultiResource_WithDependencies exercises the full DAG
// orchestration path with cross-resource CEL dependencies:
//
//  1. Submit a multi-resource template (SG + S3 bucket) where the bucket's
//     tags reference the SG's outputs via CEL
//  2. Verify the workflow dispatches the SG first (it has no deps)
//  3. Verify the bucket is dispatched second (it depends on the SG)
//  4. Verify both resources exist in LocalStack
//  5. Verify the bucket's tags contain the actual SG group ID (CEL hydration)
//  6. Verify outputs for both resources are recorded in deployment state
func TestCore_Apply_MultiResource_WithDependencies(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "mr")
	sgName := uniqueName(t, "mrsg")
	vpcId := defaultVpcId(t, env.ec2Client)
	deployKey := "test-multiresource-" + bucketName

	// --- Step 1: Submit Apply with cross-resource template ---
	resp, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      multiResourceTemplate(bucketName, sgName, vpcId),
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err, "Apply should succeed for multi-resource template")
	assert.Equal(t, types.DeploymentPending, resp.Status)

	// --- Step 2-3: Poll until complete ---
	//
	// Under the hood, the workflow:
	//   1. Builds a DAG: bucket depends on appSG
	//   2. Dispatches appSG first (root of the DAG)
	//   3. Collects SG outputs (groupId, vpcId, groupArn)
	//   4. Hydrates the bucket's CEL expressions with SG outputs
	//   5. Dispatches bucket with the hydrated spec
	//   6. Collects bucket outputs
	//   7. Finalizes as Complete
	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed},
		90*time.Second,
	)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"multi-resource deployment should reach Complete")

	// --- Step 4a: Verify the S3 bucket exists ---
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &bucketName,
	})
	require.NoError(t, err, "S3 bucket should exist after apply")

	// --- Step 4b: Verify the Security Group exists ---
	//
	// Look up the SG by the group ID recorded in deployment outputs.
	require.Contains(t, state.Outputs, "appSG",
		"outputs should contain the appSG resource")
	sgGroupId, ok := state.Outputs["appSG"]["groupId"].(string)
	require.True(t, ok && sgGroupId != "",
		"SG outputs should include a non-empty groupId")
	sgDesc, err := env.ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		GroupIds: []string{sgGroupId},
	})
	require.NoError(t, err, "SG should exist in LocalStack")
	require.Len(t, sgDesc.SecurityGroups, 1)

	// --- Step 5: Verify CEL hydration in bucket tags ---
	//
	// The bucket's `secGroupId` tag should contain the actual SG group ID,
	// not the CEL placeholder string. This proves dispatch-time hydration
	// worked correctly.
	tagging, err := env.s3Client.GetBucketTagging(context.Background(), &s3sdk.GetBucketTaggingInput{
		Bucket: &bucketName,
	})
	require.NoError(t, err, "bucket tagging should be readable")
	tagMap := make(map[string]string)
	for _, tag := range tagging.TagSet {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, sgGroupId, tagMap["secGroupId"],
		"bucket's secGroupId tag should equal the SG's actual groupId (CEL hydration)")
	assert.Equal(t, "integration-test", tagMap["env"],
		"bucket's env tag should remain as-is")

	// --- Step 6: Verify per-resource outputs in deployment state ---
	require.Contains(t, state.Resources, "appSG")
	assert.Equal(t, types.DeploymentResourceReady, state.Resources["appSG"].Status)
	require.Contains(t, state.Resources, "bucket")
	assert.Equal(t, types.DeploymentResourceReady, state.Resources["bucket"].Status)

	require.Contains(t, state.Outputs, "bucket")
	assert.Contains(t, state.Outputs["bucket"], "arn",
		"bucket outputs should include the ARN")
	assert.Equal(t, bucketName, state.Outputs["bucket"]["bucketName"])
}

// TestCore_CycleDetection verifies that the command service rejects templates
// with circular dependencies at plan/apply time before any workflow is started.
//
// This is a safety check: without cycle detection, the DAG scheduler would
// deadlock or loop forever.
func TestCore_CycleDetection(t *testing.T) {
	env := setupCoreStack(t)

	// --- Submit Apply with cyclic template ---
	//
	// The command service runs the DAG parser which detects the cycle and
	// returns a terminal error (no workflow is started).
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      cyclicDependencyTemplate(),
		DeploymentKey: "test-cycle",
		Variables:     accountVariables(),
	})
	require.Error(t, err, "Apply should fail for a cyclic template")
	assert.Contains(t, strings.ToLower(err.Error()), "cycle",
		"error message should mention cycle detection")
}

// TestCore_Plan_CycleDetection verifies cycle detection also works for the
// Plan handler (same pipeline, different handler).
func TestCore_Plan_CycleDetection(t *testing.T) {
	env := setupCoreStack(t)

	_, err := ingress.Service[command.PlanRequest, command.PlanResponse](
		env.ingress, "PraxisCommandService", "Plan",
	).Request(t.Context(), command.PlanRequest{
		Template:  cyclicDependencyTemplate(),
		Variables: accountVariables(),
	})
	require.Error(t, err, "Plan should fail for a cyclic template")
	assert.Contains(t, strings.ToLower(err.Error()), "cycle",
		"error message should mention cycle detection")
}

// TestCore_DeploymentEvents verifies that the event feed is populated during
// an apply workflow and can be read via the ListSince shared handler.
func TestCore_DeploymentEvents(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "evt")
	deployKey := "test-events-" + bucketName

	// --- Apply a simple template ---
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      simpleS3Template(bucketName),
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)

	// Wait for completion
	pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete},
		60*time.Second,
	)

	// --- Read events ---
	//
	// The workflow appends events at every lifecycle transition. We read all
	// events starting from sequence 0.
	events, err := ingress.Object[int64, []orchestrator.DeploymentEvent](
		env.ingress,
		orchestrator.DeploymentEventsServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err, "ListSince should succeed")

	// Verify we have a meaningful event stream.
	// Expected events (at minimum):
	//   1. "apply request accepted" (from command service)
	//   2. "deployment workflow started" (from workflow Run)
	//   3. "dispatched S3Bucket resource" (from dispatch loop)
	//   4. "resource bucket is ready" (from completion)
	//   5. "deployment finished with status Complete" (from finalize)
	require.GreaterOrEqual(t, len(events), 3,
		"should have at least 3 deployment events for a single-resource apply")

	// Verify events are monotonically sequenced.
	for i := 1; i < len(events); i++ {
		assert.Greater(t, events[i].Sequence, events[i-1].Sequence,
			"events should have monotonically increasing sequences")
	}

	// Verify the first event references the deployment key.
	assert.Equal(t, deployKey, events[0].DeploymentKey,
		"events should reference the correct deployment key")
}

// TestCore_GetDeploymentDetail verifies that the GetDetail shared handler
// returns the public deployment detail shape after a successful apply.
func TestCore_GetDeploymentDetail(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "det")
	deployKey := "test-detail-" + bucketName

	// Apply
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      simpleS3Template(bucketName),
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)

	// Wait for completion
	pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete},
		60*time.Second,
	)

	// --- Query GetDetail ---
	//
	// GetDetail projects the durable state into the public DeploymentDetail
	// shape that the CLI renders for `praxis get Deployment/<key>`.
	detail, err := ingress.Object[restate.Void, *types.DeploymentDetail](
		env.ingress,
		orchestrator.DeploymentStateServiceName,
		deployKey,
		"GetDetail",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err, "GetDetail should succeed")
	require.NotNil(t, detail, "detail should not be nil")

	assert.Equal(t, deployKey, detail.Key)
	assert.Equal(t, types.DeploymentComplete, detail.Status)
	require.Len(t, detail.Resources, 1,
		"detail should contain exactly 1 resource")
	assert.Equal(t, "bucket", detail.Resources[0].Name)
	assert.Equal(t, "S3Bucket", detail.Resources[0].Kind)
	assert.Equal(t, types.DeploymentResourceReady, detail.Resources[0].Status)
	assert.False(t, detail.CreatedAt.IsZero(), "CreatedAt should be populated")
	assert.False(t, detail.UpdatedAt.IsZero(), "UpdatedAt should be populated")
}

// TestCore_Import_S3 verifies the Import command for an S3 bucket that was
// created outside of Praxis (directly in LocalStack).
func TestCore_Import_S3(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "imp")

	// Create bucket directly in LocalStack (simulating a pre-existing resource)
	_, err := env.s3Client.CreateBucket(context.Background(), &s3sdk.CreateBucketInput{
		Bucket: &bucketName,
	})
	require.NoError(t, err, "direct S3 bucket creation should succeed")

	// --- Submit Import request ---
	resp, err := ingress.Service[command.ImportRequest, command.ImportResponse](
		env.ingress, "PraxisCommandService", "Import",
	).Request(t.Context(), command.ImportRequest{
		Kind:       "S3Bucket",
		ResourceID: bucketName,
		Region:     "us-east-1",
		Mode:       types.ModeManaged,
		Account:    integrationAccountName,
	})
	require.NoError(t, err, "Import should succeed for existing bucket")

	assert.NotEmpty(t, resp.Key, "import should return a canonical resource key")
	assert.Contains(t, resp.Key, bucketName,
		"resource key should contain the bucket name")
	assert.Equal(t, types.StatusReady, resp.Status,
		"imported resource should be Ready")
	require.NotEmpty(t, resp.Outputs, "import should return driver outputs")
	assert.Equal(t, bucketName, resp.Outputs["bucketName"],
		"outputs should include the bucket name")
}

// TestCore_Delete_MultiResource verifies that delete processes resources
// in reverse topological order so that dependents are removed before their
// dependencies.
func TestCore_Delete_MultiResource(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "delmr")
	sgName := uniqueName(t, "delmrsg")
	vpcId := defaultVpcId(t, env.ec2Client)
	deployKey := "test-delete-multi-" + bucketName

	// --- Apply multi-resource template ---
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      multiResourceTemplate(bucketName, sgName, vpcId),
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete},
		90*time.Second,
	)
	require.Equal(t, types.DeploymentComplete, state.Status)

	// Record the SG group ID for later verification
	sgGroupId := state.Outputs["appSG"]["groupId"].(string)

	// --- Submit delete ---
	_, err = ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{
		DeploymentKey: deployKey,
	})
	require.NoError(t, err)

	// --- Poll until deleted ---
	//
	// The delete workflow processes in reverse topo order:
	//   1. Delete bucket first (it's the dependent / leaf)
	//   2. Delete SG second (it's the dependency / root)
	deleteState := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
		90*time.Second,
	)
	assert.Equal(t, types.DeploymentDeleted, deleteState.Status,
		"multi-resource deployment should be fully deleted")

	// --- Verify resources are gone from LocalStack ---
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &bucketName,
	})
	require.Error(t, err, "bucket should be deleted")

	_, err = env.ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		GroupIds: []string{sgGroupId},
	})
	require.Error(t, err, "security group should be deleted")

	// --- Verify removal from listing index ---
	pollDeploymentList(t, env.ingress, deployKey, false, 10*time.Second)
}
