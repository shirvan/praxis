package diff_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/diff"
	"github.com/shirvan/praxis/pkg/types"
)

func TestNewPlanResult_Empty(t *testing.T) {
	plan := diff.NewPlanResult()
	assert.Empty(t, plan.Resources)
	assert.False(t, plan.Summary.HasChanges())
}

func TestAdd_Create(t *testing.T) {
	plan := diff.NewPlanResult()
	diff.Add(plan, "S3Bucket", "my-bucket", types.OpCreate, []types.FieldDiff{
		{Path: "versioning", NewValue: true},
		{Path: "encryption.algorithm", NewValue: "AES256"},
	})
	require.Len(t, plan.Resources, 1)
	assert.Equal(t, types.OpCreate, plan.Resources[0].Operation)
	assert.Equal(t, 1, plan.Summary.ToCreate)
	assert.True(t, plan.Summary.HasChanges())
}

func TestAdd_Update(t *testing.T) {
	plan := diff.NewPlanResult()
	diff.Add(plan, "S3Bucket", "my-bucket", types.OpUpdate, []types.FieldDiff{
		{Path: "versioning", OldValue: false, NewValue: true},
	})
	require.Len(t, plan.Resources, 1)
	assert.Equal(t, 1, plan.Summary.ToUpdate)
}

func TestAdd_Delete(t *testing.T) {
	plan := diff.NewPlanResult()
	diff.Add(plan, "S3Bucket", "my-bucket", types.OpDelete, nil)
	require.Len(t, plan.Resources, 1)
	assert.Equal(t, 1, plan.Summary.ToDelete)
}

func TestAdd_NoOp(t *testing.T) {
	plan := diff.NewPlanResult()
	diff.Add(plan, "S3Bucket", "my-bucket", types.OpNoOp, nil)
	require.Len(t, plan.Resources, 1)
	assert.Equal(t, 1, plan.Summary.Unchanged)
	assert.False(t, plan.Summary.HasChanges())
}

func TestRender_NoChanges(t *testing.T) {
	plan := diff.NewPlanResult()
	output := diff.Render(plan)
	assert.Contains(t, output, "No changes")
}

func TestRender_Create(t *testing.T) {
	plan := diff.NewPlanResult()
	diff.Add(plan, "S3Bucket", "test", types.OpCreate, []types.FieldDiff{
		{Path: "versioning", NewValue: true},
		{Path: "region", NewValue: "us-east-1"},
	})
	output := diff.Render(plan)
	assert.Contains(t, output, "will be created")
	assert.Contains(t, output, "+ versioning: true")
	assert.Contains(t, output, "1 to create")
}

func TestRender_Update(t *testing.T) {
	plan := diff.NewPlanResult()
	diff.Add(plan, "S3Bucket", "test", types.OpUpdate, []types.FieldDiff{
		{Path: "versioning", OldValue: false, NewValue: true},
	})
	output := diff.Render(plan)
	assert.Contains(t, output, "will be updated in-place")
	assert.Contains(t, output, "~ versioning: false -> true")
}

func TestRender_Delete(t *testing.T) {
	plan := diff.NewPlanResult()
	diff.Add(plan, "S3Bucket", "test", types.OpDelete, []types.FieldDiff{
		{Path: "versioning", OldValue: true},
	})
	output := diff.Render(plan)
	assert.Contains(t, output, "will be destroyed")
	assert.Contains(t, output, "- versioning: true")
	assert.Contains(t, output, "1 to delete")
}

func TestRender_MixedOperations(t *testing.T) {
	plan := diff.NewPlanResult()
	diff.Add(plan, "S3Bucket", "create-me", types.OpCreate, []types.FieldDiff{
		{Path: "region", NewValue: "us-east-1"},
	})
	diff.Add(plan, "S3Bucket", "update-me", types.OpUpdate, []types.FieldDiff{
		{Path: "versioning", OldValue: false, NewValue: true},
	})
	diff.Add(plan, "S3Bucket", "keep-me", types.OpNoOp, nil)
	output := diff.Render(plan)
	assert.Contains(t, output, "1 to create")
	assert.Contains(t, output, "1 to update")
	assert.Contains(t, output, "0 to delete")
	assert.Contains(t, output, "1 unchanged")
}

func TestPlanSummary_String(t *testing.T) {
	s := types.PlanSummary{ToCreate: 2, ToUpdate: 1, ToDelete: 0, Unchanged: 5}
	result := s.String()
	assert.True(t, strings.HasPrefix(result, "Plan:"))
	assert.Contains(t, result, "2 to create")
}
