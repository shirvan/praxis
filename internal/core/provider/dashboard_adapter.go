// Dashboard provider adapter.
//
// This file implements the provider.Adapter interface for Amazon CloudWatch (Dashboard)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the Dashboard Restate Virtual Object driver.
//
// Key scope: global (dashboards are region-free).
// Key parts: dashboard name.
// CloudWatch dashboards are global; the key is the dashboard name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/dashboard"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// DashboardAdapter implements provider.Adapter for Dashboard (Amazon CloudWatch (Dashboard)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type DashboardAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI dashboard.DashboardAPI
	apiFactory        func(aws.Config) dashboard.DashboardAPI
}

// NewDashboardAdapterWithAuth creates a production Dashboard adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewDashboardAdapterWithAuth(auth authservice.AuthClient) *DashboardAdapter {
	return &DashboardAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) dashboard.DashboardAPI {
			return dashboard.NewDashboardAPI(awsclient.NewCloudWatchClient(cfg))
		},
	}
}

// NewDashboardAdapterWithAPI creates a Dashboard adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewDashboardAdapterWithAPI(api dashboard.DashboardAPI) *DashboardAdapter {
	return &DashboardAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "Dashboard" that maps template
// resource documents to this adapter in the provider registry.
func (a *DashboardAdapter) Kind() string { return dashboard.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// Dashboard driver. The orchestrator uses this to dispatch durable RPCs.
func (a *DashboardAdapter) ServiceName() string { return dashboard.ServiceName }

// Scope returns the key-scope strategy for Dashboard resources,
// which controls how BuildKey assembles the canonical object key.
func (a *DashboardAdapter) Scope() KeyScope { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a Dashboard resource
// from the raw JSON resource document. The key is composed of dashboard name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *DashboardAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("region", spec.Region); err != nil {
		return "", err
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("dashboard name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete Dashboard spec struct expected by the driver.
func (a *DashboardAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the Dashboard Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *DashboardAdapter) Provision(ctx restate.Context, key, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[dashboard.DashboardSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[dashboard.DashboardSpec, dashboard.DashboardOutputs](
		restate.Object[dashboard.DashboardOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)
	return &provisionHandle[dashboard.DashboardOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the Dashboard Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *DashboardAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed Dashboard driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *DashboardAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[dashboard.DashboardOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"dashboardArn":  out.DashboardArn,
		"dashboardName": out.DashboardName,
	}, nil
}

// Plan compares the desired Dashboard spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *DashboardAdapter) Plan(ctx restate.Context, key, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[dashboard.DashboardSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[dashboard.DashboardOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("dashboard plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.DashboardName == "" {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (struct {
		State dashboard.ObservedState
		Found bool
	}, error) {
		obs, found, runErr := planningAPI.GetDashboard(runCtx, outputs.DashboardName)
		if runErr != nil {
			if dashboard.IsNotFound(runErr) {
				return struct {
					State dashboard.ObservedState
					Found bool
				}{Found: false}, nil
			}
			return struct {
				State dashboard.ObservedState
				Found bool
			}{}, restate.TerminalError(runErr, 500)
		}
		return struct {
			State dashboard.ObservedState
			Found bool
		}{State: obs, Found: found}, nil
	})
	if err != nil {
		return "", nil, err
	}
	if !result.Found {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}
	rawDiffs := dashboard.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

// BuildImportKey derives the canonical Restate object key for importing
// an existing Dashboard resource by its region and provider-native ID.
func (a *DashboardAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing Dashboard resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *DashboardAdapter) Import(ctx restate.Context, key, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, dashboard.DashboardOutputs](
		restate.Object[dashboard.DashboardOutputs](ctx, a.ServiceName(), key, "Import"),
	).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed Dashboard spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *DashboardAdapter) decodeSpec(doc resourceDocument) (dashboard.DashboardSpec, error) {
	var spec dashboard.DashboardSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return dashboard.DashboardSpec{}, fmt.Errorf("decode dashboard spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return dashboard.DashboardSpec{}, fmt.Errorf("dashboard metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return dashboard.DashboardSpec{}, fmt.Errorf("dashboard spec.region is required")
	}
	spec.DashboardName = name
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *DashboardAdapter) planningAPI(ctx restate.Context, account string) (dashboard.DashboardAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("dashboard adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Dashboard planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
