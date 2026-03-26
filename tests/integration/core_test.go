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
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	cloudevents "github.com/cloudevents/sdk-go/v2"
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
	driverec2 "github.com/shirvan/praxis/internal/drivers/ec2"
	driverkeypair "github.com/shirvan/praxis/internal/drivers/keypair"
	drivers3 "github.com/shirvan/praxis/internal/drivers/s3"
	driversg "github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
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
//  2. Instantiates the S3, EC2, and SG drivers.
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
	authClient := authservice.NewAuthClient()

	// --- Typed drivers ---
	s3Driver := drivers3.NewS3BucketDriver(authClient)
	ec2Driver := driverec2.NewEC2InstanceDriver(authClient)
	keyPairDriver := driverkeypair.NewKeyPairDriver(authClient)
	sgDriver := driversg.NewSecurityGroupDriver(authClient)

	// --- Provider adapter registry ---
	//
	// The adapters get the live AWS APIs so that `praxis plan` can describe
	// existing state during tests. The same construction path as
	// cmd/praxis-core/main.go keeps these tests honest.
	providers := provider.NewRegistry(authClient)

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
	cmdService := command.NewPraxisCommandService(cfg, authClient, providers)
	applyWorkflow := orchestrator.NewDeploymentWorkflow(providers)
	deleteWorkflow := orchestrator.NewDeploymentDeleteWorkflow(providers)
	rollbackWorkflow := orchestrator.NewDeploymentRollbackWorkflow(providers)

	// --- Bind everything into one Restate test environment ---
	//
	// restatetest.Start starts a real Restate container via Testcontainers,
	// registers every service, and returns an environment whose Ingress()
	// client routes through the actual Restate runtime.
	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(workspace.NewWorkspaceService(absSchemaDir)),
		restate.Reflect(workspace.WorkspaceIndex{}),
		// Command service
		restate.Reflect(cmdService),
		// Apply workflow
		restate.Reflect(applyWorkflow),
		// Delete workflow
		restate.Reflect(deleteWorkflow),
		// Rollback workflow
		restate.Reflect(rollbackWorkflow),
		// Durable deployment state
		restate.Reflect(orchestrator.DeploymentStateObj{}),
		// Global deployment index
		restate.Reflect(orchestrator.DeploymentIndex{}),
		// Per-deployment event feed
		restate.Reflect(orchestrator.DeploymentEvents{}),
		// CloudEvents event system
		restate.Reflect(orchestrator.NewEventBus(absSchemaDir)),
		restate.Reflect(orchestrator.DeploymentEventStore{}),
		restate.Reflect(orchestrator.EventIndex{}),
		restate.Reflect(orchestrator.ResourceEventOwnerObj{}),
		restate.Reflect(orchestrator.ResourceEventBridge{}),
		restate.Reflect(orchestrator.SinkRouter{}),
		restate.Reflect(orchestrator.NewNotificationSinkConfig(absSchemaDir)),
		// Template registry
		restate.Reflect(registry.TemplateRegistry{}),
		restate.Reflect(registry.TemplateIndex{}),
		restate.Reflect(registry.PolicyRegistry{}),

		// Typed drivers — the workflows call these via Restate service-to-service
		restate.Reflect(s3Driver),
		restate.Reflect(ec2Driver),
		restate.Reflect(keyPairDriver),
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

