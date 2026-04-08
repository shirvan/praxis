package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrintTablePlainHasNoANSI(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})

	printTable(renderer, []string{"KEY", "STATUS"}, [][]string{{"demo", "Ready"}})

	text := out.String()
	assert.NotContains(t, text, "\x1b[")
	assert.Contains(t, text, "KEY")
	assert.Contains(t, text, "demo")
	assert.Contains(t, text, "Ready")
}

func TestPrintPlanPlainHasNoANSI(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})

	plan := &types.PlanResult{
		Resources: []types.ResourceDiff{
			{
				ResourceKey:  "my-bucket",
				ResourceType: "S3Bucket",
				Operation:    types.OpCreate,
				FieldDiffs: []types.FieldDiff{{
					Path:     "bucket_name",
					NewValue: "my-bucket",
				}},
			},
		},
		Summary: types.PlanSummary{ToCreate: 1},
	}

	printPlan(renderer, plan)

	text := out.String()
	require.NotEmpty(t, text)
	assert.NotContains(t, text, "\x1b[")
	assert.Contains(t, text, `# S3Bucket "my-bucket" will be created`)
	assert.Contains(t, text, `+ resource "S3Bucket" "my-bucket"`)
	assert.Contains(t, text, `+ bucket_name = "my-bucket"`)
	assert.Contains(t, text, "Plan: 1 to create, 0 to update, 0 to delete, 0 unchanged.")
}

func TestPrintPlanFieldDeletion(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})

	plan := &types.PlanResult{
		Resources: []types.ResourceDiff{
			{
				ResourceKey:  "us-east-1~my-listener",
				ResourceType: "Listener",
				Operation:    types.OpUpdate,
				FieldDiffs: []types.FieldDiff{
					{Path: "port", OldValue: float64(443), NewValue: float64(8443)},
					{Path: "sslPolicy", OldValue: "ELBSecurityPolicy-2016-08", NewValue: ""},
					{Path: "securityGroups", OldValue: []any{"sg-abc123"}, NewValue: []any{}},
				},
			},
		},
		Summary: types.PlanSummary{ToUpdate: 1},
	}

	printPlan(renderer, plan)

	text := out.String()
	// Normal update field should show ~ with old => new.
	assert.Contains(t, text, `~ port`)
	assert.Contains(t, text, "443 => 8443")
	// Field changing to empty should show as deletion with -.
	assert.Contains(t, text, `- sslPolicy`)
	assert.Contains(t, text, `"ELBSecurityPolicy-2016-08"`)
	assert.NotContains(t, text, `=> ""`)
	// Empty slice should also show as deletion.
	assert.Contains(t, text, `- securityGroups`)
	assert.Contains(t, text, `[sg-abc123]`)
	assert.NotContains(t, text, `=> []`)
}

func TestPrintDestroyPlan_ReverseOrder(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})

	detail := &types.DeploymentDetail{
		Key:    "my-stack",
		Status: types.DeploymentComplete,
		Resources: []types.DeploymentResource{
			{Name: "vpc", Kind: "VPC", Key: "us-east-1~my-vpc", Status: types.DeploymentResourceReady},
			{Name: "subnet", Kind: "Subnet", Key: "us-east-1~my-subnet", Status: types.DeploymentResourceReady, DependsOn: []string{"vpc"}},
			{Name: "instance", Kind: "EC2Instance", Key: "us-east-1~web", Status: types.DeploymentResourceReady, DependsOn: []string{"subnet"}},
		},
	}

	printDestroyPlan(renderer, detail)

	text := out.String()
	require.NotEmpty(t, text)
	assert.NotContains(t, text, "\x1b[")

	// Should show reverse topo order: instance → subnet → vpc.
	assert.Contains(t, text, `- EC2Instance "us-east-1~web" will be destroyed`)
	assert.Contains(t, text, `- Subnet "us-east-1~my-subnet" will be destroyed`)
	assert.Contains(t, text, `- VPC "us-east-1~my-vpc" will be destroyed`)
	assert.Contains(t, text, "Plan: 3 to destroy.")

	// Verify ordering: instance before subnet before vpc.
	iInstance := bytes.Index([]byte(text), []byte("EC2Instance"))
	iSubnet := bytes.Index([]byte(text), []byte("Subnet"))
	iVPC := bytes.Index([]byte(text), []byte(`VPC "us-east-1~my-vpc"`))
	assert.Less(t, iInstance, iSubnet, "instance should appear before subnet")
	assert.Less(t, iSubnet, iVPC, "subnet should appear before vpc")
}

