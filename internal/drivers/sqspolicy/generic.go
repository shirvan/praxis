package sqspolicy

import (
	"encoding/json"
	"fmt"
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
	apiFactory func(aws.Config) PolicyAPI
}

// NewGenericSQSQueuePolicyDriver binds the queue Policy attribute to the generic lifecycle kernel.
func NewGenericSQSQueuePolicyDriver(auth authservice.AuthClient) *kernel.Driver[SQSQueuePolicySpec, SQSQueuePolicyOutputs, ObservedState] {
	return newGenericSQSQueuePolicyDriverWithFactory(auth, nil)
}

func newGenericSQSQueuePolicyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) PolicyAPI) *kernel.Driver[SQSQueuePolicySpec, SQSQueuePolicyOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) PolicyAPI { return NewPolicyAPI(awsclient.NewSQSClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[SQSQueuePolicySpec, SQSQueuePolicyOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec SQSQueuePolicySpec) (SQSQueuePolicySpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return SQSQueuePolicySpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.Region = strings.TrimSpace(spec.Region)
			spec.QueueName = strings.TrimSpace(spec.QueueName)
			spec.Policy = strings.TrimSpace(spec.Policy)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return SQSQueuePolicySpec{}, restate.TerminalError(fmt.Errorf(
					"region %q does not match account region %q", spec.Region, region,
				), 400)
			}
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) SQSQueuePolicySpec {
			return SQSQueuePolicySpec{
				Account: ref.Account, Region: regionFromQueueARN(observed.QueueArn),
				QueueName: queueNameFromURL(observed.QueueUrl), Policy: observed.Policy,
			}
		},
		OutputsFromObserved: outputsFromObserved,
		FieldDiffs:          ComputeFieldDiffs,
		HasDrift:            HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired SQSQueuePolicySpec, outputs SQSQueuePolicyOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	queueURL := strings.TrimSpace(outputs.QueueUrl)
	if queueURL == "" {
		queueURL, _, err = resolveQueueURL(ctx, api, desired.QueueName)
		if err != nil || queueURL == "" {
			return kernel.Observation[ObservedState]{}, err
		}
	}
	return observePolicy(ctx, api, queueURL)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired SQSQueuePolicySpec) (kernel.CreateResult[SQSQueuePolicyOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[SQSQueuePolicyOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	queueURL, found, err := resolveQueueURL(ctx, api, desired.QueueName)
	if err != nil {
		return kernel.CreateResult[SQSQueuePolicyOutputs]{}, err
	}
	if !found {
		return kernel.CreateResult[SQSQueuePolicyOutputs]{}, restate.TerminalError(fmt.Errorf(
			"queue %q does not exist", desired.QueueName,
		), 404)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.SetQueuePolicy(rc, queueURL, desired.Policy)
	}, classifyPolicyMutation)
	return kernel.CreateResult[SQSQueuePolicyOutputs]{SeedOutputs: SQSQueuePolicyOutputs{
		QueueUrl: queueURL, QueueName: desired.QueueName,
	}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired SQSQueuePolicySpec, observed ObservedState) error {
	if err := validateImmutableIdentity(desired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	if policiesEqual(desired.Policy, observed.Policy) {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.SetQueuePolicy(rc, observed.QueueUrl, desired.Policy)
	}, classifyPolicyMutation)
	return err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired SQSQueuePolicySpec, outputs SQSQueuePolicyOutputs) error {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	queueURL := strings.TrimSpace(outputs.QueueUrl)
	if queueURL == "" && desired.QueueName != "" {
		var found bool
		queueURL, found, err = resolveQueueURL(ctx, api, desired.QueueName)
		if err != nil || !found {
			return err
		}
	}
	if queueURL == "" {
		return nil
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.RemoveQueuePolicy(rc, queueURL)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyPolicyMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	resourceID := strings.TrimSpace(ref.ResourceID)
	queueURL := ""
	if strings.HasPrefix(resourceID, "http://") || strings.HasPrefix(resourceID, "https://") {
		queueURL = resourceID
	} else {
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
		var found bool
		queueURL, found, err = resolveQueueURL(ctx, api, resourceID)
		if err != nil || !found {
			return kernel.Observation[ObservedState]{}, err
		}
	}
	return observePolicy(ctx, api, queueURL)
}

func resolveQueueURL(ctx restate.ObjectContext, api PolicyAPI, queueName string) (string, bool, error) {
	queueURL, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		url, runErr := api.GetQueueUrl(rc, queueName)
		if IsNotFound(runErr) {
			return "", nil
		}
		return url, runErr
	}, classifyPolicyObserve)
	return queueURL, queueURL != "", err
}

func observePolicy(ctx restate.ObjectContext, api PolicyAPI, queueURL string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.GetQueuePolicy(rc, queueURL)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{
			Exists: runErr == nil && strings.TrimSpace(observed.Policy) != "", Value: observed,
		}, runErr
	}, classifyPolicyObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.ObjectContext, account string) (PolicyAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("SQSQueuePolicy driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SQS account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyPolicyObserve(err error) error {
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

func classifyPolicyMutation(err error) error {
	if err == nil {
		return nil
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidInput(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func validateSpec(spec SQSQueuePolicySpec) error {
	if spec.Region == "" || spec.QueueName == "" || spec.Policy == "" {
		return fmt.Errorf("region, queueName, and policy are required")
	}
	if !json.Valid([]byte(spec.Policy)) {
		return fmt.Errorf("policy must be valid JSON")
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(spec.Policy), &document); err != nil || document == nil {
		return fmt.Errorf("policy must be a JSON object")
	}
	return nil
}

func validateImmutableIdentity(desired SQSQueuePolicySpec, observed ObservedState) error {
	if observedName := queueNameFromURL(observed.QueueUrl); observedName != "" && observedName != desired.QueueName {
		return fmt.Errorf("queueName is immutable: current=%q desired=%q", observedName, desired.QueueName)
	}
	if observedRegion := regionFromQueueARN(observed.QueueArn); observedRegion != "" && observedRegion != desired.Region {
		return fmt.Errorf("region is immutable: current=%q desired=%q", observedRegion, desired.Region)
	}
	return nil
}

func outputsFromObserved(observed ObservedState, seed SQSQueuePolicyOutputs) SQSQueuePolicyOutputs {
	queueURL := observed.QueueUrl
	if queueURL == "" {
		queueURL = seed.QueueUrl
	}
	queueName := queueNameFromURL(queueURL)
	if queueName == "" {
		queueName = seed.QueueName
	}
	return SQSQueuePolicyOutputs{QueueUrl: queueURL, QueueArn: observed.QueueArn, QueueName: queueName}
}

func queueNameFromURL(queueURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(queueURL), "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

func regionFromQueueARN(value string) string {
	parsed, err := awsarn.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Service != "sqs" {
		return ""
	}
	return parsed.Region
}
