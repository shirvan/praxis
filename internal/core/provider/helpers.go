package provider

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

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

func castSpec[T any](spec any) (T, error) {
	return castOutput[T](spec)
}

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

func unsupportedLookup(kind string) error {
	return restate.TerminalError(fmt.Errorf("data source lookup is not supported for %q", kind), 501)
}
