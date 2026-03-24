package dag

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// These patterns match ${...} expression placeholders in resource specs.
// The DAG engine stays pure and portable, depending only on the standard library.
var (
	exprPlaceholderRe = regexp.MustCompile(`\$\{([^}]+)\}`)
	resourceOutputRe  = regexp.MustCompile(`\bresources\.([A-Za-z_][A-Za-z0-9_-]*)\.outputs(?:\.|\b)`)
	dataOutputRe      = regexp.MustCompile(`\bdata\.([A-Za-z_][A-Za-z0-9_-]*)\.outputs(?:\.|\b)`)
)

// ParseDependencies scans a rendered resource document, discovers references to
// resources.<name>.outputs.* inside ${...} placeholders, and records the JSON path
// of every dispatch-time expression.
//
// Returned values follow two rules that the later orchestration layers rely on:
//
//   - deps is deduplicated and sorted alphabetically for stable graph behavior.
//   - exprs uses dot-separated JSON paths compatible with the expression hydrator.
//
// Dispatch-time expressions are expected to occupy the entire JSON scalar at
// their path, for example:
//
//	"groupId": "${resources.sg.outputs.groupId}"
//
// The parser rejects mixed literal-plus-placeholder strings such as
// "sg-${resources.sg.outputs.groupId}" because the hydrator performs typed
// replacement of the full JSON value at a path rather than substring interpolation.
func ParseDependencies(resourceName string, spec json.RawMessage) ([]string, map[string]string, error) {
	var root any
	if err := json.Unmarshal(spec, &root); err != nil {
		return nil, nil, fmt.Errorf("parse dependencies for %q: invalid JSON: %w", resourceName, err)
	}

	deps := make(map[string]struct{})
	exprs := make(map[string]string)
	if err := walkDependencies(resourceName, root, "", deps, exprs); err != nil {
		return nil, nil, err
	}

	orderedDeps := make([]string, 0, len(deps))
	for dep := range deps {
		orderedDeps = append(orderedDeps, dep)
	}
	sort.Strings(orderedDeps)

	return orderedDeps, exprs, nil
}

// walkDependencies recursively traverses decoded JSON data.
//
// The walker sorts object keys before descending so that both errors and the
// resulting expression path map are populated in a deterministic order. That
// makes the package much easier to test and to reason about during
// orchestration failures.
func walkDependencies(resourceName string, current any, path string, deps map[string]struct{}, exprs map[string]string) error {
	switch typed := current.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			nextPath := joinJSONPath(path, key)
			if err := walkDependencies(resourceName, typed[key], nextPath, deps, exprs); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for index, item := range typed {
			nextPath := joinJSONPath(path, fmt.Sprintf("%d", index))
			if err := walkDependencies(resourceName, item, nextPath, deps, exprs); err != nil {
				return err
			}
		}
		return nil
	case string:
		return parseStringDependencies(resourceName, typed, path, deps, exprs)
	default:
		return nil
	}
}

// parseStringDependencies extracts resource references from a single JSON string
// value. Strings that contain no ${...} placeholders or only non-resource
// expressions are ignored because they do not create resource-to-resource
// dependency edges.
func parseStringDependencies(resourceName, value, path string, deps map[string]struct{}, exprs map[string]string) error {
	matches := exprPlaceholderRe.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return nil
	}

	resourceAwareExpressions := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		expr := strings.TrimSpace(match[1])
		if dataOutputRe.MatchString(expr) {
			return fmt.Errorf("resource %q contains unresolved data source expression %q at %q; data sources must be resolved before dependency parsing", resourceName, expr, path)
		}
		refs := resourceOutputRe.FindAllStringSubmatch(expr, -1)
		if len(refs) == 0 {
			continue
		}
		resourceAwareExpressions = append(resourceAwareExpressions, expr)
		for _, ref := range refs {
			depName := ref[1]
			if depName == resourceName {
				return fmt.Errorf("resource %q references its own outputs at %q", resourceName, path)
			}
			deps[depName] = struct{}{}
		}
	}

	if len(resourceAwareExpressions) == 0 {
		return nil
	}

	// The hydrator stores exactly one expression per JSON path and replaces the
	// entire value at that path with the typed result. Rejecting ambiguous strings
	// here prevents silent data loss later.
	if len(resourceAwareExpressions) != 1 || strings.TrimSpace(value) != fmt.Sprintf("${%s}", resourceAwareExpressions[0]) {
		return fmt.Errorf("resource %q uses unsupported mixed interpolation at %q; dispatch-time resource expressions must occupy the full JSON value", resourceName, path)
	}

	if _, exists := exprs[path]; exists {
		return fmt.Errorf("resource %q records duplicate expression path %q", resourceName, path)
	}
	exprs[path] = resourceAwareExpressions[0]
	return nil
}

func joinJSONPath(base, segment string) string {
	if base == "" {
		return segment
	}
	return base + "." + segment
}
