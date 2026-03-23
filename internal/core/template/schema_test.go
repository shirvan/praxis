package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
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

func TestExtractVariableSchema_ListOfStrings(t *testing.T) {
	source := []byte(`
variables: {
	buckets: [...string]
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 1)

	assert.Equal(t, "list", schema["buckets"].Type)
	assert.Equal(t, "string", schema["buckets"].Items)
	// CUE treats [...string] as having a default of []
	assert.False(t, schema["buckets"].Required)
}

func TestExtractVariableSchema_ListOfStructs(t *testing.T) {
	source := []byte(`
variables: {
	subnets: [...{suffix: string, cidr: string}]
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 1)

	assert.Equal(t, "list", schema["subnets"].Type)
	assert.Equal(t, "struct", schema["subnets"].Items)
	// CUE treats [...{...}] as having a default of []
	assert.False(t, schema["subnets"].Required)
}

func TestExtractVariableSchema_ListOfInts(t *testing.T) {
	source := []byte(`
variables: {
	ports: [...int]
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 1)

	assert.Equal(t, "list", schema["ports"].Type)
	assert.Equal(t, "int", schema["ports"].Items)
}

func TestExtractVariableSchema_ListWithDefault(t *testing.T) {
	source := []byte(`
variables: {
	tags: [...string] | *["default"]
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 1)

	assert.Equal(t, "list", schema["tags"].Type)
	assert.False(t, schema["tags"].Required)
	assert.Equal(t, []any{"default"}, schema["tags"].Default)
}

func TestExtractVariableSchema_StructVariable(t *testing.T) {
	source := []byte(`
variables: {
	config: {
		retries: int
		timeout: string
	}
}
resources: {}
`)
	schema, err := ExtractVariableSchema(source)
	require.NoError(t, err)
	require.Len(t, schema, 1)

	assert.Equal(t, "struct", schema["config"].Type)
	assert.True(t, schema["config"].Required)
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

func TestValidateVariables_ListVariable(t *testing.T) {
	schema := types.VariableSchema{
		"buckets": {Type: "list", Required: true, Items: "string"},
	}
	vars := map[string]any{
		"buckets": []any{"orders", "payments"},
	}
	assert.NoError(t, ValidateVariables(schema, vars))
}

func TestValidateVariables_ListWrongType(t *testing.T) {
	schema := types.VariableSchema{
		"buckets": {Type: "list", Required: true, Items: "string"},
	}
	vars := map[string]any{
		"buckets": "not-a-list",
	}
	err := ValidateVariables(schema, vars)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected list")
}

func TestValidateVariables_ListWrongElementType(t *testing.T) {
	schema := types.VariableSchema{
		"buckets": {Type: "list", Required: true, Items: "string"},
	}
	vars := map[string]any{
		"buckets": []any{"valid", 42},
	}
	err := ValidateVariables(schema, vars)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected string element")
}

func TestValidateVariables_StructVariable(t *testing.T) {
	schema := types.VariableSchema{
		"config": {Type: "struct", Required: true},
	}
	vars := map[string]any{
		"config": map[string]any{"retries": float64(3)},
	}
	assert.NoError(t, ValidateVariables(schema, vars))
}

func TestValidateVariables_StructWrongType(t *testing.T) {
	schema := types.VariableSchema{
		"config": {Type: "struct", Required: true},
	}
	vars := map[string]any{
		"config": "not-a-struct",
	}
	err := ValidateVariables(schema, vars)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct")
}

func TestValidateVariables_ListOfStructs(t *testing.T) {
	schema := types.VariableSchema{
		"subnets": {Type: "list", Required: true, Items: "struct"},
	}
	vars := map[string]any{
		"subnets": []any{
			map[string]any{"cidr": "10.0.1.0/24"},
			map[string]any{"cidr": "10.0.2.0/24"},
		},
	}
	assert.NoError(t, ValidateVariables(schema, vars))
}

func TestValidateVariables_ListOfStructsWrongElement(t *testing.T) {
	schema := types.VariableSchema{
		"subnets": {Type: "list", Required: true, Items: "struct"},
	}
	vars := map[string]any{
		"subnets": []any{
			map[string]any{"cidr": "10.0.1.0/24"},
			"not-a-struct",
		},
	}
	err := ValidateVariables(schema, vars)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct element")
}
