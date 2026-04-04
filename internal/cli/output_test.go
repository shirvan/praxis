package cli

import (
	"bytes"
	"testing"

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
