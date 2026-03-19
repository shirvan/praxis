package template

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TemplateErrorKind classifies the category of a template evaluation error.
type TemplateErrorKind int

const (
	ErrCUELoad         TemplateErrorKind = iota // File not found, parse error, import failure
	ErrCUEValidation                            // Constraint violation (pattern, type, required field)
	ErrExprParse                                // Expression syntax error
	ErrExprEval                                 // Expression runtime error (missing variable, type mismatch)
	ErrExprUnresolved                           // Reference to unavailable output
	ErrResolve                                  // SSM resolution failure
	ErrPolicyViolation                          // Policy constraint not satisfied
)

var kindNames = [...]string{
	"CUELoad",
	"CUEValidation",
	"ExprParse",
	"ExprEval",
	"ExprUnresolved",
	"Resolve",
	"PolicyViolation",
}

func (k TemplateErrorKind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("Unknown(%d)", int(k))
}

// TemplateError is a single diagnostic from template evaluation.
type TemplateError struct {
	Kind       TemplateErrorKind `json:"kind"`
	Path       string            `json:"path"`    // Dot-path to failing field
	Source     string            `json:"source"`  // File + line
	Message    string            `json:"message"` // What went wrong
	Detail     string            `json:"detail"`  // Actionable fix suggestion
	PolicyName string            `json:"policyName,omitempty"`
	Cause      error             `json:"-"` // Underlying library error
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
type TemplateErrors []TemplateError

func (e TemplateErrors) Error() string {
	var b strings.Builder
	b.WriteString("Template evaluation failed\n")
	for _, te := range e {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  %s\n", te.Path))
		if te.Source != "" {
			b.WriteString(fmt.Sprintf("  |-- %s\n", te.Source))
		}
		b.WriteString(fmt.Sprintf("  |-- %s\n", te.Message))
		if te.Detail != "" {
			b.WriteString(fmt.Sprintf("  |__ %s\n", te.Detail))
		}
	}
	b.WriteString(fmt.Sprintf("\n%d error(s) in template evaluation.\n", len(e)))
	return b.String()
}

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
