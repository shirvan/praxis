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

type DashboardAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI dashboard.DashboardAPI
	apiFactory        func(aws.Config) dashboard.DashboardAPI
}

func NewDashboardAdapterWithAuth(auth authservice.AuthClient) *DashboardAdapter {
	return &DashboardAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) dashboard.DashboardAPI {
			return dashboard.NewDashboardAPI(awsclient.NewCloudWatchClient(cfg))
		},
	}
}

func NewDashboardAdapterWithAPI(api dashboard.DashboardAPI) *DashboardAdapter {
	return &DashboardAdapter{staticPlanningAPI: api}
}

func (a *DashboardAdapter) Kind() string { return dashboard.ServiceName }

func (a *DashboardAdapter) ServiceName() string { return dashboard.ServiceName }

func (a *DashboardAdapter) Scope() KeyScope { return KeyScopeRegion }

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

func (a *DashboardAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

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

func (a *DashboardAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

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

func (a *DashboardAdapter) Plan(ctx restate.Context, key, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[dashboard.DashboardSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[dashboard.DashboardOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("Dashboard Plan: failed to read outputs for key %q: %w", key, getErr)
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

func (a *DashboardAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

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

func (a *DashboardAdapter) decodeSpec(doc resourceDocument) (dashboard.DashboardSpec, error) {
	var spec dashboard.DashboardSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return dashboard.DashboardSpec{}, fmt.Errorf("decode Dashboard spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return dashboard.DashboardSpec{}, fmt.Errorf("Dashboard metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return dashboard.DashboardSpec{}, fmt.Errorf("Dashboard spec.region is required")
	}
	spec.DashboardName = name
	return spec, nil
}

func (a *DashboardAdapter) planningAPI(ctx restate.Context, account string) (dashboard.DashboardAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Dashboard adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Dashboard planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