func pollEventIndexTypes(
	t *testing.T,
	client *ingress.Client,
	query orchestrator.EventQuery,
	expectedTypes []string,
	timeout time.Duration,
) []orchestrator.SequencedCloudEvent {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		events, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
			client,
			orchestrator.EventIndexServiceName,
			orchestrator.EventIndexGlobalKey,
			"Query",
		).Request(t.Context(), query)
		require.NoError(t, err, "polling EventIndex should not fail")

		typeSet := make(map[string]bool, len(events))
		for _, record := range events {
			typeSet[record.Event.Type()] = true
		}

		complete := true
		for _, expectedType := range expectedTypes {
			if !typeSet[expectedType] {
				complete = false
				break
			}
		}
		if complete {
			return events
		}

		if time.Now().After(deadline) {
			seenTypes := make([]string, 0, len(typeSet))
			for eventType := range typeSet {
				seenTypes = append(seenTypes, eventType)
			}
			t.Fatalf("event index query %+v did not contain expected types %v within %v; saw %v", query, expectedTypes, timeout, seenTypes)
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

func simpleS3TemplateWithPreventDestroy(bucketName string) string {
	return fmt.Sprintf(`
resources: {
	bucket: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: {
			name: %q
		}
		lifecycle: {
			preventDestroy: true
		}
		spec: {
			region:     "us-east-1"
			versioning: true
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
		}
	}
}
`, bucketName)
}

// multiResourceTemplate returns a CUE template with an S3 bucket and a
// SecurityGroup. The bucket's tags reference the SG's outputs via an output
// expression, creating a real dependency edge in the DAG.
//
// This validates:
//   - DAG dependency parsing (the bucket depends on the SG)
//   - Correct topological dispatch order (SG first, then bucket)
//   - Dispatch-time hydration of resources.*.outputs.* expressions
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
				secGroupId: "${resources.appSG.outputs.groupId}"
			}
		}
	}
}
`, sgName, sgName, vpcId, bucketName)
}

func rollbackFailureTemplate(bucketName, dependentBucketName string) string {
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
		}
	}
	brokenBucket: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: {
			name: %q
		}
		spec: {
			region: "us-east-1"
			tags: {
				missingOutput: "${resources.bucket.outputs.noSuchField}"
			}
		}
	}
}
`, bucketName, dependentBucketName)
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
				dep: "${resources.bucketB.outputs.bucketName}"
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
				dep: "${resources.bucketA.outputs.bucketName}"
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
// orchestration path with cross-resource dependencies:
//
//  1. Submit a multi-resource template (SG + S3 bucket) where the bucket's
//     tags reference the SG's outputs via expressions
//  2. Verify the workflow dispatches the SG first (it has no deps)
//  3. Verify the bucket is dispatched second (it depends on the SG)
//  4. Verify both resources exist in LocalStack
//  5. Verify the bucket's tags contain the actual SG group ID (expression hydration)
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
	//   4. Hydrates the bucket's expressions with SG outputs
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

	// --- Step 5: Verify expression hydration in bucket tags ---
	//
	// The bucket's `secGroupId` tag should contain the actual SG group ID,
	// not the expression placeholder string. This proves dispatch-time hydration
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
		"bucket's secGroupId tag should equal the SG's actual groupId (expression hydration)")
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

	cloudEvents, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err, "CloudEvents ListSince should succeed")
	require.NotEmpty(t, cloudEvents, "CloudEvents store should receive deployment lifecycle events")
	assert.Equal(t, deployKey, orchestratorEventExtension(cloudEvents[0], orchestrator.EventExtensionDeployment))
	assert.NotEmpty(t, cloudEvents[0].Event.Type(), "CloudEvents should have a type")

	typeSet := make(map[string]bool, len(cloudEvents))
	var resourceReadyPayload map[string]any
	for _, record := range cloudEvents {
		typeSet[record.Event.Type()] = true
		if record.Event.Type() == orchestrator.EventTypeResourceReady {
			require.NoError(t, record.Event.DataAs(&resourceReadyPayload))
		}
	}
	assert.True(t, typeSet[orchestrator.EventTypeDeploymentSubmitted])
	assert.True(t, typeSet[orchestrator.EventTypeDeploymentStarted])
	assert.True(t, typeSet[orchestrator.EventTypeResourceDispatched])
	assert.True(t, typeSet[orchestrator.EventTypeResourceReady])
	assert.True(t, typeSet[orchestrator.EventTypeDeploymentCompleted])
	require.NotNil(t, resourceReadyPayload, "resource.ready payload should be present")
	assert.Contains(t, resourceReadyPayload, "outputs", "resource.ready should carry typed outputs in the CloudEvent payload")

	indexedEvents, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.EventIndexServiceName,
		orchestrator.EventIndexGlobalKey,
		"Query",
	).Request(t.Context(), orchestrator.EventQuery{DeploymentKey: deployKey})
	require.NoError(t, err, "EventIndex query should succeed")
	require.NotEmpty(t, indexedEvents, "EventIndex should contain the deployment events")
}

func TestCore_EventBusRejectsInvalidLifecycleData(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := "invalid-events"
	deployKey := "invalid-event-deployment"

	_, err := ingress.Object[workspace.WorkspaceConfig, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"Configure",
	).Request(t.Context(), workspace.WorkspaceConfig{
		Name:    workspaceName,
		Account: integrationAccountName,
		Region:  "us-east-1",
	})
	require.NoError(t, err)

	createdAt := time.Now().UTC()
	_, err = ingress.Object[orchestrator.DeploymentPlan, int64](
		env.ingress,
		orchestrator.DeploymentStateServiceName,
		deployKey,
		"InitDeployment",
	).Request(t.Context(), orchestrator.DeploymentPlan{
		Key:       deployKey,
		Workspace: workspaceName,
		CreatedAt: createdAt,
	})
	require.NoError(t, err)

	event := cloudevents.NewEvent(cloudevents.VersionV1)
	event.SetSource(fmt.Sprintf("/praxis/%s/%s", workspaceName, deployKey))
	event.SetType(orchestrator.EventTypeDeploymentStarted)
	event.SetTime(createdAt)
	event.SetExtension(orchestrator.EventExtensionDeployment, deployKey)
	event.SetExtension(orchestrator.EventExtensionWorkspace, workspaceName)
	event.SetExtension(orchestrator.EventExtensionGeneration, int64(1))
	event.SetExtension(orchestrator.EventExtensionCategory, orchestrator.EventCategoryLifecycle)
	event.SetExtension(orchestrator.EventExtensionSeverity, orchestrator.EventSeverityInfo)
	err = event.SetData(cloudevents.ApplicationJSON, map[string]any{
		"status": string(types.DeploymentRunning),
	})
	require.NoError(t, err)

	_, err = ingress.Object[cloudevents.Event, restate.Void](
		env.ingress,
		orchestrator.EventBusServiceName,
		orchestrator.EventBusGlobalKey,
		"Emit",
	).Request(t.Context(), event)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "event data validation failed")
}

