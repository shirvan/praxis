package types

import "fmt"

// DiffOperation represents the type of change detected between desired and current state.
type DiffOperation string

const (
	// OpCreate indicates a new resource will be created.
	OpCreate DiffOperation = "create"
	// OpUpdate indicates an existing resource will be modified.
	OpUpdate DiffOperation = "update"
	// OpDelete indicates a resource will be removed.
	OpDelete DiffOperation = "delete"
	// OpNoOp indicates no change is required.
	OpNoOp DiffOperation = "no-op"
)

// FieldDiff represents a single field-level change within a resource.
// This is the atomic unit of the plan output, e.g.
// "~ versioning: true => false" line items.
type FieldDiff struct {
	// Path is the dot-separated path to the field (e.g., "spec.versioning", "tags.env").
	Path string `json:"path"`

	// OldValue is the current value (nil for creates, populated for updates/deletes).
	OldValue any `json:"oldValue,omitempty"`

	// NewValue is the desired value (nil for deletes, populated for creates/updates).
	NewValue any `json:"newValue,omitempty"`
}

// ResourceDiff describes the planned changes for a single resource.
// Aggregating these across all resources in a deployment produces the
// full plan output for `praxis plan`.
type ResourceDiff struct {
	// ResourceKey is the unique identifier (e.g., "my-bucket" or "vpc-123~web-sg").
	ResourceKey string `json:"resourceKey"`

	// ResourceType is the kind of resource (e.g., "S3Bucket", "SecurityGroup").
	ResourceType string `json:"resourceType"`

	// Operation is the high-level action: create, update, delete, or no-op.
	Operation DiffOperation `json:"operation"`

	// FieldDiffs lists each individual field change. Empty for create/delete
	// (the entire resource is the diff). Populated for updates.
	FieldDiffs []FieldDiff `json:"fieldDiffs,omitempty"`
}

// PlanResult is the complete output of a `praxis plan` operation.
// It shows what would happen if the user runs `praxis apply`.
type PlanResult struct {
	// Resources is the ordered list of resource-level diffs.
	// Ordering follows the dependency graph (leaves first).
	Resources []ResourceDiff `json:"resources"`

	// Summary counts resources by operation type.
	Summary PlanSummary `json:"summary"`
}

// PlanSummary provides quick counts of each operation type.
type PlanSummary struct {
	ToCreate  int `json:"toCreate"`
	ToUpdate  int `json:"toUpdate"`
	ToDelete  int `json:"toDelete"`
	Unchanged int `json:"unchanged"`
}

// String returns a human-readable summary line, e.g.:
// "Plan: 2 to create, 1 to update, 0 to delete, 3 unchanged."
func (s PlanSummary) String() string {
	return fmt.Sprintf("Plan: %d to create, %d to update, %d to delete, %d unchanged.",
		s.ToCreate, s.ToUpdate, s.ToDelete, s.Unchanged)
}

// HasChanges returns true if the plan would modify any resources.
func (s PlanSummary) HasChanges() bool {
	return s.ToCreate > 0 || s.ToUpdate > 0 || s.ToDelete > 0
}
