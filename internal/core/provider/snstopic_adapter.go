// SNSTopic provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + topic name.
// SNS topics are region-scoped; the key combines the AWS region and topic name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/snstopic"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// SNSTopicAdapter is the descriptor-driven adapter for SNSTopic.
type SNSTopicAdapter = GenericAdapter[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs, snstopic.ObservedState]

func snsTopicDescriptor() GenericDescriptor[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs, snstopic.ObservedState] {
	return GenericDescriptor[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs, snstopic.ObservedState]{
		Kind:  snstopic.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (snstopic.SNSTopicSpec, error) {
			var parsed snstopic.SNSTopicSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return snstopic.SNSTopicSpec{}, fmt.Errorf("decode SNSTopic spec: %w", err)
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return snstopic.SNSTopicSpec{}, fmt.Errorf("SNSTopic spec.region is required")
			}
			name := strings.TrimSpace(parsed.TopicName)
			if name == "" {
				name = strings.TrimSpace(metadataName)
			}
			if name == "" {
				return snstopic.SNSTopicSpec{}, fmt.Errorf("SNSTopic spec.topicName or metadata.name is required")
			}
			if parsed.TopicName == "" {
				parsed.TopicName = name
			}
			if parsed.Tags == nil {
				parsed.Tags = make(map[string]string)
			}
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec snstopic.SNSTopicSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(spec.TopicName)
			if name == "" {
				name = strings.TrimSpace(metadataName)
			}
			if err := ValidateKeyPart("topic name", name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, name), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			// resourceID may be a topic ARN (arn:aws:sns:<region>:<account>:<topicName>) or just the topic name.
			name := resourceID
			if strings.HasPrefix(resourceID, "arn:aws:sns:") {
				parts := strings.SplitN(resourceID, ":", 6)
				if len(parts) >= 6 {
					name = parts[5]
				}
			}
			if err := ValidateKeyPart("topic name", name); err != nil {
				return "", err
			}
			return JoinKey(region, name), nil
		},

		PrepareSpec: func(spec snstopic.SNSTopicSpec, key, account string) snstopic.SNSTopicSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out snstopic.SNSTopicOutputs) map[string]any {
			result := map[string]any{
				"topicArn":  out.TopicArn,
				"topicName": out.TopicName,
			}
			if out.Owner != "" {
				result["owner"] = out.Owner
			}
			return result
		},

		PlanID: func(out snstopic.SNSTopicOutputs) string { return out.TopicArn },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[snstopic.ObservedState] {
			return snsTopicProbe(snstopic.NewTopicAPI(awsclient.NewSNSClient(cfg)))
		},

		DiffFields: func(desired snstopic.SNSTopicSpec, observed snstopic.ObservedState) []types.FieldDiff {
			rawDiffs := snstopic.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// snsTopicProbe adapts the driver API to the generic plan probe shape.
func snsTopicProbe(api snstopic.TopicAPI) PlanProbeFunc[snstopic.ObservedState] {
	return func(runCtx restate.RunContext, topicArn string) (snstopic.ObservedState, bool, error) {
		obs, err := api.GetTopicAttributes(runCtx, topicArn)
		if err != nil {
			if snstopic.IsNotFound(err) {
				return snstopic.ObservedState{}, false, nil
			}
			return snstopic.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewSNSTopicAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewSNSTopicAdapterWithAuth(auth authservice.AuthClient) *SNSTopicAdapter {
	return NewGenericAdapter(snsTopicDescriptor(), auth)
}

// NewSNSTopicAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewSNSTopicAdapterWithAPI(api snstopic.TopicAPI) *SNSTopicAdapter {
	return NewGenericAdapterWithProbe(snsTopicDescriptor(), snsTopicProbe(api))
}
