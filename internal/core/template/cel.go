package template

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/cel-go/cel"
)

var (
	celPlaceholderRe = regexp.MustCompile(`\$\{cel:([^}]+)\}`)
	// resourceOutputRe detects expressions that reference resources.*.outputs.*
	// which must be deferred to dispatch-time hydration.
	resourceOutputRe = regexp.MustCompile(`\bresources\.[A-Za-z_]`)
)

// CELResolver evaluates ${cel:expr} placeholders in raw JSON specs.
// Expressions that reference resources.*.outputs.* are left intact — they are
// resolved at dispatch time by the orchestrator's hydrator.
type CELResolver struct {
	vars map[string]any
}

// NewCELResolver creates a resolver that evaluates CEL expressions against vars.
func NewCELResolver(vars map[string]any) *CELResolver {
	return &CELResolver{vars: vars}
}

// Resolve walks rawSpecs and replaces all ${cel:expr} placeholders with
// evaluated results. All errors are collected before returning.
func (r *CELResolver) Resolve(rawSpecs map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	var errs TemplateErrors
	result := make(map[string]json.RawMessage, len(rawSpecs))

	for name, raw := range rawSpecs {
		resolved, resolveErrs := r.resolveJSON(name, raw)
		errs = append(errs, resolveErrs...)
		if len(resolveErrs) == 0 {
			result[name] = resolved
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return result, nil
}

// resolveJSON processes a single resource's JSON, replacing CEL placeholders.
func (r *CELResolver) resolveJSON(resourceName string, raw json.RawMessage) (json.RawMessage, TemplateErrors) {
	s := string(raw)
	if !strings.Contains(s, "${cel:") {
		return raw, nil
	}

	var errs TemplateErrors
	resolved := celPlaceholderRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := celPlaceholderRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		expr := sub[1]

		// Skip expressions that reference resources.*.outputs.* — those are
		// dispatch-time expressions resolved by the hydrator after upstream
		// dependencies complete. Leave the placeholder intact.
		if resourceOutputRe.MatchString(expr) {
			return match
		}

		val, err := r.evalExpr(expr)
		if err != nil {
			errs = append(errs, *err)
			return match
		}
		return val
	})

	if len(errs) > 0 {
		return nil, errs
	}

	// Validate the result is still valid JSON.
	if !json.Valid([]byte(resolved)) {
		errs = append(errs, TemplateError{
			Kind:    ErrCELEval,
			Path:    fmt.Sprintf("resources.%s", resourceName),
			Message: "CEL substitution produced invalid JSON",
			Detail:  "Check that CEL expressions produce values compatible with the JSON context.",
		})
		return nil, errs
	}

	return json.RawMessage(resolved), nil
}

// evalExpr evaluates a single CEL expression against the variable map.
func (r *CELResolver) evalExpr(expr string) (string, *TemplateError) {
	// Build CEL environment with variable declarations.
	envOpts := []cel.EnvOption{cel.Variable("variables", cel.DynType)}

	env, err := cel.NewEnv(envOpts...)
	if err != nil {
		return "", &TemplateError{
			Kind:    ErrCELParse,
			Message: fmt.Sprintf("failed to create CEL environment: %v", err),
		}
	}

	ast, issues := env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return "", &TemplateError{
			Kind:    ErrCELParse,
			Message: fmt.Sprintf("CEL syntax error in %q: %v", expr, issues.Err()),
			Detail:  "Check expression syntax. CEL uses dot notation: variables.env",
		}
	}

	checkedAST, issues := env.Check(ast)
	if issues != nil && issues.Err() != nil {
		return "", &TemplateError{
			Kind:    ErrCELEval,
			Message: fmt.Sprintf("CEL type-check error in %q: %v", expr, issues.Err()),
			Detail:  "Ensure all referenced variables exist in the variable map.",
		}
	}

	prg, err := env.Program(checkedAST)
	if err != nil {
		return "", &TemplateError{
			Kind:    ErrCELEval,
			Message: fmt.Sprintf("CEL program error in %q: %v", expr, err),
		}
	}

	activation := map[string]any{
		"variables": r.vars,
	}

	out, _, err := prg.Eval(activation)
	if err != nil {
		return "", &TemplateError{
			Kind:    ErrCELEval,
			Message: fmt.Sprintf("CEL evaluation error in %q: %v", expr, err),
			Detail:  "Ensure all referenced variables are provided.",
		}
	}

	return fmt.Sprintf("%v", out.Value()), nil
}