func TestCore_SystemAndPolicyEvents(t *testing.T) {
	env := setupCoreStack(t)

	_, err := ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name: "test-log-sink",
		Type: orchestrator.SinkTypeStructuredLog,
	})
	require.NoError(t, err)

	_, err = ingress.Object[string, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Remove",
	).Request(t.Context(), "test-log-sink")
	require.NoError(t, err)

	systemEvents, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.EventIndexServiceName,
		orchestrator.EventIndexGlobalKey,
		"Query",
	).Request(t.Context(), orchestrator.EventQuery{Workspace: "system", TypePrefix: "dev.praxis.system.sink."})
	require.NoError(t, err)
	require.NotEmpty(t, systemEvents)
	systemTypes := make(map[string]bool, len(systemEvents))
	for _, record := range systemEvents {
		systemTypes[record.Event.Type()] = true
	}
	assert.True(t, systemTypes[orchestrator.EventTypeSystemSinkRegistered])
	assert.True(t, systemTypes[orchestrator.EventTypeSystemSinkRemoved])

	bucketName := uniqueName(t, "pd")
	deployKey := "test-policy-" + bucketName
	_, err = ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      simpleS3TemplateWithPreventDestroy(bucketName),
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete},
		60*time.Second,
	)
	require.Equal(t, types.DeploymentComplete, state.Status)

	_, err = ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{DeploymentKey: deployKey})
	require.NoError(t, err)

	state = pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed},
		60*time.Second,
	)
	require.Equal(t, types.DeploymentFailed, state.Status)

	deploymentEvents := pollEventIndexTypes(
		t,
		env.ingress,
		orchestrator.EventQuery{DeploymentKey: deployKey},
		[]string{
			orchestrator.EventTypeCommandApply,
			orchestrator.EventTypeCommandDelete,
			orchestrator.EventTypePolicyPreventedDestroy,
			orchestrator.EventTypeDeploymentDeleteFailed,
		},
		30*time.Second,
	)
	require.NotEmpty(t, deploymentEvents)
	typeSet := make(map[string]bool, len(deploymentEvents))
	for _, record := range deploymentEvents {
		typeSet[record.Event.Type()] = true
	}
	assert.True(t, typeSet[orchestrator.EventTypeCommandApply])
	assert.True(t, typeSet[orchestrator.EventTypeCommandDelete])
	assert.True(t, typeSet[orchestrator.EventTypePolicyPreventedDestroy])
	assert.True(t, typeSet[orchestrator.EventTypeDeploymentDeleteFailed])
}

func TestCore_NotificationSinkValidation(t *testing.T) {
	env := setupCoreStack(t)

	_, err := ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name: "bad-webhook",
		Type: "webhook",
		URL:  "http://example.com/events",
	})
	require.Error(t, err, "non-local http webhook URLs should be rejected by schema validation")
	assert.Contains(t, strings.ToLower(err.Error()), "invalid sink config")

	_, err = ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name: "local-events",
		Type: "cloudevents_http",
		URL:  "http://localhost:8080/events",
	})
	require.NoError(t, err)

	sink, err := ingress.Object[string, *orchestrator.NotificationSink](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Get",
	).Request(t.Context(), "local-events")
	require.NoError(t, err)
	require.NotNil(t, sink)
	assert.Equal(t, "structured", sink.ContentMode, "CUE defaults should normalize contentMode")
	assert.Equal(t, 3, sink.Retry.MaxAttempts, "CUE defaults should normalize retry.maxAttempts")
	assert.Equal(t, 1000, sink.Retry.BackoffMs, "CUE defaults should normalize retry.backoffMs")
}

func TestCore_NotificationSinkStatusAndHealth(t *testing.T) {
	env := setupCoreStack(t)

	var (
		mu       sync.Mutex
		requests int
	)
	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failingServer.Close()

	_, err := ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name:  "failing-webhook",
		Type:  orchestrator.SinkTypeWebhook,
		URL:   failingServer.URL,
		Retry: orchestrator.RetryPolicy{MaxAttempts: 1, BackoffMs: 100},
	})
	require.NoError(t, err)

	for range 3 {
		_, err = ingress.Service[string, restate.Void](
			env.ingress,
			orchestrator.SinkRouterServiceName,
			"Test",
		).Request(t.Context(), "failing-webhook")
		require.Error(t, err)
	}

	sink, err := ingress.Object[string, *orchestrator.NotificationSink](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Get",
	).Request(t.Context(), "failing-webhook")
	require.NoError(t, err)
	require.NotNil(t, sink)
	assert.Equal(t, orchestrator.SinkDeliveryStateOpen, sink.DeliveryState)
	assert.Equal(t, 3, sink.ConsecutiveFailures)
	assert.Equal(t, int64(3), sink.FailedCount)
	assert.NotEmpty(t, sink.LastFailureAt)
	assert.NotEmpty(t, sink.CircuitOpenUntil)
	assert.Contains(t, sink.LastError, "unexpected HTTP 500")

	health, err := ingress.Object[restate.Void, orchestrator.NotificationSinkHealth](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Health",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, health.Total)
	assert.Equal(t, 0, health.Healthy)
	assert.Equal(t, 0, health.Degraded)
	assert.Equal(t, 1, health.Open)
	assert.NotEmpty(t, health.LastDeliveryAt)

	_, err = ingress.Service[string, restate.Void](
		env.ingress,
		orchestrator.SinkRouterServiceName,
		"Test",
	).Request(t.Context(), "failing-webhook")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circuit is open")

	mu.Lock()
	assert.Equal(t, 3, requests)
	mu.Unlock()
}

