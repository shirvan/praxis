package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type ECRLifecyclePolicyAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI ecrpolicy.LifecyclePolicyAPI
	apiFactory        func(aws.Config) ecrpolicy.LifecyclePolicyAPI
}

func NewECRLifecyclePolicyAdapterWithAuth(auth authservice.AuthClient) *ECRLifecyclePolicyAdapter {
	return &ECRLifecyclePolicyAdapter{auth: auth, apiFactory: func(cfg aws.Config) ecrpolicy.LifecyclePolicyAPI {
		return ecrpolicy.NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg))
	}}
}

func (a *ECRLifecyclePolicyAdapter) Kind() string        { return ecrpolicy.ServiceName }
func (a *ECRLifecyclePolicyAdapter) ServiceName() string { return ecrpolicy.ServiceName }
func (a *ECRLifecyclePolicyAdapter) Scope() KeyScope     { return KeyScopeCustom }

func (a *ECRLifecyclePolicyAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("repository name", spec.RepositoryName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.RepositoryName), nil
}

func (a *ECRLifecyclePolicyAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *ECRLifecyclePolicyAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[ecrpolicy.ECRLifecyclePolicySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs](restate.Object[ecrpolicy.ECRLifecyclePolicyOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[ecrpolicy.ECRLifecyclePolicyOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *ECRLifecyclePolicyAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *ECRLifecyclePolicyAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[ecrpolicy.ECRLifecyclePolicyOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"repositoryName": out.RepositoryName}
	if out.RepositoryArn != "" {
		result["repositoryArn"] = out.RepositoryArn
	}
	if out.RegistryId != "" {
		result["registryId"] = out.RegistryId
	}
	return result, nil
}

func (a *ECRLifecyclePolicyAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[ecrpolicy.ECRLifecyclePolicySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[ecrpolicy.ECRLifecyclePolicyOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ECRLifecyclePolicy Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.RepositoryName == "" {
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
		State ecrpolicy.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetLifecyclePolicy(runCtx, outputs.RepositoryName)
		if descErr != nil {
			if ecrpolicy.IsNotFound(descErr) {
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
	rawDiffs := ecrpolicy.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *ECRLifecyclePolicyAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *ECRLifecyclePolicyAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, ecrpolicy.ECRLifecyclePolicyOutputs](restate.Object[ecrpolicy.ECRLifecyclePolicyOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *ECRLifecyclePolicyAdapter) decodeSpec(doc resourceDocument) (ecrpolicy.ECRLifecyclePolicySpec, error) {
	var spec ecrpolicy.ECRLifecyclePolicySpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("decode ECRLifecyclePolicy spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("ECRLifecyclePolicy spec.region is required")
	}
	if strings.TrimSpace(spec.RepositoryName) == "" {
		return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("ECRLifecyclePolicy spec.repositoryName is required")
	}
	return spec, nil
}

func (a *ECRLifecyclePolicyAdapter) planningAPI(ctx restate.Context, account string) (ecrpolicy.LifecyclePolicyAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ecr lifecycle policy adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ECR lifecycle policy planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
