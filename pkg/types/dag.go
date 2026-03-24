package types

import "encoding/json"

// LifecyclePolicy controls resource-level update and delete behavior.
//
// Templates declare an optional lifecycle block alongside spec. The command
// pipeline extracts it during buildResourceNodes and threads it through to
// the orchestrator, where it influences plan-diff filtering and delete-time
// protection.
type LifecyclePolicy struct {
	// PreventDestroy makes the orchestrator refuse to delete this resource.
	// A delete workflow that encounters a protected resource records an error
	// rather than calling the driver's Delete handler.
	PreventDestroy bool `json:"preventDestroy,omitempty"`

	// IgnoreChanges lists field paths (relative to spec) that the plan diff
	// engine should skip when computing drift. Useful for tags managed by
	// external systems (e.g. cost allocation tags, AWS Config).
	//
	// Paths use dot notation matching the FieldDiff.Path convention,
	// for example "tags.lastModified" or "tags.updatedBy".
	IgnoreChanges []string `json:"ignoreChanges,omitempty"`
}

// ResourceNode represents a single node in the deployment dependency graph.
//
// The command service produces these nodes after template rendering, CUE
// evaluation, and SSM resolution. The orchestrator then consumes them to
// schedule resources in dependency order.
type ResourceNode struct {
	// Name is the template-local identifier of the resource. Dependency edges use
	// this name rather than the driver key because users reason about templates in
	// terms of logical resource names.
	Name string `json:"name"`

	// Kind is the resource kind and maps directly to an adapter / driver service.
	Kind string `json:"kind"`

	// Key is the canonical Restate Virtual Object key for the target driver.
	Key string `json:"key"`

	// Spec is the fully rendered resource document as raw JSON.
	//
	// The document still contains the full envelope emitted by the template
	// pipeline, such as apiVersion, kind, metadata, and spec. Some values may
	// still carry unresolved dispatch-time expressions if they depend on
	// outputs from upstream resources.
	Spec json.RawMessage `json:"spec"`

	// Dependencies lists the template-local resource names that must complete
	// before this node can be hydrated and dispatched.
	Dependencies []string `json:"dependencies"`

	// Expressions maps a JSON path to the expression that should be resolved
	// at dispatch time.
	//
	// Example:
	//   Key:   "spec.vpcSecurityGroupIds.0"
	//   Value: "resources.sg.outputs.groupId"
	//
	// This lets the orchestrator perform typed replacement at the exact JSON
	// location without rescanning the entire document for placeholders.
	Expressions map[string]string `json:"expressions,omitempty"`

	// Lifecycle holds optional resource-level lifecycle rules parsed from the
	// template. Nil when the template does not declare a lifecycle block.
	Lifecycle *LifecyclePolicy `json:"lifecycle,omitempty"`
}
