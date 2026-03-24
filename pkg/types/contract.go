package types

// ApplyRequest is the payload accepted by PraxisCommandService.Apply.
// Exactly one of Template or TemplateRef must be provided.
type ApplyRequest struct {
	Template      string         `json:"template,omitempty"`
	TemplateRef   *TemplateRef   `json:"templateRef,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	DeploymentKey string         `json:"deploymentKey,omitempty"`
	Account       string         `json:"account,omitempty"`
	Workspace     string         `json:"workspace,omitempty"`
	Targets       []string       `json:"targets,omitempty"`
	Replace       []string       `json:"replace,omitempty"`
}

// ApplyResponse is returned immediately after an apply request is accepted.
type ApplyResponse struct {
	DeploymentKey string           `json:"deploymentKey"`
	Status        DeploymentStatus `json:"status"`
	Plan          *PlanResult      `json:"plan,omitempty"`
}

// PlanRequest is the payload accepted by PraxisCommandService.Plan.
// Exactly one of Template or TemplateRef must be provided.
type PlanRequest struct {
	Template    string         `json:"template,omitempty"`
	TemplateRef *TemplateRef   `json:"templateRef,omitempty"`
	Variables   map[string]any `json:"variables,omitempty"`
	Account     string         `json:"account,omitempty"`
	Workspace   string         `json:"workspace,omitempty"`
	Targets     []string       `json:"targets,omitempty"`
}

// PlanResponse contains the machine-readable plan result and rendered output.
type PlanResponse struct {
	Plan        *PlanResult                 `json:"plan"`
	Rendered    string                      `json:"rendered"`
	DataSources map[string]DataSourceResult `json:"dataSources,omitempty"`
}

// DataSourceResult shows one resolved data source and its outputs.
type DataSourceResult struct {
	Kind    string         `json:"kind"`
	Outputs map[string]any `json:"outputs"`
}

// DeleteDeploymentRequest starts a deployment-wide delete flow.
type DeleteDeploymentRequest struct {
	DeploymentKey string `json:"deploymentKey"`
}

// DeleteDeploymentResponse confirms that deletion has been queued.
type DeleteDeploymentResponse struct {
	DeploymentKey string           `json:"deploymentKey"`
	Status        DeploymentStatus `json:"status"`
}

// ImportRequest is the payload accepted by PraxisCommandService.Import.
type ImportRequest struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resourceId"`
	Region     string `json:"region"`
	Mode       Mode   `json:"mode,omitempty"`
	Account    string `json:"account,omitempty"`
	Workspace  string `json:"workspace,omitempty"`
}

// ImportResponse mirrors the generic deployment-facing view of a driver import.
type ImportResponse struct {
	Key     string         `json:"key"`
	Status  ResourceStatus `json:"status"`
	Outputs map[string]any `json:"outputs,omitempty"`
}

// RegisterTemplateRequest registers or updates a template in the registry.
type RegisterTemplateRequest struct {
	Name        string            `json:"name"`
	Source      string            `json:"source"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// RegisterTemplateResponse is returned after a successful registration.
type RegisterTemplateResponse struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// DeleteTemplateRequest deletes a registered template.
type DeleteTemplateRequest struct {
	Name string `json:"name"`
}

// AddPolicyRequest adds a policy to a scope.
type AddPolicyRequest struct {
	Name         string      `json:"name"`
	Scope        PolicyScope `json:"scope"`
	TemplateName string      `json:"templateName,omitempty"`
	Source       string      `json:"source"`
	Description  string      `json:"description,omitempty"`
}

// RemovePolicyRequest removes a named policy from a scope.
type RemovePolicyRequest struct {
	Name         string      `json:"name"`
	Scope        PolicyScope `json:"scope"`
	TemplateName string      `json:"templateName,omitempty"`
}

// ListPoliciesRequest lists policies for a scope.
type ListPoliciesRequest struct {
	Scope        PolicyScope `json:"scope"`
	TemplateName string      `json:"templateName,omitempty"`
}

// GetPolicyRequest retrieves a specific policy by name and scope.
type GetPolicyRequest struct {
	Name         string      `json:"name"`
	Scope        PolicyScope `json:"scope"`
	TemplateName string      `json:"templateName,omitempty"`
}

// ValidateMode controls template validation depth.
type ValidateMode string

const (
	ValidateModeStatic ValidateMode = "static"
	ValidateModeFull   ValidateMode = "full"
)

// ValidateTemplateRequest validates template source or a template reference.
type ValidateTemplateRequest struct {
	Source      string         `json:"source,omitempty"`
	TemplateRef *TemplateRef   `json:"templateRef,omitempty"`
	Variables   map[string]any `json:"variables,omitempty"`
	Mode        ValidateMode   `json:"mode,omitempty"`
}

// ValidateTemplateResponse reports validation results.
type ValidateTemplateResponse struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors,omitempty"`
}

// ValidationError is one diagnostic from template validation.
type ValidationError struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Policy  string `json:"policy,omitempty"`
}

// DeployRequest is the user-facing deployment entry point.
// The template must be pre-registered in the TemplateRegistry.
type DeployRequest struct {
	Template      string         `json:"template"`                // registered template name (required)
	Variables     map[string]any `json:"variables,omitempty"`     // user-provided variables
	DeploymentKey string         `json:"deploymentKey,omitempty"` // optional stable key
	Account       string         `json:"account,omitempty"`       // AWS account override
	Workspace     string         `json:"workspace,omitempty"`     // workspace context
	Targets       []string       `json:"targets,omitempty"`       // limit to named resources + deps
	Replace       []string       `json:"replace,omitempty"`       // force Delete→Provision on named resources
}

// DeployResponse is returned after a deploy request is accepted.
type DeployResponse struct {
	DeploymentKey string           `json:"deploymentKey"`
	Status        DeploymentStatus `json:"status"`
}

// PlanDeployRequest is the dry-run variant of DeployRequest.
type PlanDeployRequest struct {
	Template  string         `json:"template"`            // registered template name (required)
	Variables map[string]any `json:"variables,omitempty"` // user-provided variables
	Account   string         `json:"account,omitempty"`   // AWS account override
	Workspace string         `json:"workspace,omitempty"` // workspace context
	Targets   []string       `json:"targets,omitempty"`   // limit to named resources + deps
}

// PlanDeployResponse contains the plan result and rendered template.
type PlanDeployResponse struct {
	Plan        *PlanResult                 `json:"plan"`
	Rendered    string                      `json:"rendered"`
	DataSources map[string]DataSourceResult `json:"dataSources,omitempty"`
}

// StateMvRequest is the CLI-facing payload for `praxis state mv`.
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
type StateMvResponse struct {
	SourceDeployment string `json:"sourceDeployment"`
	DestDeployment   string `json:"destDeployment"`
	OldName          string `json:"oldName"`
	NewName          string `json:"newName"`
}

// ResourceStatusResponse holds the status returned by a driver's GetStatus handler.
type ResourceStatusResponse struct {
	Status     ResourceStatus `json:"status"`
	Mode       Mode           `json:"mode"`
	Generation int64          `json:"generation"`
	Error      string         `json:"error,omitempty"`
}
