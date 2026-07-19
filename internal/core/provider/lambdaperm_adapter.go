// LambdaPermission provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + function name + statement ID.
// Lambda permissions are region-scoped and attached to a function; the key combines region, function name, and statement ID.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/lambdaperm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// LambdaPermissionAdapter is the descriptor-driven adapter for LambdaPermission.
type LambdaPermissionAdapter = GenericAdapter[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs, lambdaperm.ObservedState]

func lambdaPermissionDescriptor() GenericDescriptor[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs, lambdaperm.ObservedState] {
	return GenericDescriptor[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs, lambdaperm.ObservedState]{
		Kind:  lambdaperm.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (lambdaperm.LambdaPermissionSpec, error) {
			var spec lambdaperm.LambdaPermissionSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("decode LambdaPermission spec: %w", err)
			}
			if strings.TrimSpace(spec.Region) == "" {
				return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("LambdaPermission spec.region is required")
			}
			if strings.TrimSpace(spec.StatementId) == "" {
				spec.StatementId = strings.TrimSpace(metadataName)
			}
			if strings.TrimSpace(spec.StatementId) == "" {
				return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("LambdaPermission metadata.name or spec.statementId is required")
			}
			return lambdaperm.LambdaPermissionSpec{Region: spec.Region, FunctionName: spec.FunctionName, StatementId: spec.StatementId, Action: spec.Action, Principal: spec.Principal, SourceArn: spec.SourceArn, SourceAccount: spec.SourceAccount, EventSourceToken: spec.EventSourceToken, Qualifier: spec.Qualifier}, nil
		},

		KeyFromSpec: func(spec lambdaperm.LambdaPermissionSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("function name", spec.FunctionName); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("statement ID", spec.StatementId); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.FunctionName, spec.StatementId), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			functionName, statementID, err := lambdapermSplitResourceID(resourceID)
			if err != nil {
				return "", err
			}
			if err := ValidateKeyPart("function name", functionName); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("statement ID", statementID); err != nil {
				return "", err
			}
			return JoinKey(region, functionName, statementID), nil
		},

		PrepareSpec: func(spec lambdaperm.LambdaPermissionSpec, key, account string) lambdaperm.LambdaPermissionSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out lambdaperm.LambdaPermissionOutputs) map[string]any {
			return map[string]any{"statementId": out.StatementId, "functionName": out.FunctionName, "statement": out.Statement}
		},

		// The plan-time describe needs both the function name and statement ID;
		// they are packed into a single functionName~statementId identifier
		// (the same composite form used for imports) and split in the probe.
		PlanIdentity: storedPlanIdentity[lambdaperm.LambdaPermissionSpec](func(out lambdaperm.LambdaPermissionOutputs) string {
			if out.StatementId == "" {
				return ""
			}
			return out.FunctionName + "~" + out.StatementId
		}),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs, lambdaperm.ObservedState] {
			return lambdaPermissionProbe(lambdaperm.NewPermissionAPI(awsclient.NewLambdaClient(cfg)))
		},

		DiffFields: func(desired lambdaperm.LambdaPermissionSpec, observed lambdaperm.ObservedState, _ lambdaperm.LambdaPermissionOutputs) []types.FieldDiff {
			rawDiffs := lambdaperm.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// lambdaPermissionProbe adapts the driver API to the generic plan probe shape.
// The planID is the composite functionName~statementId produced by PlanID.
func lambdaPermissionProbe(api lambdaperm.PermissionAPI) PlanProbeFunc[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs, lambdaperm.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs]) (lambdaperm.ObservedState, bool, error) {
		planID := input.Identity
		functionName, statementID, err := lambdapermSplitResourceID(planID)
		if err != nil {
			return lambdaperm.ObservedState{}, false, err
		}
		obs, err := api.GetPermission(runCtx, functionName, statementID)
		if err != nil {
			if lambdaperm.IsNotFound(err) {
				return lambdaperm.ObservedState{}, false, nil
			}
			return lambdaperm.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewLambdaPermissionAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewLambdaPermissionAdapterWithAuth(auth authservice.AuthClient) *LambdaPermissionAdapter {
	return NewGenericAdapter(lambdaPermissionDescriptor(), auth)
}

// NewLambdaPermissionAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewLambdaPermissionAdapterWithAPI(api lambdaperm.PermissionAPI) *LambdaPermissionAdapter {
	return NewGenericAdapterWithProbe(lambdaPermissionDescriptor(), lambdaPermissionProbe(api))
}

func lambdapermSplitResourceID(resourceID string) (string, string, error) {
	parts := strings.SplitN(resourceID, "~", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("import resource ID must be functionName~statementId")
	}
	return parts[0], parts[1], nil
}
