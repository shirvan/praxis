package sg

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ServiceName is the Restate Virtual Object name for Security Groups.
const ServiceName = "SecurityGroup"

// SecurityGroupDriver is a Restate Virtual Object that manages EC2 Security Group lifecycle.
// Each instance is keyed by a stable resource identifier (e.g. "vpc-123~web-sg").
type SecurityGroupDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SGAPI
}

// NewSecurityGroupDriver creates a new SecurityGroupDriver that resolves AWS clients per request.
func NewSecurityGroupDriver(auth authservice.AuthClient) *SecurityGroupDriver {
	return NewSecurityGroupDriverWithFactory(auth, func(cfg aws.Config) SGAPI {
		return NewSGAPI(awsclient.NewEC2Client(cfg))
	})
}

func NewSecurityGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SGAPI) *SecurityGroupDriver {
	if factory == nil {
		factory = func(cfg aws.Config) SGAPI {
			return NewSGAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &SecurityGroupDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object name.
func (d *SecurityGroupDriver) ServiceName() string {
	return ServiceName
}

// Provision implements "ensure desired state" semantics — it is idempotent by design:
//  1. If the security group does not exist, create it and apply rules/tags.
//  2. If it already exists (re-provision), converge rules and tags.
func (d *SecurityGroupDriver) Provision(ctx restate.ObjectContext, spec SecurityGroupSpec) (SecurityGroupOutputs, error) {
	ctx.Log().Info("provisioning security group", "groupName", spec.GroupName, "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return SecurityGroupOutputs{}, restate.TerminalError(err, 400)
	}

	// --- Input validation ---
	if spec.GroupName == "" {
		return SecurityGroupOutputs{}, restate.TerminalError(fmt.Errorf("groupName is required"), 400)
	}
	if spec.Description == "" {
		return SecurityGroupOutputs{}, restate.TerminalError(fmt.Errorf("description is required"), 400)
	}
	if spec.VpcId == "" {
		return SecurityGroupOutputs{}, restate.TerminalError(fmt.Errorf("vpcId is required"), 400)
	}

	// --- Load current state ---
	state, err := restate.Get[SecurityGroupState](ctx, drivers.StateKey)
	if err != nil {
		return SecurityGroupOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	// --- Check if SG already exists (re-provision path) ---
	groupId := state.Outputs.GroupId
	if groupId != "" {
		// Verify it still exists in AWS
		_, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, err := api.DescribeSecurityGroup(rc, groupId)
			if err != nil {
				if IsNotFound(err) {
					return ObservedState{}, restate.TerminalError(err, 404)
				}
				return ObservedState{}, err
			}
			return obs, nil
		})
		if descErr != nil {
			groupId = "" // was deleted externally or not found, recreate
		}
	}

	// --- Create security group if it doesn't exist ---
	if groupId == "" {
		newGroupId, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, err := api.CreateSecurityGroup(rc, spec)
			if err != nil {
				if IsDuplicate(err) || IsInvalidParam(err) {
					return "", restate.TerminalError(err, 409)
				}
				return "", err
			}
			return id, nil
		})
		if err != nil {
			if IsDuplicate(err) {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("security group %s already exists in VPC %s", spec.GroupName, spec.VpcId)
				restate.Set(ctx, drivers.StateKey, state)
				return SecurityGroupOutputs{}, restate.TerminalError(fmt.Errorf("%s", state.Error), 409)
			}
			if IsInvalidParam(err) {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return SecurityGroupOutputs{}, restate.TerminalError(fmt.Errorf("invalid parameter: %w", err), 400)
			}
			return SecurityGroupOutputs{}, err
		}
		groupId = newGroupId
	}

	// --- Apply tags ---
	if len(spec.Tags) > 0 {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, groupId, spec.Tags)
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SecurityGroupOutputs{}, restate.TerminalError(fmt.Errorf("failed to update tags: %w", err), 500)
		}
	}

	// --- Apply rules with add-before-remove ---
	if err := d.applyRuleDiff(ctx, api, groupId, spec, ObservedState{}); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return SecurityGroupOutputs{}, restate.TerminalError(fmt.Errorf("failed to apply rules: %w", err), 500)
	}

	// --- Build outputs ---
	outputs := SecurityGroupOutputs{
		GroupId:  groupId,
		GroupArn: fmt.Sprintf("arn:aws:ec2:%s:000000000000:security-group/%s", region, groupId),
		VpcId:    spec.VpcId,
	}

	// --- Commit state atomically ---
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Import captures the current provider state as both the initial desired baseline
// and the initial observed state. First reconciliation sees no drift.
func (d *SecurityGroupDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SecurityGroupOutputs, error) {
	ctx.Log().Info("importing security group", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return SecurityGroupOutputs{}, restate.TerminalError(err, 400)
	}

	mode := drivers.DefaultMode(ref.Mode)

	state, err := restate.Get[SecurityGroupState](ctx, drivers.StateKey)
	if err != nil {
		return SecurityGroupOutputs{}, err
	}
	state.Generation++

	// --- Describe the existing security group ---
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeSecurityGroup(rc, ref.ResourceID)
	})
	if err != nil {
		if IsNotFound(err) {
			return SecurityGroupOutputs{}, restate.TerminalError(
				fmt.Errorf("import failed: security group %s does not exist", ref.ResourceID), 404,
			)
		}
		return SecurityGroupOutputs{}, err
	}

	// --- Synthesize spec from observed (so first reconcile sees no drift) ---
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	outputs := SecurityGroupOutputs{
		GroupId:  observed.GroupId,
		GroupArn: fmt.Sprintf("arn:aws:ec2:%s:000000000000:security-group/%s", region, observed.GroupId),
		VpcId:    observed.VpcId,
	}

	// --- Commit state atomically ---
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes the security group.
func (d *SecurityGroupDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting security group", "key", restate.Key(ctx))

	state, err := restate.Get[SecurityGroupState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.DeleteSecurityGroup(rc, state.Outputs.GroupId); err != nil {
			// Classify terminal errors inside the callback: restate.Run panics
			// on non-terminal errors, so post-callback classification never runs.
			if IsDependencyViolation(err) {
				return restate.Void{}, restate.TerminalError(err, 409)
			}
			if IsNotFound(err) {
				return restate.Void{}, nil // already gone
			}
			return restate.Void{}, err // transient — Restate retries
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("security group %s is still referenced by other resources", state.Outputs.GroupId)
		restate.Set(ctx, drivers.StateKey, state)
		return restate.TerminalError(fmt.Errorf("%s", state.Error), 409)
	}

	restate.Set(ctx, drivers.StateKey, SecurityGroupState{
		Status: types.StatusDeleted,
	})
	return nil
}

// Reconcile checks actual state against desired state and corrects drift (Managed)
// or reports it (Observed).
func (d *SecurityGroupDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[SecurityGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}

	state.ReconcileScheduled = false

	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	// --- Describe current AWS state ---
	type describeResult struct {
		Observed ObservedState `json:"observed"`
		Deleted  bool          `json:"deleted"`
	}

	describe, err := restate.Run(ctx, func(rc restate.RunContext) (describeResult, error) {
		obs, err := api.DescribeSecurityGroup(rc, state.Outputs.GroupId)
		if err != nil {
			if IsNotFound(err) {
				return describeResult{Deleted: true}, nil
			}
			return describeResult{}, err
		}
		return describeResult{Observed: obs}, nil
	})
	if err != nil {
		state.LastReconcile = time.Now().UTC().Format(time.RFC3339)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if describe.Deleted {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("security group %s was deleted externally", state.Outputs.GroupId)
		state.LastReconcile = time.Now().UTC().Format(time.RFC3339)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	observed := describe.Observed

	state.Observed = observed
	state.LastReconcile = time.Now().UTC().Format(time.RFC3339)

	drift := HasDrift(state.Desired, observed)

	// --- Error status: read-only describe, no correction ---
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	// --- Ready + Managed + drift: correct with add-before-remove ---
	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting", "groupId", state.Outputs.GroupId)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")

		// Apply rule diff
		if ruleErr := d.applyRuleDiff(ctx, api, state.Outputs.GroupId, state.Desired, observed); ruleErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: ruleErr.Error()}, nil
		}

		// Apply tag diff
		if !tagsMatch(state.Desired.Tags, observed.Tags) {
			_, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateTags(rc, state.Outputs.GroupId, state.Desired.Tags)
			})
			if tagErr != nil {
				restate.Set(ctx, drivers.StateKey, state)
				d.scheduleReconcile(ctx, &state)
				return types.ReconcileResult{Drift: true, Correcting: true, Error: tagErr.Error()}, nil
			}
		}

		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	// --- Ready + Observed + drift: report only ---
	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "groupId", state.Outputs.GroupId)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	// --- No drift ---
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// GetStatus is a SHARED handler — it can run concurrently with exclusive handlers.
func (d *SecurityGroupDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SecurityGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{
		Status:     state.Status,
		Mode:       state.Mode,
		Generation: state.Generation,
		Error:      state.Error,
	}, nil
}

