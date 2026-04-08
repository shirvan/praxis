// Package types — contract.go defines the request/response types for
// every operation exposed by the PraxisCommandService over Restate RPC.
//
// These types form the public API contract between the CLI and the backend.
// Every struct must be JSON-serializable because Restate uses encoding/json
// for handler input/output serialization.
package types

import "time"

// ApplyRequest is the payload accepted by PraxisCommandService.Apply.
// Apply is the primary create/update entry point: it evaluates a CUE template,
// builds a plan, and dispatches resources to drivers via the orchestrator.
//
// Template source can come from one of two places:
//   - Template: inline CUE source provided directly in the request body.
//   - TemplateRef: a reference to a pre-registered template in the registry.
//
// Exactly one of Template or TemplateRef must be provided.
type ApplyRequest struct {
	// Template is raw CUE source code. Used when the CLI sends a local file.
	Template string `json:"template,omitempty"`

	// TemplateRef identifies a template previously registered in the registry.
	// Used by `praxis deploy` and when templates are managed centrally.
	TemplateRef *TemplateRef `json:"templateRef,omitempty"`

	// Variables are user-supplied key-value pairs that get injected into
	// the CUE template's "variables" block during evaluation.
	Variables map[string]any `json:"variables,omitempty"`

	// DeploymentKey is an optional stable identifier for the deployment.
	// If omitted, the command service generates one from the template hash.
	// Re-using the same key triggers an update rather than a new deployment.
	DeploymentKey string `json:"deploymentKey,omitempty"`

	// Account selects which configured AWS identity to use for this deployment.
	// Maps to an entry in the auth registry loaded from environment variables.
	Account string `json:"account,omitempty"`

	// Workspace is an optional namespace for isolating deployments.
	// Deployments in different workspaces can use the same template without conflict.
	Workspace string `json:"workspace,omitempty"`

	// Targets limits the apply to a subset of resources by template-local name.
	// Only the named resources and their transitive dependencies are included.
	// When empty, all resources in the template are applied.
	Targets []string `json:"targets,omitempty"`

	// OrphanRemoved keeps resources that disappeared from the template running in
	// the provider and only removes them from Praxis management state.
	OrphanRemoved bool `json:"orphanRemoved,omitempty"`

	// Replace forces a destroy-then-recreate cycle on the named resources.
	// This is useful when immutable fields have changed and an in-place
	// update is not possible (e.g., VPC CIDR block change).
	Replace []string `json:"replace,omitempty"`

	// AllowReplace, when true, automatically replaces resources that fail
	// with a 409 immutable-field conflict during provisioning. The
	// orchestrator will delete the existing resource and re-provision it.
	// WARNING: this destroys and recreates the resource, which may cause
	// downtime and data loss. Resources with lifecycle.preventDestroy are
	// still protected.
	AllowReplace bool `json:"allowReplace,omitempty"`

	// TemplatePath is the original source filename (e.g., "webapp.cue").
	// When set, it replaces the default "inline://template.cue" in
	// deployment audit records. Only meaningful for inline templates.
	TemplatePath string `json:"templatePath,omitempty"`

	// MaxParallelism limits concurrent resource operations. Zero means unlimited.
	MaxParallelism int `json:"maxParallelism,omitempty"`

	// MaxRetries overrides the deployment-wide default resource retry count.
	MaxRetries *int `json:"maxRetries,omitempty"`
}

// ApplyResponse is returned immediately after an apply request is accepted.
// The actual resource provisioning happens asynchronously in the orchestrator
// workflow. The CLI uses the DeploymentKey to poll for progress via GetDetail.
type ApplyResponse struct {
	// DeploymentKey is the stable identifier for tracking this deployment.
	DeploymentKey string `json:"deploymentKey"`

	// Status is the initial deployment status, typically DeploymentRunning.
	Status DeploymentStatus `json:"status"`

	// Plan is the computed diff showing what will change. Included in the
	// response so the CLI can display the plan alongside the acceptance message.
	Plan *PlanResult `json:"plan,omitempty"`
}

