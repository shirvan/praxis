package ebs

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	stssdk "github.com/aws/aws-sdk-go-v2/service/sts"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

const managedKeyTag = "praxis:managed-key"

type genericOperations struct {
	auth             authservice.AuthClient
	apiFactory       func(aws.Config) EBSAPI
	accountIDFactory func(restate.ObjectContext, aws.Config) (string, error)
}

func NewGenericEBSVolumeDriver(auth authservice.AuthClient) *kernel.Driver[EBSVolumeSpec, EBSVolumeOutputs, ObservedState] {
	return newGenericEBSVolumeDriverWithFactories(auth, nil, nil)
}

func newGenericEBSVolumeDriverWithFactories(
	auth authservice.AuthClient,
	apiFactory func(aws.Config) EBSAPI,
	accountIDFactory func(restate.ObjectContext, aws.Config) (string, error),
) *kernel.Driver[EBSVolumeSpec, EBSVolumeOutputs, ObservedState] {
	if apiFactory == nil {
		apiFactory = func(cfg aws.Config) EBSAPI { return NewEBSAPI(awsclient.NewEC2Client(cfg)) }
	}
	if accountIDFactory == nil {
		accountIDFactory = func(ctx restate.ObjectContext, cfg aws.Config) (string, error) {
			return drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
				out, err := stssdk.NewFromConfig(cfg).GetCallerIdentity(rc, &stssdk.GetCallerIdentityInput{})
				return aws.ToString(out.Account), err
			}, classifyEBSAccount)
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: apiFactory, accountIDFactory: accountIDFactory}
	return kernel.MustNew(kernel.Descriptor[EBSVolumeSpec, EBSVolumeOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			ManagedDriftCorrection: true, Readiness: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec EBSVolumeSpec) (EBSVolumeSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return EBSVolumeSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return EBSVolumeSpec{}, restate.TerminalError(fmt.Errorf("region %q does not match account region %q", spec.Region, region), 400)
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) EBSVolumeSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: outputsFromGenericObserved,
		FieldDiffs:          ComputeFieldDiffs,
		HasDrift:            HasDrift,
		CheckReadiness: func(observed ObservedState) kernel.ReadinessResult {
			if observed.State == "available" || observed.State == "in-use" {
				return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
			}
			return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "waiting for volume availability"}
		},
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired EBSVolumeSpec, outputs EBSVolumeOutputs) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	id := strings.TrimSpace(outputs.VolumeId)
	recovered := false
	if id == "" && desired.ManagedKey != "" {
		id, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyEBSFind)
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		recovered = id != ""
	}
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	observation, err := observeEBS(ctx, api, id)
	if err != nil || !observation.Exists {
		return observation, err
	}
	owner := observation.Value.Tags[managedKeyTag]
	if owner != "" && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("volume %s is owned by Praxis object %q, not %q", id, owner, desired.ManagedKey), 409)
	}
	if recovered && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt volume %s without exact Praxis ownership tag %q", id, desired.ManagedKey), 409)
	}
	if outputs.ARN == "" {
		accountID, accountErr := o.accountID(ctx, desired.Account)
		if accountErr != nil {
			return kernel.Observation[ObservedState]{}, accountErr
		}
		observation.Value.Region = region
		observation.Value.AccountId = accountID
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired EBSVolumeSpec) (kernel.CreateResult[EBSVolumeOutputs], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[EBSVolumeOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	id, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		if desired.ManagedKey != "" {
			existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil || existing != "" {
				return existing, findErr
			}
		}
		return api.CreateVolume(rc, desired)
	}, classifyEBSCreate)
	if err != nil {
		return kernel.CreateResult[EBSVolumeOutputs]{}, err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitUntilAvailable(rc, id)
	}, classifyEBSMutation)
	accountID, accountErr := o.accountID(ctx, desired.Account)
	if err == nil {
		err = accountErr
	}
	return kernel.CreateResult[EBSVolumeOutputs]{SeedOutputs: EBSVolumeOutputs{
		VolumeId: id, ARN: volumeARN(region, accountID, id), AvailabilityZone: desired.AvailabilityZone,
	}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired EBSVolumeSpec, observed ObservedState) error {
	if err := validateImmutableIdentity(desired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	if desired.SizeGiB < observed.SizeGiB {
		return restate.TerminalError(fmt.Errorf("sizeGiB cannot shrink from %d to %d; delete and reprovision", observed.SizeGiB, desired.SizeGiB), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if volumeNeedsModification(desired, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyVolume(rc, observed.VolumeId, modificationSpec(desired, observed))
		}, classifyEBSMutation); err != nil {
			return fmt.Errorf("modify volume: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.VolumeId, desired.Tags)
		}, classifyEBSMutation); err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired EBSVolumeSpec, outputs EBSVolumeOutputs) error {
	if outputs.VolumeId == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeEBS(ctx, api, outputs.VolumeId)
	if err != nil || !observation.Exists {
		return err
	}
	if owner := observation.Value.Tags[managedKeyTag]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete volume %s owned by Praxis object %q, not %q", outputs.VolumeId, owner, desired.ManagedKey), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteVolume(rc, outputs.VolumeId)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyEBSDelete)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observation, err := observeEBS(ctx, api, strings.TrimSpace(ref.ResourceID))
	if err != nil || !observation.Exists {
		return observation, err
	}
	accountID, err := o.accountID(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, err
	}
	observation.Value.Region = region
	observation.Value.AccountId = accountID
	return observation, nil
}

func observeEBS(ctx restate.ObjectContext, api EBSAPI, id string) (kernel.Observation[ObservedState], error) {
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeVolume(rc, id)
		if IsNotFound(err) || observed.State == "deleted" {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyEBSObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (EBSAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("EBSVolume driver is not configured")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve EBS account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func (o *genericOperations) accountID(ctx restate.ObjectContext, account string) (string, error) {
	if o == nil || o.auth == nil || o.accountIDFactory == nil {
		return "", restate.TerminalError(fmt.Errorf("EBSVolume account resolver is not configured"), 500)
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return "", drivers.ClassifyCredentialError(fmt.Errorf("resolve EBS account %q: %w", account, err))
	}
	return o.accountIDFactory(ctx, cfg)
}

func validateSpec(spec EBSVolumeSpec) error {
	if spec.Region == "" || spec.AvailabilityZone == "" || spec.VolumeType == "" {
		return fmt.Errorf("region, availabilityZone, and volumeType are required")
	}
	if spec.SizeGiB < 1 {
		return fmt.Errorf("sizeGiB must be >= 1")
	}
	return nil
}

func validateImmutableIdentity(desired EBSVolumeSpec, observed ObservedState) error {
	switch {
	case desired.AvailabilityZone != observed.AvailabilityZone:
		return fmt.Errorf("availabilityZone is immutable: observed %q, requested %q; delete and reprovision", observed.AvailabilityZone, desired.AvailabilityZone)
	case desired.Encrypted != observed.Encrypted:
		return fmt.Errorf("encrypted is immutable: observed %t, requested %t; delete and reprovision", observed.Encrypted, desired.Encrypted)
	case desired.SnapshotId != "" && desired.SnapshotId != observed.SnapshotId:
		return fmt.Errorf("snapshotId is immutable: observed %q, requested %q; delete and reprovision", observed.SnapshotId, desired.SnapshotId)
	default:
		return nil
	}
}

func specFromObserved(obs ObservedState) EBSVolumeSpec {
	return EBSVolumeSpec{
		AvailabilityZone: obs.AvailabilityZone, VolumeType: obs.VolumeType, SizeGiB: obs.SizeGiB,
		Iops: obs.Iops, Throughput: obs.Throughput, Encrypted: obs.Encrypted,
		KmsKeyId: obs.KmsKeyId, SnapshotId: obs.SnapshotId, Tags: drivers.FilterPraxisTags(obs.Tags),
	}
}

func outputsFromGenericObserved(obs ObservedState, seed EBSVolumeOutputs) EBSVolumeOutputs {
	arn := seed.ARN
	if arn == "" {
		arn = volumeARN(obs.Region, obs.AccountId, obs.VolumeId)
	}
	return EBSVolumeOutputs{
		VolumeId: obs.VolumeId, ARN: arn, AvailabilityZone: obs.AvailabilityZone,
		State: obs.State, SizeGiB: obs.SizeGiB, VolumeType: obs.VolumeType, Encrypted: obs.Encrypted,
	}
}

func outputsFromObserved(obs ObservedState, region, accountID string) EBSVolumeOutputs {
	obs.Region, obs.AccountId = region, accountID
	return outputsFromGenericObserved(obs, EBSVolumeOutputs{})
}

func volumeARN(region, accountID, id string) string {
	if region == "" || accountID == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("arn:aws:ec2:%s:%s:volume/%s", region, accountID, id)
}

func applyDefaults(spec EBSVolumeSpec) EBSVolumeSpec {
	if spec.VolumeType == "" {
		spec.VolumeType = "gp3"
	}
	if spec.SizeGiB == 0 {
		spec.SizeGiB = 20
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func volumeNeedsModification(desired EBSVolumeSpec, observed ObservedState) bool {
	return desired.VolumeType != observed.VolumeType || desired.SizeGiB > observed.SizeGiB ||
		(desired.Iops > 0 && desired.Iops != observed.Iops) ||
		(desired.Throughput > 0 && desired.Throughput != observed.Throughput)
}

func modificationSpec(desired EBSVolumeSpec, observed ObservedState) EBSVolumeSpec {
	copy := desired
	if copy.SizeGiB < observed.SizeGiB {
		copy.SizeGiB = observed.SizeGiB
	}
	return copy
}

func classifyEBSObserve(err error) error {
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

func classifyEBSFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyEBSObserve(err)
}

func classifyEBSCreate(err error) error { return classifyEBSObserve(err) }

func classifyEBSMutation(err error) error {
	if err != nil && IsModificationCooldown(err) {
		return restate.TerminalError(err, 429)
	}
	return classifyEBSObserve(err)
}

func classifyEBSDelete(err error) error {
	if err != nil && IsVolumeInUse(err) {
		return restate.TerminalError(fmt.Errorf("volume is attached to an instance; detach it before deleting: %w", err), 409)
	}
	return classifyEBSMutation(err)
}

func classifyEBSAccount(err error) error {
	if err != nil && awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if err != nil && awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}
