package command

import "github.com/shirvan/praxis/pkg/types"

type (
	ApplyRequest             = types.ApplyRequest
	ApplyResponse            = types.ApplyResponse
	PlanRequest              = types.PlanRequest
	PlanResponse             = types.PlanResponse
	DeleteDeploymentRequest  = types.DeleteDeploymentRequest
	DeleteDeploymentResponse = types.DeleteDeploymentResponse
	ImportRequest            = types.ImportRequest
	ImportResponse           = types.ImportResponse
	RegisterTemplateRequest  = types.RegisterTemplateRequest
	RegisterTemplateResponse = types.RegisterTemplateResponse
	DeleteTemplateRequest    = types.DeleteTemplateRequest
	ValidateTemplateRequest  = types.ValidateTemplateRequest
	ValidateTemplateResponse = types.ValidateTemplateResponse
	AddPolicyRequest         = types.AddPolicyRequest
	RemovePolicyRequest      = types.RemovePolicyRequest
	ListPoliciesRequest      = types.ListPoliciesRequest
	GetPolicyRequest         = types.GetPolicyRequest
	DeployRequest            = types.DeployRequest
	DeployResponse           = types.DeployResponse
	PlanDeployRequest        = types.PlanDeployRequest
	PlanDeployResponse       = types.PlanDeployResponse
)
