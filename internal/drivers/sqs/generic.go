package sqs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsarn "github.com/aws/aws-sdk-go-v2/aws/arn"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) QueueAPI
}

// NewGenericSQSQueueDriver binds SQS queue provider behavior to the generic lifecycle kernel.
func NewGenericSQSQueueDriver(auth authservice.AuthClient) *kernel.Driver[SQSQueueSpec, SQSQueueOutputs, ObservedState] {
	return newGenericSQSQueueDriverWithFactory(auth, nil)
}

func newGenericSQSQueueDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) QueueAPI) *kernel.Driver[SQSQueueSpec, SQSQueueOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) QueueAPI { return NewQueueAPI(awsclient.NewSQSClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[SQSQueueSpec, SQSQueueOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			ManagedDriftCorrection: true, LateInitialization: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec SQSQueueSpec) (SQSQueueSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return SQSQueueSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return SQSQueueSpec{}, restate.TerminalError(fmt.Errorf(
					"region %q does not match account region %q", spec.Region, region,
				), 400)
			}
			// ManagedKey is internal ownership metadata. The Restate object key is
			// the only current alpha identity; caller-provided values are ignored.
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) SQSQueueSpec {
			return specFromObserved(observed, ref)
		},
		OutputsFromObserved: outputsFromObserved,
		HasDrift:            HasDrift,
		LateInitialize:      lateInitializeFIFODefaults,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired SQSQueueSpec, outputs SQSQueueOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}

	if strings.TrimSpace(outputs.QueueUrl) != "" {
		observation, observeErr := observeQueueURL(ctx, api, outputs.QueueUrl)
		if observeErr == nil && observation.Exists {
			if current := observation.Value.Tags["praxis:managed-key"]; current != "" && current != desired.ManagedKey {
				return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
					"queue %q is tagged as owned by Praxis object %q, not %q",
					observation.Value.QueueName, current, desired.ManagedKey,
				), 409)
			}
		}
		return observation, observeErr
	}

	queueName := strings.TrimSpace(desired.QueueName)
	if strings.TrimSpace(outputs.QueueArn) != "" {
		parsedName, parseErr := queueNameFromARN(outputs.QueueArn)
		if parseErr != nil {
			return kernel.Observation[ObservedState]{}, restate.TerminalError(parseErr, 400)
		}
		queueName = parsedName
	}
	if queueName == "" {
		return kernel.Observation[ObservedState]{}, nil
	}

	queueURL, found, err := resolveQueueURLByName(ctx, api, queueName)
	if err != nil || !found {
		return kernel.Observation[ObservedState]{}, err
	}
	observation, err := observeQueueURL(ctx, api, queueURL)
	if err != nil || !observation.Exists {
		return observation, err
	}
	// With no committed URL, a name match is only crash recovery when the
	// queue was atomically created with this object's exact managed key.
	if observation.Value.Tags["praxis:managed-key"] != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"queue %q already exists but is not owned by Praxis object %q",
			queueName, desired.ManagedKey,
		), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired SQSQueueSpec) (kernel.CreateResult[SQSQueueOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[SQSQueueOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	queueURL, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// SQS has no client token. CreateQueue is provider-idempotent by queue
		// name and the complete attribute set, and the ownership tag is included
		// atomically so observe-before-create can recover an ambiguous response.
		return api.CreateQueue(rc, desired)
	}, classifyQueueMutation)
	return kernel.CreateResult[SQSQueueOutputs]{SeedOutputs: SQSQueueOutputs{
		QueueUrl: queueURL, QueueName: desired.QueueName,
	}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired SQSQueueSpec, observed ObservedState) error {
	if err := validateImmutableIdentity(desired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if current := observed.Tags["praxis:managed-key"]; current != "" && current != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf(
			"queue %q is tagged as owned by Praxis object %q, not %q",
			desired.QueueName, current, desired.ManagedKey,
		), 409)
	}

	attrs, err := changedQueueAttributes(desired, observed)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	if len(attrs) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetQueueAttributes(rc, observed.QueueUrl, attrs)
		}, classifyQueueMutation); err != nil {
			return err
		}
	}

	if !drivers.TagsMatch(desired.Tags, observed.Tags) ||
		(desired.ManagedKey != "" && observed.Tags["praxis:managed-key"] != desired.ManagedKey) {
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.QueueUrl, managedTags(desired.Tags, desired.ManagedKey))
		}, classifyQueueMutation)
	}
	return err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired SQSQueueSpec, outputs SQSQueueOutputs) error {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	queueURL := strings.TrimSpace(outputs.QueueUrl)
	if queueURL == "" && desired.QueueName != "" {
		var found bool
		queueURL, found, err = resolveQueueURLByName(ctx, api, desired.QueueName)
		if err != nil || !found {
			return err
		}
		observation, observeErr := observeQueueURL(ctx, api, queueURL)
		if observeErr != nil || !observation.Exists {
			return observeErr
		}
		if desired.ManagedKey == "" || observation.Value.Tags["praxis:managed-key"] != desired.ManagedKey {
			return restate.TerminalError(fmt.Errorf(
				"refusing to delete name-resolved queue %q without exact Praxis ownership tag %q",
				desired.QueueName, desired.ManagedKey,
			), 409)
		}
	}
	if queueURL == "" {
		return nil
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteQueue(rc, queueURL)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyQueueMutation)
	// DeleteQueue is acceptance-based: AWS may retain the queue briefly and
	// enforces a 60-second same-name recreation cooldown. QueueDeletedRecently
	// remains retryable rather than being misclassified as a permanent conflict.
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	resourceID := strings.TrimSpace(ref.ResourceID)
	if strings.HasPrefix(resourceID, "http://") || strings.HasPrefix(resourceID, "https://") {
		return observeQueueURL(ctx, api, resourceID)
	}
	if strings.HasPrefix(resourceID, "arn:") {
		parsed, parseErr := awsarn.Parse(resourceID)
		if parseErr != nil || parsed.Service != "sqs" || parsed.Resource == "" {
			return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("invalid SQS queue ARN %q", resourceID), 400)
		}
		if region != "" && parsed.Region != region {
			return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
				"queue ARN region %q does not match account region %q", parsed.Region, region,
			), 400)
		}
		resourceID = parsed.Resource
	}
	queueURL, found, err := resolveQueueURLByName(ctx, api, resourceID)
	if err != nil || !found {
		return kernel.Observation[ObservedState]{}, err
	}
	return observeQueueURL(ctx, api, queueURL)
}

