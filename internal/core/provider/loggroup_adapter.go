package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/loggroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type LogGroupAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI loggroup.LogGroupAPI
	apiFactory        func(aws.Config) loggroup.LogGroupAPI
}

func NewLogGroupAdapterWithAuth(auth authservice.AuthClient) *LogGroupAdapter {
	return &LogGroupAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) loggroup.LogGroupAPI {
			return loggroup.NewLogGroupAPI(awsclient.NewCloudWatchLogsClient(cfg))
		},
	}
}

func NewLogGroupAdapterWithAPI(api loggroup.LogGroupAPI) *LogGroupAdapter {
	return &LogGroupAdapter{staticPlanningAPI: api}
}

func (a *LogGroupAdapter) Kind() string { return loggroup.ServiceName }

func (a *LogGroupAdapter) ServiceName() string { return loggroup.ServiceName }

func (a *LogGroupAdapter) Scope() KeyScope { return KeyScopeRegion }

func (a *LogGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("log group name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *LogGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *LogGroupAdapter) Provision(ctx restate.Context, key, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[loggroup.LogGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[loggroup.LogGroupSpec, loggroup.LogGroupOutputs](
		restate.Object[loggroup.LogGroupOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)
	return &provisionHandle[loggroup.LogGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *LogGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *LogGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[loggroup.LogGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"logGroupName":    out.LogGroupName,
		"logGroupClass":   out.LogGroupClass,
		"retentionInDays": out.RetentionInDays,
		"creationTime":    out.CreationTime,
		"storedBytes":     out.StoredBytes,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	if out.KmsKeyID != "" {
		result["kmsKeyId"] = out.KmsKeyID
	}
	return result, nil
}

func (a *LogGroupAdapter) Plan(ctx restate.Context, key, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[loggroup.LogGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[loggroup.LogGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("LogGroup Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.LogGroupName == "" {
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
		State loggroup.ObservedState
		Found bool
	}, error) {
		obs, found, runErr := planningAPI.DescribeLogGroup(runCtx, outputs.LogGroupName)
		if runErr != nil {
			if loggroup.IsNotFound(runErr) {
				return struct {
					State loggroup.ObservedState
					Found bool
				}{Found: false}, nil
			}
			return struct {
				State loggroup.ObservedState
				Found bool
			}{}, restate.TerminalError(runErr, 500)
		}
		return struct {
			State loggroup.ObservedState
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
	rawDiffs := loggroup.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *LogGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *LogGroupAdapter) Import(ctx restate.Context, key, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, loggroup.LogGroupOutputs](
		restate.Object[loggroup.LogGroupOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *LogGroupAdapter) decodeSpec(doc resourceDocument) (loggroup.LogGroupSpec, error) {
	var spec loggroup.LogGroupSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return loggroup.LogGroupSpec{}, fmt.Errorf("decode LogGroup spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return loggroup.LogGroupSpec{}, fmt.Errorf("LogGroup metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return loggroup.LogGroupSpec{}, fmt.Errorf("LogGroup spec.region is required")
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.LogGroupName = name
	return spec, nil
}

func (a *LogGroupAdapter) planningAPI(ctx restate.Context, account string) (loggroup.LogGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("LogGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve LogGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
