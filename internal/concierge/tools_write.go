package concierge

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// registerWriteTools adds all write (mutating) tools to the registry. These tools
// modify infrastructure state and ALL have RequiresApproval=true, meaning they
// trigger the human-in-the-loop approval flow:
//
//  1. LLM invokes the tool (e.g., "applyTemplate")
//  2. Session creates a Restate awakeable and suspends execution
//  3. Transport (CLI/Slack) shows the approval prompt with DescribeAction() output
//  4. Human approves → awakeable resolved → tool executes via PraxisCommandService
//  5. Human rejects → awakeable rejected → rejection message returned to LLM
//
// Write tools delegate to PraxisCommandService (a separate Restate service) for
// the actual infrastructure operations (Apply, Deploy, DeleteDeployment, Import).
func (r *ToolRegistry) registerWriteTools() {
	r.Register(&ToolDef{
		Name:             "applyTemplate",
		Description:      "Apply a CUE template to provision resources. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"template":      map[string]any{"type": "string", "description": "CUE template source"},
				"variables":     map[string]any{"type": "object", "description": "Template variables"},
				"deploymentKey": map[string]any{"type": "string", "description": "Deployment key (optional, auto-generated if omitted)"},
				"account":       map[string]any{"type": "string", "description": "AWS account alias"},
				"workspace":     map[string]any{"type": "string", "description": "Workspace name"},
				"targets":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Limit apply to these resource names plus their dependencies (optional)"},
				"replace":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Force destroy-then-recreate on these resource names (optional)"},
			},
			"required": []string{"template"},
		},
		Execute: toolApplyTemplate,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				DeploymentKey string `json:"deploymentKey"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			if args.DeploymentKey != "" {
				return fmt.Sprintf("Apply template to deployment %q", args.DeploymentKey)
			}
			return "Apply template (new deployment)"
		},
	})

	r.Register(&ToolDef{
		Name:             "deployTemplate",
		Description:      "Deploy from a registered template. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"templateName":  map[string]any{"type": "string", "description": "Registered template name"},
				"variables":     map[string]any{"type": "object", "description": "Template variables"},
				"deploymentKey": map[string]any{"type": "string", "description": "Deployment key (optional)"},
			},
			"required": []string{"templateName"},
		},
		Execute: toolDeployTemplate,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				TemplateName  string         `json:"templateName"`
				Variables     map[string]any `json:"variables"`
				DeploymentKey string         `json:"deploymentKey"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			desc := fmt.Sprintf("Deploy template %q", args.TemplateName)
			if args.DeploymentKey != "" {
				desc += fmt.Sprintf(" as %q", args.DeploymentKey)
			}
			if len(args.Variables) > 0 {
				vars, _ := json.Marshal(args.Variables)
				desc += fmt.Sprintf(" with variables: %s", string(vars))
			}
			return desc
		},
	})

	r.Register(&ToolDef{
		Name:             "deleteDeployment",
		Description:      "Delete all resources in a deployment. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deploymentKey": map[string]any{"type": "string", "description": "The deployment to delete"},
			},
			"required": []string{"deploymentKey"},
		},
		Execute: toolDeleteDeployment,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				DeploymentKey string `json:"deploymentKey"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			return fmt.Sprintf("Delete deployment %q and all its resources", args.DeploymentKey)
		},
	})

	r.Register(&ToolDef{
		Name:             "importResource",
		Description:      "Import an existing cloud resource into Praxis management. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":       map[string]any{"type": "string", "description": "Resource kind (e.g. S3Bucket)"},
				"resourceId": map[string]any{"type": "string", "description": "Cloud resource ID"},
				"region":     map[string]any{"type": "string", "description": "AWS region"},
				"mode":       map[string]any{"type": "string", "description": "Management mode: Managed or Observed"},
			},
			"required": []string{"kind", "resourceId", "region"},
		},
		Execute: toolImportResource,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				Kind       string `json:"kind"`
				ResourceID string `json:"resourceId"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			return fmt.Sprintf("Import %s %q into Praxis management", args.Kind, args.ResourceID)
		},
	})

	r.Register(&ToolDef{
		Name:             "rollbackDeployment",
		Description:      "Roll back a failed or cancelled deployment to its previous state. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deploymentKey": map[string]any{"type": "string", "description": "The deployment to roll back"},
			},
			"required": []string{"deploymentKey"},
		},
		Execute: toolRollbackDeployment,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				DeploymentKey string `json:"deploymentKey"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			return fmt.Sprintf("Roll back deployment %q to its previous state", args.DeploymentKey)
		},
	})

	r.Register(&ToolDef{
		Name:             "registerTemplate",
		Description:      "Register a CUE template in the template registry for reuse with 'praxis deploy'. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique template name for the registry"},
				"source":      map[string]any{"type": "string", "description": "CUE template source code"},
				"description": map[string]any{"type": "string", "description": "Human-readable description (optional)"},
			},
			"required": []string{"name", "source"},
		},
		Execute: toolRegisterTemplate,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			return fmt.Sprintf("Register template %q in the template registry", args.Name)
		},
	})

	r.Register(&ToolDef{
		Name:             "deleteTemplate",
		Description:      "Delete a registered template from the registry. Existing deployments are not affected. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Template name to delete"},
			},
			"required": []string{"name"},
		},
		Execute: toolDeleteTemplate,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			return fmt.Sprintf("Delete template %q from the registry", args.Name)
		},
	})

	r.Register(&ToolDef{
		Name:             "addPolicy",
		Description:      "Add a CUE policy constraint to enforce rules on templates. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":         map[string]any{"type": "string", "description": "Unique policy name within its scope"},
				"scope":        map[string]any{"type": "string", "description": "Policy scope: 'global' (all templates) or 'template' (specific template)"},
				"templateName": map[string]any{"type": "string", "description": "Target template name (required when scope is 'template')"},
				"source":       map[string]any{"type": "string", "description": "CUE policy source code"},
				"description":  map[string]any{"type": "string", "description": "Human-readable description of the policy (optional)"},
			},
			"required": []string{"name", "scope", "source"},
		},
		Execute: toolAddPolicy,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				Name  string `json:"name"`
				Scope string `json:"scope"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			return fmt.Sprintf("Add %s policy %q", args.Scope, args.Name)
		},
	})

	r.Register(&ToolDef{
		Name:             "removePolicy",
		Description:      "Remove a policy constraint. Requires approval.",
		RequiresApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":         map[string]any{"type": "string", "description": "Policy name to remove"},
				"scope":        map[string]any{"type": "string", "description": "Policy scope: 'global' or 'template'"},
				"templateName": map[string]any{"type": "string", "description": "Template name (required when scope is 'template')"},
			},
			"required": []string{"name", "scope"},
		},
		Execute: toolRemovePolicy,
		DescribeAction: func(argsJSON string) string {
			var args struct {
				Name  string `json:"name"`
				Scope string `json:"scope"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			return fmt.Sprintf("Remove %s policy %q", args.Scope, args.Name)
		},
	})
}

// toolApplyTemplate applies a raw CUE template to provision resources via
// PraxisCommandService.Apply. Falls back to session's account/workspace if not
// provided in the arguments.
func toolApplyTemplate(ctx restate.Context, argsJSON string, session SessionState) (string, error) {
	var args struct {
		Template      string         `json:"template"`
		Variables     map[string]any `json:"variables"`
		DeploymentKey string         `json:"deploymentKey,omitempty"`
		Account       string         `json:"account"`
		Workspace     string         `json:"workspace"`
		Targets       []string       `json:"targets"`
		Replace       []string       `json:"replace"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	account := args.Account
	if account == "" {
		account = session.Account
	}
	workspace := args.Workspace
	if workspace == "" {
		workspace = session.Workspace
	}

	resp, err := restate.Service[types.ApplyResponse](
		ctx, "PraxisCommandService", "Apply",
	).Request(types.ApplyRequest{
		Template:      args.Template,
		Variables:     args.Variables,
		DeploymentKey: args.DeploymentKey,
		Account:       account,
		Workspace:     workspace,
		Targets:       args.Targets,
		Replace:       args.Replace,
	})
	if err != nil {
		return fmt.Sprintf("Apply failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Deployment submitted.\nKey: %s\nStatus: %s", resp.DeploymentKey, resp.Status), nil
}

// toolDeployTemplate deploys from a registered template (by name) via
// PraxisCommandService.Deploy. Uses the session's account/workspace context.
func toolDeployTemplate(ctx restate.Context, argsJSON string, session SessionState) (string, error) {
	var args struct {
		TemplateName  string         `json:"templateName"`
		Variables     map[string]any `json:"variables"`
		DeploymentKey string         `json:"deploymentKey,omitempty"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	resp, err := restate.Service[types.DeployResponse](
		ctx, "PraxisCommandService", "Deploy",
	).Request(types.DeployRequest{
		Template:      args.TemplateName,
		Variables:     args.Variables,
		DeploymentKey: args.DeploymentKey,
		Account:       session.Account,
		Workspace:     session.Workspace,
	})
	if err != nil {
		return fmt.Sprintf("Deploy failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Deployment submitted.\nKey: %s\nStatus: %s", resp.DeploymentKey, resp.Status), nil
}

// toolDeleteDeployment deletes all resources in a deployment via
// PraxisCommandService.DeleteDeployment.
func toolDeleteDeployment(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		DeploymentKey string `json:"deploymentKey"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.DeploymentKey == "" {
		return "Error: deploymentKey is required", nil
	}

	resp, err := restate.Service[types.DeleteDeploymentResponse](
		ctx, "PraxisCommandService", "DeleteDeployment",
	).Request(types.DeleteDeploymentRequest{
		DeploymentKey: args.DeploymentKey,
	})
	if err != nil {
		return fmt.Sprintf("Delete failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Deletion started.\nKey: %s\nStatus: %s", resp.DeploymentKey, resp.Status), nil
}

// toolImportResource imports an existing cloud resource into Praxis management via
// PraxisCommandService.Import. Supports Managed (full control) and Observed (read-only)
// modes. Uses the session's account/workspace context.
func toolImportResource(ctx restate.Context, argsJSON string, session SessionState) (string, error) {
	var args struct {
		Kind       string `json:"kind"`
		ResourceID string `json:"resourceId"`
		Region     string `json:"region"`
		Mode       string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Kind == "" || args.ResourceID == "" || args.Region == "" {
		return "Error: kind, resourceId, and region are required", nil
	}

	mode := types.ModeManaged
	if args.Mode == "Observed" {
		mode = types.ModeObserved
	}

	resp, err := restate.Service[types.ImportResponse](
		ctx, "PraxisCommandService", "Import",
	).Request(types.ImportRequest{
		Kind:       args.Kind,
		ResourceID: args.ResourceID,
		Region:     args.Region,
		Mode:       mode,
		Account:    session.Account,
		Workspace:  session.Workspace,
	})
	if err != nil {
		return fmt.Sprintf("Import failed: %s", err.Error()), nil
	}

	result, _ := json.MarshalIndent(resp, "", "  ")
	return string(result), nil
}

// toolRollbackDeployment rolls back a failed or cancelled deployment via
// PraxisCommandService.RollbackDeployment.
func toolRollbackDeployment(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		DeploymentKey string `json:"deploymentKey"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.DeploymentKey == "" {
		return "Error: deploymentKey is required", nil
	}

	resp, err := restate.Service[types.DeleteDeploymentResponse](
		ctx, "PraxisCommandService", "RollbackDeployment",
	).Request(types.DeleteDeploymentRequest{
		DeploymentKey: args.DeploymentKey,
	})
	if err != nil {
		return fmt.Sprintf("Rollback failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Rollback started.\nKey: %s\nStatus: %s", resp.DeploymentKey, resp.Status), nil
}

// toolRegisterTemplate registers a CUE template in the template registry via
// PraxisCommandService.RegisterTemplate.
func toolRegisterTemplate(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Name        string `json:"name"`
		Source      string `json:"source"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Name == "" || args.Source == "" {
		return "Error: name and source are required", nil
	}

	resp, err := restate.Service[types.RegisterTemplateResponse](
		ctx, "PraxisCommandService", "RegisterTemplate",
	).Request(types.RegisterTemplateRequest{
		Name:        args.Name,
		Source:      args.Source,
		Description: args.Description,
	})
	if err != nil {
		return fmt.Sprintf("Registration failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Template registered.\nName: %s\nDigest: %s", resp.Name, resp.Digest), nil
}

// toolDeleteTemplate deletes a registered template via PraxisCommandService.DeleteTemplate.
func toolDeleteTemplate(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Name == "" {
		return "Error: name is required", nil
	}

	_, err := restate.Service[restate.Void](
		ctx, "PraxisCommandService", "DeleteTemplate",
	).Request(types.DeleteTemplateRequest{
		Name: args.Name,
	})
	if err != nil {
		return fmt.Sprintf("Delete failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Template %q deleted.", args.Name), nil
}

// toolAddPolicy adds a CUE policy via PraxisCommandService.AddPolicy.
func toolAddPolicy(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Name         string `json:"name"`
		Scope        string `json:"scope"`
		TemplateName string `json:"templateName"`
		Source       string `json:"source"`
		Description  string `json:"description"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Name == "" || args.Scope == "" || args.Source == "" {
		return "Error: name, scope, and source are required", nil
	}

	_, err := restate.Service[restate.Void](
		ctx, "PraxisCommandService", "AddPolicy",
	).Request(types.AddPolicyRequest{
		Name:         args.Name,
		Scope:        types.PolicyScope(args.Scope),
		TemplateName: args.TemplateName,
		Source:       args.Source,
		Description:  args.Description,
	})
	if err != nil {
		return fmt.Sprintf("Add policy failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Policy %q added to %s scope.", args.Name, args.Scope), nil
}

// toolRemovePolicy removes a policy via PraxisCommandService.RemovePolicy.
func toolRemovePolicy(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Name         string `json:"name"`
		Scope        string `json:"scope"`
		TemplateName string `json:"templateName"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Name == "" || args.Scope == "" {
		return "Error: name and scope are required", nil
	}

	_, err := restate.Service[restate.Void](
		ctx, "PraxisCommandService", "RemovePolicy",
	).Request(types.RemovePolicyRequest{
		Name:         args.Name,
		Scope:        types.PolicyScope(args.Scope),
		TemplateName: args.TemplateName,
	})
	if err != nil {
		return fmt.Sprintf("Remove policy failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Policy %q removed from %s scope.", args.Name, args.Scope), nil
}