// PlanRequest is the payload accepted by PraxisCommandService.Plan.
// Plan is the dry-run counterpart to Apply: it evaluates the template
// and computes diffs without dispatching any changes.
//
// Exactly one of Template or TemplateRef must be provided.
type PlanRequest struct {
	// Template is raw CUE source code for inline evaluation.
	Template string `json:"template,omitempty"`

	// TemplateRef identifies a registered template to evaluate.
	TemplateRef *TemplateRef `json:"templateRef,omitempty"`

	// Variables are user-supplied values injected into the CUE template.
	Variables map[string]any `json:"variables,omitempty"`

	// Account selects the AWS identity for plan-time resource lookups.
	Account string `json:"account,omitempty"`

	// Workspace scopes the plan to a particular namespace.
	Workspace string `json:"workspace,omitempty"`

	// Targets limits the plan to a subset of resources plus dependencies.
	Targets []string `json:"targets,omitempty"`

	// TemplatePath is the original source filename (e.g., "webapp.cue").
	// When set, it replaces the default "inline://template.cue" in plan output.
	TemplatePath string `json:"templatePath,omitempty"`

	// DeploymentKey is an optional deployment key used to look up prior
	// deployment state. When provided, the plan can compare expression-bearing
	// resources against cloud state by resolving expressions from the previous
	// deployment's outputs. When omitted, the plan auto-derives the key from
	// the rendered template (same logic as Apply).
	DeploymentKey string `json:"deploymentKey,omitempty"`
}

// PlanResponse contains the machine-readable plan result and the rendered
// template output. The rendered field is the fully-evaluated CUE template
// with sensitive values masked, suitable for display to the user.
type PlanResponse struct {
	// Plan holds the resource-level diffs and summary counts.
	Plan *PlanResult `json:"plan"`

	// ExecutionPlan is the workflow-ready deployment plan that can be saved and
	// later applied without re-evaluating the template.
	ExecutionPlan *ExecutionPlan `json:"executionPlan,omitempty"`

	// Rendered is the pretty-printed, sensitivity-masked template output.
	// Displayed by `praxis plan` to show the user what the template evaluates to.
	Rendered string `json:"rendered"`

	// TemplateHash is the SHA-256 digest of the template source used to build
	// this plan.
	TemplateHash string `json:"templateHash,omitempty"`

	// DataSources maps data source names to their resolved outputs.
	// Data sources are read-only lookups (e.g., "find the VPC with tag X")
	// that the template can reference but Praxis does not manage.
	DataSources map[string]DataSourceResult `json:"dataSources,omitempty"`

	// Graph describes the resource dependency DAG for visualization.
	// Each entry is a node with its dependencies. Populated when the
	// plan pipeline successfully constructs the dependency graph.
	Graph []GraphNode `json:"graph,omitempty"`

	// Warnings contains non-fatal diagnostic messages from the plan pipeline.
	// For example, when expression-bearing resources cannot be resolved against
	// prior deployment state, a warning explains what was tried.
	Warnings []string `json:"warnings,omitempty"`
}

// GraphNode is a lightweight representation of a resource in the dependency
// DAG, suitable for serialization in API responses and CLI rendering.
type GraphNode struct {
	// Name is the template-local resource identifier.
	Name string `json:"name"`
	// Kind is the resource type (e.g., "AWS::S3::Bucket").
	Kind string `json:"kind"`
	// Dependencies lists the names of resources this node depends on.
	Dependencies []string `json:"dependencies,omitempty"`
}

// DataSourceResult shows one resolved data source and its outputs.
// Data sources are evaluated during the plan phase by calling the
// driver's Plan-time API to look up existing cloud resources.
type DataSourceResult struct {
	// Kind is the data source type (e.g., "data.aws_vpc", "data.aws_subnet").
	Kind string `json:"kind"`

	// Outputs are the resolved values from the cloud lookup.
	// These can be referenced in the template via data source expressions.
	Outputs map[string]any `json:"outputs"`
}

