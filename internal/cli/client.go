// client.go is the Restate ingress client wrapper for the CLI.
//
// The CLI never reads or writes cloud resources directly. Every operation is
// a typed HTTP/JSON call through the Restate ingress endpoint, which routes
// to the correct Restate service handler. This file maps each CLI operation
// to its corresponding Restate service + handler pair.
//
// Service topology accessed from this client:
//
//	PraxisCommandService (Service)       — Apply, Plan, Delete, Import, Deploy,
//	                                       PlanDeploy, RegisterTemplate, etc.
//	DeploymentStateObj   (Virtual Object) — GetDetail, MoveResource, etc.
//	DeploymentIndex      (Virtual Object) — List (key="global")
//	DeploymentEventStore (Virtual Object) — ListSince (per-deployment events)
//	EventIndex           (Virtual Object) — Query (cross-deployment search)
//	NotificationSinkConfig (Virtual Object) — Upsert, List, Get, Remove, Health
//	SinkRouter           (Service)        — Test delivery
//	WorkspaceService     (Virtual Object) — Configure, Get, Delete, retention
//	WorkspaceIndex       (Virtual Object) — List (key="global")
//	ConciergeSession     (Virtual Object) — Ask, GetStatus, GetHistory, Reset
//	ConciergeConfig      (Virtual Object) — Configure, Get
//	ApprovalRelay        (Service)        — Resolve awakeable
//	SlackGatewayConfig   (Virtual Object) — Configure, Get, allowed-user ops
//	SlackWatchConfig     (Virtual Object) — AddWatch, ListWatches, etc.
package cli

import (
	"context"
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/workspace"
	"github.com/shirvan/praxis/pkg/types"
)

// --------------------------------------------------------------------------
// Restate service name and handler constants
// --------------------------------------------------------------------------

// These must match the service names registered in cmd/praxis-core/main.go.
// They are repeated here rather than importing the internal packages because
// the CLI should depend only on stable contract strings.
const (
	commandServiceName         = "PraxisCommandService"
	stateServiceName           = "DeploymentStateObj"
	indexServiceName           = "DeploymentIndex"
	cloudEventStoreName        = "DeploymentEventStore"
	cloudEventIndexName        = "EventIndex"
	notificationSinkConfigName = "NotificationSinkConfig"
	sinkRouterServiceName      = "SinkRouter"
	indexGlobalKey             = "global"
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
		return nil, err
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
		return nil, err
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
		return nil, err
	}
	return &resp, nil
}

