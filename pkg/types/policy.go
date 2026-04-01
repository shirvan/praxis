// Package types — policy.go defines the types for Praxis Policy-as-Code.
//
// Policies are CUE constraints that are unified with templates during
// evaluation. They enforce organizational rules (naming conventions,
// required tags, region restrictions) without modifying the template itself.
//
// Policies are stored durably in the PolicyRegistry virtual object and are
// loaded by the command service during Apply/Plan/Deploy evaluation.
package types

import "time"

// PolicyScope defines the blast radius of a policy. Policies can either
// apply globally (to every template evaluation) or be scoped to a specific
// registered template.
type PolicyScope string

const (
	// PolicyScopeGlobal means the policy is evaluated against every template.
	// Use for organization-wide rules like "all resources must have an Owner tag".
	PolicyScopeGlobal PolicyScope = "global"

	// PolicyScopeTemplate means the policy only applies when evaluating the
	// specific template identified by TemplateName. Use for template-specific
	// constraints like "this VPC template must use 10.0.0.0/16 CIDR".
	PolicyScopeTemplate PolicyScope = "template"
)

// Policy is the complete definition of a single policy constraint.
// Policies are CUE files that are unified with the template during evaluation.
// If the unification fails (CUE error), the policy violation is reported to
// the user as a ValidationError.
type Policy struct {
	// Name is the unique identifier within a scope. For global policies,
	// names must be unique across all global policies. For template-scoped
	// policies, names are unique within that template's policy set.
	Name string `json:"name"`

	// Scope determines whether this policy applies globally or to a
	// specific template.
	Scope PolicyScope `json:"scope"`

	// TemplateName is set only when Scope is PolicyScopeTemplate.
	// Identifies which registered template this policy constrains.
	TemplateName string `json:"templateName,omitempty"`

	// Source is the raw CUE policy source code. The command service
	// passes this to the CUE engine for unification during evaluation.
	Source string `json:"source"`

	// Digest is a SHA-256 hash of Source, used for change detection.
	Digest string `json:"digest"`

	// Description is a human-readable explanation of what the policy enforces.
	// Displayed in `praxis policy list` and in validation error messages.
	Description string `json:"description,omitempty"`

	// CreatedAt records when the policy was first added.
	CreatedAt time.Time `json:"createdAt"`
}

// PolicyRecord is the durable state for a policy scope, stored as
// Restate virtual object state. For global scope, there is one record
// keyed by "global". For template scope, each template name has its own record.
type PolicyRecord struct {
	// Scope identifies whether this record holds global or template-scoped policies.
	Scope PolicyScope `json:"scope"`

	// Policies is the ordered list of all policies in this scope.
	// The order matters: policies are evaluated sequentially, and the first
	// violation encountered is reported.
	Policies []Policy `json:"policies"`
}

// PolicySummary is the compact listing representation used by `praxis policy list`.
// Contains only the fields needed for tabular display.
type PolicySummary struct {
	// Name is the policy's unique identifier within its scope.
	Name string `json:"name"`

	// Scope is the policy's application scope (global or template).
	Scope PolicyScope `json:"scope"`

	// TemplateName identifies the target template (only set for template-scoped policies).
	TemplateName string `json:"templateName,omitempty"`

	// Description is a human-readable summary for list views.
	Description string `json:"description,omitempty"`

	// UpdatedAt is the last modification timestamp.
	UpdatedAt time.Time `json:"updatedAt"`
}
