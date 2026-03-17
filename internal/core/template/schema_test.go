package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestExtractVariableSchema_BasicTypes(t *testing.T) {
	source := []byte(`
variables: {
	name:   string
	count:  int
	debug:  bool
	ratio:  float
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 4)

	assert.Equal(t, "string", schema["name"].Type)
	assert.True(t, schema["name"].Required)

	assert.Equal(t, "int", schema["count"].Type)
	assert.True(t, schema["count"].Required)

	assert.Equal(t, "bool", schema["debug"].Type)
	assert.True(t, schema["debug"].Required)

	assert.Equal(t, "float", schema["ratio"].Type)
	assert.True(t, schema["ratio"].Required)
}

func TestExtractVariableSchema_WithDefault(t *testing.T) {
	source := []byte(`
variables: {
	env: *"dev" | string
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 1)

	assert.False(t, schema["env"].Required)
	assert.Equal(t, "dev", schema["env"].Default)
}

func TestExtractVariableSchema_Enum(t *testing.T) {
	source := []byte(`
variables: {
	environment: "dev" | "staging" | "prod"
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 1)

	assert.Equal(t, "string", schema["environment"].Type)
	assert.True(t, schema["environment"].Required)
	assert.Equal(t, []string{"dev", "staging", "prod"}, schema["environment"].Enum)
}

func TestExtractVariableSchema_NoVariables(t *testing.T) {
	source := []byte(`
resources: {
	bucket: {
		kind: "S3Bucket"
	}
}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	assert.Nil(t, schema)
}

func TestExtractVariableSchema_InvalidCUE(t *testing.T) {
	source := []byte(`this is not valid CUE {{{`)
	_, err := ExtractVariableSchema(source)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse CUE")
}

func TestExtractVariableSchema_MixedFields(t *testing.T) {
	source := []byte(`
variables: {
	name:        string
	environment: "dev" | "staging" | "prod"
	vpcId:       string
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 3)

	assert.Equal(t, "string", schema["name"].Type)
	assert.True(t, schema["name"].Required)
	assert.Nil(t, schema["name"].Enum)

	assert.Equal(t, "string", schema["environment"].Type)
	assert.Equal(t, []string{"dev", "staging", "prod"}, schema["environment"].Enum)

	assert.Equal(t, "string", schema["vpcId"].Type)
	assert.True(t, schema["vpcId"].Required)
}

func TestValidateVariables_HappyPath(t *testing.T) {
	schema := types.VariableSchema{
		"name":        {Type: "string", Required: true},
		"environment": {Type: "string", Required: true, Enum: []string{"dev", "staging", "prod"}},
	}
	vars := map[string]any{
		"name":        "orders-api",
		"environment": "prod",
	}
	assert.NoError(t, ValidateVariables(schema, vars))
}

func TestValidateVariables_MissingRequired(t *testing.T) {
	schema := types.VariableSchema{
		"name":  {Type: "string", Required: true},
		"vpcId": {Type: "string", Required: true},
	}
	vars := map[string]any{"name": "test"}
	err := ValidateVariables(schema, vars)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing required variable "vpcId"`)
}

func TestValidateVariables_WrongType(t *testing.T) {
	schema := types.VariableSchema{
		"debug": {Type: "bool", Required: true},
	}
	vars := map[string]any{"debug": "true"}
	err := ValidateVariables(schema, vars)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected bool")
}

func TestValidateVariables_InvalidEnum(t *testing.T) {
	schema := types.VariableSchema{
		"env": {Type: "string", Required: true, Enum: []string{"dev", "prod"}},
	}
	vars := map[string]any{"env": "staging"}
	err := ValidateVariables(schema, vars)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `not in allowed set`)
}

func TestValidateVariables_EmptySchema(t *testing.T) {
	assert.NoError(t, ValidateVariables(nil, map[string]any{"anything": "goes"}))
}

func TestValidateVariables_OptionalMissing(t *testing.T) {
	schema := types.VariableSchema{
		"env": {Type: "string", Required: false, Default: "dev"},
	}
	assert.NoError(t, ValidateVariables(schema, nil))
}