// DeleteDeploymentRequest starts a deployment-wide delete flow.
// The orchestrator walks the resource graph in reverse-dependency order,
// calling each driver's Delete handler. Resources with preventDestroy
// lifecycle policy will cause the delete to fail with an error.
type DeleteDeploymentRequest struct {
	// DeploymentKey identifies which deployment to tear down.
	DeploymentKey string `json:"deploymentKey"`

	// Force overrides lifecycle.preventDestroy protection on resources.
	// This is a release valve for stuck deployments where the normal
	// workflow of disabling preventDestroy via a re-deploy is impractical.
	Force bool `json:"force,omitempty"`

	// Orphan leaves resources running in the provider and only removes Praxis
	// management state when the workflow supports it.
	Orphan bool `json:"orphan,omitempty"`

	// Parallelism limits concurrent delete operations. Zero means unlimited.
	Parallelism int `json:"parallelism,omitempty"`
}

// DeleteDeploymentResponse confirms that the deletion workflow has been
// queued. The actual deletion proceeds asynchronously. Poll via GetDetail
// to track progress.
type DeleteDeploymentResponse struct {
	// DeploymentKey echoes back the requested deployment identifier.
	DeploymentKey string `json:"deploymentKey"`

	// Status is typically DeploymentDeleting at this point.
	Status DeploymentStatus `json:"status"`
}

// ImportRequest is the payload accepted by PraxisCommandService.Import.
// Import brings an existing cloud resource under Praxis management without
// creating it. The driver's Import handler looks up the resource by its
// cloud-native ID and captures a complete snapshot of its current state.
type ImportRequest struct {
	// Kind is the resource type to import (e.g., "S3Bucket", "VPC").
	// Must match a registered adapter in the provider registry.
	Kind string `json:"kind"`

	// ResourceID is the cloud-native identifier (e.g., bucket name, VPC ID).
	ResourceID string `json:"resourceId"`

	// Region is the AWS region where the resource exists. Required for
	// region-scoped resources; ignored for globally-scoped ones like S3.
	Region string `json:"region"`

	// Mode determines post-import behavior: Managed (drift corrected) or
	// Observed (drift reported only). Defaults to Managed.
	Mode Mode `json:"mode,omitempty"`

	// Account selects which AWS identity to use for the import lookup.
	Account string `json:"account,omitempty"`

	// Workspace scopes the import to a particular namespace.
	Workspace string `json:"workspace,omitempty"`
}

// ImportResponse mirrors the generic deployment-facing view of a driver import.
// Contains the driver key, resulting resource status, and any outputs captured
// from the existing cloud resource.
type ImportResponse struct {
	// Key is the canonical Restate Virtual Object key assigned to the resource.
	Key string `json:"key"`

	// Status is the resulting resource status, typically StatusReady on success.
	Status ResourceStatus `json:"status"`

	// Outputs contains the normalized output values captured from the existing
	// cloud resource (ARN, resource ID, etc.).
	Outputs map[string]any `json:"outputs,omitempty"`
}

// RegisterTemplateRequest registers or updates a template in the registry.
// If a template with the same name already exists and the digest differs,
// the existing source is preserved as PreviousSource/PreviousDigest for
// one generation of rollback visibility.
type RegisterTemplateRequest struct {
	// Name is the unique registry key for the template.
	Name string `json:"name"`

	// Source is the raw CUE template source code.
	Source string `json:"source"`

	// Description is an optional human-readable summary.
	Description string `json:"description,omitempty"`

	// Labels are optional key-value pairs for organization and filtering.
	Labels map[string]string `json:"labels,omitempty"`
}

// RegisterTemplateResponse is returned after a successful registration.
// The digest can be used to verify idempotency: if the same digest is
// returned, the registration was a no-op (source unchanged).
type RegisterTemplateResponse struct {
	// Name echoes back the template's registry key.
	Name string `json:"name"`

	// Digest is the SHA-256 hash of the registered source.
	Digest string `json:"digest"`
}

