// SQSQueuePolicy provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + queue name.
// SQS queue policies are region-scoped, tied to a queue; the key combines the
// AWS region and queue name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/sqspolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// SQSQueuePolicyAdapter is the descriptor-driven adapter for SQSQueuePolicy.
type SQSQueuePolicyAdapter = GenericAdapter[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs, sqspolicy.ObservedState]

func sqsQueuePolicyDescriptor() GenericDescriptor[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs, sqspolicy.ObservedState] {
	return GenericDescriptor[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs, sqspolicy.ObservedState]{
		Kind:  sqspolicy.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (sqspolicy.SQSQueuePolicySpec, error) {
			var parsed struct {
				Region    string          `json:"region"`
				QueueName string          `json:"queueName"`
				Policy    json.RawMessage `json:"policy"`
			}
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return sqspolicy.SQSQueuePolicySpec{}, fmt.Errorf("decode SQSQueuePolicy spec: %w", err)
			}
			queueName := strings.TrimSpace(parsed.QueueName)
			if queueName == "" {
				queueName = strings.TrimSpace(metadataName)
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return sqspolicy.SQSQueuePolicySpec{}, fmt.Errorf("SQSQueuePolicy spec.region is required")
			}
			if queueName == "" {
				return sqspolicy.SQSQueuePolicySpec{}, fmt.Errorf("SQSQueuePolicy spec.queueName or metadata.name is required")
			}
			if len(parsed.Policy) == 0 {
				return sqspolicy.SQSQueuePolicySpec{}, fmt.Errorf("SQSQueuePolicy spec.policy is required")
			}
			return sqspolicy.SQSQueuePolicySpec{Region: parsed.Region, QueueName: queueName, Policy: string(parsed.Policy)}, nil
		},

		KeyFromSpec: func(spec sqspolicy.SQSQueuePolicySpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("queue name", spec.QueueName); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.QueueName), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			queueName := resourceID
			if strings.HasPrefix(resourceID, "http://") || strings.HasPrefix(resourceID, "https://") {
				parts := strings.Split(resourceID, "/")
				queueName = parts[len(parts)-1]
			}
			if err := ValidateKeyPart("queue name", queueName); err != nil {
				return "", err
			}
			return JoinKey(region, queueName), nil
		},

		PrepareSpec: func(spec sqspolicy.SQSQueuePolicySpec, _ string, account string) sqspolicy.SQSQueuePolicySpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out sqspolicy.SQSQueuePolicyOutputs) map[string]any {
			return map[string]any{
				"queueUrl":  out.QueueUrl,
				"queueArn":  out.QueueArn,
				"queueName": out.QueueName,
			}
		},

		PlanIdentity: storedPlanIdentity[sqspolicy.SQSQueuePolicySpec](func(out sqspolicy.SQSQueuePolicyOutputs) string { return out.QueueUrl }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs, sqspolicy.ObservedState] {
			return sqsQueuePolicyProbe(sqspolicy.NewPolicyAPI(awsclient.NewSQSClient(cfg)))
		},

		DiffFields: func(desired sqspolicy.SQSQueuePolicySpec, observed sqspolicy.ObservedState, _ sqspolicy.SQSQueuePolicyOutputs) []types.FieldDiff {
			return sqspolicy.ComputeFieldDiffs(desired, observed)
		},
	}
}

// sqsQueuePolicyProbe adapts the driver API to the generic plan probe shape.
func sqsQueuePolicyProbe(api sqspolicy.PolicyAPI) PlanProbeFunc[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs, sqspolicy.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs]) (sqspolicy.ObservedState, bool, error) {
		queueURL := input.Identity
		obs, err := api.GetQueuePolicy(runCtx, queueURL)
		if err != nil {
			if sqspolicy.IsNotFound(err) {
				return sqspolicy.ObservedState{}, false, nil
			}
			return sqspolicy.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewSQSQueuePolicyAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewSQSQueuePolicyAdapterWithAuth(auth authservice.AuthClient) *SQSQueuePolicyAdapter {
	return NewGenericAdapter(sqsQueuePolicyDescriptor(), auth)
}

// NewSQSQueuePolicyAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewSQSQueuePolicyAdapterWithAPI(api sqspolicy.PolicyAPI) *SQSQueuePolicyAdapter {
	return NewGenericAdapterWithProbe(sqsQueuePolicyDescriptor(), sqsQueuePolicyProbe(api))
}
