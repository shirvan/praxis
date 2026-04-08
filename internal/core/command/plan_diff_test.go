package command

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

func TestAnnotateExpressionFields_ReplacesMatchingPaths(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "spec.vpcId", OldValue: "vpc-123", NewValue: "vpc-123"},
		{Path: "spec.region", OldValue: "us-east-1", NewValue: "us-east-1"},
	}
	exprs := map[string]string{
		"spec.vpcId": "resources.vpc.outputs.vpcId",
	}

	result := annotateExpressionFields(fields, exprs)
	assert.Equal(t, "${resources.vpc.outputs.vpcId}", result[0].NewValue)
	assert.Equal(t, "us-east-1", result[1].NewValue) // non-expression unchanged
}

func TestAnnotateExpressionFields_HandlesNestedPaths(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "spec.tags.vpcId", OldValue: "vpc-old", NewValue: "vpc-new"},
	}
	exprs := map[string]string{
		"spec.tags": "resources.vpc.outputs.tags",
	}

	result := annotateExpressionFields(fields, exprs)
	assert.Equal(t, "${resources.vpc.outputs.tags}", result[0].NewValue)
}

func TestAnnotateExpressionFields_NoExpressionsReturnsUnchanged(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "spec.region", OldValue: "us-east-1", NewValue: "us-west-2"},
	}

	result := annotateExpressionFields(fields, nil)
	assert.Equal(t, "us-west-2", result[0].NewValue)
}

func TestAnnotateExpressionFields_NoMatchingPathsReturnsUnchanged(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "spec.region", OldValue: "us-east-1", NewValue: "us-west-2"},
	}
	exprs := map[string]string{
		"spec.vpcId": "resources.vpc.outputs.vpcId",
	}

	result := annotateExpressionFields(fields, exprs)
	assert.Equal(t, "us-west-2", result[0].NewValue)
}

func TestAnnotateExpressionFields_MultipleExpressions(t *testing.T) {
	fields := []types.FieldDiff{
		{Path: "spec.vpcId", OldValue: "vpc-123", NewValue: "vpc-123"},
		{Path: "spec.subnetId", OldValue: "subnet-abc", NewValue: "subnet-abc"},
		{Path: "spec.region", OldValue: "us-east-1", NewValue: "us-east-1"},
	}
	exprs := map[string]string{
		"spec.vpcId":    "resources.vpc.outputs.vpcId",
		"spec.subnetId": "resources.subnet.outputs.subnetId",
	}

	result := annotateExpressionFields(fields, exprs)
	assert.Equal(t, "${resources.vpc.outputs.vpcId}", result[0].NewValue)
	assert.Equal(t, "${resources.subnet.outputs.subnetId}", result[1].NewValue)
	assert.Equal(t, "us-east-1", result[2].NewValue)
}

func TestReferencedResourceNames(t *testing.T) {
	resources := []orchestrator.PlanResource{
		{
			Name: "publicSubnetA",
			Expressions: map[string]string{
				"spec.vpcId": "resources.vpc.outputs.vpcId",
			},
		},
		{
			Name: "appRT",
			Expressions: map[string]string{
				"spec.vpcId":                   "resources.vpc.outputs.vpcId",
				"spec.associations.0.subnetId": "resources.publicSubnetA.outputs.subnetId",
			},
		},
		{
			Name: "vpc",
			// no expressions
		},
	}

	refs := referencedResourceNames(resources)
	assert.True(t, refs["vpc"], "vpc should be referenced")
	assert.True(t, refs["publicSubnetA"], "publicSubnetA should be referenced")
	assert.False(t, refs["appRT"], "appRT should not be referenced (no downstream)")
}