// GetOutputs is a SHARED handler — returns the resource outputs (GroupId, ARN, etc.).
func (d *SecurityGroupDriver) GetOutputs(ctx restate.ObjectSharedContext) (SecurityGroupOutputs, error) {
	state, err := restate.Get[SecurityGroupState](ctx, drivers.StateKey)
	if err != nil {
		return SecurityGroupOutputs{}, err
	}
	return state.Outputs, nil
}

// scheduleReconcile sends a delayed self-invocation to trigger Reconcile.
func (d *SecurityGroupDriver) scheduleReconcile(ctx restate.ObjectContext, state *SecurityGroupState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

// applyRuleDiff computes the rule diff between desired spec and observed state,
// then applies rules in add-before-remove order.
func (d *SecurityGroupDriver) applyRuleDiff(ctx restate.ObjectContext, api SGAPI, groupId string, desired SecurityGroupSpec, observed ObservedState) error {
	desiredRules := Normalize(desired)
	observedRules := mergeObservedRules(observed)

	toAdd, toRemove := ComputeDiff(desiredRules, observedRules)

	// --- Add new rules first (safety: no traffic disruption) ---
	addIngress, addEgress := SplitByDirection(toAdd)

	if len(addIngress) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AuthorizeIngress(rc, groupId, addIngress)
		})
		if err != nil {
			return fmt.Errorf("authorize ingress: %w", err)
		}
	}

	if len(addEgress) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AuthorizeEgress(rc, groupId, addEgress)
		})
		if err != nil {
			return fmt.Errorf("authorize egress: %w", err)
		}
	}

	// --- Remove stale rules after adds succeed ---
	removeIngress, removeEgress := SplitByDirection(toRemove)

	if len(removeIngress) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.RevokeIngress(rc, groupId, removeIngress)
		})
		if err != nil {
			return fmt.Errorf("revoke ingress: %w", err)
		}
	}

	if len(removeEgress) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.RevokeEgress(rc, groupId, removeEgress)
		})
		if err != nil {
			return fmt.Errorf("revoke egress: %w", err)
		}
	}

	return nil
}

