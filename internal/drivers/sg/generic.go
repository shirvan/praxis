package sg

import (
	"errors"
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

// ServiceName is the Restate Virtual Object name for Security Groups.
const ServiceName = "SecurityGroup"

const managedKeyTag = "praxis:managed-key"

type ownershipConflictError struct {
	message string
}

func (e *ownershipConflictError) Error() string { return e.message }

type kernelOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SGAPI
}

// NewGenericSecurityGroupDriver binds EC2 security-group semantics to the
// shared lifecycle kernel while retaining add-before-remove rule convergence.
func NewGenericSecurityGroupDriver(auth authservice.AuthClient) *kernel.Driver[SecurityGroupSpec, SecurityGroupOutputs, ObservedState] {
	return newGenericSecurityGroupDriverWithFactory(auth, nil)
}

func newGenericSecurityGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SGAPI) *kernel.Driver[SecurityGroupSpec, SecurityGroupOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) SGAPI { return NewSGAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[SecurityGroupSpec, SecurityGroupOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec SecurityGroupSpec) (SecurityGroupSpec, error) {
			if _, _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return SecurityGroupSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) SecurityGroupSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed SecurityGroupOutputs) SecurityGroupOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.GroupId == "" {
				outputs.GroupId = seed.GroupId
			}
			if outputs.GroupArn == "" {
				outputs.GroupArn = seed.GroupArn
			}
			if outputs.VpcId == "" {
				outputs.VpcId = seed.VpcId
			}
			return outputs
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired SecurityGroupSpec, outputs SecurityGroupOutputs) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	if outputs.GroupId != "" {
		return o.observeSecurityGroup(ctx, api, region, outputs.GroupId)
	}
	return o.discoverSecurityGroup(ctx, api, region, desired)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired SecurityGroupSpec) (kernel.CreateResult[SecurityGroupOutputs], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[SecurityGroupOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	groupID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// CreateSecurityGroup has no idempotency token. Recover a resource bearing
		// this exact ownership key before retrying a create whose response may have
		// been lost. A same-name resource with no or different ownership is never
		// adopted.
		if desired.ManagedKey != "" {
			existingID, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil {
				return "", findErr
			}
			if existingID != "" {
				observed, describeErr := api.DescribeSecurityGroup(rc, existingID)
				if describeErr != nil {
					return "", describeErr
				}
				if conflictErr := validateRecoveryCandidate(desired, observed); conflictErr != nil {
					return "", conflictErr
				}
				return existingID, nil
			}
		}
		existing, findErr := api.FindSecurityGroup(rc, desired.GroupName, desired.VpcId)
		if findErr == nil {
			if conflictErr := validateRecoveryCandidate(desired, existing); conflictErr != nil {
				return "", conflictErr
			}
			return existing.GroupId, nil
		}
		if !IsNotFound(findErr) {
			return "", findErr
		}
		return api.CreateSecurityGroup(rc, desired)
	}, classifyMutation)
	return kernel.CreateResult[SecurityGroupOutputs]{SeedOutputs: SecurityGroupOutputs{
		GroupId: groupID, GroupArn: groupArn(region, "", groupID), VpcId: desired.VpcId,
	}}, err
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired SecurityGroupSpec, observed ObservedState) error {
	if err := immutableIdentityError(desired, observed); err != nil {
		return restate.TerminalError(err, 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if err := o.applyRuleDiff(ctx, api, observed.GroupId, desired, observed); err != nil {
		return err
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.GroupId, desired.Tags)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update security group tags: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired SecurityGroupSpec, outputs SecurityGroupOutputs) error {
	if outputs.GroupId == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteSecurityGroup(rc, outputs.GroupId)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeSecurityGroup(ctx, api, region, strings.TrimSpace(ref.ResourceID))
}

func (o *kernelOperations) discoverSecurityGroup(ctx restate.ObjectContext, api SGAPI, region string, desired SecurityGroupSpec) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		if desired.ManagedKey != "" {
			groupID, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil {
				return kernel.Observation[ObservedState]{}, findErr
			}
			if groupID != "" {
				observed, describeErr := api.DescribeSecurityGroup(rc, groupID)
				if describeErr != nil {
					return kernel.Observation[ObservedState]{}, describeErr
				}
				observed.Region = region
				if conflictErr := validateRecoveryCandidate(desired, observed); conflictErr != nil {
					return kernel.Observation[ObservedState]{}, conflictErr
				}
				return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
			}
		}

		observed, findErr := api.FindSecurityGroup(rc, desired.GroupName, desired.VpcId)
		if IsNotFound(findErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if findErr != nil {
			return kernel.Observation[ObservedState]{}, findErr
		}
		observed.Region = region
		if conflictErr := validateRecoveryCandidate(desired, observed); conflictErr != nil {
			return kernel.Observation[ObservedState]{}, conflictErr
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyObserve)
}

func (o *kernelOperations) observeSecurityGroup(ctx restate.ObjectContext, api SGAPI, region, groupID string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribeSecurityGroup(rc, groupID)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		observed.Region = region
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

// applyRuleDiff converges additions before removals to avoid transient traffic
// loss while replacing a permission.
func (o *kernelOperations) applyRuleDiff(ctx restate.ObjectContext, api SGAPI, groupID string, desired SecurityGroupSpec, observed ObservedState) error {
	toAdd, toRemove := ComputeDiff(Normalize(desired), mergeObservedRules(observed))
	addIngress, addEgress := SplitByDirection(toAdd)
	removeIngress, removeEgress := SplitByDirection(toRemove)

	if len(addIngress) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AuthorizeIngress(rc, groupID, addIngress)
		}, classifyMutation); err != nil {
			return fmt.Errorf("authorize ingress: %w", err)
		}
	}
	if len(addEgress) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AuthorizeEgress(rc, groupID, addEgress)
		}, classifyMutation); err != nil {
			return fmt.Errorf("authorize egress: %w", err)
		}
	}
	if len(removeIngress) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.RevokeIngress(rc, groupID, removeIngress)
		}, classifyMutation); err != nil {
			return fmt.Errorf("revoke ingress: %w", err)
		}
	}
	if len(removeEgress) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.RevokeEgress(rc, groupID, removeEgress)
		}, classifyMutation); err != nil {
			return fmt.Errorf("revoke egress: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (SGAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("SecurityGroup driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SecurityGroup account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	var ownershipErr *ownershipConflictError
	if errors.As(err, &ownershipErr) {
		return restate.TerminalError(err, 409)
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
	var ownershipErr *ownershipConflictError
	if errors.As(err, &ownershipErr) {
		return restate.TerminalError(err, 409)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsDuplicate(err) {
		return restate.TerminalError(err, 409)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	// DependencyViolation remains retryable for dependency-ordered teardown.
	return err
}

func specFromObserved(observed ObservedState) SecurityGroupSpec {
	spec := SecurityGroupSpec{
		GroupName: observed.GroupName, Description: observed.Description, VpcId: observed.VpcId,
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
	for _, rule := range observed.IngressRules {
		spec.IngressRules = append(spec.IngressRules, IngressRule{
			Protocol: denormalizeProtocol(rule.Protocol), FromPort: rule.FromPort, ToPort: rule.ToPort, CidrBlock: extractCidr(rule.Target),
		})
	}
	for _, rule := range observed.EgressRules {
		spec.EgressRules = append(spec.EgressRules, EgressRule{
			Protocol: denormalizeProtocol(rule.Protocol), FromPort: rule.FromPort, ToPort: rule.ToPort, CidrBlock: extractCidr(rule.Target),
		})
	}
	return spec
}

func outputsFromObserved(observed ObservedState) SecurityGroupOutputs {
	return SecurityGroupOutputs{
		GroupId: observed.GroupId, GroupArn: groupArn(observed.Region, observed.OwnerId, observed.GroupId), VpcId: observed.VpcId,
	}
}

func applyDefaults(spec SecurityGroupSpec) SecurityGroupSpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.GroupName = strings.TrimSpace(spec.GroupName)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.VpcId = strings.TrimSpace(spec.VpcId)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateRecoveryCandidate(desired SecurityGroupSpec, observed ObservedState) error {
	actualKey := observed.Tags[managedKeyTag]
	if desired.ManagedKey == "" || actualKey != desired.ManagedKey {
		return &ownershipConflictError{message: fmt.Sprintf(
			"security group %s (%s in VPC %s) already exists with different ownership; expected managed-key %q, found %q",
			observed.GroupId, observed.GroupName, observed.VpcId, desired.ManagedKey, actualKey,
		)}
	}
	if err := immutableIdentityError(desired, observed); err != nil {
		return &ownershipConflictError{message: fmt.Sprintf(
			"security group managed-key %q identifies an incompatible resource: %v",
			desired.ManagedKey, err,
		)}
	}
	return nil
}

func immutableIdentityError(desired SecurityGroupSpec, observed ObservedState) error {
	if desired.GroupName != observed.GroupName {
		return fmt.Errorf("groupName is immutable: current=%q desired=%q; delete and reprovision the security group", observed.GroupName, desired.GroupName)
	}
	if desired.Description != observed.Description {
		return fmt.Errorf("description is immutable: current=%q desired=%q; delete and reprovision the security group", observed.Description, desired.Description)
	}
	if desired.VpcId != observed.VpcId {
		return fmt.Errorf("vpcId is immutable: current=%q desired=%q; delete and reprovision the security group", observed.VpcId, desired.VpcId)
	}
	return nil
}

func validateSpec(spec SecurityGroupSpec) error {
	if spec.GroupName == "" {
		return fmt.Errorf("groupName is required")
	}
	if spec.Description == "" {
		return fmt.Errorf("description is required")
	}
	if spec.VpcId == "" {
		return fmt.Errorf("vpcId is required")
	}
	return nil
}

func groupArn(region, ownerID, groupID string) string {
	if ownerID == "" {
		ownerID = "000000000000"
	}
	return fmt.Sprintf("arn:aws:ec2:%s:%s:security-group/%s", region, ownerID, groupID)
}

func extractCidr(target string) string {
	return strings.TrimPrefix(target, "cidr:")
}
