package types

import "time"

// TemplateRef identifies a template to load from the registry.
type TemplateRef struct {
	Name string `json:"name"`
}

// TemplateMetadata describes a registered template.
type TemplateMetadata struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// TemplateRecord is the durable record for a registered template.
type TemplateRecord struct {
	Metadata       TemplateMetadata `json:"metadata"`
	Source         string           `json:"source"`
	Digest         string           `json:"digest"`
	VariableSchema VariableSchema   `json:"variableSchema,omitempty"`
	PreviousSource string           `json:"previousSource,omitempty"`
	PreviousDigest string           `json:"previousDigest,omitempty"`
}

// TemplateSummary is the compact listing representation used by TemplateIndex.
type TemplateSummary struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// VariableField describes one variable expected by a template.
type VariableField struct {
	Type     string   `json:"type"`              // "string", "bool", "int", "float"
	Required bool     `json:"required"`          // true if no default exists
	Default  any      `json:"default,omitempty"` // default value if present
	Enum     []string `json:"enum,omitempty"`    // allowed values for disjunctions
}

// VariableSchema maps variable names to their field descriptors.
type VariableSchema map[string]VariableField
