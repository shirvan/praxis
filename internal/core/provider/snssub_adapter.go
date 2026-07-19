// SNSSubscription provider adapter — descriptor for the GenericAdapter.
//
// Key scope: custom.
// Key parts: region + topicArn + protocol + endpoint.
// SNS subscriptions are region-scoped; the key combines the AWS region,
// topic ARN, protocol, and endpoint.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/snssub"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// SNSSubscriptionAdapter is the descriptor-driven adapter for SNSSubscription.
type SNSSubscriptionAdapter = GenericAdapter[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs, snssub.ObservedState]

func snsSubscriptionDescriptor() GenericDescriptor[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs, snssub.ObservedState] {
	return GenericDescriptor[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs, snssub.ObservedState]{
		Kind:  snssub.ServiceName,
		Scope: KeyScopeCustom,

		DecodeSpec: func(spec json.RawMessage, _ string) (snssub.SNSSubscriptionSpec, error) {
			var parsed snssub.SNSSubscriptionSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return snssub.SNSSubscriptionSpec{}, fmt.Errorf("decode SNSSubscription spec: %w", err)
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return snssub.SNSSubscriptionSpec{}, fmt.Errorf("SNSSubscription spec.region is required")
			}
			if strings.TrimSpace(parsed.TopicArn) == "" {
				return snssub.SNSSubscriptionSpec{}, fmt.Errorf("SNSSubscription spec.topicArn is required")
			}
			if strings.TrimSpace(parsed.Protocol) == "" {
				return snssub.SNSSubscriptionSpec{}, fmt.Errorf("SNSSubscription spec.protocol is required")
			}
			if strings.TrimSpace(parsed.Endpoint) == "" {
				return snssub.SNSSubscriptionSpec{}, fmt.Errorf("SNSSubscription spec.endpoint is required")
			}
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec snssub.SNSSubscriptionSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("topicArn", spec.TopicArn); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("protocol", spec.Protocol); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("endpoint", spec.Endpoint); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.TopicArn, spec.Protocol, spec.Endpoint), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("subscription ARN", resourceID); err != nil {
				return "", err
			}
			// resourceID is the subscription ARN; the driver resolves topic/protocol/endpoint from it.
			return JoinKey(region, resourceID), nil
		},

		PrepareSpec: func(spec snssub.SNSSubscriptionSpec, _ string, account string) snssub.SNSSubscriptionSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out snssub.SNSSubscriptionOutputs) map[string]any {
			result := map[string]any{
				"subscriptionArn": out.SubscriptionArn,
				"topicArn":        out.TopicArn,
				"protocol":        out.Protocol,
				"endpoint":        out.Endpoint,
			}
			if out.Owner != "" {
				result["owner"] = out.Owner
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[snssub.SNSSubscriptionSpec](func(out snssub.SNSSubscriptionOutputs) string { return out.SubscriptionArn }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs, snssub.ObservedState] {
			return snsSubscriptionProbe(snssub.NewSubscriptionAPI(awsclient.NewSNSClient(cfg)))
		},

		DiffFields: func(desired snssub.SNSSubscriptionSpec, observed snssub.ObservedState, _ snssub.SNSSubscriptionOutputs) []types.FieldDiff {
			rawDiffs := snssub.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// snsSubscriptionProbe adapts the driver API to the generic plan probe shape.
func snsSubscriptionProbe(api snssub.SubscriptionAPI) PlanProbeFunc[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs, snssub.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs]) (snssub.ObservedState, bool, error) {
		subscriptionArn := input.Identity
		obs, err := api.GetSubscriptionAttributes(runCtx, subscriptionArn)
		if err != nil {
			if snssub.IsNotFound(err) {
				return snssub.ObservedState{}, false, nil
			}
			return snssub.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewSNSSubscriptionAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewSNSSubscriptionAdapterWithAuth(auth authservice.AuthClient) *SNSSubscriptionAdapter {
	return NewGenericAdapter(snsSubscriptionDescriptor(), auth)
}

// NewSNSSubscriptionAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewSNSSubscriptionAdapterWithAPI(api snssub.SubscriptionAPI) *SNSSubscriptionAdapter {
	return NewGenericAdapterWithProbe(snsSubscriptionDescriptor(), snsSubscriptionProbe(api))
}
