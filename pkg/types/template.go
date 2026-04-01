// Package types defines the shared types that flow between Praxis components
// over Restate RPC. This file contains template-related types used by the
// template registry, command service, and CLI.
package types

import "time"

// TemplateRef identifies a template to load from the registry by name.
// Used in ApplyRequest and PlanRequest when the user wants to reference
// a pre-registered template instead of providing inline CUE source.
//
// Example JSON: {"name": "vpc-standard"}
type TemplateRef struct {
	// Name is the unique registry key for the template. Must match a
	// previously registered template via RegisterTemplate.
	Name string `json:"name"`
}

// TemplateMetadata describes a registered template's identity and organizational
// attributes. This is the metadata envelope stored alongside the template source
// in the TemplateRegistry virtual object.
type TemplateMetadata struct {
	// Name is the unique identifier used to reference this template.
	// Immutable after initial registration.
	Name string `json:"name"`

	// Description is a human-readable summary displayed in `praxis template list`.
	Description string `json:"description,omitempty"`

	// Labels are arbitrary key-value pairs for filtering and organization.
	// Example: {"team": "platform", "env": "production"}
	Labels map[string]string `json:"labels,omitempty"`

	// CreatedAt records when the template was first registered.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt records the last time the template source was modified.
	UpdatedAt time.Time `json:"updatedAt"`
}

// TemplateRecord is the complete durable record for a registered template,
// stored as Restate virtual object state keyed by template name.
// It preserves one level of history (previous source/digest) so that the
// system can detect when a template has changed between deployments.
type TemplateRecord struct {
	// Metadata holds the template's identity and organizational attributes.
	Metadata TemplateMetadata `json:"metadata"`

	// Source is the raw CUE template source code. Stored verbatim so it can
	// be served back to the CUE evaluation engine during Apply/Plan/Deploy.
	Source string `json:"source"`

	// Digest is a SHA-256 hash of the Source, used for change detection
	// and idempotency — if the digest matches, the registration is a no-op.
	Digest string `json:"digest"`

	// VariableSchema describes the variables this template expects.
	// Extracted automatically from the CUE source during registration by
	// parsing the "variables" block's structure and constraints.
	VariableSchema VariableSchema `json:"variableSchema,omitempty"`

	// PreviousSource is the source from the prior registration, kept for
	// one generation of rollback visibility.
	PreviousSource string `json:"previousSource,omitempty"`

	// PreviousDigest is the digest from the prior registration.
	PreviousDigest string `json:"previousDigest,omitempty"`
}

// TemplateSummary is the compact listing representation used by TemplateIndex.
// It contains only the fields needed for `praxis template list` output,
// avoiding the cost of loading full source for every template.
type TemplateSummary struct {
	// Name is the template's unique registry key.
	Name string `json:"name"`

	// Description is the human-readable summary for list views.
	Description string `json:"description,omitempty"`

	// UpdatedAt is the last modification timestamp, used for sorting.
	UpdatedAt time.Time `json:"updatedAt"`
}

// VariableField describes one variable expected by a template.
// The CUE evaluation engine extracts these from the template's "variables"
// block by inspecting CUE types and constraints (disjunctions → Enum,
// default values → Default, missing defaults → Required=true).
type VariableField struct {
	// Type is the CUE type mapped to a JSON-friendly name.
	// One of: "string", "bool", "int", "float", "list", "struct".
	Type string `json:"type"`

	// Required is true when the CUE field has no default value,
	// meaning the user must supply it at apply/deploy time.
	Required bool `json:"required"`

	// Default is the CUE-declared default value, if any.
	// Nil when Required is true.
	Default any `json:"default,omitempty"`

	// Enum lists the allowed values when the CUE type is a disjunction.
	// Example: for `env: "dev" | "staging" | "prod"`, Enum=["dev","staging","prod"].
	Enum []string `json:"enum,omitempty"`

	// Items is the element type for list variables (e.g., "string", "int", "struct").
	// Only populated when Type is "list".
	Items string `json:"items,omitempty"`
}

// VariableSchema maps variable names to their field descriptors.
// Used by the CLI to validate user-supplied variables before sending
// them to the command service, and by `praxis template describe` to
// display the expected input contract.
type VariableSchema map[string]VariableField
