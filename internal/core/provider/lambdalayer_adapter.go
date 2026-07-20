// LambdaLayer provider adapter — descriptor for the GenericAdapter.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/lambdalayer"
	"github.com/shirvan/praxis/internal/infra/awsclient"
)

type LambdaLayerAdapter = GenericAdapter[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs, lambdalayer.ObservedState]

func lambdaLayerDescriptor() GenericDescriptor[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs, lambdalayer.ObservedState] {
	return GenericDescriptor[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs, lambdalayer.ObservedState]{
		Kind:  lambdalayer.ServiceName,
		Scope: KeyScopeRegion,
		DecodeSpec: func(raw json.RawMessage, metadataName string) (lambdalayer.LambdaLayerSpec, error) {
			var spec lambdalayer.LambdaLayerSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return lambdalayer.LambdaLayerSpec{}, fmt.Errorf("decode LambdaLayer spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return lambdalayer.LambdaLayerSpec{}, fmt.Errorf("LambdaLayer metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return lambdalayer.LambdaLayerSpec{}, fmt.Errorf("LambdaLayer spec.region is required")
			}
			spec.LayerName = name
			return spec, nil
		},
		KeyFromSpec: func(spec lambdalayer.LambdaLayerSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("Lambda layer name", spec.LayerName); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.LayerName), nil
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
		PrepareSpec: func(spec lambdalayer.LambdaLayerSpec, key, account string) lambdalayer.LambdaLayerSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},
		NormalizeOutputs: func(out lambdalayer.LambdaLayerOutputs) map[string]any {
			result := map[string]any{
				"layerArn": out.LayerArn, "layerVersionArn": out.LayerVersionArn,
				"layerName": out.LayerName, "version": out.Version,
			}
			if out.CodeSize > 0 {
				result["codeSize"] = out.CodeSize
			}
			if out.CodeSha256 != "" {
				result["codeSha256"] = out.CodeSha256
			}
			if out.CreatedDate != "" {
				result["createdDate"] = out.CreatedDate
			}
			return result
		},
		PlanIdentity: func(_ lambdalayer.LambdaLayerSpec, outputs lambdalayer.LambdaLayerOutputs) (string, bool) {
			return outputs.LayerName, outputs.LayerVersionArn != ""
		},
		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs, lambdalayer.ObservedState] {
			return lambdaLayerProbe(lambdalayer.NewLayerAPI(awsclient.NewLambdaClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[lambdalayer.LambdaLayerOutputs] {
			return lambdaLayerLookupProbe(lambdalayer.NewLayerAPI(awsclient.NewLambdaClient(cfg)))
		},
		DiffFields: lambdalayer.ComputeFieldDiffs,
	}
}

func lambdaLayerLookupProbe(api lambdalayer.LayerAPI) LookupProbeFunc[lambdalayer.LambdaLayerOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (lambdalayer.LambdaLayerOutputs, bool, error) {
		if strings.TrimSpace(filter.ID) != "" {
			return lambdalayer.LambdaLayerOutputs{}, false, restate.TerminalError(
				fmt.Errorf("LambdaLayer lookup by id is not available; use name"),
				400,
			)
		}
		if len(filter.Tag) != 0 {
			return lambdalayer.LambdaLayerOutputs{}, false, restate.TerminalError(
				fmt.Errorf("LambdaLayer lookup does not support tags"),
				400,
			)
		}
		name := strings.TrimSpace(filter.Name)
		if name == "" {
			return lambdalayer.LambdaLayerOutputs{}, false, restate.TerminalError(
				fmt.Errorf("LambdaLayer lookup requires name"),
				400,
			)
		}
		observed, err := api.GetLatestLayerVersion(ctx, name)
		if err != nil {
			if isLookupNotFound(err, lambdalayer.IsNotFound) {
				return lambdalayer.LambdaLayerOutputs{}, false, nil
			}
			return lambdalayer.LambdaLayerOutputs{}, false, err
		}
		if observed.LayerName != name {
			return lambdalayer.LambdaLayerOutputs{}, false, nil
		}
		return lambdalayer.LambdaLayerOutputs{
			LayerArn:        observed.LayerArn,
			LayerVersionArn: observed.LayerVersionArn,
			LayerName:       observed.LayerName,
			Version:         observed.Version,
			CodeSize:        observed.CodeSize,
			CodeSha256:      observed.CodeSha256,
			CreatedDate:     observed.CreatedDate,
		}, true, nil
	}
}

func lambdaLayerProbe(api lambdalayer.LayerAPI) PlanProbeFunc[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs, lambdalayer.ObservedState] {
	return func(ctx restate.RunContext, input PlanProbeInput[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs]) (lambdalayer.ObservedState, bool, error) {
		observed, err := api.GetLatestLayerVersion(ctx, input.Identity)
		if err != nil {
			if lambdalayer.IsNotFound(err) {
				return lambdalayer.ObservedState{}, false, nil
			}
			return lambdalayer.ObservedState{}, false, err
		}
		return observed, true, nil
	}
}

func NewLambdaLayerAdapterWithAuth(auth authservice.AuthClient) *LambdaLayerAdapter {
	return NewGenericAdapter(lambdaLayerDescriptor(), auth)
}

func NewLambdaLayerAdapterWithAPI(api lambdalayer.LayerAPI) *LambdaLayerAdapter {
	return NewGenericAdapterWithProbes(lambdaLayerDescriptor(), lambdaLayerProbe(api), lambdaLayerLookupProbe(api))
}
