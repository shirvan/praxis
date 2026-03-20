package jsonpath

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSet_ObjectPath(t *testing.T) {
	root := map[string]any{
		"spec": map[string]any{
			"name": "old",
		},
	}
	updated, err := Set(root, "spec.name", "new")
	require.NoError(t, err)
	assert.Equal(t, "new", updated.(map[string]any)["spec"].(map[string]any)["name"])
}

func TestSet_ArrayIndex(t *testing.T) {
	root := map[string]any{
		"items": []any{"a", "b", "c"},
	}
	updated, err := Set(root, "items.1", "B")
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "B", "c"}, updated.(map[string]any)["items"])
}

func TestSet_NestedArrayInObject(t *testing.T) {
	root := map[string]any{
		"spec": map[string]any{
			"tags": []any{
				map[string]any{"key": "env", "value": "dev"},
			},
		},
	}
	updated, err := Set(root, "spec.tags.0.value", "prod")
	require.NoError(t, err)
	tags := updated.(map[string]any)["spec"].(map[string]any)["tags"].([]any)
	assert.Equal(t, "prod", tags[0].(map[string]any)["value"])
}

func TestSet_EmptyPath_ReplacesRoot(t *testing.T) {
	updated, err := Set("old", "", "new")
	require.NoError(t, err)
	assert.Equal(t, "new", updated)
}

func TestSet_MissingSegment_ReturnsError(t *testing.T) {
	root := map[string]any{"a": "b"}
	_, err := Set(root, "x.y", "v")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSet_IndexOutOfRange_ReturnsError(t *testing.T) {
	root := map[string]any{"items": []any{"a"}}
	_, err := Set(root, "items.5", "v")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestSet_NonContainerSegment_ReturnsError(t *testing.T) {
	root := map[string]any{"val": "string"}
	_, err := Set(root, "val.nested", "v")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected object or array")
}
