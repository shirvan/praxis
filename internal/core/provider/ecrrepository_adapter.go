package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type ECRRepositoryAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI ecrrepo.RepositoryAPI
	apiFactory        func(aws.Config) ecrrepo.RepositoryAPI
}

func NewECRRepositoryAdapterWithAuth(auth authservice.AuthClient) *ECRRepositoryAdapter {
	return &ECRRepositoryAdapter{auth: auth, apiFactory: func(cfg aws.Config) ecrrepo.RepositoryAPI { return ecrrepo.NewRepositoryAPI(awsclient.NewECRClient(cfg)) }}
}

func (a *ECRRepositoryAdapter) Kind() string        { return ecrrepo.ServiceName }
func (a *ECRRepositoryAdapter) ServiceName() string { return ecrrepo.ServiceName }
func (a *ECRRepositoryAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *ECRRepositoryAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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

func (a *ECRRepositoryAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *ECRRepositoryAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[ecrrepo.ECRRepositorySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs](restate.Object[ecrrepo.ECRRepositoryOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[ecrrepo.ECRRepositoryOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *ECRRepositoryAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *ECRRepositoryAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[ecrrepo.ECRRepositoryOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repositoryArn": out.RepositoryArn, "repositoryName": out.RepositoryName, "repositoryUri": out.RepositoryUri, "registryId": out.RegistryId}, nil
}

func (a *ECRRepositoryAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[ecrrepo.ECRRepositorySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[ecrrepo.ECRRepositoryOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ECRRepository Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.RepositoryArn == "" {
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
		State ecrrepo.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeRepository(runCtx, outputs.RepositoryName)
		if descErr != nil {
			if ecrrepo.IsNotFound(descErr) {
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
	rawDiffs := ecrrepo.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *ECRRepositoryAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *ECRRepositoryAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, ecrrepo.ECRRepositoryOutputs](restate.Object[ecrrepo.ECRRepositoryOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *ECRRepositoryAdapter) decodeSpec(doc resourceDocument) (ecrrepo.ECRRepositorySpec, error) {
	var spec ecrrepo.ECRRepositorySpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("decode ECRRepository spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("ECRRepository metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("ECRRepository spec.region is required")
	}
	spec.RepositoryName = name
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec, nil
}

func (a *ECRRepositoryAdapter) planningAPI(ctx restate.Context, account string) (ecrrepo.RepositoryAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ecr repository adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ECR repository planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}