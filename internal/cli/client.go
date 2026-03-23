package cli

import (
	"context"
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

// --------------------------------------------------------------------------
// Restate service name and handler constants
// --------------------------------------------------------------------------

// These must match the service names registered in cmd/praxis-core/main.go.
// They are repeated here rather than importing the internal packages because
// the CLI should depend only on stable contract strings.
const (
	commandServiceName = "PraxisCommandService"
	stateServiceName   = "DeploymentStateObj"
	indexServiceName   = "DeploymentIndex"
	eventsServiceName  = "DeploymentEvents"
	indexGlobalKey     = "global"
)

// --------------------------------------------------------------------------
// Client
// --------------------------------------------------------------------------

// Client wraps Restate ingress calls for the Praxis CLI. It encapsulates the
// typed service invocations so that command implementations stay focused on
// user interaction rather than HTTP plumbing.
//
// Every method maps to exactly one Restate handler call, which keeps the CLI
// thin and avoids duplicating any business logic that belongs in Core.
type Client struct {
	rc *ingress.Client
}

// NewClient creates a new CLI client pointed at the given Restate ingress URL.
func NewClient(endpoint string) *Client {
	return &Client{
		rc: ingress.NewClient(endpoint),
	}
}

// --------------------------------------------------------------------------
// Command service calls (Apply, Plan, Delete, Import)
// --------------------------------------------------------------------------

// Apply submits a CUE template for provisioning through the command service.
// The command service evaluates the template, builds the DAG, and starts the
// deployment workflow asynchronously.
func (c *Client) Apply(ctx context.Context, req types.ApplyRequest) (*types.ApplyResponse, error) {
	resp, err := ingress.Service[types.ApplyRequest, types.ApplyResponse](c.rc, commandServiceName, "Apply").
		Request(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("apply: %w", err)
	}
	return &resp, nil
}

// Plan performs a dry-run evaluation of a CUE template. It returns the plan
// result showing what would change if applied, without actually provisioning
// any resources.
func (c *Client) Plan(ctx context.Context, req types.PlanRequest) (*types.PlanResponse, error) {
	resp, err := ingress.Service[types.PlanRequest, types.PlanResponse](c.rc, commandServiceName, "Plan").
		Request(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	return &resp, nil
}

// DeleteDeployment starts an asynchronous deletion of all resources in a
// deployment, in reverse dependency order.
func (c *Client) DeleteDeployment(ctx context.Context, key string) (*types.DeleteDeploymentResponse, error) {
	resp, err := ingress.Service[types.DeleteDeploymentRequest, types.DeleteDeploymentResponse](
		c.rc, commandServiceName, "DeleteDeployment",
	).Request(ctx, types.DeleteDeploymentRequest{DeploymentKey: key})
	if err != nil {
		return nil, fmt.Errorf("delete deployment: %w", err)
	}
	return &resp, nil
}

// ImportResource adopts an existing cloud resource under Praxis management.
func (c *Client) ImportResource(ctx context.Context, req types.ImportRequest) (*types.ImportResponse, error) {
	resp, err := ingress.Service[types.ImportRequest, types.ImportResponse](
		c.rc, commandServiceName, "Import",
	).Request(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	return &resp, nil
}

// --------------------------------------------------------------------------
// Read model calls (Deployment state, index, events)
// --------------------------------------------------------------------------

// GetDeployment returns the full deployment detail from the DeploymentState
// virtual object. This is the primary read path for `praxis get Deployment/<key>`.
func (c *Client) GetDeployment(ctx context.Context, key string) (*types.DeploymentDetail, error) {
	resp, err := ingress.Object[restate.Void, *types.DeploymentDetail](
		c.rc, stateServiceName, key, "GetDetail",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %q: %w", key, err)
	}
	return resp, nil
}

// ListDeployments returns all deployment summaries from the global index.
func (c *Client) ListDeployments(ctx context.Context) ([]types.DeploymentSummary, error) {
	resp, err := ingress.Object[restate.Void, []types.DeploymentSummary](
		c.rc, indexServiceName, indexGlobalKey, "List",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	return resp, nil
}

// GetDeploymentEvents returns deployment progress events with sequence > since.
// This is used by the observe command for incremental progress polling.
func (c *Client) GetDeploymentEvents(ctx context.Context, key string, since int64) ([]orchestrator.DeploymentEvent, error) {
	resp, err := ingress.Object[int64, []orchestrator.DeploymentEvent](
		c.rc, eventsServiceName, key, "ListSince",
	).Request(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("get events for %q: %w", key, err)
	}
	return resp, nil
}

// --------------------------------------------------------------------------
// Resource reads (direct driver queries)
// --------------------------------------------------------------------------

// GetResourceStatus reads a resource's current status from its driver service.
// kind is the Restate service name (e.g., "S3Bucket"), key is the canonical
// resource key (e.g., "my-bucket" or "vpc-123~web-sg").
func (c *Client) GetResourceStatus(ctx context.Context, kind, key string) (*types.ResourceStatusResponse, error) {
	resp, err := ingress.Object[restate.Void, types.ResourceStatusResponse](
		c.rc, kind, key, "GetStatus",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, fmt.Errorf("get resource status %s/%s: %w", kind, key, err)
	}
	return &resp, nil
}

// GetResourceOutputs reads a resource's outputs as raw JSON from its driver.
// The outputs are returned as a generic map since different drivers return
// different typed structs.
func (c *Client) GetResourceOutputs(ctx context.Context, kind, key string) (map[string]any, error) {
	resp, err := ingress.Object[restate.Void, json.RawMessage](
		c.rc, kind, key, "GetOutputs",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, fmt.Errorf("get resource outputs %s/%s: %w", kind, key, err)
	}
	if len(resp) == 0 || string(resp) == "null" {
		return nil, nil
	}
	var outputs map[string]any
	if err := json.Unmarshal(resp, &outputs); err != nil {
		return nil, fmt.Errorf("decode resource outputs: %w", err)
	}
	return outputs, nil
}

// --------------------------------------------------------------------------
// Deploy service calls (template-first user API)
// --------------------------------------------------------------------------

// Deploy submits a deployment request against a registered template.
func (c *Client) Deploy(ctx context.Context, req types.DeployRequest) (*types.DeployResponse, error) {
	resp, err := ingress.Service[types.DeployRequest, types.DeployResponse](
		c.rc, commandServiceName, "Deploy",
	).Request(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("deploy: %w", err)
	}
	return &resp, nil
}

// PlanDeploy performs a dry-run against a registered template.
func (c *Client) PlanDeploy(ctx context.Context, req types.PlanDeployRequest) (*types.PlanDeployResponse, error) {
	resp, err := ingress.Service[types.PlanDeployRequest, types.PlanDeployResponse](
		c.rc, commandServiceName, "PlanDeploy",
	).Request(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("plan deploy: %w", err)
	}
	return &resp, nil
}

// --------------------------------------------------------------------------
// Template management calls
// --------------------------------------------------------------------------

// RegisterTemplate registers or updates a template in the registry.
func (c *Client) RegisterTemplate(ctx context.Context, req types.RegisterTemplateRequest) (*types.RegisterTemplateResponse, error) {
	resp, err := ingress.Service[types.RegisterTemplateRequest, types.RegisterTemplateResponse](
		c.rc, commandServiceName, "RegisterTemplate",
	).Request(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("register template: %w", err)
	}
	return &resp, nil
}

// GetTemplate retrieves a registered template's full record.
func (c *Client) GetTemplate(ctx context.Context, name string) (*types.TemplateRecord, error) {
	resp, err := ingress.Service[string, types.TemplateRecord](
		c.rc, commandServiceName, "GetTemplate",
	).Request(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get template %q: %w", name, err)
	}
	return &resp, nil
}

// ListTemplates returns all template summaries from the registry index.
func (c *Client) ListTemplates(ctx context.Context) ([]types.TemplateSummary, error) {
	resp, err := ingress.Service[restate.Void, []types.TemplateSummary](
		c.rc, commandServiceName, "ListTemplates",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	return resp, nil
}

// DeleteTemplate removes a registered template from the registry.
func (c *Client) DeleteTemplate(ctx context.Context, name string) error {
	_, err := ingress.Service[types.DeleteTemplateRequest, restate.Void](
		c.rc, commandServiceName, "DeleteTemplate",
	).Request(ctx, types.DeleteTemplateRequest{Name: name})
	if err != nil {
		return fmt.Errorf("delete template %q: %w", name, err)
	}
	return nil
}