func TestCore_NotificationSinkDeliverySuccess(t *testing.T) {
	env := setupCoreStack(t)

	var (
		mu       sync.Mutex
		requests int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	_, err := ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name:  "healthy-webhook",
		Type:  orchestrator.SinkTypeWebhook,
		URL:   server.URL,
		Retry: orchestrator.RetryPolicy{MaxAttempts: 1, BackoffMs: 100},
	})
	require.NoError(t, err)

	_, err = ingress.Service[string, restate.Void](
		env.ingress,
		orchestrator.SinkRouterServiceName,
		"Test",
	).Request(t.Context(), "healthy-webhook")
	require.NoError(t, err)

	sink, err := ingress.Object[string, *orchestrator.NotificationSink](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Get",
	).Request(t.Context(), "healthy-webhook")
	require.NoError(t, err)
	require.NotNil(t, sink)
	assert.Equal(t, orchestrator.SinkDeliveryStateHealthy, sink.DeliveryState)
	assert.Equal(t, int64(1), sink.DeliveredCount)
	assert.Equal(t, 0, sink.ConsecutiveFailures)
	assert.NotEmpty(t, sink.LastSuccessAt)
	assert.Empty(t, sink.LastError)

	health, err := ingress.Object[restate.Void, orchestrator.NotificationSinkHealth](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Health",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, health.Total)
	assert.Equal(t, 1, health.Healthy)
	assert.Equal(t, 0, health.Open)

	mu.Lock()
	assert.Equal(t, 1, requests)
	mu.Unlock()
}

func TestCore_WorkspaceEventRetention(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := "events-retention"

	_, err := ingress.Object[workspace.WorkspaceConfig, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"Configure",
	).Request(t.Context(), workspace.WorkspaceConfig{
		Name:    workspaceName,
		Account: integrationAccountName,
		Region:  "us-east-1",
	})
	require.NoError(t, err)

	defaultPolicy, err := ingress.Object[restate.Void, workspace.EventRetentionPolicy](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"GetEventRetention",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, workspace.DefaultEventRetentionPolicy(), defaultPolicy)

	_, err = ingress.Object[workspace.EventRetentionPolicy, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"SetEventRetention",
	).Request(t.Context(), workspace.EventRetentionPolicy{
		MaxAge: "30x",
	})
	require.Error(t, err, "invalid retention durations should be rejected by schema validation")
	assert.Contains(t, strings.ToLower(err.Error()), "invalid retention policy")

	_, err = ingress.Object[workspace.EventRetentionPolicy, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"SetEventRetention",
	).Request(t.Context(), workspace.EventRetentionPolicy{
		MaxAge:           "30d",
		ShipBeforeDelete: true,
		DrainSink:        "archive",
	})
	require.Error(t, err, "retention policies that ship before delete should require a registered drain sink")
	assert.Contains(t, strings.ToLower(err.Error()), "not registered")

	_, err = ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name: "archive",
		Type: "structured_log",
	})
	require.NoError(t, err)

	_, err = ingress.Object[workspace.EventRetentionPolicy, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"SetEventRetention",
	).Request(t.Context(), workspace.EventRetentionPolicy{
		MaxAge:                 "30d",
		MaxEventsPerDeployment: 500,
		ShipBeforeDelete:       true,
		DrainSink:              "archive",
	})
	require.NoError(t, err)

	policy, err := ingress.Object[restate.Void, workspace.EventRetentionPolicy](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"GetEventRetention",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "30d", policy.MaxAge)
	assert.Equal(t, 500, policy.MaxEventsPerDeployment)
	assert.Equal(t, 100000, policy.MaxIndexEntries, "CUE defaults should normalize maxIndexEntries")
	assert.Equal(t, "24h", policy.SweepInterval, "CUE defaults should normalize sweepInterval")
	assert.True(t, policy.ShipBeforeDelete)
	assert.Equal(t, "archive", policy.DrainSink)
}