func observeQueueURL(ctx restate.ObjectContext, api QueueAPI, queueURL string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.GetQueueAttributes(rc, queueURL)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyQueueObserve)
}

func resolveQueueURLByName(ctx restate.ObjectContext, api QueueAPI, queueName string) (string, bool, error) {
	queueURL, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		url, runErr := api.GetQueueUrl(rc, queueName)
		if IsNotFound(runErr) {
			return "", nil
		}
		return url, runErr
	}, classifyQueueObserve)
	return queueURL, queueURL != "", err
}

func (o *genericOperations) apiForAccount(ctx restate.ObjectContext, account string) (QueueAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("SQSQueue driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SQS account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyQueueObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidInput(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyQueueMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsAlreadyExists(err) {
		return restate.TerminalError(err, 409)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidInput(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	// QueueDeletedRecently is a temporary provider cooldown and must retry.
	return err
}

func changedQueueAttributes(desired SQSQueueSpec, observed ObservedState) (map[string]string, error) {
	attrs := map[string]string{}
	setInt := func(name string, desiredValue, observedValue int) {
		if desiredValue != observedValue {
			attrs[name] = strconv.Itoa(desiredValue)
		}
	}
	setInt("VisibilityTimeout", desired.VisibilityTimeout, observed.VisibilityTimeout)
	setInt("MessageRetentionPeriod", desired.MessageRetentionPeriod, observed.MessageRetentionPeriod)
	setInt("MaximumMessageSize", desired.MaximumMessageSize, observed.MaximumMessageSize)
	setInt("DelaySeconds", desired.DelaySeconds, observed.DelaySeconds)
	setInt("ReceiveMessageWaitTimeSeconds", desired.ReceiveMessageWaitTimeSeconds, observed.ReceiveMessageWaitTimeSeconds)

	if !redrivePolicyEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
		if desired.RedrivePolicy == nil {
			attrs["RedrivePolicy"] = ""
		} else {
			payload, err := json.Marshal(desired.RedrivePolicy)
			if err != nil {
				return nil, fmt.Errorf("encode redrive policy: %w", err)
			}
			attrs["RedrivePolicy"] = string(payload)
		}
	}

	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		attrs["KmsMasterKeyId"] = desired.KmsMasterKeyId
	}
	if desired.KmsMasterKeyId != "" {
		if desired.KmsDataKeyReusePeriodSeconds != observed.KmsDataKeyReusePeriodSeconds {
			attrs["KmsDataKeyReusePeriodSeconds"] = strconv.Itoa(desired.KmsDataKeyReusePeriodSeconds)
		}
		if observed.SqsManagedSseEnabled {
			attrs["SqsManagedSseEnabled"] = "false"
		}
	} else if desired.SqsManagedSseEnabled != observed.SqsManagedSseEnabled {
		attrs["SqsManagedSseEnabled"] = strconv.FormatBool(desired.SqsManagedSseEnabled)
	}

	if desired.FifoQueue {
		if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
			attrs["ContentBasedDeduplication"] = strconv.FormatBool(desired.ContentBasedDeduplication)
		}
		if desired.DeduplicationScope != observed.DeduplicationScope {
			attrs["DeduplicationScope"] = desired.DeduplicationScope
		}
		if desired.FifoThroughputLimit != observed.FifoThroughputLimit {
			attrs["FifoThroughputLimit"] = desired.FifoThroughputLimit
		}
	}
	return attrs, nil
}

