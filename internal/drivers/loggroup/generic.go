package loggroup

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
	apiFactory func(aws.Config) LogGroupAPI
}

// NewGenericLogGroupDriver is the CloudWatch Log Group lifecycle implementation.
func NewGenericLogGroupDriver(auth authservice.AuthClient) *kernel.Driver[LogGroupSpec, LogGroupOutputs, ObservedState] {
	return NewGenericLogGroupDriverWithFactory(auth, nil)
}

func NewGenericLogGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LogGroupAPI) *kernel.Driver[LogGroupSpec, LogGroupOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) LogGroupAPI {
			return NewLogGroupAPI(awsclient.NewCloudWatchLogsClient(cfg))
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[LogGroupSpec, LogGroupOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec LogGroupSpec) (LogGroupSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return LogGroupSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) LogGroupSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ LogGroupOutputs) LogGroupOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired LogGroupSpec, outputs LogGroupOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.LogGroupName
	if name == "" {
		name = desired.LogGroupName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeLogGroup(ctx, api, name)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired LogGroupSpec) (kernel.CreateResult[LogGroupOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[LogGroupOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.CreateLogGroup(rc, desired)
	}, classifyLogGroupMutation)
	return kernel.CreateResult[LogGroupOutputs]{
		SeedOutputs: LogGroupOutputs{LogGroupName: desired.LogGroupName},
	}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired LogGroupSpec, observed ObservedState) error {
	if desired.LogGroupClass != "" && observed.LogGroupClass != "" && desired.LogGroupClass != observed.LogGroupClass {
		return restate.TerminalError(fmt.Errorf(
			"logGroupClass is immutable for %s: current=%s desired=%s",
			desired.LogGroupName, observed.LogGroupClass, desired.LogGroupClass,
		), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	if !retentionMatch(desired.RetentionInDays, observed.RetentionInDays) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if desired.RetentionInDays == nil {
				return restate.Void{}, api.DeleteRetentionPolicy(rc, desired.LogGroupName)
			}
			return restate.Void{}, api.PutRetentionPolicy(rc, desired.LogGroupName, *desired.RetentionInDays)
		}, classifyLogGroupMutation); err != nil {
			return err
		}
	}

	if strings.TrimSpace(desired.KmsKeyID) != strings.TrimSpace(observed.KmsKeyID) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if strings.TrimSpace(desired.KmsKeyID) == "" {
				return restate.Void{}, api.DisassociateKmsKey(rc, desired.LogGroupName)
			}
			return restate.Void{}, api.AssociateKmsKey(rc, desired.LogGroupName, desired.KmsKeyID)
		}, classifyLogGroupMutation); err != nil {
			return err
		}
	}

	toAdd, toRemove := tagDiff(desired.Tags, observed.Tags, desired.ManagedKey)
	if len(toRemove) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, observed.ARN, toRemove)
		}, classifyLogGroupMutation); err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, observed.ARN, toAdd)
		}, classifyLogGroupMutation); err != nil {
			return err
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired LogGroupSpec, outputs LogGroupOutputs) error {
	name := outputs.LogGroupName
	if name == "" {
		name = desired.LogGroupName
	}
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteLogGroup(rc, name)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyLogGroupMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeLogGroup(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeLogGroup(ctx restate.ObjectContext, api LogGroupAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, runErr := api.DescribeLogGroup(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: found, Value: observed}, runErr
	}, classifyLogGroupObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.ObjectContext, account string) (LogGroupAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("LogGroup driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve LogGroup account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyLogGroupObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyLogGroupMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsAlreadyExists(err) || IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}

func specFromObserved(observed ObservedState) LogGroupSpec {
	return LogGroupSpec{
		LogGroupName: observed.LogGroupName, LogGroupClass: observed.LogGroupClass,
		RetentionInDays: observed.RetentionInDays, KmsKeyID: observed.KmsKeyID,
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) LogGroupOutputs {
	retention := int32(0)
	if observed.RetentionInDays != nil {
		retention = *observed.RetentionInDays
	}
	return LogGroupOutputs{
		ARN: observed.ARN, LogGroupName: observed.LogGroupName, LogGroupClass: observed.LogGroupClass,
		RetentionInDays: retention, KmsKeyID: observed.KmsKeyID,
		CreationTime: observed.CreationTime, StoredBytes: observed.StoredBytes,
	}
}

func applyDefaults(spec LogGroupSpec) LogGroupSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.LogGroupName = strings.TrimSpace(spec.LogGroupName)
	spec.LogGroupClass = strings.TrimSpace(spec.LogGroupClass)
	if spec.LogGroupClass == "" {
		spec.LogGroupClass = "STANDARD"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec LogGroupSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.LogGroupName) == "" {
		return fmt.Errorf("logGroupName is required")
	}
	if spec.LogGroupClass != "STANDARD" && spec.LogGroupClass != "INFREQUENT_ACCESS" {
		return fmt.Errorf("logGroupClass must be STANDARD or INFREQUENT_ACCESS")
	}
	return nil
}
