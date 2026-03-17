package dag

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDependencies_NoCELExpressions(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"name":"assets","count":1}}`)

	deps, exprs, err := ParseDependencies("bucket", spec)
	require.NoError(t, err)
	assert.Empty(t, deps)
	assert.Empty(t, exprs)
}

func TestParseDependencies_SingleDependency(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"groupId":"${cel:resources.sg.outputs.groupId}"}}`)

	deps, exprs, err := ParseDependencies("bucket", spec)
	require.NoError(t, err)
	assert.Equal(t, []string{"sg"}, deps)
	assert.Equal(t, map[string]string{
		"spec.groupId": "resources.sg.outputs.groupId",
	}, exprs)
}

func TestParseDependencies_MultipleReferencesToSameResource_Deduplicated(t *testing.T) {
	spec := json.RawMessage(`{
		"spec": {
			"sourceGroup": "${cel:resources.sg.outputs.groupId}",
			"ruleIds": "${cel:resources.sg.outputs.ruleIds}"
		}
	}`)

	deps, exprs, err := ParseDependencies("bucket", spec)
	require.NoError(t, err)
	assert.Equal(t, []string{"sg"}, deps)
	assert.Equal(t, map[string]string{
		"spec.ruleIds":     "resources.sg.outputs.ruleIds",
		"spec.sourceGroup": "resources.sg.outputs.groupId",
	}, exprs)
}

func TestParseDependencies_MultipleResources_Sorted(t *testing.T) {
	spec := json.RawMessage(`{
		"spec": {
			"groupId": "${cel:resources.sg.outputs.groupId}",
			"subnetId": "${cel:resources.vpc.outputs.subnetId}"
		}
	}`)

	deps, _, err := ParseDependencies("app", spec)
	require.NoError(t, err)
	assert.Equal(t, []string{"sg", "vpc"}, deps)
}

func TestParseDependencies_NestedExpressionAcrossResources(t *testing.T) {
	spec := json.RawMessage(`{
		"spec": {
			"pair": "${cel:[resources.sg.outputs.groupId, resources.vpc.outputs.vpcId]}"
		}
	}`)

	deps, exprs, err := ParseDependencies("app", spec)
	require.NoError(t, err)
	assert.Equal(t, []string{"sg", "vpc"}, deps)
	assert.Equal(t, map[string]string{
		"spec.pair": "[resources.sg.outputs.groupId, resources.vpc.outputs.vpcId]",
	}, exprs)
}

func TestParseDependencies_VariablesOnlyExpression_Ignored(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"region":"${cel:variables.region}"}}`)

	deps, exprs, err := ParseDependencies("bucket", spec)
	require.NoError(t, err)
	assert.Empty(t, deps)
	assert.Empty(t, exprs)
}

func TestParseDependencies_SelfReference_ReturnsError(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"groupId":"${cel:resources.sg.outputs.groupId}"}}`)

	deps, exprs, err := ParseDependencies("sg", spec)
	require.Error(t, err)
	assert.Nil(t, deps)
	assert.Nil(t, exprs)
	assert.Contains(t, err.Error(), "references its own outputs")
}

func TestParseDependencies_ArrayPathsIncludeIndexes(t *testing.T) {
	spec := json.RawMessage(`{
		"spec": {
			"securityGroupIds": [
				"${cel:resources.sg.outputs.primaryId}",
				"literal",
				"${cel:resources.other.outputs.secondaryId}"
			]
		}
	}`)

	deps, exprs, err := ParseDependencies("app", spec)
	require.NoError(t, err)
	assert.Equal(t, []string{"other", "sg"}, deps)
	assert.Equal(t, map[string]string{
		"spec.securityGroupIds.0": "resources.sg.outputs.primaryId",
		"spec.securityGroupIds.2": "resources.other.outputs.secondaryId",
	}, exprs)
}

func TestParseDependencies_MixedInterpolation_ReturnsError(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"name":"sg-${cel:resources.sg.outputs.groupId}"}}`)

	deps, exprs, err := ParseDependencies("bucket", spec)
	require.Error(t, err)
	assert.Nil(t, deps)
	assert.Nil(t, exprs)
	assert.Contains(t, err.Error(), "must occupy the full JSON value")
}
