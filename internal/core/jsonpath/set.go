// Package jsonpath provides shared utilities for walking and mutating
// JSON-decoded values (map[string]any / []any trees) using dot-separated paths.
package jsonpath

import (
	"fmt"
	"strings"
)

// Set writes value at the given dot-separated path inside a JSON-decoded tree.
// Numeric path segments address array elements. The updated root is returned.
func Set(root any, path string, value any) (any, error) {
	if path == "" {
		return value, nil
	}
	return setRecursive(root, strings.Split(path, "."), value)
}

func setRecursive(current any, segments []string, value any) (any, error) {
	if len(segments) == 0 {
		return value, nil
	}

	segment := segments[0]
	switch typed := current.(type) {
	case map[string]any:
		next, ok := typed[segment]
		if !ok {
			return nil, fmt.Errorf("path segment %q not found", segment)
		}
		updated, err := setRecursive(next, segments[1:], value)
		if err != nil {
			return nil, err
		}
		typed[segment] = updated
		return typed, nil
	case []any:
		index, ok := parseIndex(segment)
		if !ok {
			return nil, fmt.Errorf("path segment %q: expected numeric index for array", segment)
		}
		if index < 0 || index >= len(typed) {
			return nil, fmt.Errorf("array index %d out of range", index)
		}
		updated, err := setRecursive(typed[index], segments[1:], value)
		if err != nil {
			return nil, err
		}
		typed[index] = updated
		return typed, nil
	default:
		return nil, fmt.Errorf("path segment %q: expected object or array, got %T", segment, current)
	}
}

func parseIndex(segment string) (int, bool) {
	if segment == "" {
		return 0, false
	}
	v := 0
	for _, r := range segment {
		if r < '0' || r > '9' {
			return 0, false
		}
		v = v*10 + int(r-'0')
	}
	return v, true
}
