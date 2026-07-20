// SQSQueue provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + queue name.
// SQS queues are region-scoped; the key combines the AWS region and queue name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/sqs"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// SQSAdapter is the descriptor-driven adapter for SQSQueue.
type SQSAdapter = GenericAdapter[sqs.SQSQueueSpec, sqs.SQSQueueOutputs, sqs.ObservedState]

func sqsQueueDescriptor() GenericDescriptor[sqs.SQSQueueSpec, sqs.SQSQueueOutputs, sqs.ObservedState] {
	return GenericDescriptor[sqs.SQSQueueSpec, sqs.SQSQueueOutputs, sqs.ObservedState]{
		Kind:  sqs.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (sqs.SQSQueueSpec, error) {
			var parsed sqs.SQSQueueSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return sqs.SQSQueueSpec{}, fmt.Errorf("decode SQSQueue spec: %w", err)
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return sqs.SQSQueueSpec{}, fmt.Errorf("SQSQueue spec.region is required")
			}
			if strings.TrimSpace(parsed.QueueName) == "" {
				parsed.QueueName = strings.TrimSpace(metadataName)
			}
			if strings.TrimSpace(parsed.QueueName) == "" {
				return sqs.SQSQueueSpec{}, fmt.Errorf("SQSQueue spec.queueName or metadata.name is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = make(map[string]string)
			}
			if parsed.MessageRetentionPeriod == 0 {
				parsed.MessageRetentionPeriod = 345600
			}
			if parsed.MaximumMessageSize == 0 {
				parsed.MaximumMessageSize = 262144
			}
			if parsed.VisibilityTimeout == 0 {
				parsed.VisibilityTimeout = 30
			}
			if parsed.KmsMasterKeyId == "" {
				parsed.SqsManagedSseEnabled = true
			}
			if parsed.KmsMasterKeyId != "" && parsed.KmsDataKeyReusePeriodSeconds == 0 {
				parsed.KmsDataKeyReusePeriodSeconds = 300
			}
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec sqs.SQSQueueSpec, _ string) (string, error) {
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

		PrepareSpec: func(spec sqs.SQSQueueSpec, key, account string) sqs.SQSQueueSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out sqs.SQSQueueOutputs) map[string]any {
			return map[string]any{
				"queueUrl":  out.QueueUrl,
				"queueArn":  out.QueueArn,
				"queueName": out.QueueName,
			}
		},

		PlanIdentity: storedPlanIdentity[sqs.SQSQueueSpec](func(out sqs.SQSQueueOutputs) string { return out.QueueUrl }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[sqs.SQSQueueSpec, sqs.SQSQueueOutputs, sqs.ObservedState] {
			return sqsQueueProbe(sqs.NewQueueAPI(awsclient.NewSQSClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[sqs.SQSQueueOutputs] {
			return sqsQueueLookupProbe(sqs.NewQueueAPI(awsclient.NewSQSClient(cfg)))
		},

		DiffFields: func(desired sqs.SQSQueueSpec, observed sqs.ObservedState, _ sqs.SQSQueueOutputs) []types.FieldDiff {
			return sqs.ComputeFieldDiffs(desired, observed)
		},
	}
}

func sqsQueueLookupProbe(api sqs.QueueAPI) LookupProbeFunc[sqs.SQSQueueOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (sqs.SQSQueueOutputs, bool, error) {
		queueURL := strings.TrimSpace(filter.ID)
		if queueURL == "" {
			queueName := strings.TrimSpace(filter.Name)
			if queueName == "" {
				return sqs.SQSQueueOutputs{}, false, restate.TerminalError(
					fmt.Errorf("SQSQueue lookup supports id or name; tag-only lookup is not available"),
					400,
				)
			}
			var err error
			queueURL, err = api.GetQueueUrl(ctx, queueName)
			if err != nil {
				if isLookupNotFound(err, sqs.IsNotFound) {
					return sqs.SQSQueueOutputs{}, false, nil
				}
				return sqs.SQSQueueOutputs{}, false, err
			}
		}
		observed, err := api.GetQueueAttributes(ctx, queueURL)
		if err != nil {
			if isLookupNotFound(err, sqs.IsNotFound) {
				return sqs.SQSQueueOutputs{}, false, nil
			}
			return sqs.SQSQueueOutputs{}, false, err
		}
		if id := strings.TrimSpace(filter.ID); id != "" && observed.QueueUrl != id {
			return sqs.SQSQueueOutputs{}, false, nil
		}
		if name := strings.TrimSpace(filter.Name); name != "" && observed.QueueName != name {
			return sqs.SQSQueueOutputs{}, false, nil
		}
		if !matchesLookupTags(observed.Tags, LookupFilter{Tag: filter.Tag}) {
			return sqs.SQSQueueOutputs{}, false, nil
		}
		return sqs.SQSQueueOutputs{
			QueueUrl:  observed.QueueUrl,
			QueueArn:  observed.QueueArn,
			QueueName: observed.QueueName,
		}, true, nil
	}
}

// sqsQueueProbe adapts the driver API to the generic plan probe shape.
func sqsQueueProbe(api sqs.QueueAPI) PlanProbeFunc[sqs.SQSQueueSpec, sqs.SQSQueueOutputs, sqs.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[sqs.SQSQueueSpec, sqs.SQSQueueOutputs]) (sqs.ObservedState, bool, error) {
		queueURL := input.Identity
		obs, err := api.GetQueueAttributes(runCtx, queueURL)
		if err != nil {
			if sqs.IsNotFound(err) {
				return sqs.ObservedState{}, false, nil
			}
			return sqs.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewSQSAdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service.
func NewSQSAdapterWithAuth(auth authservice.AuthClient) *SQSAdapter {
	return NewGenericAdapter(sqsQueueDescriptor(), auth)
}

// NewSQSAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewSQSAdapterWithAPI(api sqs.QueueAPI) *SQSAdapter {
	return NewGenericAdapterWithProbes(sqsQueueDescriptor(), sqsQueueProbe(api), sqsQueueLookupProbe(api))
}