// DeleteTemplateRequest deletes a registered template from the registry.
// Deletion removes the template source and metadata. Existing deployments
// that were created from this template are not affected.
type DeleteTemplateRequest struct {
	// Name is the registry key of the template to delete.
	Name string `json:"name"`
}

// AddPolicyRequest adds a CUE policy constraint to a scope.
// The policy source is unified with templates at evaluation time;
// if unification fails, the violation is reported as a ValidationError.
type AddPolicyRequest struct {
	// Name is the unique identifier for this policy within its scope.
	Name string `json:"name"`

	// Scope determines the blast radius: global (all templates) or
	// template-specific.
	Scope PolicyScope `json:"scope"`

	// TemplateName is required when Scope is PolicyScopeTemplate.
	// Identifies which registered template this policy constrains.
	TemplateName string `json:"templateName,omitempty"`

	// Source is the raw CUE policy source code.
	Source string `json:"source"`

	// Description is an optional human-readable explanation of the policy.
	Description string `json:"description,omitempty"`
}

// RemovePolicyRequest removes a named policy from a scope.
// After removal, the policy is no longer evaluated during template processing.
type RemovePolicyRequest struct {
	// Name is the policy identifier to remove.
	Name string `json:"name"`

	// Scope identifies where the policy lives (global or template-scoped).
	Scope PolicyScope `json:"scope"`

	// TemplateName is required when Scope is PolicyScopeTemplate.
	TemplateName string `json:"templateName,omitempty"`
}

// ListPoliciesRequest lists all policies for a given scope.
// For global scope, returns all global policies. For template scope,
// returns only policies attached to the specified template.
type ListPoliciesRequest struct {
	// Scope filters by policy scope (global or template).
	Scope PolicyScope `json:"scope"`

	// TemplateName is required when Scope is PolicyScopeTemplate.
	TemplateName string `json:"templateName,omitempty"`
}

// GetPolicyRequest retrieves a specific policy by name and scope.
// Returns the full policy record including source code.
type GetPolicyRequest struct {
	// Name is the policy identifier to look up.
	Name string `json:"name"`

	// Scope identifies where the policy lives.
	Scope PolicyScope `json:"scope"`

	// TemplateName is required when Scope is PolicyScopeTemplate.
	TemplateName string `json:"templateName,omitempty"`
}

// ValidateMode controls template validation depth.
//   - Static: syntax and schema checks only (no cloud API calls).
//   - Full: includes plan-time lookups to verify resource state.
type ValidateMode string

const (
	// ValidateModeStatic performs CUE parsing, schema unification, and
	// policy evaluation without contacting cloud APIs.
	ValidateModeStatic ValidateMode = "static"

	// ValidateModeFull additionally performs plan-time describe calls
	// to validate references and detect pre-existing conflicts.
	ValidateModeFull ValidateMode = "full"
)

// ValidateTemplateRequest validates template source or a template reference
// against provider schemas and any applicable policies.
// Used by `praxis template validate` for pre-flight checks.
type ValidateTemplateRequest struct {
	// Source is raw CUE template source for inline validation.
	Source string `json:"source,omitempty"`

	// TemplateRef references a registered template to validate.
	TemplateRef *TemplateRef `json:"templateRef,omitempty"`

	// Variables are user-supplied values needed for full evaluation.
	Variables map[string]any `json:"variables,omitempty"`

	// Mode controls validation depth (static or full).
	Mode ValidateMode `json:"mode,omitempty"`
}

// ValidateTemplateResponse reports validation results.
// When Valid is false, Errors contains one or more diagnostics describing
// schema violations, constraint failures, or policy breaches.
type ValidateTemplateResponse struct {
	// Valid is true when no errors were found.
	Valid bool `json:"valid"`

	// Errors lists each individual validation diagnostic.
	Errors []ValidationError `json:"errors,omitempty"`
}

