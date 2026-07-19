// LambdaFunction provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + function name.
// Lambda functions are region-scoped; the key combines the AWS region and function name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/lambda"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// LambdaAdapter is the descriptor-driven adapter for LambdaFunction, extended
// with per-kind default timeouts and a post-provision readiness check.
type LambdaAdapter struct {
	*GenericAdapter[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs, lambda.ObservedState]
}

func lambdaDescriptor() GenericDescriptor[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs, lambda.ObservedState] {
	return GenericDescriptor[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs, lambda.ObservedState]{
		Kind:  lambda.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (lambda.LambdaFunctionSpec, error) {
			var parsed lambda.LambdaFunctionSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return lambda.LambdaFunctionSpec{}, fmt.Errorf("decode LambdaFunction spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return lambda.LambdaFunctionSpec{}, fmt.Errorf("LambdaFunction metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return lambda.LambdaFunctionSpec{}, fmt.Errorf("LambdaFunction spec.region is required")
			}
			parsed.FunctionName = name
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			return parsed, nil
		},

		KeyFromSpec: func(spec lambda.LambdaFunctionSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("Lambda function name", spec.FunctionName); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.FunctionName), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return JoinKey(region, resourceID), nil
		},

		PrepareSpec: func(spec lambda.LambdaFunctionSpec, key, account string) lambda.LambdaFunctionSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out lambda.LambdaFunctionOutputs) map[string]any {
			result := map[string]any{"functionArn": out.FunctionArn, "functionName": out.FunctionName}
			if out.Version != "" {
				result["version"] = out.Version
			}
			if out.State != "" {
				result["state"] = out.State
			}
			if out.LastModified != "" {
				result["lastModified"] = out.LastModified
			}
			if out.LastUpdateStatus != "" {
				result["lastUpdateStatus"] = out.LastUpdateStatus
			}
			if out.CodeSha256 != "" {
				result["codeSha256"] = out.CodeSha256
			}
			return result
		},

		// The existence check is on FunctionArn (as in the hand-rolled Plan),
		// while the describe call uses the function name.
		PlanIdentity: storedPlanIdentity[lambda.LambdaFunctionSpec](func(out lambda.LambdaFunctionOutputs) string {
			if out.FunctionArn == "" {
				return ""
			}
			return out.FunctionName
		}),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs, lambda.ObservedState] {
			return lambdaProbe(lambda.NewLambdaAPI(awsclient.NewLambdaClient(cfg)))
		},

		DiffFields: func(desired lambda.LambdaFunctionSpec, observed lambda.ObservedState, _ lambda.LambdaFunctionOutputs) []types.FieldDiff {
			rawDiffs := lambda.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// lambdaProbe adapts the driver API to the generic plan probe shape.
func lambdaProbe(api lambda.LambdaAPI) PlanProbeFunc[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs, lambda.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs]) (lambda.ObservedState, bool, error) {
		functionName := input.Identity
		obs, err := api.DescribeFunction(runCtx, functionName)
		if err != nil {
			if lambda.IsNotFound(err) {
				return lambda.ObservedState{}, false, nil
			}
			return lambda.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewLambdaAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewLambdaAdapterWithAuth(auth authservice.AuthClient) *LambdaAdapter {
	return &LambdaAdapter{GenericAdapter: NewGenericAdapter(lambdaDescriptor(), auth)}
}

// NewLambdaAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewLambdaAdapterWithAPI(api lambda.LambdaAPI) *LambdaAdapter {
	return &LambdaAdapter{GenericAdapter: NewGenericAdapterWithProbe(lambdaDescriptor(), lambdaProbe(api))}
}

// DefaultTimeouts provides per-kind default timeouts for Lambda functions.
func (a *LambdaAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "5m", Update: "5m", Delete: "5m"}
}

// WaitReady checks whether the Lambda function has reached the Active state.
func (a *LambdaAdapter) WaitReady(ctx restate.Context, key string) (WaitReadyResult, error) {
	status, err := restate.Object[types.StatusResponse](ctx, a.ServiceName(), key, "GetStatus").Request(restate.Void{})
	if err != nil {
		return WaitReadyResult{}, err
	}
	if status.Status == types.StatusReady {
		outputs, _ := fetchJSONMap(ctx, a.ServiceName(), key, "GetOutputs")
		return WaitReadyResult{Ready: true, Message: "function active", Outputs: outputs}, nil
	}
	return WaitReadyResult{Ready: false, Message: fmt.Sprintf("function status: %s", status.Status)}, nil
}