// RollbackDeployment starts a rollback that deletes only the resources proven
// ready by the CloudEvents store for a failed or cancelled deployment.
func (c *Client) RollbackDeployment(ctx context.Context, key string) (*types.DeleteDeploymentResponse, error) {
	resp, err := ingress.Service[types.DeleteDeploymentRequest, types.DeleteDeploymentResponse](
		c.rc, commandServiceName, "RollbackDeployment",
	).Request(ctx, types.DeleteDeploymentRequest{DeploymentKey: key})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ImportResource adopts an existing cloud resource under Praxis management.
func (c *Client) ImportResource(ctx context.Context, req types.ImportRequest) (*types.ImportResponse, error) {
	resp, err := ingress.Service[types.ImportRequest, types.ImportResponse](
		c.rc, commandServiceName, "Import",
	).Request(ctx, req)
	if err != nil {
		return nil, err
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
// If workspace is non-empty, results are filtered to that workspace.
func (c *Client) ListDeployments(ctx context.Context, workspace string) ([]types.DeploymentSummary, error) {
	resp, err := ingress.Object[orchestrator.ListFilter, []types.DeploymentSummary](
		c.rc, indexServiceName, indexGlobalKey, "List",
	).Request(ctx, orchestrator.ListFilter{Workspace: workspace})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	return resp, nil
}

// GetDeploymentCloudEvents returns CloudEvents for one deployment with
// sequence > since from the new chunked event store.
func (c *Client) GetDeploymentCloudEvents(ctx context.Context, key string, since int64) ([]orchestrator.SequencedCloudEvent, error) {
	resp, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
		c.rc, cloudEventStoreName, key, "ListSince",
	).Request(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("get CloudEvents for %q: %w", key, err)
	}
	return resp, nil
}

// QueryCloudEvents runs a cross-deployment query against the global event index.
func (c *Client) QueryCloudEvents(ctx context.Context, query orchestrator.EventQuery) ([]orchestrator.SequencedCloudEvent, error) {
	resp, err := ingress.Object[orchestrator.EventQuery, []orchestrator.SequencedCloudEvent](
		c.rc, cloudEventIndexName, indexGlobalKey, "Query",
	).Request(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	return resp, nil
}

// UpsertNotificationSink creates or updates a sink configuration.
func (c *Client) UpsertNotificationSink(ctx context.Context, sink orchestrator.NotificationSink) error {
	_, err := ingress.Object[orchestrator.NotificationSink, restate.Void](
		c.rc, notificationSinkConfigName, indexGlobalKey, "Upsert",
	).Request(ctx, sink)
	if err != nil {
		return fmt.Errorf("upsert sink %q: %w", sink.Name, err)
	}
	return nil
}

// ListNotificationSinks returns the configured notification sinks.
func (c *Client) ListNotificationSinks(ctx context.Context) ([]orchestrator.NotificationSink, error) {
	resp, err := ingress.Object[restate.Void, []orchestrator.NotificationSink](
		c.rc, notificationSinkConfigName, indexGlobalKey, "List",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, fmt.Errorf("list notification sinks: %w", err)
	}
	return resp, nil
}

// GetNotificationSink returns one configured sink by name.
func (c *Client) GetNotificationSink(ctx context.Context, name string) (*orchestrator.NotificationSink, error) {
	resp, err := ingress.Object[string, *orchestrator.NotificationSink](
		c.rc, notificationSinkConfigName, indexGlobalKey, "Get",
	).Request(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get notification sink %q: %w", name, err)
	}
	return resp, nil
}

// GetNotificationSinkHealth returns the aggregate health/read model for all sinks.
func (c *Client) GetNotificationSinkHealth(ctx context.Context) (orchestrator.NotificationSinkHealth, error) {
	resp, err := ingress.Object[restate.Void, orchestrator.NotificationSinkHealth](
		c.rc, notificationSinkConfigName, indexGlobalKey, "Health",
	).Request(ctx, restate.Void{})
	if err != nil {
		return orchestrator.NotificationSinkHealth{}, fmt.Errorf("get notification sink health: %w", err)
	}
	return resp, nil
}

// RemoveNotificationSink deletes one configured sink by name.
func (c *Client) RemoveNotificationSink(ctx context.Context, name string) error {
	_, err := ingress.Object[string, restate.Void](
		c.rc, notificationSinkConfigName, indexGlobalKey, "Remove",
	).Request(ctx, name)
	if err != nil {
		return fmt.Errorf("remove notification sink %q: %w", name, err)
	}
	return nil
}

// TestNotificationSink delivers a synthetic event to one sink.
func (c *Client) TestNotificationSink(ctx context.Context, name string) error {
	_, err := ingress.Service[string, restate.Void](c.rc, sinkRouterServiceName, "Test").Request(ctx, name)
	if err != nil {
		return fmt.Errorf("test notification sink %q: %w", name, err)
	}
	return nil
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

// ReconcileResource triggers an on-demand reconciliation of a single resource.
// kind is the Restate service name (e.g., "S3Bucket"), key is the canonical
// resource key. The handler compares actual cloud state against the desired
// spec and, in Managed mode, corrects any drift.
func (c *Client) ReconcileResource(ctx context.Context, kind, key string) (*types.ReconcileResult, error) {
	resp, err := ingress.Object[restate.Void, types.ReconcileResult](
		c.rc, kind, key, "Reconcile",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, fmt.Errorf("reconcile %s/%s: %w", kind, key, err)
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
		return nil, err
	}
	return &resp, nil
}

// PlanDeploy performs a dry-run against a registered template.
func (c *Client) PlanDeploy(ctx context.Context, req types.PlanDeployRequest) (*types.PlanDeployResponse, error) {
	resp, err := ingress.Service[types.PlanDeployRequest, types.PlanDeployResponse](
		c.rc, commandServiceName, "PlanDeploy",
	).Request(ctx, req)
	if err != nil {
		return nil, err
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
		return nil, err
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
		return nil, err
	}
	return resp, nil
}

// DeleteTemplate removes a registered template from the registry.
func (c *Client) DeleteTemplate(ctx context.Context, name string) error {
	_, err := ingress.Service[types.DeleteTemplateRequest, restate.Void](
		c.rc, commandServiceName, "DeleteTemplate",
	).Request(ctx, types.DeleteTemplateRequest{Name: name})
	if err != nil {
		return err
	}
	return nil
}

// --------------------------------------------------------------------------
// Workspace service calls
// --------------------------------------------------------------------------

const (
	workspaceServiceName = "WorkspaceService"
	workspaceIndexName   = "WorkspaceIndex"
	workspaceIndexKey    = "global"
)

// ConfigureWorkspace creates or updates a workspace.
func (c *Client) ConfigureWorkspace(ctx context.Context, cfg workspace.WorkspaceConfig) error {
	_, err := ingress.Object[workspace.WorkspaceConfig, restate.Void](
		c.rc, workspaceServiceName, cfg.Name, "Configure",
	).Request(ctx, cfg)
	return err
}

// GetWorkspace returns the workspace info for the given name.
func (c *Client) GetWorkspace(ctx context.Context, name string) (*workspace.WorkspaceInfo, error) {
	resp, err := ingress.Object[restate.Void, workspace.WorkspaceInfo](
		c.rc, workspaceServiceName, name, "Get",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteWorkspace removes a workspace.
func (c *Client) DeleteWorkspace(ctx context.Context, name string) error {
	_, err := ingress.Object[restate.Void, restate.Void](
		c.rc, workspaceServiceName, name, "Delete",
	).Request(ctx, restate.Void{})
	return err
}

// ListWorkspaces returns all workspace names in sorted order.
func (c *Client) ListWorkspaces(ctx context.Context) ([]string, error) {
	resp, err := ingress.Object[restate.Void, []string](
		c.rc, workspaceIndexName, workspaceIndexKey, "List",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// GetWorkspaceEventRetention returns the current event retention policy for a workspace.
func (c *Client) GetWorkspaceEventRetention(ctx context.Context, name string) (*workspace.EventRetentionPolicy, error) {
	resp, err := ingress.Object[restate.Void, workspace.EventRetentionPolicy](
		c.rc, workspaceServiceName, name, "GetEventRetention",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// SetWorkspaceEventRetention updates the event retention policy for a workspace.
func (c *Client) SetWorkspaceEventRetention(ctx context.Context, name string, policy workspace.EventRetentionPolicy) error {
	_, err := ingress.Object[workspace.EventRetentionPolicy, restate.Void](
		c.rc, workspaceServiceName, name, "SetEventRetention",
	).Request(ctx, policy)
	return err
}

// --------------------------------------------------------------------------
// State management calls
// --------------------------------------------------------------------------

// StateMv renames a resource within a deployment or moves it across
// deployments. For same-deployment renames it calls MoveResource directly.
// For cross-deployment moves it removes from source and adds to destination.
func (c *Client) StateMv(ctx context.Context, req types.StateMvRequest) (*types.StateMvResponse, error) {
	newName := req.NewName
	if newName == "" {
		newName = req.ResourceName
	}

	if req.SourceDeployment == req.DestDeployment {
		// Same-deployment rename.
		_, err := ingress.Object[orchestrator.MoveResourceRequest, restate.Void](
			c.rc, stateServiceName, req.SourceDeployment, "MoveResource",
		).Request(ctx, orchestrator.MoveResourceRequest{
			ResourceName: req.ResourceName,
			NewName:      newName,
		})
		if err != nil {
			return nil, fmt.Errorf("rename resource in %q: %w", req.SourceDeployment, err)
		}
	} else {
		// Cross-deployment move: remove from source, add to destination.
		rs, err := ingress.Object[string, orchestrator.ResourceState](
			c.rc, stateServiceName, req.SourceDeployment, "RemoveResource",
		).Request(ctx, req.ResourceName)
		if err != nil {
			return nil, fmt.Errorf("remove resource %q from %q: %w", req.ResourceName, req.SourceDeployment, err)
		}

		rs.Name = newName
		// Clear DependsOn since dependencies may not exist in the destination.
		rs.DependsOn = nil

		_, err = ingress.Object[orchestrator.ResourceState, restate.Void](
			c.rc, stateServiceName, req.DestDeployment, "AddResource",
		).Request(ctx, rs)
		if err != nil {
			return nil, fmt.Errorf("add resource %q to %q: %w", newName, req.DestDeployment, err)
		}
	}

	return &types.StateMvResponse{
		SourceDeployment: req.SourceDeployment,
		DestDeployment:   req.DestDeployment,
		OldName:          req.ResourceName,
		NewName:          newName,
	}, nil
}

// --------------------------------------------------------------------------
// Concierge calls
// --------------------------------------------------------------------------

const (
	conciergeSessionServiceName  = "ConciergeSession"
	conciergeConfigServiceName   = "ConciergeConfig"
	approvalRelayServiceName     = "ApprovalRelay"
	conciergeProgressServiceName = "ConciergeProgress"
	conciergeConfigKey           = "global"
)

// ConciergeAsk sends a prompt to the concierge session.
func (c *Client) ConciergeAsk(ctx context.Context, sessionID string, req conciergeAskRequest) (*conciergeAskResponse, error) {
	resp, err := ingress.Object[conciergeAskRequest, conciergeAskResponse](
		c.rc, conciergeSessionServiceName, sessionID, "Ask",
	).Request(ctx, req)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ConciergeGetProgress returns the live tool-call progress for a session.
// Used by the CLI to render tool calls in real time while Ask is executing.
func (c *Client) ConciergeGetProgress(ctx context.Context, sessionID string) (*conciergeProgressState, error) {
	resp, err := ingress.Object[restate.Void, conciergeProgressState](
		c.rc, conciergeProgressServiceName, sessionID, "Get",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ConciergeGetStatus returns the status of a concierge session.
func (c *Client) ConciergeGetStatus(ctx context.Context, sessionID string) (*conciergeSessionStatus, error) {
	resp, err := ingress.Object[restate.Void, conciergeSessionStatus](
		c.rc, conciergeSessionServiceName, sessionID, "GetStatus",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ConciergeGetHistory returns the conversation history for a session.
func (c *Client) ConciergeGetHistory(ctx context.Context, sessionID string) ([]conciergeMessage, error) {
	resp, err := ingress.Object[restate.Void, []conciergeMessage](
		c.rc, conciergeSessionServiceName, sessionID, "GetHistory",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ConciergeConfigure sets the concierge LLM provider configuration.
func (c *Client) ConciergeConfigure(ctx context.Context, req conciergeConfigureRequest) error {
	_, err := ingress.Object[conciergeConfigureRequest, restate.Void](
		c.rc, conciergeConfigServiceName, conciergeConfigKey, "Configure",
	).Request(ctx, req)
	return err
}

// ConciergeGetConfig returns the current concierge configuration (redacted).
func (c *Client) ConciergeGetConfig(ctx context.Context) (*conciergeConfiguration, error) {
	resp, err := ingress.Object[restate.Void, conciergeConfiguration](
		c.rc, conciergeConfigServiceName, conciergeConfigKey, "Get",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ConciergeApprove resolves a pending approval for a concierge action.
func (c *Client) ConciergeApprove(ctx context.Context, req conciergeApprovalRequest) error {
	_, err := ingress.Service[conciergeApprovalRequest, restate.Void](
		c.rc, approvalRelayServiceName, "Resolve",
	).Request(ctx, req)
	return err
}

// ConciergeReset clears the conversation history and state for a session.
func (c *Client) ConciergeReset(ctx context.Context, sessionID string) error {
	_, err := ingress.Object[restate.Void, restate.Void](
		c.rc, conciergeSessionServiceName, sessionID, "Reset",
	).Request(ctx, restate.Void{})
	return err
}

// Concierge request/response types — kept in the CLI package to avoid
// importing internal/concierge into the CLI binary. Fields match the
// JSON contract of the concierge Restate handlers.

// conciergeAskRequest is the payload sent to ConciergeSession.Ask.
type conciergeAskRequest struct {
	Prompt    string `json:"prompt"`
	Account   string `json:"account,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Source    string `json:"source,omitempty"`
}

// conciergeAskResponse is the reply from ConciergeSession.Ask.
type conciergeAskResponse struct {
	Response   string             `json:"response"`
	SessionID  string             `json:"sessionId"`
	TurnCount  int                `json:"turnCount"`
	ToolLog    []conciergeToolLog `json:"toolLog,omitempty"`
	Model      string             `json:"model,omitempty"`
	Provider   string             `json:"provider,omitempty"`
	Usage      conciergeUsage     `json:"usage,omitzero"`
	DurationMs int64              `json:"durationMs,omitempty"`
}

// conciergeToolLog records a single tool invocation for CLI display.
type conciergeToolLog struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// conciergeUsage holds aggregate token usage for a single Ask.
type conciergeUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// conciergeSessionStatus is returned by ConciergeSession.GetStatus.
type conciergeSessionStatus struct {
	Provider        string             `json:"provider"`
	Model           string             `json:"model"`
	TurnCount       int                `json:"turnCount"`
	LastActiveAt    string             `json:"lastActiveAt"`
	ExpiresAt       string             `json:"expiresAt"`
	PendingApproval *conciergeApproval `json:"pendingApproval,omitempty"`
}

// conciergeApproval describes a pending action that needs human approval.
type conciergeApproval struct {
	AwakeableID string `json:"awakeableId"`
	Action      string `json:"action"`
	Description string `json:"description"`
	RequestedAt string `json:"requestedAt"`
}

// conciergeMessage is a single turn in the conversation history.
type conciergeMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// conciergeConfigureRequest is the payload sent to ConciergeConfig.Configure.
type conciergeConfigureRequest struct {
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	APIKey      string   `json:"apiKey,omitempty"` //nolint:gosec // G117 not a credential, just a field name
	APIKeyRef   string   `json:"apiKeyRef,omitempty"`
	BaseURL     string   `json:"baseURL,omitempty"`
	MaxTurns    int      `json:"maxTurns,omitempty"`
	MaxMessages int      `json:"maxMessages,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	SessionTTL  string   `json:"sessionTTL,omitempty"`
	ApprovalTTL string   `json:"approvalTTL,omitempty"`
}

// conciergeConfiguration is the redacted config returned by ConciergeConfig.Get.
type conciergeConfiguration struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	APIKey      string  `json:"apiKey,omitempty"` //nolint:gosec // G117 not a credential, config field name
	MaxTurns    int     `json:"maxTurns"`
	MaxMessages int     `json:"maxMessages"`
	Temperature float64 `json:"temperature"`
	SessionTTL  string  `json:"sessionTTL"`
	ApprovalTTL string  `json:"approvalTTL"`
}

// conciergeApprovalRequest resolves a pending Restate Awakeable.
type conciergeApprovalRequest struct {
	AwakeableID string `json:"awakeableId"`
	Approved    bool   `json:"approved"`
	Reason      string `json:"reason,omitempty"`
	Actor       string `json:"actor,omitempty"`
}

// conciergeProgressState holds real-time tool execution progress.
type conciergeProgressState struct {
	Entries []conciergeProgressEntry `json:"entries"`
}

// conciergeProgressEntry is a single progress event from ConciergeProgress.
type conciergeProgressEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "running", "ok", "error"
	Error  string `json:"error,omitempty"`
}

// --------------------------------------------------------------------------
// Slack Gateway calls
// --------------------------------------------------------------------------

const (
	slackGatewayConfigServiceName = "SlackGatewayConfig"
	slackWatchConfigServiceName   = "SlackWatchConfig"
	slackConfigGlobalKey          = "global"
)

// SlackConfigure sets the Slack gateway configuration.
func (c *Client) SlackConfigure(ctx context.Context, req slackConfigRequest) error {
	_, err := ingress.Object[slackConfigRequest, restate.Void](
		c.rc, slackGatewayConfigServiceName, slackConfigGlobalKey, "Configure",
	).Request(ctx, req)
	return err
}

// SlackGetConfig returns the current Slack gateway configuration (redacted).
func (c *Client) SlackGetConfig(ctx context.Context) (*slackConfiguration, error) {
	resp, err := ingress.Object[restate.Void, slackConfiguration](
		c.rc, slackGatewayConfigServiceName, slackConfigGlobalKey, "Get",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// SlackSetAllowedUsers replaces the allowed-user list.
func (c *Client) SlackSetAllowedUsers(ctx context.Context, req slackSetAllowedUsersRequest) error {
	_, err := ingress.Object[slackSetAllowedUsersRequest, restate.Void](
		c.rc, slackGatewayConfigServiceName, slackConfigGlobalKey, "SetAllowedUsers",
	).Request(ctx, req)
	return err
}

// SlackAddAllowedUser adds a single user to the allowed list.
func (c *Client) SlackAddAllowedUser(ctx context.Context, userID string) error {
	_, err := ingress.Object[string, restate.Void](
		c.rc, slackGatewayConfigServiceName, slackConfigGlobalKey, "AddAllowedUser",
	).Request(ctx, userID)
	return err
}

// SlackRemoveAllowedUser removes a user from the allowed list.
func (c *Client) SlackRemoveAllowedUser(ctx context.Context, userID string) error {
	_, err := ingress.Object[string, restate.Void](
		c.rc, slackGatewayConfigServiceName, slackConfigGlobalKey, "RemoveAllowedUser",
	).Request(ctx, userID)
	return err
}

// SlackAddWatch adds a new event watch rule.
func (c *Client) SlackAddWatch(ctx context.Context, req slackAddWatchRequest) (*slackWatchRule, error) {
	resp, err := ingress.Object[slackAddWatchRequest, slackWatchRule](
		c.rc, slackWatchConfigServiceName, slackConfigGlobalKey, "AddWatch",
	).Request(ctx, req)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// SlackRemoveWatch removes an event watch rule.
func (c *Client) SlackRemoveWatch(ctx context.Context, req slackRemoveWatchRequest) error {
	_, err := ingress.Object[slackRemoveWatchRequest, restate.Void](
		c.rc, slackWatchConfigServiceName, slackConfigGlobalKey, "RemoveWatch",
	).Request(ctx, req)
	return err
}

// SlackUpdateWatch updates an existing event watch rule.
func (c *Client) SlackUpdateWatch(ctx context.Context, req slackUpdateWatchRequest) (*slackWatchRule, error) {
	resp, err := ingress.Object[slackUpdateWatchRequest, slackWatchRule](
		c.rc, slackWatchConfigServiceName, slackConfigGlobalKey, "UpdateWatch",
	).Request(ctx, req)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// SlackListWatches returns all configured watch rules.
func (c *Client) SlackListWatches(ctx context.Context) ([]slackWatchRule, error) {
	resp, err := ingress.Object[restate.Void, []slackWatchRule](
		c.rc, slackWatchConfigServiceName, slackConfigGlobalKey, "ListWatches",
	).Request(ctx, restate.Void{})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Slack request/response types — mirror the JSON contracts of the
// SlackGatewayConfig and SlackWatchConfig Restate handlers.

// slackConfigRequest is the payload for SlackGatewayConfig.Configure.
type slackConfigRequest struct {
	BotToken     string   `json:"botToken,omitempty"`
	BotTokenRef  string   `json:"botTokenRef,omitempty"`
	AppToken     string   `json:"appToken,omitempty"`
	AppTokenRef  string   `json:"appTokenRef,omitempty"`
	TeamID       string   `json:"teamId,omitempty"`
	BotUserID    string   `json:"botUserId,omitempty"`
	EventChannel string   `json:"eventChannel,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
	AllowedUsers []string `json:"allowedUsers,omitempty"`
}

// slackConfiguration is returned by SlackGatewayConfig.Get (tokens redacted).
type slackConfiguration struct {
	BotToken     string   `json:"botToken,omitempty"`
	BotTokenRef  string   `json:"botTokenRef,omitempty"`
	AppToken     string   `json:"appToken,omitempty"`
	AppTokenRef  string   `json:"appTokenRef,omitempty"`
	TeamID       string   `json:"teamId"`
	BotUserID    string   `json:"botUserId"`
	EventChannel string   `json:"eventChannel"`
	Workspace    string   `json:"workspace,omitempty"`
	AllowedUsers []string `json:"allowedUsers"`
	Version      int      `json:"version"`
}

// slackSetAllowedUsersRequest replaces the allowed-user list.
type slackSetAllowedUsersRequest struct {
	UserIDs []string `json:"userIds"`
}

// slackWatchRule is a single event-routing rule for Slack notifications.
type slackWatchRule struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Channel   string           `json:"channel"`
	Filter    slackWatchFilter `json:"filter"`
	CreatedBy string           `json:"createdBy"`
	CreatedAt string           `json:"createdAt"`
	Enabled   bool             `json:"enabled"`
}

// slackWatchFilter specifies which events a watch rule matches.
type slackWatchFilter struct {
	Types       []string `json:"types,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	Severities  []string `json:"severities,omitempty"`
	Workspaces  []string `json:"workspaces,omitempty"`
	Deployments []string `json:"deployments,omitempty"`
}

// slackAddWatchRequest is the payload for SlackWatchConfig.AddWatch.
type slackAddWatchRequest struct {
	Name      string           `json:"name"`
	Channel   string           `json:"channel,omitempty"`
	Filter    slackWatchFilter `json:"filter"`
	CreatedBy string           `json:"createdBy,omitempty"`
}

// slackRemoveWatchRequest is the payload for SlackWatchConfig.RemoveWatch.
type slackRemoveWatchRequest struct {
	ID string `json:"id"`
}

// slackUpdateWatchRequest is the payload for SlackWatchConfig.UpdateWatch.
// nil fields are left unchanged.
type slackUpdateWatchRequest struct {
	ID      string            `json:"id"`
	Name    *string           `json:"name,omitempty"`
	Channel *string           `json:"channel,omitempty"`
	Filter  *slackWatchFilter `json:"filter,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}
