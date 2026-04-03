package dag

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Regex patterns for expression extraction
// ---------------------------------------------------------------------------
//
// These patterns match ${...} expression placeholders embedded in resource spec
// JSON values. The DAG engine stays pure and portable, depending only on the
// standard library.
//
// Expression syntax example:
//
//	"subnetId": "${resources.my_subnet.outputs.subnetId}"
//
// The parser finds these via a two-step regex approach:
//  1. exprPlaceholderRe finds all ${...} wrappers in a string value.
//  2. resourceOutputRe / dataOutputRe extract the resource/data source name
//     from within the expression body.
var (
	// exprPlaceholderRe matches the ${...} wrapper. Captures the inner expression
	// body (everything between ${ and }). Example: "${resources.vpc.outputs.vpcId}"
	// captures "resources.vpc.outputs.vpcId".
	exprPlaceholderRe = regexp.MustCompile(`\$\{([^}]+)\}`)

	// resourceOutputRe matches resource output references inside an expression.
	// Pattern: "resources.<name>.outputs" where <name> starts with a letter or
	// underscore, followed by alphanumerics, underscores, or hyphens.
	// The \b word boundary ensures we don't match partial identifiers.
	// Capture group 1 is the resource name (e.g. "vpc" from "resources.vpc.outputs.vpcId").
	resourceOutputRe = regexp.MustCompile(`\bresources\.([A-Za-z_][A-Za-z0-9_-]*)\.outputs(?:\.|\b)`)

	// dataOutputRe matches data source output references. Data sources should be
	// fully resolved before dependency parsing, so encountering one here is an error.
	// Same naming rules as resourceOutputRe but anchored on "data." prefix.
	dataOutputRe = regexp.MustCompile(`\bdata\.([A-Za-z_][A-Za-z0-9_-]*)\.outputs(?:\.|\b)`)
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

	// deps collects unique resource names this resource depends on.
	// exprs maps JSON path -> expression body for the orchestrator's expression hydrator.
	deps := make(map[string]struct{})
	exprs := make(map[string]string)
	if err := walkDependencies(resourceName, root, "", deps, exprs); err != nil {
		return nil, nil, err
	}

	// Convert the dep set to a sorted slice for deterministic graph construction.
	orderedDeps := make([]string, 0, len(deps))
	for dep := range deps {
		orderedDeps = append(orderedDeps, dep)
	}
	sort.Strings(orderedDeps)

	return orderedDeps, exprs, nil
}

// walkDependencies recursively traverses decoded JSON data (the output of
// json.Unmarshal into any), building dot-separated JSON paths as it descends.
//
// The traversal handles three JSON node types:
//   - Objects (map[string]any): iterate sorted keys, descend with path "parent.key".
//   - Arrays ([]any): iterate elements, descend with path "parent.0", "parent.1", etc.
//   - Strings: check for ${...} expressions via parseStringDependencies.
//   - All other scalar types (numbers, bools, nulls) are ignored.
//
// The walker sorts object keys before descending so that both errors and the
// resulting expression path map are populated in a deterministic order. That
// makes the package much easier to test and to reason about during
// orchestration failures.
func walkDependencies(resourceName string, current any, path string, deps map[string]struct{}, exprs map[string]string) error {
	switch typed := current.(type) {
	case map[string]any:
		// Sort keys for deterministic traversal order.
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
		// Array elements are indexed numerically in the JSON path.
		for index, item := range typed {
			nextPath := joinJSONPath(path, fmt.Sprintf("%d", index))
			if err := walkDependencies(resourceName, item, nextPath, deps, exprs); err != nil {
				return err
			}
		}
		return nil
	case string:
		// Only string values can contain ${...} expressions.
		return parseStringDependencies(resourceName, typed, path, deps, exprs)
	default:
		// Numbers, bools, nulls — no expressions possible.
		return nil
	}
}

// parseStringDependencies extracts resource references from a single JSON string
// value. This is where the actual regex matching happens.
//
// Processing steps:
//  1. Find all ${...} placeholders in the string (exprPlaceholderRe).
//  2. For each placeholder, check if it's a data source reference (error) or
//     a resource output reference (dependency edge).
//  3. Validate that self-references are not allowed (resource referencing its own outputs).
//  4. Enforce the "full-value" rule: a resource expression must occupy the entire
//     JSON string value (no mixed interpolation like "prefix-${resources.x.outputs.id}").
//     This is required because the expression hydrator does typed replacement of the
//     full JSON value at a path, not substring interpolation.
//  5. Record the expression in the exprs map keyed by its JSON path.
//
// Strings that contain no ${...} placeholders or only non-resource expressions
// (e.g. ${env.REGION}) are ignored because they do not create dependency edges.
func parseStringDependencies(resourceName, value, path string, deps map[string]struct{}, exprs map[string]string) error {
	// Step 1: Find all ${...} expression placeholders.
	matches := exprPlaceholderRe.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return nil // No expressions — nothing to do.
	}

	resourceAwareExpressions := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		expr := strings.TrimSpace(match[1])

		// Step 2a: Reject unresolved data source references. Data sources
		// (data.<name>.outputs.*) must be resolved at render time, before
		// the DAG parser sees them.
		if dataOutputRe.MatchString(expr) {
			return fmt.Errorf("resource %q contains unresolved data source expression %q at %q; data sources must be resolved before dependency parsing", resourceName, expr, path)
		}

		// Step 2b: Extract resource output references.
		refs := resourceOutputRe.FindAllStringSubmatch(expr, -1)
		if len(refs) == 0 {
			continue // Expression doesn't reference any resource outputs.
		}
		resourceAwareExpressions = append(resourceAwareExpressions, expr)

		// Step 3: Record dependencies, rejecting self-references.
		for _, ref := range refs {
			depName := ref[1]
			if depName == resourceName {
				return fmt.Errorf("resource %q references its own outputs at %q", resourceName, path)
			}
			deps[depName] = struct{}{}
		}
	}

	if len(resourceAwareExpressions) == 0 {
		return nil // No resource-aware expressions found.
	}

	// Step 4: Enforce the "full-value" rule.
	// The hydrator stores exactly one expression per JSON path and replaces the
	// entire value at that path with the typed result. Rejecting ambiguous strings
	// here prevents silent data loss later.
	// Valid:   "${resources.vpc.outputs.vpcId}"          (single expression, full value)
	// Invalid: "prefix-${resources.vpc.outputs.vpcId}"   (mixed interpolation)
	// Invalid: "${resources.a.outputs.x}-${resources.b.outputs.y}"  (multiple expressions)
	if len(resourceAwareExpressions) != 1 || strings.TrimSpace(value) != fmt.Sprintf("${%s}", resourceAwareExpressions[0]) {
		return fmt.Errorf("resource %q uses unsupported mixed interpolation at %q; dispatch-time resource expressions must occupy the full JSON value", resourceName, path)
	}

	// Step 5: Record the expression path for the hydrator.
	if _, exists := exprs[path]; exists {
		return fmt.Errorf("resource %q records duplicate expression path %q", resourceName, path)
	}
	exprs[path] = resourceAwareExpressions[0]
	return nil
}

// joinJSONPath builds dot-separated JSON paths used as keys in the expression
// map. For example, joinJSONPath("spec", "vpcId") returns "spec.vpcId".
// The root path is the empty string, so the first segment stands alone.
func joinJSONPath(base, segment string) string {
	if base == "" {
		return segment
	}
	return base + "." + segment
}
