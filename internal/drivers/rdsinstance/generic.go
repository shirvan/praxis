package rdsinstance

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

const managedKeyTag = "praxis:managed-key"

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) RDSInstanceAPI
}

func NewGenericRDSInstanceDriver(auth authservice.AuthClient) *kernel.Driver[RDSInstanceSpec, RDSInstanceOutputs, ObservedState] {
	return newGenericRDSInstanceDriverWithFactory(auth, nil)
}

func newGenericRDSInstanceDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RDSInstanceAPI) *kernel.Driver[RDSInstanceSpec, RDSInstanceOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) RDSInstanceAPI { return NewRDSInstanceAPI(awsclient.NewRDSClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[RDSInstanceSpec, RDSInstanceOutputs, ObservedState]{
		ServiceName:  ServiceName,
		Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true},
		Operations:   ops,
		Prepare: func(ctx restate.ObjectContext, spec RDSInstanceSpec) (RDSInstanceSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return RDSInstanceSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			if region != "" && spec.Region != region {
				return RDSInstanceSpec{}, restate.TerminalError(fmt.Errorf("region %q does not match account region %q", spec.Region, region), 400)
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate:       validateSpec,
		ValidateImport: validateImportSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) RDSInstanceSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ RDSInstanceOutputs) RDSInstanceOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired RDSInstanceSpec, outputs RDSInstanceOutputs) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	id := strings.TrimSpace(outputs.DBIdentifier)
	byIdentity := false
	if id == "" {
		id = strings.TrimSpace(desired.DBIdentifier)
		byIdentity = id != ""
	}
	observation, err := observeInstance(ctx, api, id)
	if err != nil || !observation.Exists {
		return observation, err
	}
	observation.Value.Region = region
	owner := observation.Value.Tags[managedKeyTag]
	if owner != "" && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("RDS instance %s is owned by Praxis object %q, not %q", id, owner, desired.ManagedKey), 409)
	}
	if byIdentity && owner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt RDS instance %s without exact Praxis ownership tag %q", id, desired.ManagedKey), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired RDSInstanceSpec) (kernel.CreateResult[RDSInstanceOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[RDSInstanceOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	arn, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		observed, describeErr := api.DescribeDBInstance(rc, desired.DBIdentifier)
		if describeErr == nil {
			if observed.Tags[managedKeyTag] != desired.ManagedKey {
				return "", restate.TerminalError(fmt.Errorf("refusing to adopt RDS instance %s without exact Praxis ownership tag %q", desired.DBIdentifier, desired.ManagedKey), 409)
			}
			return observed.ARN, nil
		}
		if !IsNotFound(describeErr) {
			return "", describeErr
		}
		return api.CreateDBInstance(rc, desired)
	}, classifyInstanceMutation)
	if err != nil {
		return kernel.CreateResult[RDSInstanceOutputs]{}, err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitUntilAvailable(rc, desired.DBIdentifier)
	}, classifyInstanceMutation)
	return kernel.CreateResult[RDSInstanceOutputs]{SeedOutputs: RDSInstanceOutputs{DBIdentifier: desired.DBIdentifier, ARN: arn}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired RDSInstanceSpec, observed ObservedState) error {
	if err := validateExisting(desired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	if desired.AllocatedStorage > 0 && desired.AllocatedStorage < observed.AllocatedStorage {
		return restate.TerminalError(fmt.Errorf("allocatedStorage cannot shrink from %d to %d; delete and reprovision", observed.AllocatedStorage, desired.AllocatedStorage), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if HasDrift(desired, observed) {
		visible := desired
		visible.MasterUserPassword = ""
		if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyDBInstance(rc, visible, true)
		}, classifyInstanceMutation); err != nil {
			return fmt.Errorf("modify RDS instance: %w", err)
		}
		if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.WaitUntilAvailable(rc, desired.DBIdentifier)
		}, classifyInstanceMutation); err != nil {
			return err
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) && observed.ARN != "" {
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.ARN, desired.Tags)
		}, classifyInstanceMutation)
	}
	return err
}

