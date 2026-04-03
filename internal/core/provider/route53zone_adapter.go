// Route53HostedZone provider adapter.
//
// This file implements the provider.Adapter interface for Amazon Route 53 (Hosted Zone)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the Route53HostedZone Restate Virtual Object driver.
//
// Key scope: global (DNS zones are global).
// Key parts: zone name.
// Route 53 hosted zones are global; the key is the zone name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/route53zone"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type Route53HostedZoneAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI route53zone.HostedZoneAPI
	apiFactory        func(aws.Config) route53zone.HostedZoneAPI
}

func NewRoute53HostedZoneAdapterWithAuth(auth authservice.AuthClient) *Route53HostedZoneAdapter {
	return &Route53HostedZoneAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) route53zone.HostedZoneAPI {
			return route53zone.NewHostedZoneAPI(awsclient.NewRoute53Client(cfg))
		},
	}
}

func NewRoute53HostedZoneAdapterWithAPI(api route53zone.HostedZoneAPI) *Route53HostedZoneAdapter {
	return &Route53HostedZoneAdapter{staticPlanningAPI: api}
}

func (a *Route53HostedZoneAdapter) Kind() string        { return route53zone.ServiceName }
func (a *Route53HostedZoneAdapter) ServiceName() string { return route53zone.ServiceName }
func (a *Route53HostedZoneAdapter) Scope() KeyScope     { return KeyScopeGlobal }

func (a *Route53HostedZoneAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("hosted zone name", spec.Name); err != nil {
		return "", err
	}
	return spec.Name, nil
}

func (a *Route53HostedZoneAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *Route53HostedZoneAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[route53zone.HostedZoneSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs](restate.Object[route53zone.HostedZoneOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[route53zone.HostedZoneOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *Route53HostedZoneAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *Route53HostedZoneAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[route53zone.HostedZoneOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"hostedZoneId": out.HostedZoneId, "name": out.Name, "nameServers": out.NameServers, "isPrivate": out.IsPrivate, "recordCount": out.RecordCount}, nil
}

func (a *Route53HostedZoneAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[route53zone.HostedZoneSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[route53zone.HostedZoneOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("Route53HostedZone Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.HostedZoneId == "" {
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
	type describePlanResult struct {
		State route53zone.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeHostedZone(runCtx, outputs.HostedZoneId)
		if descErr != nil {
			if route53zone.IsNotFound(descErr) {
				return describePlanResult{Found: false}, nil
			}
			return describePlanResult{}, restate.TerminalError(descErr, 500)
		}
		return describePlanResult{State: obs, Found: true}, nil
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
	rawDiffs := route53zone.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *Route53HostedZoneAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return strings.TrimSpace(resourceID), nil
}

func (a *Route53HostedZoneAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, route53zone.HostedZoneOutputs](restate.Object[route53zone.HostedZoneOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *Route53HostedZoneAdapter) Lookup(ctx restate.Context, account string, filter LookupFilter) (map[string]any, error) {
	api, err := a.planningAPI(ctx, account)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	observed, err := restate.Run(ctx, func(runCtx restate.RunContext) (route53zone.ObservedState, error) {
		return lookupHostedZone(runCtx, api, filter)
	})
	if err != nil {
		return nil, classifyLookupError(err, route53zone.IsNotFound)
	}
	if !matchesHostedZoneFilter(observed, filter) {
		return nil, restate.TerminalError(fmt.Errorf("data source lookup: no Route53HostedZone found matching filter"), 404)
	}
	return a.NormalizeOutputs(route53zone.HostedZoneOutputs{
		HostedZoneId: observed.HostedZoneId,
		Name:         observed.Name,
		NameServers:  observed.NameServers,
		IsPrivate:    observed.IsPrivate,
		RecordCount:  observed.RecordCount,
	})
}

func (a *Route53HostedZoneAdapter) decodeSpec(doc resourceDocument) (route53zone.HostedZoneSpec, error) {
	var spec route53zone.HostedZoneSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return route53zone.HostedZoneSpec{}, fmt.Errorf("decode Route53HostedZone spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return route53zone.HostedZoneSpec{}, fmt.Errorf("Route53HostedZone metadata.name is required")
	}
	spec.Name = strings.ToLower(strings.TrimSuffix(name, "."))
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Account = ""
	return spec, nil
}

func (a *Route53HostedZoneAdapter) planningAPI(ctx restate.Context, account string) (route53zone.HostedZoneAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Route53HostedZone adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53HostedZone planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func lookupHostedZone(ctx restate.RunContext, api route53zone.HostedZoneAPI, filter LookupFilter) (route53zone.ObservedState, error) {
	if strings.TrimSpace(filter.ID) != "" {
		return api.DescribeHostedZone(ctx, strings.TrimSpace(filter.ID))
	}
	var id string
	var err error
	switch {
	case strings.TrimSpace(filter.Name) != "":
		id, err = api.FindHostedZoneByName(ctx, normalizeHostedZoneLookupName(filter.Name))
	case len(filter.Tag) > 0:
		id, err = api.FindHostedZoneByTags(ctx, filter.Tag)
	default:
		return route53zone.ObservedState{}, fmt.Errorf("Route53HostedZone lookup requires at least one of: id, name, tag")
	}
	if err != nil {
		return route53zone.ObservedState{}, err
	}
	if strings.TrimSpace(id) == "" {
		return route53zone.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeHostedZone(ctx, id)
}

func normalizeHostedZoneLookupName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

func matchesHostedZoneFilter(observed route53zone.ObservedState, filter LookupFilter) bool {
	if strings.TrimSpace(filter.ID) != "" && observed.HostedZoneId != strings.TrimSpace(filter.ID) {
		return false
	}
	if strings.TrimSpace(filter.Name) != "" && observed.Name != normalizeHostedZoneLookupName(filter.Name) {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}