func TestCore_RetentionSweep_PrunesAndShips(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := "retention-" + uniqueName(t, "ws")

	_, err := ingress.Object[workspace.WorkspaceConfig, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"Configure",
	).Request(t.Context(), workspace.WorkspaceConfig{
		Name:    workspaceName,
		Account: integrationAccountName,
		Region:  "us-east-1",
	})
	require.NoError(t, err)

	var (
		mu            sync.Mutex
		drainRequests int
	)
	drainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		drainRequests++
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer drainServer.Close()

	_, err = ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name: "archive-webhook",
		Type: orchestrator.SinkTypeWebhook,
		URL:  drainServer.URL,
	})
	require.NoError(t, err)

	_, err = ingress.Object[workspace.EventRetentionPolicy, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"SetEventRetention",
	).Request(t.Context(), workspace.EventRetentionPolicy{
		MaxAge:                 "1d",
		MaxEventsPerDeployment: 100,
		MaxIndexEntries:        1000,
		SweepInterval:          "24h",
		ShipBeforeDelete:       true,
		DrainSink:              "archive-webhook",
	})
	require.NoError(t, err)

	deployKey := "retention-sweep-" + uniqueName(t, "dep")
	oldTime := time.Now().Add(-48 * time.Hour).UTC()
	_, err = ingress.Object[types.DeploymentSummary, restate.Void](
		env.ingress,
		orchestrator.DeploymentIndexServiceName,
		orchestrator.DeploymentIndexGlobalKey,
		"Upsert",
	).Request(t.Context(), types.DeploymentSummary{
		Key:       deployKey,
		Status:    types.DeploymentComplete,
		Resources: 1,
		Workspace: workspaceName,
		CreatedAt: oldTime,
		UpdatedAt: oldTime,
	})
	require.NoError(t, err)

	for index := 0; index < 3; index++ {
		event := cloudevents.NewEvent(cloudevents.VersionV1)
		event.SetSource(fmt.Sprintf("/praxis/%s/%s", workspaceName, deployKey))
		event.SetType(orchestrator.EventTypeDeploymentStarted)
		event.SetTime(oldTime.Add(time.Duration(index) * time.Minute))
		event.SetExtension(orchestrator.EventExtensionDeployment, deployKey)
		event.SetExtension(orchestrator.EventExtensionWorkspace, workspaceName)
		event.SetExtension(orchestrator.EventExtensionGeneration, int64(1))
		event.SetExtension(orchestrator.EventExtensionCategory, orchestrator.EventCategoryLifecycle)
		event.SetExtension(orchestrator.EventExtensionSeverity, orchestrator.EventSeverityInfo)
		err = event.SetData(cloudevents.ApplicationJSON, map[string]any{
			"message": fmt.Sprintf("old event %d", index+1),
		})
		require.NoError(t, err)

		_, err = ingress.Object[cloudevents.Event, restate.Void](
			env.ingress,
			orchestrator.EventBusServiceName,
			orchestrator.EventBusGlobalKey,
			"Emit",
		).Request(t.Context(), event)
		require.NoError(t, err)
	}

	beforeEvents, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	require.Greater(t, len(beforeEvents), 0)

	beforeCount, err := ingress.Object[restate.Void, int64](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"Count",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	sweepResult, err := ingress.Object[orchestrator.RetentionSweepRequest, orchestrator.RetentionSweepResult](
		env.ingress,
		orchestrator.EventBusServiceName,
		orchestrator.EventBusGlobalKey,
		"RunRetentionSweep",
	).Request(t.Context(), orchestrator.RetentionSweepRequest{Workspace: workspaceName})
	require.NoError(t, err)
	assert.Equal(t, workspaceName, sweepResult.Workspace)
	assert.Greater(t, sweepResult.PrunedEvents, 0)
	assert.Greater(t, sweepResult.ShippedEvents, 0)

	afterEvents, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	require.NotEmpty(t, afterEvents)
	assert.Less(t, len(afterEvents), len(beforeEvents), "retention sweep should prune older deployment events")
	assert.Greater(t, afterEvents[0].Sequence, beforeEvents[0].Sequence, "oldest deployment events should be pruned first")

	afterCount, err := ingress.Object[restate.Void, int64](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"Count",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Less(t, afterCount, beforeCount)

	mu.Lock()
	receivedDrainRequests := drainRequests
	mu.Unlock()
	assert.Greater(t, receivedDrainRequests, 0, "drain sink should receive pruned event batches")

	retentionEvents, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.EventIndexServiceName,
		orchestrator.EventIndexGlobalKey,
		"Query",
	).Request(t.Context(), orchestrator.EventQuery{
		Workspace:  workspaceName,
		TypePrefix: "dev.praxis.system.retention.",
	})
	require.NoError(t, err)
	require.NotEmpty(t, retentionEvents, "retention sweep should emit system retention events")
}

func TestCore_RetentionSweep_ShipFailureSkipsPrune(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := uniqueName(t, "rf")

	_, err := ingress.Object[workspace.WorkspaceConfig, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"Configure",
	).Request(t.Context(), workspace.WorkspaceConfig{
		Name:    workspaceName,
		Account: integrationAccountName,
		Region:  "us-east-1",
	})
	require.NoError(t, err)

	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failingServer.Close()

	_, err = ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name:  "archive-failing-webhook",
		Type:  orchestrator.SinkTypeWebhook,
		URL:   failingServer.URL,
		Retry: orchestrator.RetryPolicy{MaxAttempts: 1, BackoffMs: 100},
	})
	require.NoError(t, err)

	_, err = ingress.Object[workspace.EventRetentionPolicy, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"SetEventRetention",
	).Request(t.Context(), workspace.EventRetentionPolicy{
		MaxAge:                 "1d",
		MaxEventsPerDeployment: 100,
		MaxIndexEntries:        1000,
		SweepInterval:          "24h",
		ShipBeforeDelete:       true,
		DrainSink:              "archive-failing-webhook",
	})
	require.NoError(t, err)

	deployKey := "retention-fail-" + uniqueName(t, "dep")
	oldTime := time.Now().Add(-48 * time.Hour).UTC()
	_, err = ingress.Object[types.DeploymentSummary, restate.Void](
		env.ingress,
		orchestrator.DeploymentIndexServiceName,
		orchestrator.DeploymentIndexGlobalKey,
		"Upsert",
	).Request(t.Context(), types.DeploymentSummary{
		Key:       deployKey,
		Status:    types.DeploymentComplete,
		Resources: 1,
		Workspace: workspaceName,
		CreatedAt: oldTime,
		UpdatedAt: oldTime,
	})
	require.NoError(t, err)

	for index := 0; index < 2; index++ {
		event := cloudevents.NewEvent(cloudevents.VersionV1)
		event.SetSource(fmt.Sprintf("/praxis/%s/%s", workspaceName, deployKey))
		event.SetType(orchestrator.EventTypeDeploymentStarted)
		event.SetTime(oldTime.Add(time.Duration(index) * time.Minute))
		event.SetExtension(orchestrator.EventExtensionDeployment, deployKey)
		event.SetExtension(orchestrator.EventExtensionWorkspace, workspaceName)
		event.SetExtension(orchestrator.EventExtensionGeneration, int64(1))
		event.SetExtension(orchestrator.EventExtensionCategory, orchestrator.EventCategoryLifecycle)
		event.SetExtension(orchestrator.EventExtensionSeverity, orchestrator.EventSeverityInfo)
		err = event.SetData(cloudevents.ApplicationJSON, map[string]any{
			"message": fmt.Sprintf("old event %d", index+1),
		})
		require.NoError(t, err)

		_, err = ingress.Object[cloudevents.Event, restate.Void](
			env.ingress,
			orchestrator.EventBusServiceName,
			orchestrator.EventBusGlobalKey,
			"Emit",
		).Request(t.Context(), event)
		require.NoError(t, err)
	}

	beforeCount, err := ingress.Object[restate.Void, int64](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"Count",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	result, err := ingress.Object[orchestrator.RetentionSweepRequest, orchestrator.RetentionSweepResult](
		env.ingress,
		orchestrator.EventBusServiceName,
		orchestrator.EventBusGlobalKey,
		"RunRetentionSweep",
	).Request(t.Context(), orchestrator.RetentionSweepRequest{Workspace: workspaceName})
	require.NoError(t, err)
	assert.Contains(t, result.FailedDeployments, deployKey)
	assert.Equal(t, 0, result.PrunedEvents)
	assert.Equal(t, 0, result.ShippedEvents)

	afterCount, err := ingress.Object[restate.Void, int64](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"Count",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, afterCount, beforeCount, "failed drain delivery should not prune the original deployment events")

	afterEvents, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	var lifecycleEvents int
	for _, record := range afterEvents {
		if record.Event.Type() == orchestrator.EventTypeDeploymentStarted {
			lifecycleEvents++
		}
	}
	assert.Equal(t, 2, lifecycleEvents, "failed drain delivery should leave the original deployment events intact")

	retentionEvents, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.EventIndexServiceName,
		orchestrator.EventIndexGlobalKey,
		"Query",
	).Request(t.Context(), orchestrator.EventQuery{
		Workspace:  workspaceName,
		TypePrefix: orchestrator.EventTypeSystemRetentionShipFailed,
	})
	require.NoError(t, err)
	require.NotEmpty(t, retentionEvents)
}

