package iampolicy

import (
	"fmt"
	"sort"
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
	apiFactory func(aws.Config) IAMPolicyAPI
}

// NewGenericIAMPolicyDriver binds IAM customer-managed policy behavior to the
// shared lifecycle kernel. AWS managed-policy versions are provider resources;
// they are not Praxis contract versions and do not imply compatibility paths.
func NewGenericIAMPolicyDriver(auth authservice.AuthClient) *kernel.Driver[IAMPolicySpec, IAMPolicyOutputs, ObservedState] {
	return newGenericIAMPolicyDriverWithFactory(auth, nil)
}

func newGenericIAMPolicyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMPolicyAPI) *kernel.Driver[IAMPolicySpec, IAMPolicyOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) IAMPolicyAPI { return NewIAMPolicyAPI(awsclient.NewIAMClient(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[IAMPolicySpec, IAMPolicyOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec IAMPolicySpec) (IAMPolicySpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return IAMPolicySpec{}, drivers.ClassifyCredentialError(err)
			}
			return applyDefaults(spec), nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) IAMPolicySpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed IAMPolicyOutputs) IAMPolicyOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.Arn == "" {
				outputs.Arn = seed.Arn
			}
			if outputs.PolicyId == "" {
				outputs.PolicyId = seed.PolicyId
			}
			if outputs.PolicyName == "" {
				outputs.PolicyName = seed.PolicyName
			}
			return outputs
		},
		HasDrift: HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired IAMPolicySpec, outputs IAMPolicyOutputs) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	if outputs.Arn != "" {
		return o.observeByARN(ctx, api, outputs.Arn)
	}
	if desired.PolicyName == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return o.observeByName(ctx, api, desired.PolicyName, desired.Path)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired IAMPolicySpec) (kernel.CreateResult[IAMPolicyOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[IAMPolicyOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (IAMPolicyOutputs, error) {
		arn, policyID, runErr := api.CreatePolicy(rc, desired)
		return IAMPolicyOutputs{Arn: arn, PolicyId: policyID, PolicyName: desired.PolicyName}, runErr
	}, classifyMutation)
	return kernel.CreateResult[IAMPolicyOutputs]{SeedOutputs: outputs}, err
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired IAMPolicySpec, observed ObservedState) error {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if desired.PolicyName != observed.PolicyName {
		return restate.TerminalError(fmt.Errorf("policyName is immutable; delete and recreate the policy to change it"), 409)
	}
	if desired.Path != observed.Path {
		return restate.TerminalError(fmt.Errorf("path is immutable; delete and recreate the policy to change it"), 409)
	}
	if desired.Description != observed.Description {
		return restate.TerminalError(fmt.Errorf("description is immutable; delete and recreate the policy to change it"), 409)
	}
	if !policyDocumentsEqual(desired.PolicyDocument, observed.PolicyDocument) {
		if err := o.rotateDefaultVersion(ctx, api, observed.Arn, desired.PolicyDocument); err != nil {
			return fmt.Errorf("update policy document: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.Arn, desired.Tags)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update policy tags: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired IAMPolicySpec, outputs IAMPolicyOutputs) error {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	var observation kernel.Observation[ObservedState]
	if outputs.Arn != "" {
		observation, err = o.observeByARN(ctx, api, outputs.Arn)
	} else if desired.PolicyName != "" {
		observation, err = o.observeByName(ctx, api, desired.PolicyName, desired.Path)
	}
	if err != nil || !observation.Exists {
		return err
	}
	observed := observation.Value
	if observed.AttachmentCount > 0 {
		return restate.TerminalError(fmt.Errorf("cannot delete IAM policy %s: %d external principal attachment(s) remain", observed.Arn, observed.AttachmentCount), 409)
	}

	versions, err := o.listVersions(ctx, api, observed.Arn)
	if err != nil {
		return fmt.Errorf("list policy versions before delete: %w", err)
	}
	for _, version := range sortedVersions(versions) {
		if version.IsDefaultVersion {
			continue
		}
		if err := o.deleteVersion(ctx, api, observed.Arn, version.VersionID); err != nil {
			return fmt.Errorf("delete policy version %s: %w", version.VersionID, err)
		}
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeletePolicy(rc, observed.Arn)
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
	resourceID := strings.TrimSpace(ref.ResourceID)
	if strings.HasPrefix(resourceID, "arn:") {
		return o.observeByARN(ctx, api, resourceID)
	}
	return o.observeByName(ctx, api, resourceID, "")
}

func (o *kernelOperations) observeByARN(ctx restate.ObjectContext, api IAMPolicyAPI, arn string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribePolicy(rc, arn)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) observeByName(ctx restate.ObjectContext, api IAMPolicyAPI, name, path string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribePolicyByName(rc, name, path)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) rotateDefaultVersion(ctx restate.ObjectContext, api IAMPolicyAPI, arn, document string) error {
	versions, err := o.listVersions(ctx, api, arn)
	if err != nil {
		return err
	}
	if len(versions) >= 5 {
		oldest := findOldestNonDefault(versions)
		if oldest == "" {
			return restate.TerminalError(fmt.Errorf("IAM policy version limit reached and no non-default version can be removed"), 409)
		}
		if err := o.deleteVersion(ctx, api, arn, oldest); err != nil {
			return err
		}
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.CreatePolicyVersion(rc, arn, document)
	}, classifyMutation)
	return err
}

func (o *kernelOperations) listVersions(ctx restate.ObjectContext, api IAMPolicyAPI, arn string) ([]PolicyVersionInfo, error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) ([]PolicyVersionInfo, error) {
		return api.ListPolicyVersions(rc, arn)
	}, classifyObserve)
}

func (o *kernelOperations) deleteVersion(ctx restate.ObjectContext, api IAMPolicyAPI, arn, versionID string) error {
	_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeletePolicyVersion(rc, arn, versionID)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (IAMPolicyAPI, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("IAMPolicy driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAM account %q: %w", account, err)
	}
	return o.apiFactory(cfg), nil
}

func classifyObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
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
	if IsMalformedPolicy(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsAlreadyExists(err) || IsDeleteConflict(err) || IsVersionLimitExceeded(err) {
		return restate.TerminalError(err, 409)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	return err
}

func validateSpec(spec IAMPolicySpec) error {
	if spec.PolicyName == "" {
		return fmt.Errorf("policyName is required")
	}
	if spec.PolicyDocument == "" {
		return fmt.Errorf("policyDocument is required")
	}
	return nil
}

func applyDefaults(spec IAMPolicySpec) IAMPolicySpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.PolicyName = strings.TrimSpace(spec.PolicyName)
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func specFromObserved(obs ObservedState) IAMPolicySpec {
	return IAMPolicySpec{
		Path: obs.Path, PolicyName: obs.PolicyName,
		PolicyDocument: normalizePolicyDocument(obs.PolicyDocument),
		Description:    obs.Description, Tags: drivers.FilterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) IAMPolicyOutputs {
	return IAMPolicyOutputs{Arn: obs.Arn, PolicyId: obs.PolicyId, PolicyName: obs.PolicyName}
}

func sortedVersions(versions []PolicyVersionInfo) []PolicyVersionInfo {
	result := append([]PolicyVersionInfo(nil), versions...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreateDate.Equal(result[j].CreateDate) {
			return result[i].VersionID < result[j].VersionID
		}
		return result[i].CreateDate.Before(result[j].CreateDate)
	})
	return result
}
