// Package jsonpath provides shared utilities for walking and mutating
// JSON-decoded values (map[string]any / []any trees) using dot-separated paths.
//
// This package is used by the expression hydrator in the orchestrator. After a
// resource completes and its outputs are known, the hydrator uses jsonpath.Set
// to replace dispatch-time expression placeholders with actual output values.
// For example, after a VPC is created, the hydrator sets the "vpcId" field in
// a dependent subnet's spec to the VPC's actual ID.
//
// Path syntax:
//   - Dot-separated segments: "spec.vpcId" navigates root["spec"]["vpcId"].
//   - Numeric segments address array elements: "subnets.0.cidr" navigates
//     root["subnets"][0]["cidr"].
//   - An empty path replaces the entire root value.
package jsonpath

import (
	"fmt"
	"strings"
)

// Set writes value at the given dot-separated path inside a JSON-decoded tree.
// Numeric path segments address array elements. The updated root is returned.
//
// The function performs in-place mutation of the tree structure. Maps and slices
// are modified directly (not copied), so callers should be aware that the
// original tree is modified.
//
// Edge cases:
//   - Empty path: returns value directly (replaces entire root).
//   - Missing path segment: returns an error (does not auto-create intermediate nodes).
//   - Array index out of range: returns an error.
//   - Non-numeric index on an array: returns an error.
func Set(root any, path string, value any) (any, error) {
	if path == "" {
		return value, nil // Empty path = replace the entire root.
	}
	return setRecursive(root, strings.Split(path, "."), value)
}

// setRecursive walks the JSON tree segment by segment, descending into maps
// and arrays until the final segment is reached, at which point the value
// is written. Each recursive call consumes one path segment.
func setRecursive(current any, segments []string, value any) (any, error) {
	if len(segments) == 0 {
		return value, nil // Base case: all segments consumed, write the value.
	}

	segment := segments[0]
	switch typed := current.(type) {
	case map[string]any:
		// Object node: look up the key, recurse into the child.
		next, ok := typed[segment]
		if !ok {
			return nil, fmt.Errorf("path segment %q not found", segment)
		}
		updated, err := setRecursive(next, segments[1:], value)
		if err != nil {
			return nil, err
		}
		typed[segment] = updated // Write back the (possibly replaced) child.
		return typed, nil
	case []any:
		// Array node: parse the segment as a numeric index.
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
		typed[index] = updated // Write back the (possibly replaced) element.
		return typed, nil
	default:
		// Scalar or nil at a non-terminal position — can't descend further.
		return nil, fmt.Errorf("path segment %q: expected object or array, got %T", segment, current)
	}
}

// parseIndex converts a string to a non-negative integer without using strconv.
// Returns false if the string is empty or contains any non-digit character.
// This avoids a strconv import for the single use case of array indexing.
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