func TestCore_EventIndexQuery_MultiDeployment(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := uniqueName(t, "q")
	deploymentKeys := []string{
		"query-a-" + uniqueName(t, "dep"),
		"query-b-" + uniqueName(t, "dep"),
	}

	for _, deployKey := range deploymentKeys {
		_, err := ingress.Object[orchestrator.DeploymentPlan, int64](
			env.ingress,
			orchestrator.DeploymentStateServiceName,
			deployKey,
			"InitDeployment",
		).Request(t.Context(), orchestrator.DeploymentPlan{
			Key:       deployKey,
			Workspace: workspaceName,
			CreatedAt: time.Now().UTC(),
		})
		require.NoError(t, err)

		event := cloudevents.NewEvent(cloudevents.VersionV1)
		event.SetSource(fmt.Sprintf("/praxis/%s/%s", workspaceName, deployKey))
		event.SetType(orchestrator.EventTypeResourceReady)
		event.SetTime(time.Now().UTC())
		event.SetSubject("bucket")
		event.SetExtension(orchestrator.EventExtensionDeployment, deployKey)
		event.SetExtension(orchestrator.EventExtensionWorkspace, workspaceName)
		event.SetExtension(orchestrator.EventExtensionGeneration, int64(1))
		event.SetExtension(orchestrator.EventExtensionResourceKind, "S3Bucket")
		event.SetExtension(orchestrator.EventExtensionCategory, orchestrator.EventCategoryLifecycle)
		event.SetExtension(orchestrator.EventExtensionSeverity, orchestrator.EventSeverityInfo)
		err = event.SetData(cloudevents.ApplicationJSON, map[string]any{
			"message":      "resource ready",
			"resourceName": "bucket",
			"resourceKind": "S3Bucket",
			"resourceKey":  deployKey + "-bucket",
			"status":       "Running",
		})
		require.NoError(t, err)

		_, err = ingress.Object[cloudevents.Event, restate.Void](
			env.ingress,
			orchestrator.EventBusServiceName,
			orchestrator.EventBusGlobalKey,
			"Emit",
		).Request(t.Context(), event)
		require.NoError(t, err)
	}

	var (
		records  []orchestrator.SequencedCloudEvent
		queryErr error
	)
	require.Eventually(t, func() bool {
		records, queryErr = ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
			env.ingress,
			orchestrator.EventIndexServiceName,
			orchestrator.EventIndexGlobalKey,
			"Query",
		).Request(t.Context(), orchestrator.EventQuery{
			Workspace:  workspaceName,
			TypePrefix: orchestrator.EventTypeResourceReady,
			Limit:      10,
		})
		require.NoError(t, queryErr)
		return len(records) == 2
	}, 10*time.Second, 200*time.Millisecond)

	seenDeployments := map[string]bool{}
	for _, record := range records {
		seenDeployments[orchestratorEventExtension(record, orchestrator.EventExtensionDeployment)] = true
		assert.Equal(t, orchestrator.EventTypeResourceReady, record.Event.Type())
		assert.Equal(t, workspaceName, orchestratorEventExtension(record, orchestrator.EventExtensionWorkspace))
	}
	assert.True(t, seenDeployments[deploymentKeys[0]])
	assert.True(t, seenDeployments[deploymentKeys[1]])
}

