// Package orchestrator contains generic orchestration helpers shared by the
// future workflow implementation.
//
// The typed CEL hydrator is implemented here because it is part of the
// cross-package contract between DAG parsing, deployment state, and driver
// dispatch.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"

	"github.com/praxiscloud/praxis/internal/core/template"
)

// HydrateCEL evaluates dispatch-time CEL expressions against collected resource
// outputs and optional variables, then writes the typed results back into the
// JSON document at the recorded paths.
//
// Unlike the template-time resolver, this function does not stringify the CEL
// result before inserting it. That difference is the whole point: integers stay
// integers, booleans stay booleans, arrays stay arrays, and so on.
func HydrateCEL(
	spec json.RawMessage,
	celExprs map[string]string,
	outputs map[string]map[string]any,
	variables map[string]any,
) (json.RawMessage, error) {
	if len(celExprs) == 0 {
		return spec, nil
	}

	var root any
	if err := json.Unmarshal(spec, &root); err != nil {
		return nil, template.TemplateErrors{template.TemplateError{
			Kind:    template.ErrCELUnresolved,
			Path:    "spec",
			Message: fmt.Sprintf("invalid JSON document for CEL hydration: %v", err),
			Cause:   err,
		}}
	}

	paths := make([]string, 0, len(celExprs))
	for path := range celExprs {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var errs template.TemplateErrors
	resources := buildCELResources(outputs)
	for _, path := range paths {
		expr := normalizeCELExpression(celExprs[path])
		value, err := evalCELValue(expr, resources, variables)
		if err != nil {
			errs = append(errs, template.TemplateError{
				Kind:    classifyCELError(err),
				Path:    path,
				Message: fmt.Sprintf("failed to hydrate CEL expression %q: %v", expr, err),
				Detail:  "Ensure every referenced dependency output exists before dispatching this resource.",
				Cause:   err,
			})
			continue
		}

		updated, setErr := setHydrationPath(root, path, value)
		if setErr != nil {
			errs = append(errs, template.TemplateError{
				Kind:    template.ErrCELUnresolved,
				Path:    path,
				Message: fmt.Sprintf("failed to write hydrated value: %v", setErr),
				Detail:  "Ensure CELExpressions contains valid JSON paths for the rendered resource document.",
				Cause:   setErr,
			})
			continue
		}
		root = updated
	}

	marshaled, err := json.Marshal(root)
	if err != nil {
		errs = append(errs, template.TemplateError{
			Kind:    template.ErrCELUnresolved,
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

func normalizeCELExpression(expr string) string {
	trimmed := strings.TrimSpace(expr)
	trimmed = strings.TrimPrefix(trimmed, "${cel:")
	trimmed = strings.TrimSuffix(trimmed, "}")
	return strings.TrimSpace(trimmed)
}

func buildCELResources(outputs map[string]map[string]any) map[string]any {
	resources := make(map[string]any, len(outputs))
	for name, outputMap := range outputs {
		resources[name] = map[string]any{
			"outputs": outputMap,
		}
	}
	return resources
}

func evalCELValue(expr string, resources map[string]any, variables map[string]any) (any, error) {
	env, err := cel.NewEnv(
		cel.Variable("resources", cel.DynType),
		cel.Variable("variables", cel.DynType),
	)
	if err != nil {
		return nil, err
	}

	ast, issues := env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	checked, issues := env.Check(ast)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	program, err := env.Program(checked)
	if err != nil {
		return nil, err
	}
	value, _, err := program.Eval(map[string]any{
		"resources": resources,
		"variables": variables,
	})
	if err != nil {
		return nil, err
	}
	return value.Value(), nil
}

func classifyCELError(err error) template.TemplateErrorKind {
	if err == nil {
		return template.ErrCELEval
	}
	message := err.Error()
	if strings.Contains(message, "undeclared reference") || strings.Contains(message, "no such key") || strings.Contains(message, "not found") {
		return template.ErrCELUnresolved
	}
	if strings.Contains(message, "Syntax error") {
		return template.ErrCELParse
	}
	return template.ErrCELEval
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
