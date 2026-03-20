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

	"github.com/praxiscloud/praxis/internal/core/jsonpath"
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

		updated, setErr := jsonpath.Set(root, path, value)
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
