package cuevalidate

// Unit tests for DecodeFile, the CUE-based validation entrypoint used by the
// event bus (internal/core/orchestrator) and WorkspaceService to validate
// payloads against schemas/*.cue definitions.
//
// Most cases use small inline CUE sources written to t.TempDir(); one test
// validates against the real schemas/events directory to catch drift between
// these tests and the shipped schema files.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSchema is a small inline schema exercising required fields, optional
// constrained fields, defaults, open structs ("..."), and closed definitions.
const testSchema = `
package testschema

#Event: {
	message:      string
	resourceName: string
	count?:       int & >=0
	mode:         string | *"observed"
	...
}

#Closed: {
	name: string
}
`

// writeSchemaDir writes the given CUE source as <dir>/event.cue and returns dir.
func writeSchemaDir(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "event.cue"), []byte(source), 0o600))
	return dir
}

func TestDecodeFile_Validation(t *testing.T) {
	tests := []struct {
		name       string
		definition string
		input      any
		wantErr    []string // substrings the error must contain; empty means success
	}{
		{
			name:       "valid payload passes",
			definition: "#Event",
			input:      map[string]any{"message": "drift", "resourceName": "webBucket", "mode": "managed"},
		},
		{
			name:       "missing required field fails with field name in message",
			definition: "#Event",
			input:      map[string]any{"resourceName": "webBucket"},
			wantErr:    []string{"#Event", "message", "incomplete value string"},
		},
		{
			name:       "wrong type fails with conflict message",
			definition: "#Event",
			input:      map[string]any{"message": 42, "resourceName": "webBucket"},
			wantErr:    []string{"message", "conflicting values"},
		},
		{
			name:       "constraint violation fails",
			definition: "#Event",
			input:      map[string]any{"message": "m", "resourceName": "r", "count": -1},
			wantErr:    []string{"count", "invalid value -1"},
		},
		{
			name:       "open struct allows extra fields",
			definition: "#Event",
			input:      map[string]any{"message": "m", "resourceName": "r", "extra": "allowed"},
		},
		{
			name:       "closed definition rejects extra fields",
			definition: "#Closed",
			input:      map[string]any{"name": "a", "extra": true},
			wantErr:    []string{"extra", "not allowed"},
		},
		{
			name:       "unknown definition name errors",
			definition: "#Nope",
			input:      map[string]any{},
			wantErr:    []string{"does not define #Nope"},
		},
	}

	dir := writeSchemaDir(t, testSchema)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := DecodeFile(dir, "event.cue", tc.definition, tc.input, nil)
			if len(tc.wantErr) == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tc.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestDecodeFile_DecodesDefaultsIntoOut(t *testing.T) {
	dir := writeSchemaDir(t, testSchema)

	var out struct {
		Message      string `json:"message"`
		ResourceName string `json:"resourceName"`
		Mode         string `json:"mode"`
	}
	err := DecodeFile(dir, "event.cue", "#Event",
		map[string]any{"message": "m", "resourceName": "r"}, &out)
	require.NoError(t, err)

	assert.Equal(t, "m", out.Message)
	assert.Equal(t, "r", out.ResourceName)
	assert.Equal(t, "observed", out.Mode, "CUE default must be merged into the decoded value")
}

func TestDecodeFile_InputErrors(t *testing.T) {
	dir := writeSchemaDir(t, testSchema)

	t.Run("empty schema dir", func(t *testing.T) {
		err := DecodeFile("  ", "event.cue", "#Event", map[string]any{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "schema directory is required")
	})

	t.Run("missing schema file", func(t *testing.T) {
		err := DecodeFile(dir, "missing.cue", "#Event", map[string]any{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `read schema "missing.cue"`)
	})

	t.Run("schema that does not compile", func(t *testing.T) {
		bad := writeSchemaDir(t, "#Event: {\n")
		err := DecodeFile(bad, "event.cue", "#Event", map[string]any{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `compile schema "event.cue"`)
	})

	t.Run("unmarshalable input", func(t *testing.T) {
		err := DecodeFile(dir, "event.cue", "#Event", make(chan int), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "marshal validation input")
	})

	t.Run("decode into incompatible out type", func(t *testing.T) {
		var out struct {
			Message int `json:"message"`
		}
		err := DecodeFile(dir, "event.cue", "#Event",
			map[string]any{"message": "m", "resourceName": "r"}, &out)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode validated value")
	})
}

// TestDecodeFile_RealEventSchemas validates against the repository's actual
// schemas/events directory, mirroring how the orchestrator event bus calls
// DecodeFile. This catches drift between the inline test schemas above and
// the shipped schema files.
func TestDecodeFile_RealEventSchemas(t *testing.T) {
	schemaDir := filepath.Join("..", "..", "..", "schemas")
	require.DirExists(t, filepath.Join(schemaDir, "events"))

	t.Run("valid drift payload passes", func(t *testing.T) {
		err := DecodeFile(schemaDir, filepath.Join("events", "drift.cue"), "#DriftDetectedData",
			map[string]any{
				"message":      "drift detected on webBucket",
				"resourceName": "webBucket",
				"resourceKind": "S3Bucket",
			}, nil)
		assert.NoError(t, err)
	})

	t.Run("missing resourceKind fails", func(t *testing.T) {
		err := DecodeFile(schemaDir, filepath.Join("events", "drift.cue"), "#DriftDetectedData",
			map[string]any{
				"message":      "drift detected on webBucket",
				"resourceName": "webBucket",
			}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resourceKind")
	})
}