func orchestratorEventExtension(record orchestrator.SequencedCloudEvent, key string) string {
	value, ok := record.Event.Extensions()[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
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

func TestCore_Rollback_DeletesOnlyReadyResources(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "rbucket")
	dependentBucketName := uniqueName(t, "rdep")
	deployKey := "test-rollback-" + bucketName

	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      rollbackFailureTemplate(bucketName, dependentBucketName),
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed},
		90*time.Second,
	)
	require.Equal(t, types.DeploymentFailed, state.Status)

	rollbackPlan, err := ingress.Object[restate.Void, orchestrator.RollbackPlan](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"RollbackPlan",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	require.Len(t, rollbackPlan.Resources, 1)
	assert.Equal(t, "bucket", rollbackPlan.Resources[0].Name)
	assert.Equal(t, "S3Bucket", rollbackPlan.Resources[0].Kind)

	_, err = ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "RollbackDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{DeploymentKey: deployKey})
	require.NoError(t, err)

	rollbackState := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
		90*time.Second,
	)
	assert.Equal(t, types.DeploymentDeleted, rollbackState.Status)

	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: &bucketName})
	require.Error(t, err, "rollback should delete the successfully created bucket")

	events, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	var sawDeleteStarted bool
	var sawDeleteCompleted bool
	for _, record := range events {
		switch record.Event.Type() {
		case orchestrator.EventTypeResourceDeleteStarted:
			if record.Event.Subject() == "bucket" {
				sawDeleteStarted = true
			}
		case orchestrator.EventTypeResourceDeleted:
			if record.Event.Subject() == "bucket" {
				sawDeleteCompleted = true
			}
		}
	}
	assert.True(t, sawDeleteStarted)
	assert.True(t, sawDeleteCompleted)
}

// TestCore_Delete_EmitsCloudEvents verifies that the delete workflow emits
// the full lifecycle of CloudEvents: delete.started, resource.delete.started,
// resource.deleted, and delete.completed.
func TestCore_Delete_EmitsCloudEvents(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "delev")
	deployKey := "test-delete-events-" + bucketName

	// --- Apply first ---
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      simpleS3Template(bucketName),
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete},
		60*time.Second,
	)
	require.Equal(t, types.DeploymentComplete, state.Status)

	// --- Record the pre-delete event count ---
	preDeleteEvents, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	preDeleteCount := len(preDeleteEvents)

	// --- Submit delete ---
	_, err = ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		env.ingress, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{
		DeploymentKey: deployKey,
	})
	require.NoError(t, err)

	// --- Wait for delete to finish ---
	state = pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentDeleted, types.DeploymentFailed},
		60*time.Second,
	)
	require.Equal(t, types.DeploymentDeleted, state.Status)

	// --- Verify delete-specific CloudEvents were emitted ---
	allEvents, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	require.Greater(t, len(allEvents), preDeleteCount,
		"delete workflow should have appended new CloudEvents")

	// Collect only the events emitted after apply
	deleteEvents := allEvents[preDeleteCount:]
	typeSet := make(map[string]bool, len(deleteEvents))
	for _, record := range deleteEvents {
		typeSet[record.Event.Type()] = true
		// Every delete event should carry the deployment extension
		assert.Equal(t, deployKey,
			orchestratorEventExtension(record, orchestrator.EventExtensionDeployment),
			"all delete events should reference the deployment key")
	}

	assert.True(t, typeSet[orchestrator.EventTypeDeploymentDeleteStarted],
		"delete workflow should emit deployment.delete.started")
	assert.True(t, typeSet[orchestrator.EventTypeResourceDeleteStarted],
		"delete workflow should emit resource.delete.started")
	assert.True(t, typeSet[orchestrator.EventTypeResourceDeleted],
		"delete workflow should emit resource.deleted")
	assert.True(t, typeSet[orchestrator.EventTypeDeploymentDeleteDone],
		"delete workflow should emit deployment.delete.completed")

	// --- Verify the delete events are indexed ---
	indexedEvents := pollEventIndexTypes(
		t,
		env.ingress,
		orchestrator.EventQuery{DeploymentKey: deployKey},
		[]string{
			orchestrator.EventTypeDeploymentDeleteStarted,
			orchestrator.EventTypeResourceDeleteStarted,
			orchestrator.EventTypeResourceDeleted,
			orchestrator.EventTypeDeploymentDeleteDone,
		},
		15*time.Second,
	)
	require.NotEmpty(t, indexedEvents)
}

