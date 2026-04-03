// types.go re-exports the public request/response types used by the command
// service handlers. The canonical definitions live in pkg/types so that the
// CLI, API gateway, and integration tests can reference them without importing
// internal packages. The aliases here keep handler signatures short and let
// the command package evolve its wire types independently if needed.
package command

import "github.com/shirvan/praxis/pkg/types"

type (
	// --- Deployment lifecycle ---

	// ApplyRequest carries the user's intent to create or update a deployment
	// from an inline template or a registered template reference.
	ApplyRequest = types.ApplyRequest
	// ApplyResponse returns the deployment key and initial status after
	// the async deployment workflow has been enqueued.
	ApplyResponse = types.ApplyResponse

	// PlanRequest is the dry-run counterpart to ApplyRequest – it runs the
	// full evaluation pipeline but never mutates durable state.
	PlanRequest = types.PlanRequest
	// PlanResponse contains the rendered template, resolved data sources,
	// and per-resource diff plan.
	PlanResponse = types.PlanResponse

	// DeleteDeploymentRequest identifies a deployment to destroy. The command
	// service validates state guards and dispatches a delete workflow.
	DeleteDeploymentRequest = types.DeleteDeploymentRequest
	// DeleteDeploymentResponse confirms the deployment entered the deleting
	// state (or was already deleting).
	DeleteDeploymentResponse = types.DeleteDeploymentResponse

	// ImportRequest describes a single cloud resource to adopt into Praxis
	// management without recreating it.
	ImportRequest = types.ImportRequest
	// ImportResponse returns the canonical resource key, resolved status,
	// and outputs after a successful import.
	ImportResponse = types.ImportResponse

	// --- Template registry ---

	// RegisterTemplateRequest persists a CUE template in the durable
	// template registry virtual object.
	RegisterTemplateRequest = types.RegisterTemplateRequest
	// RegisterTemplateResponse confirms registration with the template
	// name and version metadata.
	RegisterTemplateResponse = types.RegisterTemplateResponse
	// DeleteTemplateRequest identifies a template to remove from the registry.
	DeleteTemplateRequest = types.DeleteTemplateRequest
	// ValidateTemplateRequest asks for static or full validation of a template.
	ValidateTemplateRequest = types.ValidateTemplateRequest
	// ValidateTemplateResponse reports whether the template is valid and
	// lists any structured validation errors.
	ValidateTemplateResponse = types.ValidateTemplateResponse

	// --- Policy registry ---

	// AddPolicyRequest attaches a CUE policy to global or template scope.
	AddPolicyRequest = types.AddPolicyRequest
	// RemovePolicyRequest removes a named policy from a scope.
	RemovePolicyRequest = types.RemovePolicyRequest
	// ListPoliciesRequest enumerates policies in a given scope.
	ListPoliciesRequest = types.ListPoliciesRequest
	// GetPolicyRequest fetches a single policy by name and scope.
	GetPolicyRequest = types.GetPolicyRequest

	// --- Deploy (schema-validated) ---

	// DeployRequest is the schema-validated variant of ApplyRequest. It
	// requires a pre-registered template and validates variables against the
	// template's declared variable schema before evaluation.
	DeployRequest = types.DeployRequest
	// DeployResponse mirrors ApplyResponse: deployment key + initial status.
	DeployResponse = types.DeployResponse

	// PlanDeployRequest is the dry-run variant of DeployRequest – validates
	// variables against the registered schema, then produces a plan.
	PlanDeployRequest = types.PlanDeployRequest
	// PlanDeployResponse mirrors PlanResponse for the Deploy path.
	PlanDeployResponse = types.PlanDeployResponse
)