// ValidationError is one diagnostic from template validation.
// Each error pinpoints the exact CUE path where the issue was found
// and categorizes it by kind for programmatic handling.
type ValidationError struct {
	// Kind classifies the error: "schema" (CUE constraint), "variable"
	// (missing or wrong type), "policy" (policy violation), "parse" (syntax).
	Kind string `json:"kind"`

	// Path is the CUE path to the offending field (e.g., "resources.myVpc.spec.region").
	Path string `json:"path"`

	// Message is a short human-readable description of the issue.
	Message string `json:"message"`

	// Detail provides additional context, such as the expected type or
	// allowed values for disjunctions.
	Detail string `json:"detail"`

	// Policy is the name of the policy that was violated, if applicable.
	// Empty for non-policy errors.
	Policy string `json:"policy,omitempty"`
}

// DeployRequest is the user-facing deployment entry point.
// Unlike ApplyRequest (which accepts inline CUE), DeployRequest requires a
// pre-registered template. This is the recommended production path because
// it enforces version control and policy evaluation on registered templates.
type DeployRequest struct {
	// Template is the registered template name (required, must exist in registry).
	Template string `json:"template"`

	// Variables are user-provided values injected into the template.
	Variables map[string]any `json:"variables,omitempty"`

	// DeploymentKey is an optional stable identifier. If omitted, generated
	// from the template name and workspace.
	DeploymentKey string `json:"deploymentKey,omitempty"`

	// Account selects the AWS identity for this deployment.
	Account string `json:"account,omitempty"`

	// Workspace is an optional isolation namespace.
	Workspace string `json:"workspace,omitempty"`

	// Targets limits deployment to named resources plus their transitive dependencies.
	Targets []string `json:"targets,omitempty"`

	// OrphanRemoved keeps resources that disappeared from the template running in
	// the provider and only removes them from Praxis management state.
	OrphanRemoved bool `json:"orphanRemoved,omitempty"`

	// Replace forces a destroy-then-recreate cycle on the named resources.
	Replace []string `json:"replace,omitempty"`

	// AllowReplace, when true, automatically replaces resources that fail
	// with a 409 immutable-field conflict during provisioning.
	// WARNING: this destroys and recreates the resource, which may cause
	// downtime and data loss.
	AllowReplace bool `json:"allowReplace,omitempty"`

	// MaxParallelism limits concurrent resource operations. Zero means unlimited.
	MaxParallelism int `json:"maxParallelism,omitempty"`

	// MaxRetries overrides the deployment-wide default resource retry count.
	MaxRetries *int `json:"maxRetries,omitempty"`
}

// DeployResponse is returned after a deploy request is accepted.
// The deployment proceeds asynchronously in the orchestrator workflow.
type DeployResponse struct {
	// DeploymentKey is the stable identifier for tracking this deployment.
	DeploymentKey string `json:"deploymentKey"`

	// Status is the initial deployment status, typically DeploymentRunning.
	Status DeploymentStatus `json:"status"`
}

// PlanDeployRequest is the dry-run variant of DeployRequest.
// Evaluates the registered template with variables and computes diffs
// without dispatching any changes to drivers.
type PlanDeployRequest struct {
	// Template is the registered template name (required).
	Template string `json:"template"`

	// Variables are user-provided values for template evaluation.
	Variables map[string]any `json:"variables,omitempty"`

	// Account selects the AWS identity for plan-time lookups.
	Account string `json:"account,omitempty"`

	// Workspace scopes the plan to a particular namespace.
	Workspace string `json:"workspace,omitempty"`

	// Targets limits the plan to named resources plus dependencies.
	Targets []string `json:"targets,omitempty"`

	// DeploymentKey is an optional deployment key for prior state lookup.
	// See PlanRequest.DeploymentKey for details.
	DeploymentKey string `json:"deploymentKey,omitempty"`
}

