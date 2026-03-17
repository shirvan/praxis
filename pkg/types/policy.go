package types

import "time"

// PolicyScope defines where a policy applies.
type PolicyScope string

const (
	PolicyScopeGlobal   PolicyScope = "global"
	PolicyScopeTemplate PolicyScope = "template"
)

// Policy is one policy definition.
type Policy struct {
	Name         string      `json:"name"`
	Scope        PolicyScope `json:"scope"`
	TemplateName string      `json:"templateName,omitempty"`
	Source       string      `json:"source"`
	Digest       string      `json:"digest"`
	Description  string      `json:"description,omitempty"`
	CreatedAt    time.Time   `json:"createdAt"`
}

// PolicyRecord is the durable record for a policy scope.
type PolicyRecord struct {
	Scope    PolicyScope `json:"scope"`
	Policies []Policy    `json:"policies"`
}

// PolicySummary is the compact listing representation.
type PolicySummary struct {
	Name         string      `json:"name"`
	Scope        PolicyScope `json:"scope"`
	TemplateName string      `json:"templateName,omitempty"`
	Description  string      `json:"description,omitempty"`
	UpdatedAt    time.Time   `json:"updatedAt"`
}
