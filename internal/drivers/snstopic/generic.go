package snstopic

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	apiFactory func(aws.Config) TopicAPI
}

// NewGenericSNSTopicDriver returns the SNS topic driver backed by the shared
// generic lifecycle kernel.
func NewGenericSNSTopicDriver(auth authservice.AuthClient) *kernel.Driver[SNSTopicSpec, SNSTopicOutputs, ObservedState] {
	return newGenericSNSTopicDriverWithFactory(auth, nil)
}

func newGenericSNSTopicDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) TopicAPI) *kernel.Driver[SNSTopicSpec, SNSTopicOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) TopicAPI {
			return NewTopicAPI(awsclient.NewSNSClient(cfg))
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[SNSTopicSpec, SNSTopicOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec SNSTopicSpec) (SNSTopicSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return SNSTopicSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = prepareSpec(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) SNSTopicSpec {
			spec := specFromObserved(observed, ref)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: outputsFromObserved,
		HasDrift:            HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired SNSTopicSpec, outputs SNSTopicOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}

	topicARN := strings.TrimSpace(outputs.TopicArn)
	if topicARN == "" && desired.TopicName != "" {
		topicARN, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByName(rc, desired.TopicName)
		}, classifyTopicObserve)
		if err != nil || topicARN == "" {
			return kernel.Observation[ObservedState]{}, err
		}
	}
	if topicARN == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return observeTopicByARN(ctx, api, topicARN)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired SNSTopicSpec) (kernel.CreateResult[SNSTopicOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[SNSTopicOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	topicARN, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		return api.CreateTopic(rc, desired)
	}, classifyTopicMutation)
	return kernel.CreateResult[SNSTopicOutputs]{
		SeedOutputs: SNSTopicOutputs{TopicArn: topicARN, TopicName: desired.TopicName},
	}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired SNSTopicSpec, observed ObservedState) error {
	if observed.TopicName != "" && desired.TopicName != observed.TopicName {
		return restate.TerminalError(fmt.Errorf(
			"topicName is immutable for %s: current=%s desired=%s",
			observed.TopicArn, observed.TopicName, desired.TopicName,
		), 409)
	}
	if desired.FifoTopic != observed.FifoTopic {
		return restate.TerminalError(fmt.Errorf(
			"fifoTopic is immutable for %s: current=%t desired=%t",
			observed.TopicArn, observed.FifoTopic, desired.FifoTopic,
		), 409)
	}

	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	type attributeUpdate struct {
		name  string
		value string
	}
	updates := make([]attributeUpdate, 0, 5)
	if desired.DisplayName != observed.DisplayName {
		updates = append(updates, attributeUpdate{name: "DisplayName", value: desired.DisplayName})
	}
	if !topicPoliciesEqual(desired.Policy, observed) {
		policy := desired.Policy
		if policy == "" {
			policy, err = defaultTopicPolicy(observed)
			if err != nil {
				return restate.TerminalError(err, 409)
			}
		}
		updates = append(updates, attributeUpdate{name: "Policy", value: policy})
	}
	if !optionalTopicPoliciesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		policy := desired.DeliveryPolicy
		if policy == "" {
			policy = "{}"
		}
		updates = append(updates, attributeUpdate{name: "DeliveryPolicy", value: policy})
	}
	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		updates = append(updates, attributeUpdate{name: "KmsMasterKeyId", value: desired.KmsMasterKeyId})
	}
	if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
		value := "false"
		if desired.ContentBasedDeduplication {
			value = "true"
		}
		updates = append(updates, attributeUpdate{name: "ContentBasedDeduplication", value: value})
	}

	for _, update := range updates {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetTopicAttribute(rc, observed.TopicArn, update.name, update.value)
		}, classifyTopicMutation); err != nil {
			return err
		}
	}

	if !drivers.TagsMatch(desired.Tags, observed.Tags) || observed.Tags["praxis:managed-key"] != desired.ManagedKey {
		tags := mergeTags(drivers.FilterPraxisTags(desired.Tags), map[string]string{"praxis:managed-key": desired.ManagedKey})
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.TopicArn, tags)
		}, classifyTopicMutation); err != nil {
			return err
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired SNSTopicSpec, outputs SNSTopicOutputs) error {
	topicARN := strings.TrimSpace(outputs.TopicArn)
	if topicARN == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	hasSubscriptions, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (bool, error) {
		hasAny, runErr := api.HasSubscriptions(rc, topicARN)
		if IsNotFound(runErr) {
			return false, nil
		}
		return hasAny, runErr
	}, classifyTopicObserve)
	if err != nil {
		return err
	}
	if hasSubscriptions {
		return restate.TerminalError(fmt.Errorf(
			"cannot delete SNS topic %s while subscriptions exist; delete the separate SNSSubscription resources first",
			topicARN,
		), 409)
	}

	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteTopic(rc, topicARN)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyTopicMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	resourceID := strings.TrimSpace(ref.ResourceID)
	if !strings.HasPrefix(resourceID, "arn:") {
		resourceID, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByName(rc, resourceID)
		}, classifyTopicObserve)
		if err != nil || resourceID == "" {
			return kernel.Observation[ObservedState]{}, err
		}
	}
	return observeTopicByARN(ctx, api, resourceID)
}

func observeTopicByARN(ctx restate.ObjectContext, api TopicAPI, topicARN string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.GetTopicAttributes(rc, topicARN)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyTopicObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (TopicAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("SNSTopic driver is not configured with an auth client")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SNS account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyTopicObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if isAuthError(err) || awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyTopicMutation(err error) error {
	if err == nil {
		return nil
	}
	if isAuthError(err) || awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	return err
}

func prepareSpec(spec SNSTopicSpec) SNSTopicSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.TopicName = strings.TrimSpace(spec.TopicName)
	spec.DisplayName = strings.TrimSpace(spec.DisplayName)
	spec.KmsMasterKeyId = strings.TrimSpace(spec.KmsMasterKeyId)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec SNSTopicSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.TopicName == "" {
		return fmt.Errorf("topicName is required")
	}
	if spec.FifoTopic && !strings.HasSuffix(spec.TopicName, ".fifo") {
		return fmt.Errorf("FIFO topics must have a name ending with .fifo")
	}
	if spec.ContentBasedDeduplication && !spec.FifoTopic {
		return fmt.Errorf("contentBasedDeduplication requires fifoTopic")
	}
	return nil
}

func outputsFromObserved(observed ObservedState, seed SNSTopicOutputs) SNSTopicOutputs {
	if observed.TopicArn != "" {
		seed.TopicArn = observed.TopicArn
	}
	if observed.TopicName != "" {
		seed.TopicName = observed.TopicName
	}
	seed.Owner = observed.Owner
	return seed
}

func specFromObserved(observed ObservedState, ref types.ImportRef) SNSTopicSpec {
	region := ""
	if parts := strings.SplitN(observed.TopicArn, ":", 7); len(parts) >= 5 {
		region = parts[3]
	}
	return SNSTopicSpec{
		Account: ref.Account, Region: region, TopicName: observed.TopicName,
		DisplayName: observed.DisplayName, FifoTopic: observed.FifoTopic,
		ContentBasedDeduplication: observed.ContentBasedDeduplication,
		Policy:                    observed.Policy,
		DeliveryPolicy:            observed.DeliveryPolicy,
		KmsMasterKeyId:            observed.KmsMasterKeyId,
		Tags:                      drivers.FilterPraxisTags(observed.Tags),
	}
}
