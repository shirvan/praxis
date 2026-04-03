package provider

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// provisionHandle is the generic implementation of ProvisionInvocation.
// Type parameter O is the concrete driver output type (e.g. s3types.BucketOutput).
// It wraps a Restate ResponseFuture and a normalize function that converts the
// typed driver output into the generic map[string]any stored in deployment state.
type provisionHandle[O any] struct {
	id        string
	raw       restate.ResponseFuture[O]
	normalize func(any) (map[string]any, error)
}

func (h *provisionHandle[O]) ID() string {
	return h.id
}

func (h *provisionHandle[O]) Future() restate.Future {
	return h.raw
}

func (h *provisionHandle[O]) Outputs() (map[string]any, error) {
	output, err := h.raw.Response()
	if err != nil {
		return nil, err
	}
	return h.normalize(output)
}

// deleteHandle is the implementation of DeleteInvocation.
// It wraps a Restate ResponseFuture[restate.Void] because delete operations
// return no payload — only success or error.
type deleteHandle struct {
	id  string
	raw restate.ResponseFuture[restate.Void]
}

func (h *deleteHandle) ID() string {
	return h.id
}

func (h *deleteHandle) Future() restate.Future {
	return h.raw
}

func (h *deleteHandle) Done() error {
	_, err := h.raw.Response()
	return err
}

// castSpec safely casts a generic spec (any) to the concrete driver input type T.
// This is called by each adapter's DecodeSpec to convert the any produced by
// JSON unmarshalling into the specific Go struct the driver expects.
func castSpec[T any](spec any) (T, error) {
	return castOutput[T](spec)
}

// castOutput safely casts a generic value to type T, handling both value and
// pointer forms. This flexibility is needed because Restate's JSON codec may
// deserialize the same type as either T or *T depending on the call context.
func castOutput[T any](value any) (T, error) {
	var zero T
	if typed, ok := value.(T); ok {
		return typed, nil
	}
	ptr, ok := value.(*T)
	if ok && ptr != nil {
		return *ptr, nil
	}
	return zero, fmt.Errorf("expected %T but got %T", zero, value)
}

// createFieldDiffsFromSpec generates a flat list of FieldDiff entries from a
// spec struct. This is used for "create" operations where there is no existing
// state to diff against — every field is reported as a new value. The spec is
// round-tripped through JSON to get a generic map, then recursively flattened.
func createFieldDiffsFromSpec(spec any) ([]types.FieldDiff, error) {
	if spec == nil {
		return nil, nil
	}

	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal spec for create diff: %w", err)
	}

	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decode spec for create diff: %w", err)
	}

	var diffs []types.FieldDiff
	flattenFieldDiffs("spec", decoded, &diffs)
	return diffs, nil
}

// flattenFieldDiffs recursively walks a generic JSON value (maps, slices,
// scalars) and appends a FieldDiff for each leaf node. Map keys are sorted
// for deterministic output. The resulting paths use dot notation to mirror
// the resource spec structure (e.g. "spec.tags.Environment").
func flattenFieldDiffs(path string, value any, diffs *[]types.FieldDiff) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			*diffs = append(*diffs, types.FieldDiff{Path: path, NewValue: map[string]any{}})
			return
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			flattenFieldDiffs(path+"."+key, typed[key], diffs)
		}
	case []any:
		if len(typed) == 0 {
			*diffs = append(*diffs, types.FieldDiff{Path: path, NewValue: []any{}})
			return
		}
		for index, item := range typed {
			flattenFieldDiffs(path+"."+strconv.Itoa(index), item, diffs)
		}
	default:
		*diffs = append(*diffs, types.FieldDiff{Path: path, NewValue: typed})
	}
}
