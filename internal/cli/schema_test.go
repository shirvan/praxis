package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The schema commands are backed by the embedded CUE schema FS, so all of
// these tests run fully offline — the endpoint is never contacted.

// ---------------------------------------------------------------------------
// get schema <Kind>
// ---------------------------------------------------------------------------

func TestGetSchemaCmd_KnownKind(t *testing.T) {
	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"get", "schema", "SSMParameter"}, "http://unused")
	})
	require.NoError(t, execErr)
	assert.Contains(t, out, "#SSMParameter")
	assert.Contains(t, out, `kind:       "SSMParameter"`)
}

func TestGetSchemaCmd_CaseInsensitive(t *testing.T) {
	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"get", "schema", "ssmparameter"}, "http://unused")
	})
	require.NoError(t, execErr)
	assert.Contains(t, out, "#SSMParameter")
}

func TestGetSchemaCmd_UnknownKind_ListsKnownKinds(t *testing.T) {
	_, _, err := executeCmd(t, []string{"get", "schema", "NoSuchKind"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown kind "NoSuchKind"`)
	// The error must enumerate the known kinds so the user can self-correct.
	assert.Contains(t, err.Error(), "SSMParameter")
	assert.Contains(t, err.Error(), "LogGroup")
}

func TestGetSchemaCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"get", "schema"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestGetSchemaCmd_JSONOutput(t *testing.T) {
	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"get", "schema", "SSMParameter", "-o", "json"}, "http://unused")
	})
	require.NoError(t, execErr)

	var decoded map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &decoded), "get schema -o json must emit valid JSON, got: %s", out)
	assert.Equal(t, "SSMParameter", decoded["kind"])
	assert.Equal(t, "schemas/aws/ssm/parameter.cue", decoded["file"])
	assert.Contains(t, decoded["source"], "#SSMParameter")
}

// ---------------------------------------------------------------------------
// list schemas
// ---------------------------------------------------------------------------

func TestListSchemasCmd_Table(t *testing.T) {
	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"list", "schemas"}, "http://unused")
	})
	require.NoError(t, execErr)
	assert.Contains(t, out, "SSMParameter")
	assert.Contains(t, out, "LogGroup")
	assert.Contains(t, out, "schemas/aws/ssm/parameter.cue")
}

func TestListSchemasCmd_JSONOutput(t *testing.T) {
	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"list", "schemas", "-o", "json"}, "http://unused")
	})
	require.NoError(t, execErr)

	var decoded []schemaInfo
	require.NoError(t, json.Unmarshal([]byte(out), &decoded), "list schemas -o json must emit a valid JSON array, got: %s", out)
	require.NotEmpty(t, decoded)

	kinds := make(map[string]string, len(decoded))
	for _, info := range decoded {
		kinds[info.Kind] = info.File
	}
	assert.Equal(t, "schemas/aws/ssm/parameter.cue", kinds["SSMParameter"])
	assert.Equal(t, "schemas/aws/cloudwatch/log_group.cue", kinds["LogGroup"])
}
