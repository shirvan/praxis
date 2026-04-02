package concierge

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

func (r *ToolRegistry) registerReadTools() {
	r.Register(&ToolDef{
		Name:        "getDeploymentStatus",
		Description: "Get the current status and details of a deployment",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deploymentKey": map[string]any{"type": "string", "description": "The deployment key to look up"},
			},
			"required": []string{"deploymentKey"},
		},
		Execute: toolGetDeploymentStatus,
	})

	r.Register(&ToolDef{
		Name:        "listDeployments",
		Description: "List all active deployments, optionally filtered by workspace",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{"type": "string", "description": "Filter by workspace (optional)"},
			},
		},
		Execute: toolListDeployments,
	})

	r.Register(&ToolDef{
		Name:        "listTemplates",
		Description: "List all registered templates",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: toolListTemplates,
	})

	r.Register(&ToolDef{
		Name:        "describeTemplate",
		Description: "Get template details including variable schema and metadata",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"templateName": map[string]any{"type": "string", "description": "Name of the template"},
			},
			"required": []string{"templateName"},
		},
		Execute: toolDescribeTemplate,
	})

	r.Register(&ToolDef{
		Name:        "getTemplateSource",
		Description: "Get the raw CUE source of a registered template",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"templateName": map[string]any{"type": "string", "description": "Name of the template"},
			},
			"required": []string{"templateName"},
		},
		Execute: toolGetTemplateSource,
	})

	r.Register(&ToolDef{
		Name:        "getResourceOutputs",
		Description: "Get the outputs (IDs, ARNs, endpoints) of a provisioned resource",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "description": "Resource kind (e.g. S3Bucket, SecurityGroup)"},
				"key":  map[string]any{"type": "string", "description": "Resource key"},
			},
			"required": []string{"kind", "key"},
		},
		Execute: toolGetResourceOutputs,
	})

	r.Register(&ToolDef{
		Name:        "getDrift",
		Description: "Check if a resource has drifted from its desired state",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "description": "Resource kind"},
				"key":  map[string]any{"type": "string", "description": "Resource key"},
			},
			"required": []string{"kind", "key"},
		},
		Execute: toolGetDrift,
	})

	r.Register(&ToolDef{
		Name:        "planDeployment",
		Description: "Run a plan (dry-run) to see what would change. Plans are read-only.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"template":  map[string]any{"type": "string", "description": "CUE template source"},
				"variables": map[string]any{"type": "object", "description": "Template variables"},
				"account":   map[string]any{"type": "string", "description": "AWS account alias"},
				"workspace": map[string]any{"type": "string", "description": "Workspace name"},
			},
			"required": []string{"template"},
		},
		Execute: toolPlanDeployment,
	})

	r.Register(&ToolDef{
		Name:        "listWorkspaces",
		Description: "List all workspaces",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: toolListWorkspaces,
	})
}

func toolGetDeploymentStatus(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		DeploymentKey string `json:"deploymentKey"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.DeploymentKey == "" {
		return "Error: deploymentKey is required", nil
	}

	detail, err := restate.Object[*types.DeploymentDetail](
		ctx, "DeploymentStateObj", args.DeploymentKey, "GetDetail",
	).Request(restate.Void{})
	if err != nil {
		return fmt.Sprintf("Error fetching deployment: %s", err.Error()), nil
	}

	result, _ := json.MarshalIndent(detail, "", "  ")
	return string(result), nil
}

func toolListDeployments(ctx restate.Context, argsJSON string, session SessionState) (string, error) {
	var args struct {
		Workspace string `json:"workspace"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)

	ws := args.Workspace
	if ws == "" {
		ws = session.Workspace
	}

	type listFilter struct {
		Workspace string `json:"workspace"`
	}
	deployments, err := restate.Object[[]types.DeploymentSummary](
		ctx, "DeploymentIndex", "global", "List",
	).Request(listFilter{Workspace: ws})
	if err != nil {
		return fmt.Sprintf("Error listing deployments: %s", err.Error()), nil
	}

	if len(deployments) == 0 {
		return "No deployments found.", nil
	}

	result, _ := json.MarshalIndent(deployments, "", "  ")
	return string(result), nil
}

