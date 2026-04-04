// hydrator.go implements dispatch-time expression hydration.
//
// When a template declares cross-resource references like:
//
//	vpcId: ${resources.vpc.outputs.vpcId}
//
// the template evaluator records these as Expressions (JSON path → dot-path
// expression) and leaves placeholder values in the rendered Spec. At dispatch
// time—after the referenced dependency has completed and emitted its outputs—
// HydrateExprs resolves these expressions and writes typed values back into
// the JSON document.
//
// Type preservation is critical: integers stay integers, booleans stay booleans,
// and string arrays stay string arrays. This avoids driver-side type coercion
// bugs that would arise from string-interpolation approaches.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shirvan/praxis/internal/core/jsonpath"
	"github.com/shirvan/praxis/internal/core/template"
)

// arrayIndexRe matches a trailing bracket index like "fieldName[0]" and
// captures the field name and the numeric index separately.
var arrayIndexRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\[(\d+)\]$`)

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
//
// Supports nested navigation into output values including array indexing:
//
//	"resources.cert.outputs.dnsValidationRecords[0].resourceRecordName"
//
// The first segment after "outputs." is used as the top-level output key. If
// that segment contains an array index (e.g. "records[0]"), the key is the
// base name and the index navigates into the array value. Any remaining
// segments navigate further into nested maps or arrays.
func resolveExpr(expr string, outputs map[string]map[string]any) (any, error) {
	parts := strings.Split(expr, ".")
	// Expected form: resources.<name>.outputs.<field>[.<nested>...]
	if len(parts) < 4 || parts[0] != "resources" || parts[2] != "outputs" {
		return nil, fmt.Errorf("unsupported expression format: %q", expr)
	}
	resourceName := parts[1]
	fieldParts := parts[3:] // everything after "outputs."

	outputMap, ok := outputs[resourceName]
	if !ok {
		return nil, fmt.Errorf("resource %q not found in outputs", resourceName)
	}

	return resolveNestedOutput(outputMap, fieldParts)
}

// resolveNestedOutput navigates into an output value using the remaining field
// parts after "outputs.". It supports:
//   - Plain map keys: "fieldName" looks up map["fieldName"]
//   - Array-indexed keys: "fieldName[0]" looks up map["fieldName"].([]any)[0]
//   - Chained access: "records[0].name" → map["records"][0]["name"]
func resolveNestedOutput(outputMap map[string]any, fieldParts []string) (any, error) {
	if len(fieldParts) == 0 {
		return nil, fmt.Errorf("empty field path")
	}

	// Parse the first segment — it may contain an array index.
	first := fieldParts[0]
	key, idx, hasIndex := parseFieldIndex(first)

	value, ok := outputMap[key]
	if !ok {
		return nil, fmt.Errorf("output %q not found for resource", strings.Join(fieldParts, "."))
	}

	// If the first segment has an array index, navigate into the array.
	if hasIndex {
		arr, isArr := toSlice(value)
		if !isArr {
			return nil, fmt.Errorf("output %q is not an array", key)
		}
		if idx < 0 || idx >= len(arr) {
			return nil, fmt.Errorf("output %q: array index %d out of range (length %d)", key, idx, len(arr))
		}
		value = arr[idx]
	}

	// If there are more segments, keep walking.
	remaining := fieldParts[1:]
	for i, seg := range remaining {
		segKey, segIdx, segHasIndex := parseFieldIndex(seg)

		switch typed := value.(type) {
		case map[string]any:
			next, exists := typed[segKey]
			if !exists {
				return nil, fmt.Errorf("output %q not found for resource", strings.Join(fieldParts, "."))
			}
			value = next
		default:
			return nil, fmt.Errorf("cannot navigate into %T at segment %q (path: %s)", value, seg, strings.Join(fieldParts[:3+i], "."))
		}

		if segHasIndex {
			arr, isArr := toSlice(value)
			if !isArr {
				return nil, fmt.Errorf("output %q is not an array at segment %q", segKey, seg)
			}
			if segIdx < 0 || segIdx >= len(arr) {
				return nil, fmt.Errorf("output %q: array index %d out of range (length %d)", seg, segIdx, len(arr))
			}
			value = arr[segIdx]
		}
	}

	return value, nil
}

// parseFieldIndex splits a segment like "records[2]" into ("records", 2, true).
// For plain segments like "fieldName" it returns ("fieldName", 0, false).
func parseFieldIndex(segment string) (string, int, bool) {
	m := arrayIndexRe.FindStringSubmatch(segment)
	if m == nil {
		return segment, 0, false
	}
	idx, _ := strconv.Atoi(m[2]) // regex guarantees digits
	return m[1], idx, true
}

// toSlice normalises an output value to []any. Driver outputs may store arrays
// as []any (from JSON unmarshal) or as typed slices like []map[string]any.
func toSlice(v any) ([]any, bool) {
	switch typed := v.(type) {
	case []any:
		return typed, true
	case []map[string]any:
		out := make([]any, len(typed))
		for i, m := range typed {
			out[i] = m
		}
		return out, true
	default:
		return nil, false
	}
}