func validateImmutableIdentity(desired SQSQueueSpec, observed ObservedState) error {
	if observed.QueueName != "" && desired.QueueName != observed.QueueName {
		return fmt.Errorf("queueName is immutable: current=%q desired=%q", observed.QueueName, desired.QueueName)
	}
	if desired.FifoQueue != observed.FifoQueue {
		return fmt.Errorf("fifoQueue is immutable for queue %q: current=%t desired=%t", desired.QueueName, observed.FifoQueue, desired.FifoQueue)
	}
	observedRegion := regionFromQueueARN(observed.QueueArn)
	if observedRegion != "" && desired.Region != observedRegion {
		return fmt.Errorf("region is immutable for queue %q: current=%q desired=%q", desired.QueueName, observedRegion, desired.Region)
	}
	return nil
}

func outputsFromObserved(observed ObservedState, seed SQSQueueOutputs) SQSQueueOutputs {
	queueURL := observed.QueueUrl
	if queueURL == "" {
		queueURL = seed.QueueUrl
	}
	queueName := observed.QueueName
	if queueName == "" {
		queueName = seed.QueueName
	}
	return SQSQueueOutputs{QueueUrl: queueURL, QueueArn: observed.QueueArn, QueueName: queueName}
}

func specFromObserved(obs ObservedState, ref types.ImportRef) SQSQueueSpec {
	return SQSQueueSpec{
		Account: ref.Account, Region: regionFromQueueARN(obs.QueueArn), QueueName: obs.QueueName,
		FifoQueue: obs.FifoQueue, VisibilityTimeout: obs.VisibilityTimeout,
		MessageRetentionPeriod: obs.MessageRetentionPeriod, MaximumMessageSize: obs.MaximumMessageSize,
		DelaySeconds: obs.DelaySeconds, ReceiveMessageWaitTimeSeconds: obs.ReceiveMessageWaitTimeSeconds,
		RedrivePolicy: cloneRedrivePolicy(obs.RedrivePolicy), SqsManagedSseEnabled: obs.SqsManagedSseEnabled,
		KmsMasterKeyId: obs.KmsMasterKeyId, KmsDataKeyReusePeriodSeconds: obs.KmsDataKeyReusePeriodSeconds,
		ContentBasedDeduplication: obs.ContentBasedDeduplication,
		DeduplicationScope:        obs.DeduplicationScope, FifoThroughputLimit: obs.FifoThroughputLimit,
		Tags: drivers.FilterPraxisTags(obs.Tags),
	}
}

func lateInitializeFIFODefaults(desired SQSQueueSpec, observed ObservedState) (SQSQueueSpec, bool) {
	if !desired.FifoQueue {
		return desired, false
	}
	changed := false
	if desired.DeduplicationScope == "" && observed.DeduplicationScope != "" {
		desired.DeduplicationScope = observed.DeduplicationScope
		changed = true
	}
	if desired.FifoThroughputLimit == "" && observed.FifoThroughputLimit != "" {
		desired.FifoThroughputLimit = observed.FifoThroughputLimit
		changed = true
	}
	return desired, changed
}

func applyDefaults(spec SQSQueueSpec) SQSQueueSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.QueueName = strings.TrimSpace(spec.QueueName)
	if spec.MessageRetentionPeriod == 0 {
		spec.MessageRetentionPeriod = 345600
	}
	if spec.MaximumMessageSize == 0 {
		spec.MaximumMessageSize = 262144
	}
	if spec.VisibilityTimeout == 0 {
		spec.VisibilityTimeout = 30
	}
	if spec.KmsMasterKeyId != "" && spec.KmsDataKeyReusePeriodSeconds == 0 {
		spec.KmsDataKeyReusePeriodSeconds = 300
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec SQSQueueSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.QueueName == "" {
		return fmt.Errorf("queueName is required")
	}
	if spec.FifoQueue && !strings.HasSuffix(spec.QueueName, ".fifo") {
		return fmt.Errorf("FIFO queues must have a name ending with .fifo")
	}
	if !spec.FifoQueue && strings.HasSuffix(spec.QueueName, ".fifo") {
		return fmt.Errorf("queueName ending with .fifo requires fifoQueue=true")
	}
	if spec.KmsMasterKeyId != "" && spec.SqsManagedSseEnabled {
		return fmt.Errorf("kmsMasterKeyId and sqsManagedSseEnabled are mutually exclusive")
	}
	return nil
}

func queueNameFromARN(value string) (string, error) {
	parsed, err := awsarn.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Service != "sqs" || parsed.Resource == "" {
		return "", fmt.Errorf("invalid SQS queue ARN %q", value)
	}
	return parsed.Resource, nil
}

func regionFromQueueARN(value string) string {
	parsed, err := awsarn.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Service != "sqs" {
		return ""
	}
	return parsed.Region
}

func cloneRedrivePolicy(policy *RedrivePolicy) *RedrivePolicy {
	if policy == nil {
		return nil
	}
	copy := *policy
	return &copy
}