// PlanDeployResponse contains the plan result and rendered template.
// Identical structure to PlanResponse but produced by the Deploy code path.
type PlanDeployResponse struct {
	// Plan holds resource-level diffs and summary counts.
	Plan *PlanResult `json:"plan"`

	// ExecutionPlan is the workflow-ready deployment plan that can be saved and
	// later applied without re-evaluating the template.
	ExecutionPlan *ExecutionPlan `json:"executionPlan,omitempty"`

	// Rendered is the fully-evaluated, sensitivity-masked template output.
	Rendered string `json:"rendered"`

	// TemplateHash is the SHA-256 digest of the template source used to build
	// this plan.
	TemplateHash string `json:"templateHash,omitempty"`

	// DataSources maps data source names to their resolved outputs.
	DataSources map[string]DataSourceResult `json:"dataSources,omitempty"`

	// Warnings contains non-fatal diagnostic messages from the plan pipeline.
	Warnings []string `json:"warnings,omitempty"`
}

// StateMvRequest is the CLI-facing payload for `praxis state mv`.
// Supports both renaming a resource within a deployment and moving
// a resource between deployments. The driver's Virtual Object key and
// state remain unchanged — only the deployment record is updated.
type StateMvRequest struct {
	// SourceDeployment is the deployment key containing the resource.
	SourceDeployment string `json:"sourceDeployment"`

	// ResourceName is the current template-local resource name.
	ResourceName string `json:"resourceName"`

	// DestDeployment is the target deployment key. When equal to
	// SourceDeployment this is a rename; when different, the resource is
	// moved across deployments.
	DestDeployment string `json:"destDeployment"`

	// NewName is the new resource name. If empty, the original name is kept.
	NewName string `json:"newName,omitempty"`
}

// StateMvResponse confirms a successful state move or rename.
// Contains both old and new names so the CLI can display the change.
type StateMvResponse struct {
	// SourceDeployment is the deployment the resource was moved from.
	SourceDeployment string `json:"sourceDeployment"`

	// DestDeployment is the deployment the resource was moved to.
	DestDeployment string `json:"destDeployment"`

	// OldName is the previous template-local resource name.
	OldName string `json:"oldName"`

	// NewName is the new template-local resource name.
	NewName string `json:"newName"`
}

// ApplySavedPlanRequest submits a previously saved execution plan directly to
// the command service without re-evaluating template source.
type ApplySavedPlanRequest struct {
	Plan ExecutionPlan `json:"plan"`

	// OrphanRemoved keeps resources that disappeared from the template running in
	// the provider and only removes them from Praxis management state.
	OrphanRemoved bool `json:"orphanRemoved,omitempty"`

	// MaxParallelism limits concurrent resource operations. Zero means unlimited.
	MaxParallelism int `json:"maxParallelism,omitempty"`

	// MaxRetries overrides the deployment-wide default resource retry count.
	MaxRetries *int `json:"maxRetries,omitempty"`
}

// SavedPlan captures a workflow-ready execution plan plus the dry-run diff and
// integrity metadata used for plan file workflows.
type SavedPlan struct {
	Version      int           `json:"version"`
	Plan         ExecutionPlan `json:"plan"`
	Diff         *PlanResult   `json:"diff,omitempty"`
	ContentHash  string        `json:"contentHash"`
	Signature    string        `json:"signature,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
	TemplateHash string        `json:"templateHash,omitempty"`
}

// ResourceStatusResponse holds the status returned by a driver's GetStatus
// shared handler. This is a lightweight query that does not trigger any
// cloud API calls — it reads the driver's locally cached state only.
type ResourceStatusResponse struct {
	// Status is the current lifecycle status of the resource.
	Status ResourceStatus `json:"status"`

	// Mode indicates whether the resource is Managed or Observed.
	Mode Mode `json:"mode"`

	// Generation is a monotonically increasing counter incremented on
	// each state-changing operation (Provision, Import, Reconcile).
	Generation int64 `json:"generation"`

	// Error holds the last error message, if the resource is in StatusError.
	Error string `json:"error,omitempty"`
}
