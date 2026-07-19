package snssub

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
	apiFactory func(aws.Config) SubscriptionAPI
}

// NewGenericSNSSubscriptionDriver returns the SNS subscription lifecycle
// implementation backed by the shared generic kernel.
func NewGenericSNSSubscriptionDriver(auth authservice.AuthClient) *kernel.Driver[SNSSubscriptionSpec, SNSSubscriptionOutputs, ObservedState] {
	return newGenericSNSSubscriptionDriverWithFactory(auth, nil)
}

func newGenericSNSSubscriptionDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SubscriptionAPI) *kernel.Driver[SNSSubscriptionSpec, SNSSubscriptionOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) SubscriptionAPI {
			return NewSubscriptionAPI(awsclient.NewSNSClient(cfg))
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[SNSSubscriptionSpec, SNSSubscriptionOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			Readiness: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec SNSSubscriptionSpec) (SNSSubscriptionSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return SNSSubscriptionSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.Region = region
			return spec, nil
		},
		Validate: validateProtocolConstraints,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) SNSSubscriptionSpec {
			return specFromObserved(observed, ref)
		},
		OutputsFromObserved: outputsFromObserved,
		FieldDiffs:          ComputeFieldDiffs,
		HasDrift:            HasDrift,
		CheckReadiness: func(observed ObservedState) kernel.ReadinessResult {
			if observed.PendingConfirmation {
				return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "waiting for subscription confirmation"}
			}
			return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
		},
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired SNSSubscriptionSpec, outputs SNSSubscriptionOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	arn := strings.TrimSpace(outputs.SubscriptionArn)
	if arn != "" && !isPendingARN(arn) {
		return getSubscription(ctx, api, arn)
	}
	if desired.TopicArn == "" || desired.Protocol == "" || desired.Endpoint == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	foundARN, err := findSubscription(ctx, api, desired)
	if err != nil {
		return kernel.Observation[ObservedState]{}, err
	}
	if foundARN != "" && !isPendingARN(foundARN) {
		return getSubscription(ctx, api, foundARN)
	}
	if isPendingARN(arn) || isPendingARN(foundARN) {
		return kernel.Observation[ObservedState]{Exists: true, Value: pendingObservation(desired, arn)}, nil
	}
	return kernel.Observation[ObservedState]{}, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired SNSSubscriptionSpec) (kernel.CreateResult[SNSSubscriptionOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[SNSSubscriptionOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	arn, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		return api.Subscribe(rc, desired)
	}, classifySubscriptionMutation)
	return kernel.CreateResult[SNSSubscriptionOutputs]{SeedOutputs: SNSSubscriptionOutputs{
		SubscriptionArn: arn, TopicArn: desired.TopicArn, Protocol: desired.Protocol, Endpoint: desired.Endpoint,
	}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired SNSSubscriptionSpec, observed ObservedState, currentOutputs SNSSubscriptionOutputs) (SNSSubscriptionOutputs, error) {
	if err := validateImmutableIdentity(desired, observed); err != nil {
		return currentOutputs, restate.TerminalError(err, 409)
	}
	if observed.PendingConfirmation {
		return currentOutputs, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	return currentOutputs, convergeAttributes(ctx, api, observed.SubscriptionArn, desired, specFromObserved(observed, types.ImportRef{}))
}

func validateImmutableIdentity(desired SNSSubscriptionSpec, observed ObservedState) error {
	switch {
	case desired.TopicArn != observed.TopicArn:
		return fmt.Errorf("topicArn is immutable; delete and reprovision the SNS subscription to change its topic")
	case desired.Protocol != observed.Protocol:
		return fmt.Errorf("protocol is immutable; delete and reprovision the SNS subscription to change its protocol")
	case desired.Endpoint != observed.Endpoint:
		return fmt.Errorf("endpoint is immutable; delete and reprovision the SNS subscription to change its endpoint")
	default:
		return nil
	}
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired SNSSubscriptionSpec, outputs SNSSubscriptionOutputs) error {
	arn := strings.TrimSpace(outputs.SubscriptionArn)
	if arn == "" {
		return nil
	}
	if isPendingARN(arn) {
		return restate.TerminalError(fmt.Errorf(
			"cannot delete pending SNS subscription for %s: AWS has not assigned a subscription ARN; confirm it before deletion",
			desired.Endpoint,
		), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.Unsubscribe(rc, arn)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifySubscriptionMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	if !strings.HasPrefix(ref.ResourceID, "arn:aws:sns:") {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(
			fmt.Errorf("SNSSubscription import requires a subscription ARN as resourceID, got: %s", ref.ResourceID), 400,
		)
	}
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return getSubscription(ctx, api, ref.ResourceID)
}

func getSubscription(ctx restate.ObjectContext, api SubscriptionAPI, arn string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.GetSubscriptionAttributes(rc, arn)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifySubscriptionObserve)
}

func findSubscription(ctx restate.ObjectContext, api SubscriptionAPI, desired SNSSubscriptionSpec) (string, error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		return api.FindByTopicProtocolEndpoint(rc, desired.TopicArn, desired.Protocol, desired.Endpoint)
	}, classifySubscriptionObserve)
}

func convergeAttributes(ctx restate.ObjectContext, api SubscriptionAPI, subscriptionARN string, desired, previous SNSSubscriptionSpec) error {
	type attributeUpdate struct{ name, value string }
	updates := make([]attributeUpdate, 0, 6)
	if !filterPoliciesEqual(desired.FilterPolicy, previous.FilterPolicy) {
		value := desired.FilterPolicy
		if value == "" {
			value = "{}"
		}
		updates = append(updates, attributeUpdate{"FilterPolicy", value})
	}
	if !filterPolicyScopesEqual(desired.FilterPolicyScope, previous.FilterPolicyScope) {
		scope := desired.FilterPolicyScope
		if scope == "" {
			scope = "MessageAttributes"
		}
		updates = append(updates, attributeUpdate{"FilterPolicyScope", scope})
	}
	if desired.RawMessageDelivery != previous.RawMessageDelivery {
		value := "false"
		if desired.RawMessageDelivery {
			value = "true"
		}
		updates = append(updates, attributeUpdate{"RawMessageDelivery", value})
	}
	if !optionalPoliciesEqual(desired.DeliveryPolicy, previous.DeliveryPolicy) {
		value := desired.DeliveryPolicy
		if value == "" {
			value = "{}"
		}
		updates = append(updates, attributeUpdate{"DeliveryPolicy", value})
	}
	if !optionalPoliciesEqual(desired.RedrivePolicy, previous.RedrivePolicy) {
		value := desired.RedrivePolicy
		if value == "" {
			value = "{}"
		}
		updates = append(updates, attributeUpdate{"RedrivePolicy", value})
	}
	if desired.SubscriptionRoleArn != previous.SubscriptionRoleArn {
		updates = append(updates, attributeUpdate{"SubscriptionRoleArn", desired.SubscriptionRoleArn})
	}
	for _, update := range updates {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetSubscriptionAttribute(rc, subscriptionARN, update.name, update.value)
		}, classifySubscriptionMutation); err != nil {
			return err
		}
	}
	return nil
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (SubscriptionAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("SNSSubscription driver is not configured with an auth client")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SNS account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifySubscriptionObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifySubscriptionMutation(err error) error {
	if err == nil {
		return nil
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if isSubscriptionLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}

func isPendingARN(arn string) bool {
	return strings.EqualFold(strings.TrimSpace(arn), "PendingConfirmation") ||
		strings.EqualFold(strings.TrimSpace(arn), "pending confirmation")
}

func pendingObservation(desired SNSSubscriptionSpec, arn string) ObservedState {
	if arn == "" {
		arn = "PendingConfirmation"
	}
	return ObservedState{
		SubscriptionArn: arn, TopicArn: desired.TopicArn, Protocol: desired.Protocol, Endpoint: desired.Endpoint,
		PendingConfirmation: true, ConfirmationStatus: "pending",
	}
}

func outputsFromObserved(observed ObservedState, seed SNSSubscriptionOutputs) SNSSubscriptionOutputs {
	if observed.SubscriptionArn != "" {
		seed.SubscriptionArn = observed.SubscriptionArn
	}
	if observed.TopicArn != "" {
		seed.TopicArn = observed.TopicArn
	}
	if observed.Protocol != "" {
		seed.Protocol = observed.Protocol
	}
	if observed.Endpoint != "" {
		seed.Endpoint = observed.Endpoint
	}
	seed.Owner = observed.Owner
	return seed
}

func validateProtocolConstraints(spec SNSSubscriptionSpec) error {
	switch spec.Protocol {
	case "lambda":
		if !strings.HasPrefix(spec.Endpoint, "arn:aws:lambda:") {
			return fmt.Errorf("lambda protocol requires a Lambda function ARN as endpoint")
		}
	case "sqs":
		if !strings.HasPrefix(spec.Endpoint, "arn:aws:sqs:") {
			return fmt.Errorf("sqs protocol requires an SQS queue ARN as endpoint")
		}
	case "firehose":
		if !strings.HasPrefix(spec.Endpoint, "arn:aws:firehose:") {
			return fmt.Errorf("firehose protocol requires a Firehose delivery stream ARN as endpoint")
		}
		if spec.SubscriptionRoleArn == "" {
			return fmt.Errorf("firehose protocol requires a subscriptionRoleArn")
		}
	case "email", "email-json":
		if !strings.Contains(spec.Endpoint, "@") {
			return fmt.Errorf("%s protocol requires an email address as endpoint", spec.Protocol)
		}
	case "http":
		if !strings.HasPrefix(spec.Endpoint, "http://") {
			return fmt.Errorf("http protocol requires an HTTP URL as endpoint")
		}
	case "https":
		if !strings.HasPrefix(spec.Endpoint, "https://") {
			return fmt.Errorf("https protocol requires an HTTPS URL as endpoint")
		}
	case "sms":
		if !strings.HasPrefix(spec.Endpoint, "+") {
			return fmt.Errorf("sms protocol requires a phone number in E.164 format")
		}
	}
	if spec.RawMessageDelivery {
		switch spec.Protocol {
		case "sqs", "http", "https", "firehose":
		default:
			return fmt.Errorf("rawMessageDelivery is only supported for sqs, http, https, and firehose protocols")
		}
	}
	if spec.DeliveryPolicy != "" {
		switch spec.Protocol {
		case "http", "https":
		default:
			return fmt.Errorf("deliveryPolicy is only supported for http and https protocols")
		}
	}
	return nil
}

func specFromObserved(observed ObservedState, ref types.ImportRef) SNSSubscriptionSpec {
	region := ""
	if parts := strings.SplitN(observed.SubscriptionArn, ":", 8); len(parts) >= 5 {
		region = parts[3]
	}
	return SNSSubscriptionSpec{
		Account: ref.Account, Region: region, TopicArn: observed.TopicArn,
		Protocol: observed.Protocol, Endpoint: observed.Endpoint,
		FilterPolicy: observed.FilterPolicy, FilterPolicyScope: observed.FilterPolicyScope,
		RawMessageDelivery: observed.RawMessageDelivery, DeliveryPolicy: observed.DeliveryPolicy,
		RedrivePolicy: observed.RedrivePolicy, SubscriptionRoleArn: observed.SubscriptionRoleArn,
	}
}
