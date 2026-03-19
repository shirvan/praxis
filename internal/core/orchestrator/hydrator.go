// Package orchestrator contains generic orchestration helpers shared by the
// deployment workflow implementation.
//
// The typed expression hydrator is implemented here because it is part of the
// cross-package contract between DAG parsing, deployment state, and driver
// dispatch.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/praxiscloud/praxis/internal/core/template"
)

// HydrateExprs resolves dispatch-time expressions against collected resource
// outputs, then writes the typed results back into the JSON document at the
// recorded paths.
//
// Expressions use dot-path syntax: resources.<name>.outputs.<field>.
// Integers stay integers, booleans stay booleans, arrays stay arrays.
func HydrateExprs(
	spec json.RawMessage,
	exprs map[string]string,
	outputs map[string]map[string]any,
) (json.RawMessage, error) {
	if len(exprs) == 0 {
		return spec, nil
	}

	var root any
	if err := json.Unmarshal(spec, &root); err != nil {
		return nil, template.TemplateErrors{template.TemplateError{
			Kind:    template.ErrExprUnresolved,
			Path:    "spec",
			Message: fmt.Sprintf("invalid JSON document for expression hydration: %v", err),
			Cause:   err,
		}}
	}

	paths := make([]string, 0, len(exprs))
	for path := range exprs {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var errs template.TemplateErrors
	for _, path := range paths {
		expr := exprs[path]
		value, err := resolveExpr(expr, outputs)
		if err != nil {
			errs = append(errs, template.TemplateError{
				Kind:    template.ErrExprUnresolved,
				Path:    path,
				Message: fmt.Sprintf("failed to resolve expression %q: %v", expr, err),
				Detail:  "Ensure every referenced dependency output exists before dispatching this resource.",
				Cause:   err,
			})
			continue
		}

		updated, setErr := setHydrationPath(root, path, value)
		if setErr != nil {
			errs = append(errs, template.TemplateError{
				Kind:    template.ErrExprUnresolved,
				Path:    path,
				Message: fmt.Sprintf("failed to write hydrated value: %v", setErr),
				Detail:  "Ensure Expressions contains valid JSON paths for the rendered resource document.",
				Cause:   setErr,
			})
			continue
		}
		root = updated
	}

	marshaled, err := json.Marshal(root)
	if err != nil {
		errs = append(errs, template.TemplateError{
			Kind:    template.ErrExprUnresolved,
			Path:    "spec",
			Message: fmt.Sprintf("failed to marshal hydrated JSON document: %v", err),
			Cause:   err,
		})
	}

	if len(errs) > 0 {
		return marshaled, errs
	}
	return marshaled, nil
}

// resolveExpr walks a dot-path expression like "resources.sg.outputs.groupId"
// against the collected outputs map.
func resolveExpr(expr string, outputs map[string]map[string]any) (any, error) {
	parts := strings.Split(expr, ".")
	// Expected form: resources.<name>.outputs.<field>
	if len(parts) < 4 || parts[0] != "resources" || parts[2] != "outputs" {
		return nil, fmt.Errorf("unsupported expression format: %q", expr)
	}
	resourceName := parts[1]
	fieldName := parts[3]

	outputMap, ok := outputs[resourceName]
	if !ok {
		return nil, fmt.Errorf("resource %q not found in outputs", resourceName)
	}
	value, ok := outputMap[fieldName]
	if !ok {
		return nil, fmt.Errorf("output %q not found for resource %q", fieldName, resourceName)
	}
	return value, nil
}

func setHydrationPath(root any, path string, value any) (any, error) {
	if path == "" {
		return value, nil
	}
	segments := strings.Split(path, ".")
	updated, err := setHydrationPathRecursive(root, segments, value)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func setHydrationPathRecursive(current any, segments []string, value any) (any, error) {
	if len(segments) == 0 {
		return value, nil
	}

	segment := segments[0]
	if index, ok := parseHydrationIndex(segment); ok {
		list, ok := current.([]any)
		if !ok {
			return nil, fmt.Errorf("segment %q expected array, got %T", segment, current)
		}
		if index < 0 || index >= len(list) {
			return nil, fmt.Errorf("array index %d out of range", index)
		}
		next, err := setHydrationPathRecursive(list[index], segments[1:], value)
		if err != nil {
			return nil, err
		}
		list[index] = next
		return list, nil
	}

	object, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("segment %q expected object, got %T", segment, current)
	}
	nextCurrent, ok := object[segment]
	if !ok {
		return nil, fmt.Errorf("segment %q not found", segment)
	}
	next, err := setHydrationPathRecursive(nextCurrent, segments[1:], value)
	if err != nil {
		return nil, err
	}
	object[segment] = next
	return object, nil
}

func parseHydrationIndex(segment string) (int, bool) {
	if segment == "" {
		return 0, false
	}
	for _, r := range segment {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	var index int
	_, err := fmt.Sscanf(segment, "%d", &index)
	if err != nil {
		return 0, false
	}
	return index, true
}