func TestPrintDestroyPlan_SkipsAlreadyDeleted(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})

	detail := &types.DeploymentDetail{
		Key:    "my-stack",
		Status: types.DeploymentFailed,
		Resources: []types.DeploymentResource{
			{Name: "bucket", Kind: "S3Bucket", Key: "my-bucket", Status: types.DeploymentResourceReady},
			{Name: "policy", Kind: "S3BucketPolicy", Key: "my-policy", Status: types.DeploymentResourceDeleted, DependsOn: []string{"bucket"}},
			{Name: "ghost", Kind: "IAMRole", Key: "ghost-role", Status: types.DeploymentResourcePending},
		},
	}

	printDestroyPlan(renderer, detail)

	text := out.String()
	assert.Contains(t, text, `- S3Bucket "my-bucket" will be destroyed`)
	assert.Contains(t, text, `will be skipped (Deleted)`)
	assert.Contains(t, text, `will be skipped (Pending)`)
	assert.Contains(t, text, "Plan: 1 to destroy, 2 to skip.")
}

func TestPrintPlanImmutableReplacementHint(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})

	plan := &types.PlanResult{
		Resources: []types.ResourceDiff{
			{
				ResourceKey:  "vpc-123~my-subnet",
				ResourceType: "Subnet",
				Operation:    types.OpUpdate,
				FieldDiffs: []types.FieldDiff{
					{Path: "availabilityZone (immutable, requires replacement)", OldValue: "us-east-1c", NewValue: "us-east-1a"},
				},
			},
			{
				ResourceKey:  "my-bucket",
				ResourceType: "S3Bucket",
				Operation:    types.OpUpdate,
				FieldDiffs: []types.FieldDiff{
					{Path: "tags.env", OldValue: "dev", NewValue: "prod"},
				},
			},
		},
		Summary: types.PlanSummary{ToUpdate: 2},
	}

	printPlan(renderer, plan)

	text := out.String()
	assert.Contains(t, text, "immutable field changes that require replacement")
	assert.Contains(t, text, "--replace vpc-123~my-subnet")
	assert.Contains(t, text, "--allow-replace")
	// Should not suggest replace for the mutable-only resource.
	assert.NotContains(t, text, "--replace my-bucket")
}

func TestPrintPlanNoHintWhenNoImmutableChanges(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})

	plan := &types.PlanResult{
		Resources: []types.ResourceDiff{
			{
				ResourceKey:  "my-bucket",
				ResourceType: "S3Bucket",
				Operation:    types.OpUpdate,
				FieldDiffs: []types.FieldDiff{
					{Path: "tags.env", OldValue: "dev", NewValue: "prod"},
				},
			},
		},
		Summary: types.PlanSummary{ToUpdate: 1},
	}

	printPlan(renderer, plan)

	text := out.String()
	assert.NotContains(t, text, "immutable")
	assert.NotContains(t, text, "--replace")
	assert.NotContains(t, text, "--allow-replace")
}

func TestPrintDestroyPlan_EmptyDeployment(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})

	printDestroyPlan(renderer, &types.DeploymentDetail{Key: "empty"})

	text := out.String()
	assert.Contains(t, text, "No resources to destroy.")
}

func TestPrintDeploymentDetail_Conditions(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})
	now := time.Date(2024, time.January, 15, 10, 30, 22, 0, time.UTC)

	detail := &types.DeploymentDetail{
		Key:       "my-stack",
		Status:    types.DeploymentComplete,
		CreatedAt: now,
		UpdatedAt: now,
		Resources: []types.DeploymentResource{
			{
				Name:   "bucket",
				Kind:   "S3Bucket",
				Key:    "my-bucket",
				Status: types.DeploymentResourceReady,
				Conditions: []types.Condition{
					{Type: types.ConditionReady, Status: types.ConditionTrue, Reason: types.ReasonSucceeded, Message: "resource ready", LastTransitionTime: now},
					{Type: types.ConditionDriftFree, Status: types.ConditionFalse, Reason: types.ReasonDriftDetected, Message: "spec mismatch detected", LastTransitionTime: now},
				},
			},
		},
	}

	printDeploymentDetail(renderer, detail, deploymentSections{})

	text := out.String()
	assert.Contains(t, text, "CONDITIONS")
	assert.Contains(t, text, "Ready=True(Succeeded), DriftFree=False(DriftDetected)")
	assert.Contains(t, text, "Conditions:")
	assert.Contains(t, text, "bucket (S3Bucket):")
	assert.Contains(t, text, "Ready:")
	assert.Contains(t, text, "True (Succeeded) - resource ready")
	assert.Contains(t, text, "DriftFree:")
	assert.Contains(t, text, "False (DriftDetected) - spec mismatch detected")
	assert.Contains(t, text, "Last transition: 2024-01-15T10:30:22Z")
}
