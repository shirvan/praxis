package concierge

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

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
}

func toolApplyTemplate(ctx restate.Context, argsJSON string, session SessionState) (string, error) {
	var args struct {
		Template      string         `json:"template"`
		Variables     map[string]any `json:"variables"`
		DeploymentKey string         `json:"deploymentKey,omitempty"`
		Account       string         `json:"account"`
		Workspace     string         `json:"workspace"`
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
	})
	if err != nil {
		return fmt.Sprintf("Apply failed: %s", err.Error()), nil
	}

	return fmt.Sprintf("Deployment submitted.\nKey: %s\nStatus: %s", resp.DeploymentKey, resp.Status), nil
}

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
