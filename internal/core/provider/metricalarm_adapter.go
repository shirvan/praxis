package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/metricalarm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type MetricAlarmAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI metricalarm.MetricAlarmAPI
	apiFactory        func(aws.Config) metricalarm.MetricAlarmAPI
}

func NewMetricAlarmAdapterWithAuth(auth authservice.AuthClient) *MetricAlarmAdapter {
	return &MetricAlarmAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) metricalarm.MetricAlarmAPI {
			return metricalarm.NewMetricAlarmAPI(awsclient.NewCloudWatchClient(cfg))
		},
	}
}

func NewMetricAlarmAdapterWithAPI(api metricalarm.MetricAlarmAPI) *MetricAlarmAdapter {
	return &MetricAlarmAdapter{staticPlanningAPI: api}
}

func (a *MetricAlarmAdapter) Kind() string { return metricalarm.ServiceName }

func (a *MetricAlarmAdapter) ServiceName() string { return metricalarm.ServiceName }

func (a *MetricAlarmAdapter) Scope() KeyScope { return KeyScopeRegion }

func (a *MetricAlarmAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("alarm name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *MetricAlarmAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *MetricAlarmAdapter) Provision(ctx restate.Context, key, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[metricalarm.MetricAlarmSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs](
		restate.Object[metricalarm.MetricAlarmOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)
	return &provisionHandle[metricalarm.MetricAlarmOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *MetricAlarmAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *MetricAlarmAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[metricalarm.MetricAlarmOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"alarmArn":   out.AlarmArn,
		"alarmName":  out.AlarmName,
		"stateValue": out.StateValue,
	}
	if out.StateReason != "" {
		result["stateReason"] = out.StateReason
	}
	return result, nil
}

func (a *MetricAlarmAdapter) Plan(ctx restate.Context, key, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[metricalarm.MetricAlarmSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[metricalarm.MetricAlarmOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("MetricAlarm Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.AlarmName == "" {
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
		State metricalarm.ObservedState
		Found bool
	}, error) {
		obs, found, runErr := planningAPI.DescribeAlarm(runCtx, outputs.AlarmName)
		if runErr != nil {
			if metricalarm.IsNotFound(runErr) {
				return struct {
					State metricalarm.ObservedState
					Found bool
				}{Found: false}, nil
			}
			return struct {
				State metricalarm.ObservedState
				Found bool
			}{}, restate.TerminalError(runErr, 500)
		}
		return struct {
			State metricalarm.ObservedState
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
	rawDiffs := metricalarm.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *MetricAlarmAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *MetricAlarmAdapter) Import(ctx restate.Context, key, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, metricalarm.MetricAlarmOutputs](
		restate.Object[metricalarm.MetricAlarmOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *MetricAlarmAdapter) decodeSpec(doc resourceDocument) (metricalarm.MetricAlarmSpec, error) {
	var spec metricalarm.MetricAlarmSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return metricalarm.MetricAlarmSpec{}, fmt.Errorf("decode MetricAlarm spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return metricalarm.MetricAlarmSpec{}, fmt.Errorf("MetricAlarm metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return metricalarm.MetricAlarmSpec{}, fmt.Errorf("MetricAlarm spec.region is required")
	}
	if spec.Dimensions == nil {
		spec.Dimensions = map[string]string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.AlarmName = name
	return spec, nil
}

func (a *MetricAlarmAdapter) planningAPI(ctx restate.Context, account string) (metricalarm.MetricAlarmAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("MetricAlarm adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve MetricAlarm planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
