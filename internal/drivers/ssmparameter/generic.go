package ssmparameter

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

type kernelOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SSMParameterAPI
}

// NewGenericSSMParameterDriver binds Parameter Store semantics to the shared
// lifecycle kernel. AWS parameter versions remain provider resource data. An
// ambiguous overwrite is retried by Restate and may therefore leave gaps in
// that provider-owned sequence; Praxis does not add compensation or replay
// bookkeeping for those gaps during alpha.
func NewGenericSSMParameterDriver(auth authservice.AuthClient) *kernel.Driver[SSMParameterSpec, SSMParameterOutputs, ObservedState] {
	return newGenericSSMParameterDriverWithFactory(auth, nil)
}

func newGenericSSMParameterDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SSMParameterAPI) *kernel.Driver[SSMParameterSpec, SSMParameterOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) SSMParameterAPI {
			return NewSSMParameterAPI(awsclient.NewSSMClient(cfg))
		}
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[SSMParameterSpec, SSMParameterOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec SSMParameterSpec) (SSMParameterSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return SSMParameterSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) SSMParameterSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed SSMParameterOutputs) SSMParameterOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.ARN == "" {
				outputs.ARN = seed.ARN
			}
			if outputs.ParameterName == "" {
				outputs.ParameterName = seed.ParameterName
			}
			if outputs.Version == 0 {
				outputs.Version = seed.Version
			}
			return outputs
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired SSMParameterSpec, outputs SSMParameterOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.ParameterName
	if name == "" {
		name = desired.ParameterName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeParameter(ctx, api, name)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired SSMParameterSpec) (kernel.CreateResult[SSMParameterOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[SSMParameterOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	version, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (int64, error) {
		return api.PutParameter(rc, desired, false)
	}, classifyMutation)
	return kernel.CreateResult[SSMParameterOutputs]{
		SeedOutputs: SSMParameterOutputs{ParameterName: desired.ParameterName, Version: version},
	}, err
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired SSMParameterSpec, observed ObservedState) error {
	if desired.ParameterName != observed.ParameterName {
		return restate.TerminalError(fmt.Errorf("parameterName is immutable; delete and recreate the parameter to change its name"), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if parameterFieldsDrift(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (int64, error) {
			return api.PutParameter(rc, desired, true)
		}, classifyMutation); err != nil {
			return fmt.Errorf("overwrite SSM parameter: %w", err)
		}
	}
	toAdd, toRemove := tagDiff(desired.Tags, observed.Tags, desired.ManagedKey)
	if len(toRemove) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.RemoveTags(rc, desired.ParameterName, toRemove)
		}, classifyMutation); err != nil {
			return fmt.Errorf("remove SSM parameter tags: %w", err)
		}
	}
	if len(toAdd) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AddTags(rc, desired.ParameterName, toAdd)
		}, classifyMutation); err != nil {
			return fmt.Errorf("add SSM parameter tags: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired SSMParameterSpec, outputs SSMParameterOutputs) error {
	name := outputs.ParameterName
	if name == "" {
		name = desired.ParameterName
	}
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteParameter(rc, name)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeParameter(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func (o *kernelOperations) observeParameter(ctx restate.ObjectContext, api SSMParameterAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, runErr := api.DescribeParameter(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil && found, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (SSMParameterAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("SSMParameter driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SSMParameter account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyObserve(err error) error {
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

func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsAlreadyExists(err) {
		return restate.TerminalError(err, 409)
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	return err
}

func specFromObserved(observed ObservedState) SSMParameterSpec {
	kmsKeyID := observed.KmsKeyID
	if observed.Type == "SecureString" && kmsKeyID == "alias/aws/ssm" {
		kmsKeyID = ""
	}
	return SSMParameterSpec{
		ParameterName: observed.ParameterName, Type: observed.Type, Value: observed.Value,
		Description: observed.Description, Tier: normalizeTier(observed.Tier), KmsKeyID: kmsKeyID,
		AllowedPattern: observed.AllowedPattern, DataType: normalizeDataType(observed.DataType),
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) SSMParameterOutputs {
	return SSMParameterOutputs{
		ARN: observed.ARN, ParameterName: observed.ParameterName, Type: observed.Type,
		Version: observed.Version, Tier: normalizeTier(observed.Tier), DataType: normalizeDataType(observed.DataType),
	}
}

func applyDefaults(spec SSMParameterSpec) SSMParameterSpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.Region = strings.TrimSpace(spec.Region)
	spec.ParameterName = strings.TrimSpace(spec.ParameterName)
	spec.Type = strings.TrimSpace(spec.Type)
	if spec.Type == "" {
		spec.Type = "String"
	}
	spec.Tier = normalizeTier(spec.Tier)
	spec.DataType = normalizeDataType(spec.DataType)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec SSMParameterSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.ParameterName == "" {
		return fmt.Errorf("parameterName is required")
	}
	if spec.Value == "" {
		return fmt.Errorf("value is required")
	}
	switch spec.Type {
	case "String", "StringList", "SecureString":
	default:
		return fmt.Errorf("type must be String, StringList, or SecureString")
	}
	switch spec.Tier {
	case "Standard", "Advanced", "Intelligent-Tiering":
	default:
		return fmt.Errorf("tier must be Standard, Advanced, or Intelligent-Tiering")
	}
	if spec.KmsKeyID != "" && spec.Type != "SecureString" {
		return fmt.Errorf("kmsKeyId is only valid for SecureString parameters")
	}
	switch spec.DataType {
	case "text", "aws:ec2:image", "aws:ssm:integration":
	default:
		return fmt.Errorf("dataType must be text, aws:ec2:image, or aws:ssm:integration")
	}
	return nil
}
