package s3

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

// ServiceName is the Restate Virtual Object name for S3 buckets.
const ServiceName = "S3Bucket"

type kernelOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) S3API
}

// NewGenericS3BucketDriver binds S3 lifecycle behavior to the generic kernel.
func NewGenericS3BucketDriver(auth authservice.AuthClient) *kernel.Driver[S3BucketSpec, S3BucketOutputs, ObservedState] {
	return NewGenericS3BucketDriverWithFactory(auth, nil)
}

func NewGenericS3BucketDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) S3API) *kernel.Driver[S3BucketSpec, S3BucketOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) S3API { return NewS3API(awsclient.NewS3Client(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[S3BucketSpec, S3BucketOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			ManagedDriftCorrection: true, LateInitialization: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec S3BucketSpec) (S3BucketSpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return S3BucketSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.BucketName = strings.TrimSpace(spec.BucketName)
			spec.Region = strings.TrimSpace(spec.Region)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) S3BucketSpec {
			spec := specFromObserved(ref.ResourceID, observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: outputsFromObserved,
		HasDrift:            HasDrift,
		LateInitialize:      LateInitS3Bucket,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired S3BucketSpec, outputs S3BucketOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.BucketName
	if name == "" {
		name = desired.BucketName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribeBucket(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired S3BucketSpec) (kernel.CreateResult[S3BucketOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[S3BucketOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.CreateBucket(rc, desired.BucketName, desired.Region)
	}, classifyMutation)
	return kernel.CreateResult[S3BucketOutputs]{
		SeedOutputs: S3BucketOutputs{BucketName: desired.BucketName, Region: desired.Region},
	}, err
}

func (o *kernelOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next S3BucketSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.BucketName != next.BucketName:
		return restate.TerminalError(fmt.Errorf("bucketName is immutable; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired S3BucketSpec, _ ObservedState) error {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.ConfigureBucket(rc, desired)
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired S3BucketSpec, outputs S3BucketOutputs) error {
	name := outputs.BucketName
	if name == "" {
		name = desired.BucketName
	}
	if name == "" {
		return nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteBucket(rc, name)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribeBucket(rc, strings.TrimSpace(ref.ResourceID))
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (S3API, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("S3Bucket driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve S3 account %q: %w", account, err)
	}
	return o.apiFactory(cfg), nil
}

func validateSpec(spec S3BucketSpec) error {
	if spec.BucketName == "" {
		return fmt.Errorf("bucketName is required")
	}
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	return nil
}

func outputsFromObserved(observed ObservedState, seed S3BucketOutputs) S3BucketOutputs {
	name := observed.BucketName
	if name == "" {
		name = seed.BucketName
	}
	region := observed.Region
	if region == "" {
		region = seed.Region
	}
	return S3BucketOutputs{
		ARN:        fmt.Sprintf("arn:aws:s3:::%s", name),
		BucketName: name,
		Region:     region,
		DomainName: fmt.Sprintf("%s.s3.%s.amazonaws.com", name, region),
	}
}

func specFromObserved(name string, observed ObservedState) S3BucketSpec {
	spec := S3BucketSpec{
		BucketName: name,
		Region:     observed.Region, Versioning: observed.VersioningStatus == "Enabled",
		Tags: observed.Tags,
	}
	if observed.EncryptionAlgo != "" {
		spec.Encryption = EncryptionSpec{Enabled: true, Algorithm: observed.EncryptionAlgo}
	}
	return spec
}

func classifyObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 409)
	}
	if awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsBucketNotEmpty(err) || IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsBucketLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}