// TestCore_EventIndexQuery_Filters verifies that EventIndex.Query correctly
// filters events by workspace, type prefix, severity, and limit.
func TestCore_EventIndexQuery_Filters(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := uniqueName(t, "qf")
	deployKey := "query-filter-" + uniqueName(t, "dep")

	// Create a workspace and init the deployment
	_, err := ingress.Object[workspace.WorkspaceConfig, restate.Void](
		env.ingress,
		workspace.WorkspaceServiceName,
		workspaceName,
		"Configure",
	).Request(t.Context(), workspace.WorkspaceConfig{
		Name:    workspaceName,
		Account: integrationAccountName,
		Region:  "us-east-1",
	})
	require.NoError(t, err)

	_, err = ingress.Object[orchestrator.DeploymentPlan, int64](
		env.ingress,
		orchestrator.DeploymentStateServiceName,
		deployKey,
		"InitDeployment",
	).Request(t.Context(), orchestrator.DeploymentPlan{
		Key:       deployKey,
		Workspace: workspaceName,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	// Emit a mix of event types and severities
	emitEvent := func(eventType, category, severity string) {
		event := cloudevents.NewEvent(cloudevents.VersionV1)
		event.SetSource(fmt.Sprintf("/praxis/%s/%s", workspaceName, deployKey))
		event.SetType(eventType)
		event.SetTime(time.Now().UTC())
		event.SetExtension(orchestrator.EventExtensionDeployment, deployKey)
		event.SetExtension(orchestrator.EventExtensionWorkspace, workspaceName)
		event.SetExtension(orchestrator.EventExtensionGeneration, int64(1))
		event.SetExtension(orchestrator.EventExtensionCategory, category)
		event.SetExtension(orchestrator.EventExtensionSeverity, severity)
		err := event.SetData(cloudevents.ApplicationJSON, map[string]any{"message": eventType})
		require.NoError(t, err)

		_, err = ingress.Object[cloudevents.Event, restate.Void](
			env.ingress,
			orchestrator.EventBusServiceName,
			orchestrator.EventBusGlobalKey,
			"Emit",
		).Request(t.Context(), event)
		require.NoError(t, err)
	}

	emitEvent(orchestrator.EventTypeDeploymentStarted, orchestrator.EventCategoryLifecycle, orchestrator.EventSeverityInfo)
	emitEvent(orchestrator.EventTypeResourceDispatched, orchestrator.EventCategoryLifecycle, orchestrator.EventSeverityInfo)
	emitEvent(orchestrator.EventTypeResourceReady, orchestrator.EventCategoryLifecycle, orchestrator.EventSeverityInfo)
	emitEvent(orchestrator.EventTypeDeploymentDeleteFailed, orchestrator.EventCategoryLifecycle, orchestrator.EventSeverityError)

	// Wait for all 4 events to be indexed
	pollEventIndexTypes(t, env.ingress, orchestrator.EventQuery{DeploymentKey: deployKey},
		[]string{
			orchestrator.EventTypeDeploymentStarted,
			orchestrator.EventTypeResourceDispatched,
			orchestrator.EventTypeResourceReady,
			orchestrator.EventTypeDeploymentDeleteFailed,
		},
		15*time.Second,
	)

	// --- Filter by workspace ---
	wsEvents, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.EventIndexServiceName,
		orchestrator.EventIndexGlobalKey,
		"Query",
	).Request(t.Context(), orchestrator.EventQuery{Workspace: workspaceName})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(wsEvents), 4, "workspace filter should return at least the 4 emitted events")
	for _, record := range wsEvents {
		assert.Equal(t, workspaceName, orchestratorEventExtension(record, orchestrator.EventExtensionWorkspace))
	}

	// --- Filter by type prefix ---
	resourceEvents, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.EventIndexServiceName,
		orchestrator.EventIndexGlobalKey,
		"Query",
	).Request(t.Context(), orchestrator.EventQuery{
		DeploymentKey: deployKey,
		TypePrefix:    "dev.praxis.resource.",
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(resourceEvents), 2,
		"type prefix filter should return resource events")
	for _, record := range resourceEvents {
		assert.True(t, strings.HasPrefix(record.Event.Type(), "dev.praxis.resource."),
			"filtered events should match the type prefix")
	}

	// --- Filter by severity ---
	errorEvents, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.EventIndexServiceName,
		orchestrator.EventIndexGlobalKey,
		"Query",
	).Request(t.Context(), orchestrator.EventQuery{
		DeploymentKey: deployKey,
		Severity:      orchestrator.EventSeverityError,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(errorEvents), 1,
		"severity filter should find the error event")
	for _, record := range errorEvents {
		assert.Equal(t, orchestrator.EventSeverityError,
			orchestratorEventExtension(record, orchestrator.EventExtensionSeverity))
	}

	// --- Filter with limit ---
	limitedEvents, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.EventIndexServiceName,
		orchestrator.EventIndexGlobalKey,
		"Query",
	).Request(t.Context(), orchestrator.EventQuery{
		DeploymentKey: deployKey,
		Limit:         2,
	})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(limitedEvents), 2,
		"limit filter should cap the number of returned events")
}

// TestCore_CloudEventsJSON_Serialization verifies that CloudEvents stored in
// the deployment event store are properly serializable to JSON, which is what
// `praxis observe --output json` relies on.
func TestCore_CloudEventsJSON_Serialization(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "json")
	deployKey := "test-json-events-" + bucketName

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

	// --- Retrieve CloudEvents and verify JSON round-trip ---
	cloudEvents, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		env.ingress,
		orchestrator.DeploymentEventStoreServiceName,
		deployKey,
		"ListSince",
	).Request(t.Context(), int64(0))
	require.NoError(t, err)
	require.NotEmpty(t, cloudEvents)

	for _, record := range cloudEvents {
		// Verify each event has the required CloudEvents attributes
		assert.NotEmpty(t, record.Event.ID(), "event should have an ID")
		assert.NotEmpty(t, record.Event.Type(), "event should have a type")
		assert.NotEmpty(t, record.Event.Source(), "event should have a source")
		assert.False(t, record.Event.Time().IsZero(), "event should have a timestamp")
		assert.Greater(t, record.Sequence, int64(0), "event should have a positive sequence number")

		// Verify JSON serialization succeeds (what observe --output json uses)
		jsonBytes, err := json.Marshal(record)
		require.NoError(t, err, "CloudEvent record should be JSON-serializable")
		require.NotEmpty(t, jsonBytes)

		// Verify JSON round-trip preserves structure
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(jsonBytes, &decoded),
			"JSON output should be valid JSON")
		assert.Contains(t, decoded, "event",
			"serialized record should contain the event field")
		assert.Contains(t, decoded, "sequence",
			"serialized record should contain the sequence field")

		// Verify the event field contains expected CloudEvents attributes
		if eventMap, ok := decoded["event"].(map[string]any); ok {
			assert.Contains(t, eventMap, "type")
			assert.Contains(t, eventMap, "source")
			assert.Contains(t, eventMap, "id")
			assert.Contains(t, eventMap, "time")
		}
	}
}