func toolListTemplates(ctx restate.Context, _ string, _ SessionState) (string, error) {
	templates, err := restate.Object[[]types.TemplateMetadata](
		ctx, "TemplateIndex", "global", "List",
	).Request(restate.Void{})
	if err != nil {
		return fmt.Sprintf("Error listing templates: %s", err.Error()), nil
	}

	if len(templates) == 0 {
		return "No templates registered.", nil
	}

	result, _ := json.MarshalIndent(templates, "", "  ")
	return string(result), nil
}

func toolDescribeTemplate(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		TemplateName string `json:"templateName"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.TemplateName == "" {
		return "Error: templateName is required", nil
	}

	metadata, err := restate.Object[types.TemplateMetadata](
		ctx, "TemplateRegistry", args.TemplateName, "GetMetadata",
	).Request(restate.Void{})
	if err != nil {
		return fmt.Sprintf("Error: %s", err.Error()), nil
	}

	result, _ := json.MarshalIndent(metadata, "", "  ")
	return string(result), nil
}

func toolGetTemplateSource(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		TemplateName string `json:"templateName"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.TemplateName == "" {
		return "Error: templateName is required", nil
	}

	source, err := restate.Object[string](
		ctx, "TemplateRegistry", args.TemplateName, "GetSource",
	).Request(restate.Void{})
	if err != nil {
		return fmt.Sprintf("Error: %s", err.Error()), nil
	}

	return source, nil
}

func toolGetResourceOutputs(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Kind string `json:"kind"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Kind == "" || args.Key == "" {
		return "Error: kind and key are required", nil
	}

	outputs, err := restate.Object[types.ResourceOutputs](
		ctx, args.Kind, args.Key, "GetOutputs",
	).Request(restate.Void{})
	if err != nil {
		return fmt.Sprintf("Error: %s", err.Error()), nil
	}

	result, _ := json.MarshalIndent(outputs, "", "  ")
	return string(result), nil
}

func toolGetDrift(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Kind string `json:"kind"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Kind == "" || args.Key == "" {
		return "Error: kind and key are required", nil
	}

	status, err := restate.Object[types.StatusResponse](
		ctx, args.Kind, args.Key, "GetStatus",
	).Request(restate.Void{})
	if err != nil {
		return fmt.Sprintf("Error: %s", err.Error()), nil
	}

	result, _ := json.MarshalIndent(status, "", "  ")
	return string(result), nil
}

func toolPlanDeployment(ctx restate.Context, argsJSON string, session SessionState) (string, error) {
	var args struct {
		Template  string         `json:"template"`
		Variables map[string]any `json:"variables"`
		Account   string         `json:"account"`
		Workspace string         `json:"workspace"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Template == "" {
		return "Error: template is required", nil
	}

	account := args.Account
	if account == "" {
		account = session.Account
	}
	workspace := args.Workspace
	if workspace == "" {
		workspace = session.Workspace
	}

	planResp, err := restate.Service[types.PlanResponse](
		ctx, "PraxisCommandService", "Plan",
	).Request(types.PlanRequest{
		Template:  args.Template,
		Variables: args.Variables,
		Account:   account,
		Workspace: workspace,
	})
	if err != nil {
		return fmt.Sprintf("Plan failed: %s", err.Error()), nil
	}

	result, _ := json.MarshalIndent(planResp, "", "  ")
	return string(result), nil
}

func toolListWorkspaces(ctx restate.Context, _ string, _ SessionState) (string, error) {
	type WorkspaceSummary struct {
		Name string `json:"name"`
	}
	workspaces, err := restate.Object[[]WorkspaceSummary](
		ctx, "WorkspaceIndex", "global", "List",
	).Request(restate.Void{})
	if err != nil {
		return fmt.Sprintf("Error listing workspaces: %s", err.Error()), nil
	}

	if len(workspaces) == 0 {
		return "No workspaces found.", nil
	}

	result, _ := json.MarshalIndent(workspaces, "", "  ")
	return string(result), nil
}
