package command

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

func TestWriteAndReadSavedPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	saved := types.SavedPlan{
		Version: SavedPlanVersion,
		Plan: types.ExecutionPlan{
			DeploymentKey: "demo",
			Account:       "dev",
			Targets:       []string{"bucket"},
			Resources: []types.ExecutionPlanResource{{
				Name: "bucket",
				Kind: "S3Bucket",
				Key:  "my-bucket",
				Spec: json.RawMessage(`{"kind":"S3Bucket","spec":{"region":"us-east-1"}}`),
			}},
		},
		ContentHash: "abc123",
		CreatedAt:   time.Date(2024, time.January, 15, 10, 30, 22, 0, time.UTC),
	}

	require.NoError(t, WriteSavedPlanFile(path, saved))
	loaded, err := ReadSavedPlanFile(path)
	require.NoError(t, err)
	assert.Equal(t, saved.Version, loaded.Version)
	assert.Equal(t, saved.Plan.DeploymentKey, loaded.Plan.DeploymentKey)
	assert.Equal(t, saved.Plan.Targets, loaded.Plan.Targets)
	assert.JSONEq(t, string(saved.Plan.Resources[0].Spec), string(loaded.Plan.Resources[0].Spec))
}

func TestVerifySavedPlan_WithSignature(t *testing.T) {
	plan := types.ExecutionPlan{
		DeploymentKey: "demo",
		Resources: []types.ExecutionPlanResource{{
			Name: "bucket",
			Kind: "S3Bucket",
			Key:  "my-bucket",
			Spec: json.RawMessage(`{"kind":"S3Bucket"}`),
		}},
	}
	hash, err := ComputeSavedPlanHash(plan)
	require.NoError(t, err)

	saved := types.SavedPlan{
		Version:     SavedPlanVersion,
		Plan:        plan,
		ContentHash: hash,
		Signature:   SignSavedPlanHash(hash, []byte("secret")),
	}

	require.NoError(t, VerifySavedPlan(saved, []byte("secret")))
	require.Error(t, VerifySavedPlan(saved, []byte("other-secret")))
}

func TestMissingDeploymentResources_ReverseTopoOrder(t *testing.T) {
	state := &orchestrator.DeploymentState{
		Resources: map[string]*orchestrator.ResourceState{
			"vpc":      {Name: "vpc", Kind: "VPC", Key: "vpc", Status: types.DeploymentResourceReady},
			"subnet":   {Name: "subnet", Kind: "Subnet", Key: "subnet", DependsOn: []string{"vpc"}, Status: types.DeploymentResourceReady},
			"instance": {Name: "instance", Kind: "EC2Instance", Key: "instance", DependsOn: []string{"subnet"}, Status: types.DeploymentResourceReady},
		},
	}
	desired := []orchestrator.PlanResource{{Name: "vpc", Kind: "VPC", Key: "vpc"}}

	missing := missingDeploymentResources(state, desired)
	require.Len(t, missing, 2)
	assert.Equal(t, "instance", missing[0].Name)
	assert.Equal(t, "subnet", missing[1].Name)
}

func TestMissingDeletionsForPlan_SkipsDetachedStatusesAndOrphanPolicy(t *testing.T) {
	state := &orchestrator.DeploymentState{
		Resources: map[string]*orchestrator.ResourceState{
			"pending": {Name: "pending", Kind: "S3Bucket", Key: "pending", Status: types.DeploymentResourcePending},
			"orphan": {
				Name:   "orphan",
				Kind:   "S3Bucket",
				Key:    "orphan",
				Status: types.DeploymentResourceReady,
				Lifecycle: &types.LifecyclePolicy{
					DeletionPolicy: types.DeletionPolicyOrphan,
				},
			},
			"delete-me": {Name: "delete-me", Kind: "S3Bucket", Key: "delete-me", Status: types.DeploymentResourceReady},
		},
	}

	missing := missingDeletionsForPlan(state, nil)
	require.Len(t, missing, 1)
	assert.Equal(t, "delete-me", missing[0].Name)
}