func (d *SecurityGroupDriver) apiForAccount(ctx restate.ObjectContext, account string) (SGAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("SecurityGroupDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SecurityGroup account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// specFromObserved creates a SecurityGroupSpec from observed AWS state.
// Ensures the first reconciliation after import sees no drift.
func specFromObserved(obs ObservedState) SecurityGroupSpec {
	spec := SecurityGroupSpec{
		GroupName:   obs.GroupName,
		Description: obs.Description,
		VpcId:       obs.VpcId,
		Tags:        obs.Tags,
	}

	for _, r := range obs.IngressRules {
		spec.IngressRules = append(spec.IngressRules, IngressRule{
			Protocol:  denormalizeProtocol(r.Protocol),
			FromPort:  r.FromPort,
			ToPort:    r.ToPort,
			CidrBlock: extractCidr(r.Target),
		})
	}

	for _, r := range obs.EgressRules {
		spec.EgressRules = append(spec.EgressRules, EgressRule{
			Protocol:  denormalizeProtocol(r.Protocol),
			FromPort:  r.FromPort,
			ToPort:    r.ToPort,
			CidrBlock: extractCidr(r.Target),
		})
	}

	return spec
}

// extractCidr strips the "cidr:" prefix from a target string.
func extractCidr(target string) string {
	if len(target) > 5 && target[:5] == "cidr:" {
		return target[5:]
	}
	return target
}
