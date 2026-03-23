package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers/keypair"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type KeyPairAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI keypair.KeyPairAPI
	apiFactory        func(aws.Config) keypair.KeyPairAPI
}

func NewKeyPairAdapter() *KeyPairAdapter {
	return NewKeyPairAdapterWithRegistry(auth.LoadFromEnv())
}

func NewKeyPairAdapterWithRegistry(accounts *auth.Registry) *KeyPairAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &KeyPairAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) keypair.KeyPairAPI {
			return keypair.NewKeyPairAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewKeyPairAdapterWithAPI(api keypair.KeyPairAPI) *KeyPairAdapter {
	return &KeyPairAdapter{staticPlanningAPI: api}
}

func (a *KeyPairAdapter) Kind() string {
	return keypair.ServiceName
}

func (a *KeyPairAdapter) ServiceName() string {
	return keypair.ServiceName
}

func (a *KeyPairAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *KeyPairAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("key pair name", spec.KeyName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.KeyName), nil
}

func (a *KeyPairAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *KeyPairAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[keypair.KeyPairSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[keypair.KeyPairSpec, keypair.KeyPairOutputs](
		restate.Object[keypair.KeyPairOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[keypair.KeyPairOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *KeyPairAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *KeyPairAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[keypair.KeyPairOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"keyName":        out.KeyName,
		"keyPairId":      out.KeyPairId,
		"keyFingerprint": out.KeyFingerprint,
		"keyType":        out.KeyType,
	}
	if out.PrivateKeyMaterial != "" {
		result["privateKeyMaterial"] = out.PrivateKeyMaterial
	}
	return result, nil
}

func (a *KeyPairAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[keypair.KeyPairSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[keypair.KeyPairOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("KeyPair Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.KeyName == "" {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}

	planningAPI, err := a.planningAPI(account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State keypair.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeKeyPair(runCtx, outputs.KeyName)
		if descErr != nil {
			if keypair.IsNotFound(descErr) {
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

	rawDiffs := keypair.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *KeyPairAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *KeyPairAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, keypair.KeyPairOutputs](
		restate.Object[keypair.KeyPairOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *KeyPairAdapter) decodeSpec(doc resourceDocument) (keypair.KeyPairSpec, error) {
	var spec keypair.KeyPairSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return keypair.KeyPairSpec{}, fmt.Errorf("decode KeyPair spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair spec.region is required")
	}
	if spec.KeyType == "" {
		spec.KeyType = "ed25519"
	}
	if spec.KeyType != "rsa" && spec.KeyType != "ed25519" {
		return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair spec.keyType must be \"rsa\" or \"ed25519\"")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	spec.KeyName = name
	spec.Account = ""
	return spec, nil
}

func (a *KeyPairAdapter) planningAPI(account string) (keypair.KeyPairAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("KeyPair adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve KeyPair planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