func (o *genericOperations) ConvergeProvisionChange(ctx restate.ObjectContext, previousDesired, nextDesired RDSInstanceSpec, observed ObservedState) error {
	if err := validateExisting(nextDesired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	if nextDesired.MasterUserPassword == "" || nextDesired.MasterUserPassword == previousDesired.MasterUserPassword {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, nextDesired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.ModifyDBInstance(rc, nextDesired, true)
	}, classifyInstanceMutation); err != nil {
		return fmt.Errorf("rotate RDS instance master password: %w", err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitUntilAvailable(rc, nextDesired.DBIdentifier)
	}, classifyInstanceMutation)
	return err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired RDSInstanceSpec, outputs RDSInstanceOutputs) error {
	id := outputs.DBIdentifier
	if id == "" {
		id = desired.DBIdentifier
	}
	if id == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeInstance(ctx, api, id)
	if err != nil || !observation.Exists {
		return err
	}
	if owner := observation.Value.Tags[managedKeyTag]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete RDS instance owned by Praxis object %q", owner), 409)
	}
	if observation.Value.DeletionProtection {
		return deletionProtectionError(id)
	}
	if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		e := api.DeleteDBInstance(rc, id, true)
		if IsNotFound(e) {
			e = nil
		}
		return restate.Void{}, e
	}, classifyInstanceMutation); err != nil {
		return err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		e := api.WaitUntilDeleted(rc, id)
		if IsNotFound(e) {
			e = nil
		}
		return restate.Void{}, e
	}, classifyInstanceMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observation, err := observeInstance(ctx, api, strings.TrimSpace(ref.ResourceID))
	observation.Value.Region = region
	return observation, err
}
func observeInstance(ctx restate.ObjectContext, api RDSInstanceAPI, id string) (kernel.Observation[ObservedState], error) {
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeDBInstance(rc, id)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: err == nil, Value: observed}, err
	}, classifyInstanceObserve)
}
func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (RDSInstanceAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("RDS instance driver is not configured")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RDS account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}
func classifyInstanceObserve(err error) error {
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
func classifyInstanceMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsAlreadyExists(err) || IsInvalidState(err) {
		return restate.TerminalError(err, 409)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func validateImportSpec(spec RDSInstanceSpec) error {
	password := spec.MasterUserPassword
	spec.MasterUserPassword = "import-placeholder"
	err := validateSpec(spec)
	spec.MasterUserPassword = password
	return err
}
func validateSpec(spec RDSInstanceSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.DBIdentifier) == "" {
		return fmt.Errorf("dbIdentifier is required")
	}
	if strings.TrimSpace(spec.Engine) == "" {
		return fmt.Errorf("engine is required")
	}
	if strings.TrimSpace(spec.EngineVersion) == "" {
		return fmt.Errorf("engineVersion is required")
	}
	if strings.TrimSpace(spec.InstanceClass) == "" {
		return fmt.Errorf("instanceClass is required")
	}
	if spec.DBClusterIdentifier == "" {
		if spec.AllocatedStorage <= 0 {
			return fmt.Errorf("allocatedStorage is required for non-Aurora RDS instances")
		}
		if strings.TrimSpace(spec.MasterUsername) == "" {
			return fmt.Errorf("masterUsername is required for non-Aurora RDS instances")
		}
		if strings.TrimSpace(spec.MasterUserPassword) == "" {
			return fmt.Errorf("masterUserPassword is required for non-Aurora RDS instances")
		}
	}
	if spec.MonitoringInterval > 0 && strings.TrimSpace(spec.MonitoringRoleArn) == "" {
		return fmt.Errorf("monitoringRoleArn is required when monitoringInterval > 0")
	}
	return nil
}
func validateExisting(spec RDSInstanceSpec, observed ObservedState) error {
	if observed.DBIdentifier != "" && spec.DBIdentifier != observed.DBIdentifier {
		return fmt.Errorf("dbIdentifier is immutable: desired %q, observed %q", spec.DBIdentifier, observed.DBIdentifier)
	}
	if observed.Engine != "" && spec.Engine != observed.Engine {
		return fmt.Errorf("engine is immutable: desired %q, observed %q", spec.Engine, observed.Engine)
	}
	if spec.DBClusterIdentifier == "" && observed.MasterUsername != "" && spec.MasterUsername != observed.MasterUsername {
		return fmt.Errorf("masterUsername is immutable: desired %q, observed %q", spec.MasterUsername, observed.MasterUsername)
	}
	if spec.DBClusterIdentifier != observed.DBClusterIdentifier {
		return fmt.Errorf("dbClusterIdentifier is immutable: desired %q, observed %q", spec.DBClusterIdentifier, observed.DBClusterIdentifier)
	}
	if spec.StorageEncrypted != observed.StorageEncrypted {
		return fmt.Errorf("storageEncrypted is immutable: desired %t, observed %t", spec.StorageEncrypted, observed.StorageEncrypted)
	}
	if spec.KMSKeyId != "" && spec.KMSKeyId != observed.KMSKeyId {
		return fmt.Errorf("kmsKeyId is immutable: desired %q, observed %q", spec.KMSKeyId, observed.KMSKeyId)
	}
	return nil
}
func specFromObserved(observed ObservedState) RDSInstanceSpec {
	return applyDefaults(RDSInstanceSpec{DBIdentifier: observed.DBIdentifier, Engine: observed.Engine, EngineVersion: observed.EngineVersion, InstanceClass: observed.InstanceClass, AllocatedStorage: observed.AllocatedStorage, StorageType: observed.StorageType, IOPS: observed.IOPS, StorageThroughput: observed.StorageThroughput, StorageEncrypted: observed.StorageEncrypted, KMSKeyId: observed.KMSKeyId, MasterUsername: observed.MasterUsername, DBSubnetGroupName: observed.DBSubnetGroupName, ParameterGroupName: observed.ParameterGroupName, VpcSecurityGroupIds: observed.VpcSecurityGroupIds, DBClusterIdentifier: observed.DBClusterIdentifier, MultiAZ: observed.MultiAZ, PubliclyAccessible: observed.PubliclyAccessible, BackupRetentionPeriod: observed.BackupRetentionPeriod, PreferredBackupWindow: observed.PreferredBackupWindow, PreferredMaintenanceWindow: observed.PreferredMaintenanceWindow, DeletionProtection: observed.DeletionProtection, AutoMinorVersionUpgrade: observed.AutoMinorVersionUpgrade, MonitoringInterval: observed.MonitoringInterval, MonitoringRoleArn: observed.MonitoringRoleArn, PerformanceInsightsEnabled: observed.PerformanceInsightsEnabled, Tags: drivers.FilterPraxisTags(observed.Tags)})
}
func outputsFromObserved(observed ObservedState) RDSInstanceOutputs {
	return RDSInstanceOutputs{DBIdentifier: observed.DBIdentifier, DbiResourceId: observed.DbiResourceId, ARN: observed.ARN, Endpoint: observed.Endpoint, Port: observed.Port, Engine: observed.Engine, EngineVersion: observed.EngineVersion, Status: observed.Status}
}
func deletionProtectionError(identifier string) error {
	return restate.TerminalError(fmt.Errorf("deletion protection is enabled on %s; set deletionProtection: false in the spec, apply, then retry the delete", identifier), 409)
}
