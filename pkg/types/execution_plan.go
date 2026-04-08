package types

import "encoding/json"

// ExecutionPlan is a serializable, workflow-ready deployment plan used for
// saved plan files and plan-based deploys.
type ExecutionPlan struct {
	DeploymentKey string                  `json:"deploymentKey"`
	Account       string                  `json:"account,omitempty"`
	Workspace     string                  `json:"workspace,omitempty"`
	Variables     map[string]any          `json:"variables,omitempty"`
	TemplatePath  string                  `json:"templatePath,omitempty"`
	Targets       []string                `json:"targets,omitempty"`
	Resources     []ExecutionPlanResource `json:"resources"`
}

// ExecutionPlanResource is the public wire representation of one
// workflow-ready resource entry in an execution plan.
type ExecutionPlanResource struct {
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`
	DriverService string            `json:"driverService,omitempty"`
	Key           string            `json:"key"`
	Spec          json.RawMessage   `json:"spec"`
	Dependencies  []string          `json:"dependencies,omitempty"`
	Expressions   map[string]string `json:"expressions,omitempty"`
	Lifecycle     *LifecyclePolicy  `json:"lifecycle,omitempty"`
}
