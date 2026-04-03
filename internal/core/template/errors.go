package template

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TemplateErrorKind classifies the category of a template evaluation error.
// The kind determines how the CLI formats the error message and whether the
// error is attributable to user input, schema constraints, or policy enforcement.
type TemplateErrorKind int

const (
	// ErrCUELoad indicates a CUE loader failure: file not found, parse error,
	// or import resolution failure. The template source is syntactically invalid.
	ErrCUELoad TemplateErrorKind = iota
	// ErrCUEValidation indicates a schema constraint violation: wrong type,
	// missing required field, pattern mismatch, or disallowed field in a
	// closed definition.
	ErrCUEValidation
	// ErrExprUnresolved indicates a reference to output that is not yet
	// available (e.g. referencing another resource's outputs before it has
	// been provisioned).
	ErrExprUnresolved
	// ErrResolve indicates an SSM parameter resolution failure (parameter
	// not found, access denied, or invalid URI format).
	ErrResolve
	// ErrPolicyViolation indicates that a CUE policy constraint was not
	// satisfied. The PolicyName field on the TemplateError identifies which
	// policy introduced the violation.
	ErrPolicyViolation
)

var kindNames = [...]string{
	"CUELoad",
	"CUEValidation",
	"ExprUnresolved",
	"Resolve",
	"PolicyViolation",
}

// String returns the human-readable name of the error kind.
func (k TemplateErrorKind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("Unknown(%d)", int(k))
}

// TemplateError is a single diagnostic from template evaluation. It follows
// a structured format so the CLI can render rich error output with file
// positions, dot-paths, and actionable fix suggestions.
type TemplateError struct {
	Kind       TemplateErrorKind `json:"kind"`
	Path       string            `json:"path"`                 // Dot-path to failing field (e.g. "resources.bucket.spec.region")
	Source     string            `json:"source"`               // File + line position from CUE
	Message    string            `json:"message"`              // What went wrong
	Detail     string            `json:"detail"`               // Actionable fix suggestion
	PolicyName string            `json:"policyName,omitempty"` // Responsible policy name(s), comma-separated
	Cause      error             `json:"-"`                    // Underlying library error, not serialized
}

func (e TemplateError) Error() string {
	return fmt.Sprintf("%s: %s (%s)", e.Path, e.Message, e.Kind)
}

func (e TemplateError) Unwrap() error {
	return e.Cause
}

type templateErrorJSON struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Source     string `json:"source"`
	Message    string `json:"message"`
	Detail     string `json:"detail"`
	PolicyName string `json:"policyName,omitempty"`
}

// TemplateErrors collects multiple errors from a single evaluation pass.
// It implements the error interface so it can be returned directly, and
// provides a rich multi-line Error() string as well as a JSON() method
// for machine-readable output.
type TemplateErrors []TemplateError

func (e TemplateErrors) Error() string {
	var b strings.Builder
	b.WriteString("Template evaluation failed\n")
	for _, te := range e {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  %s\n", te.Path)
		if te.Source != "" {
			fmt.Fprintf(&b, "  |-- %s\n", te.Source)
		}
		fmt.Fprintf(&b, "  |-- %s\n", te.Message)
		if te.Detail != "" {
			fmt.Fprintf(&b, "  |__ %s\n", te.Detail)
		}
	}
	fmt.Fprintf(&b, "\n%d error(s) in template evaluation.\n", len(e))
	return b.String()
}

// JSON serializes the error list to indented JSON for machine consumption.
// Error kinds are converted to their string names for readability.
func (e TemplateErrors) JSON() []byte {
	items := make([]templateErrorJSON, len(e))
	for i, te := range e {
		items[i] = templateErrorJSON{
			Kind:       te.Kind.String(),
			Path:       te.Path,
			Source:     te.Source,
			Message:    te.Message,
			Detail:     te.Detail,
			PolicyName: te.PolicyName,
		}
	}
	data, _ := json.MarshalIndent(items, "", "  ")
	return data
}
