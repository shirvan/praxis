// LogGroup provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + log group name.
// Log groups are region-scoped; the key combines the AWS region and log group name.
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

// LogGroupAdapter is the descriptor-driven adapter for LogGroup.
type LogGroupAdapter = GenericAdapter[loggroup.LogGroupSpec, loggroup.LogGroupOutputs, loggroup.ObservedState]

func logGroupDescriptor() GenericDescriptor[loggroup.LogGroupSpec, loggroup.LogGroupOutputs, loggroup.ObservedState] {
	return GenericDescriptor[loggroup.LogGroupSpec, loggroup.LogGroupOutputs, loggroup.ObservedState]{
		Kind:  loggroup.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (loggroup.LogGroupSpec, error) {
			var parsed loggroup.LogGroupSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return loggroup.LogGroupSpec{}, fmt.Errorf("decode LogGroup spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return loggroup.LogGroupSpec{}, fmt.Errorf("LogGroup metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return loggroup.LogGroupSpec{}, fmt.Errorf("LogGroup spec.region is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			parsed.LogGroupName = name
			return parsed, nil
		},

		KeyFromSpec: func(spec loggroup.LogGroupSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("log group name", name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, name), nil
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

		PrepareSpec: func(spec loggroup.LogGroupSpec, key, account string) loggroup.LogGroupSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out loggroup.LogGroupOutputs) map[string]any {
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
			return result
		},

		PlanIdentity: storedPlanIdentity[loggroup.LogGroupSpec](func(out loggroup.LogGroupOutputs) string { return out.LogGroupName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[loggroup.LogGroupSpec, loggroup.LogGroupOutputs, loggroup.ObservedState] {
			return logGroupProbe(loggroup.NewLogGroupAPI(awsclient.NewCloudWatchLogsClient(cfg)))
		},

		DiffFields: func(desired loggroup.LogGroupSpec, observed loggroup.ObservedState, _ loggroup.LogGroupOutputs) []types.FieldDiff {
			rawDiffs := loggroup.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// logGroupProbe adapts the driver API to the generic plan probe shape. The
// driver's describe reports existence directly alongside the observed state.
func logGroupProbe(api loggroup.LogGroupAPI) PlanProbeFunc[loggroup.LogGroupSpec, loggroup.LogGroupOutputs, loggroup.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[loggroup.LogGroupSpec, loggroup.LogGroupOutputs]) (loggroup.ObservedState, bool, error) {
		logGroupName := input.Identity
		obs, found, err := api.DescribeLogGroup(runCtx, logGroupName)
		if err != nil {
			if loggroup.IsNotFound(err) {
				return loggroup.ObservedState{}, false, nil
			}
			return loggroup.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

// NewLogGroupAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewLogGroupAdapterWithAuth(auth authservice.AuthClient) *LogGroupAdapter {
	return NewGenericAdapter(logGroupDescriptor(), auth)
}

// NewLogGroupAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewLogGroupAdapterWithAPI(api loggroup.LogGroupAPI) *LogGroupAdapter {
	return NewGenericAdapterWithProbe(logGroupDescriptor(), logGroupProbe(api))
}
