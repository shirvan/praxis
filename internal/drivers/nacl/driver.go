package nacl

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// NetworkACLDriver is a Restate Virtual Object that manages EC2 Network ACL lifecycle.
type NetworkACLDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) NetworkACLAPI
}

// NewNetworkACLDriver creates a production NetworkACLDriver.
func NewNetworkACLDriver(auth authservice.AuthClient) *NetworkACLDriver {
	return NewNetworkACLDriverWithFactory(auth, func(cfg aws.Config) NetworkACLAPI {
		return NewNetworkACLAPI(awsclient.NewEC2Client(cfg))
	})
}

// NewNetworkACLDriverWithFactory allows tests to inject a custom NetworkACLAPI factory.
func NewNetworkACLDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) NetworkACLAPI) *NetworkACLDriver {
	if factory == nil {
		factory = func(cfg aws.Config) NetworkACLAPI {
			return NewNetworkACLAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &NetworkACLDriver{auth: auth, apiFactory: factory}
}

func (d *NetworkACLDriver) ServiceName() string {
	return ServiceName
}

// Provision implements idempotent create-or-converge for a Network ACL.
//
// Flow: normalize spec (validate rules, dedup) → load state → ownership check →
// create if missing → apply desired state (rules + associations + tags) →
// describe final state → commit state → schedule reconcile.
//
// Rule convergence: add new rules → replace changed rules → remove old rules.
// Association convergence: associate missing subnets → disassociate extra subnets
// (disassociation moves the subnet back to the VPC's default NACL).
func (d *NetworkACLDriver) Provision(ctx restate.ObjectContext, spec NetworkACLSpec) (NetworkACLOutputs, error) {
	ctx.Log().Info("provisioning network ACL", "key", restate.Key(ctx))
	if spec.ManagedKey == "" {
		spec.ManagedKey = restate.Key(ctx)
	}
	normalizedSpec, err := normalizeSpec(spec)
	if err != nil {
		return NetworkACLOutputs{}, restate.TerminalError(err, 400)
	}
	spec = normalizedSpec

	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return NetworkACLOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[NetworkACLState](ctx, drivers.StateKey)
	if err != nil {
		return NetworkACLOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	networkAclID := state.Outputs.NetworkAclId
	currentObserved := state.Observed
	if networkAclID != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeNetworkACL(rc, networkAclID)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil {
			networkAclID = ""
			currentObserved = ObservedState{}
		} else {
			currentObserved = described
		}
	}

	if networkAclID == "" && spec.ManagedKey != "" {
		conflictID, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.FindByManagedKey(rc, spec.ManagedKey)
			if runErr != nil {
				if strings.Contains(runErr.Error(), "ownership corruption") {
					return "", restate.TerminalError(runErr, 500)
				}
				return "", runErr
			}
			return id, nil
		})
		if conflictErr != nil {
			state.Status = types.StatusError
			state.Error = conflictErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return NetworkACLOutputs{}, conflictErr
		}
		if conflictID != "" {
			err := formatManagedKeyConflict(spec.ManagedKey, conflictID)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return NetworkACLOutputs{}, restate.TerminalError(err, 409)
		}
	}

	if networkAclID == "" {
		createdID, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateNetworkACL(rc, spec)
			if runErr != nil {
				if IsLimitExceeded(runErr) {
					return "", restate.TerminalError(runErr, 503)
				}
				if IsInvalidParam(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				return "", runErr
			}
			return id, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return NetworkACLOutputs{}, createErr
		}
		networkAclID = createdID
		currentObserved = ObservedState{}
	}

	if err := d.applyDesiredState(ctx, api, networkAclID, spec, currentObserved); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = NetworkACLOutputs{NetworkAclId: networkAclID, VpcId: spec.VpcId}
		restate.Set(ctx, drivers.StateKey, state)
		return NetworkACLOutputs{}, err
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeNetworkACL(rc, networkAclID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = NetworkACLOutputs{NetworkAclId: networkAclID, VpcId: spec.VpcId}
		restate.Set(ctx, drivers.StateKey, state)
		return NetworkACLOutputs{}, err
	}

	outputs := outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Import captures an existing Network ACL's live state as the baseline.
func (d *NetworkACLDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (NetworkACLOutputs, error) {
	ctx.Log().Info("importing network ACL", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return NetworkACLOutputs{}, restate.TerminalError(err, 400)
	}

	mode := defaultNetworkACLImportMode(ref.Mode)
	state, err := restate.Get[NetworkACLState](ctx, drivers.StateKey)
	if err != nil {
		return NetworkACLOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeNetworkACL(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: network ACL %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return NetworkACLOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	outputs := outputsFromObserved(observed)

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

// Delete removes the Network ACL. Before deleting, all associated subnets
// are reassociated to the VPC's default NACL via ReplaceNetworkACLAssociation.
func (d *NetworkACLDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting network ACL", "key", restate.Key(ctx))
	state, err := restate.Get[NetworkACLState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete network ACL %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.NetworkAclId), 409)
	}
	if state.Outputs.IsDefault || state.Observed.IsDefault {
		return restate.TerminalError(fmt.Errorf("cannot delete default network ACL %s", state.Outputs.NetworkAclId), 409)
	}

	networkAclID := state.Outputs.NetworkAclId
	if networkAclID == "" {
		restate.Set(ctx, drivers.StateKey, NetworkACLState{Status: types.StatusDeleted})
		return nil
	}

	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeNetworkACL(rc, networkAclID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, nil
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	if observed.NetworkAclId == "" {
		restate.Set(ctx, drivers.StateKey, NetworkACLState{Status: types.StatusDeleted})
		return nil
	}
	if observed.IsDefault {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("cannot delete default network ACL %s", networkAclID)
		restate.Set(ctx, drivers.StateKey, state)
		return restate.TerminalError(fmt.Errorf("%s", state.Error), 409)
	}

	defaultACLID, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return api.FindDefaultNetworkACL(rc, observed.VpcId)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}

	for _, association := range observed.Associations {

		_, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.ReplaceNetworkACLAssociation(rc, association.AssociationId, defaultACLID)
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("reassociate subnet %s to default network ACL: %v", association.SubnetId, err)
			restate.Set(ctx, drivers.StateKey, state)
			return err
		}
	}

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteNetworkACL(rc, networkAclID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsDefaultACL(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("cannot delete default network ACL %s", networkAclID), 409)
			}
			if IsInUse(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("cannot delete network ACL %s: subnets are still associated", networkAclID), 409)
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}

	restate.Set(ctx, drivers.StateKey, NetworkACLState{Status: types.StatusDeleted})
	return nil
}

// Reconcile checks actual state against desired and corrects drift (Managed)
// or reports it (Observed). Drift includes rules, associations, and tags.
func (d *NetworkACLDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[NetworkACLState](ctx, drivers.StateKey)
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
	if state.Outputs.NetworkAclId == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}

	type describeResult struct {
		Observed ObservedState `json:"observed"`
		Deleted  bool          `json:"deleted"`
	}

	describe, err := restate.Run(ctx, func(rc restate.RunContext) (describeResult, error) {
		obs, runErr := api.DescribeNetworkACL(rc, state.Outputs.NetworkAclId)
		if runErr != nil {
			if IsNotFound(runErr) {
				return describeResult{Deleted: true}, nil
			}
			return describeResult{}, runErr
		}
		return describeResult{Observed: obs}, nil
	})
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if describe.Deleted {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("network ACL %s was deleted externally", state.Outputs.NetworkAclId)
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	observed := describe.Observed

	state.Observed = observed
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)

	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting network ACL", "networkAclId", state.Outputs.NetworkAclId)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.applyDesiredState(ctx, api, state.Outputs.NetworkAclId, state.Desired, observed); correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "networkAclId", state.Outputs.NetworkAclId)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *NetworkACLDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[NetworkACLState](ctx, drivers.StateKey)
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

func (d *NetworkACLDriver) GetOutputs(ctx restate.ObjectSharedContext) (NetworkACLOutputs, error) {
	state, err := restate.Get[NetworkACLState](ctx, drivers.StateKey)
	if err != nil {
		return NetworkACLOutputs{}, err
	}
	return state.Outputs, nil
}

// applyDesiredState converges rules, associations, and tags to match the
// desired spec. Delegates to applyRuleDiff for ingress/egress rules and
// applyAssociationDiff for subnet associations.
func (d *NetworkACLDriver) applyDesiredState(ctx restate.ObjectContext, api NetworkACLAPI, networkAclID string, desired NetworkACLSpec, observed ObservedState) error {
	if err := d.applyRuleDiff(ctx, api, networkAclID, desired.IngressRules, observed.IngressRules, false); err != nil {
		return err
	}
	if err := d.applyRuleDiff(ctx, api, networkAclID, desired.EgressRules, observed.EgressRules, true); err != nil {
		return err
	}
	if err := d.applyAssociationDiff(ctx, api, networkAclID, desired.VpcId, desired.SubnetAssociations, observed.Associations); err != nil {
		return err
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, networkAclID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

// applyRuleDiff computes the diff between desired and observed rules by
// rule number, then applies changes in add → replace → remove order.
func (d *NetworkACLDriver) applyRuleDiff(ctx restate.ObjectContext, api NetworkACLAPI, networkAclID string, desiredRules, observedRules []NetworkACLRule, egress bool) error {
	desiredMap := ruleMap(desiredRules)
	observedMap := ruleMap(observedRules)

	var toAdd []NetworkACLRule
	var toReplace []NetworkACLRule
	var toRemove []NetworkACLRule

	for ruleNumber, desiredRule := range desiredMap {
		observedRule, ok := observedMap[ruleNumber]
		if !ok {
			toAdd = append(toAdd, desiredRule)
			continue
		}
		if !ruleEqual(desiredRule, observedRule) {
			toReplace = append(toReplace, desiredRule)
		}
	}
	for ruleNumber, observedRule := range observedMap {
		if _, ok := desiredMap[ruleNumber]; !ok {
			toRemove = append(toRemove, observedRule)
		}
	}

	sortRules(toAdd)
	sortRules(toReplace)
	sortRules(toRemove)

	for _, rule := range toAdd {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.CreateEntry(rc, networkAclID, rule, egress)
			if runErr != nil {
				if IsDuplicateRule(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
				}
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("create rule %d: %w", rule.RuleNumber, err)
		}
	}

	for _, rule := range toReplace {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.ReplaceEntry(rc, networkAclID, rule, egress)
			if runErr != nil {
				if IsRuleNotFound(runErr) || IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("replace rule %d: %w", rule.RuleNumber, err)
		}
	}

	for _, rule := range toRemove {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DeleteEntry(rc, networkAclID, rule.RuleNumber, egress)
			if runErr != nil {
				if IsRuleNotFound(runErr) {
					return restate.Void{}, nil
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("delete rule %d: %w", rule.RuleNumber, err)
		}
	}

	return nil
}

// applyAssociationDiff adds and removes subnet associations to match the
// desired set. Adding associates the subnet with this NACL; removing
// reassociates the subnet to the VPC's default NACL.
func (d *NetworkACLDriver) applyAssociationDiff(ctx restate.ObjectContext, api NetworkACLAPI, networkAclID string, vpcID string, desiredSubnets []string, observedAssociations []NetworkACLAssociation) error {
	desiredSet := make(map[string]struct{}, len(desiredSubnets))
	for _, subnetID := range desiredSubnets {
		desiredSet[subnetID] = struct{}{}
	}
	observedBySubnet := make(map[string]NetworkACLAssociation, len(observedAssociations))
	for _, association := range observedAssociations {
		observedBySubnet[association.SubnetId] = association
	}

	for _, subnetID := range desiredSubnets {
		if _, ok := observedBySubnet[subnetID]; ok {
			continue
		}
		associationID, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindAssociationIdForSubnet(rc, subnetID)
		})
		if err != nil {
			return fmt.Errorf("find network ACL association for subnet %s: %w", subnetID, err)
		}
		_, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.ReplaceNetworkACLAssociation(rc, associationID, networkAclID)
		})
		if err != nil {
			return fmt.Errorf("associate subnet %s: %w", subnetID, err)
		}
	}

	var toRemove []NetworkACLAssociation
	for _, association := range observedAssociations {
		if _, ok := desiredSet[association.SubnetId]; !ok {
			toRemove = append(toRemove, association)
		}
	}
	if len(toRemove) == 0 {
		return nil
	}

	defaultACLID, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return api.FindDefaultNetworkACL(rc, vpcID)
	})
	if err != nil {
		return fmt.Errorf("find default network ACL for VPC %s: %w", vpcID, err)
	}

	for _, association := range toRemove {
		associationID := association.AssociationId
		if associationID == "" {
			associationID, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
				return api.FindAssociationIdForSubnet(rc, association.SubnetId)
			})
			if err != nil {
				return fmt.Errorf("find network ACL association for subnet %s: %w", association.SubnetId, err)
			}
		}
		_, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.ReplaceNetworkACLAssociation(rc, associationID, defaultACLID)
		})
		if err != nil {
			return fmt.Errorf("reassociate subnet %s to default network ACL: %w", association.SubnetId, err)
		}
	}

	return nil
}

func (d *NetworkACLDriver) scheduleReconcile(ctx restate.ObjectContext, state *NetworkACLState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *NetworkACLDriver) apiForAccount(ctx restate.ObjectContext, account string) (NetworkACLAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("NetworkACLDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve network ACL account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// normalizeSpec validates and normalizes a NetworkACLSpec: checks required
// fields, normalizes protocol names to IANA numbers, validates rule number
// ranges (1-32766), deduplicates rules and subnet associations.
func normalizeSpec(spec NetworkACLSpec) (NetworkACLSpec, error) {
	if spec.Region == "" {
		return NetworkACLSpec{}, fmt.Errorf("region is required")
	}
	if spec.VpcId == "" {
		return NetworkACLSpec{}, fmt.Errorf("vpcId is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	normalizedIngress, err := normalizeRuleSet(spec.IngressRules, "ingressRules")
	if err != nil {
		return NetworkACLSpec{}, err
	}
	normalizedEgress, err := normalizeRuleSet(spec.EgressRules, "egressRules")
	if err != nil {
		return NetworkACLSpec{}, err
	}
	cleanSubnets := make([]string, 0, len(spec.SubnetAssociations))
	seenSubnets := make(map[string]struct{}, len(spec.SubnetAssociations))
	for _, subnetID := range spec.SubnetAssociations {
		subnetID = strings.TrimSpace(subnetID)
		if subnetID == "" {
			return NetworkACLSpec{}, fmt.Errorf("subnetAssociations cannot contain empty values")
		}
		if _, ok := seenSubnets[subnetID]; ok {
			return NetworkACLSpec{}, fmt.Errorf("subnetAssociations contains duplicate subnet %q", subnetID)
		}
		seenSubnets[subnetID] = struct{}{}
		cleanSubnets = append(cleanSubnets, subnetID)
	}
	sort.Strings(cleanSubnets)
	spec.IngressRules = normalizedIngress
	spec.EgressRules = normalizedEgress
	spec.SubnetAssociations = cleanSubnets
	return spec, nil
}

func normalizeRuleSet(rules []NetworkACLRule, label string) ([]NetworkACLRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	normalized := make([]NetworkACLRule, 0, len(rules))
	seen := make(map[int]struct{}, len(rules))
	for _, rule := range rules {
		rule, err := normalizeAndValidateRule(rule)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if _, ok := seen[rule.RuleNumber]; ok {
			return nil, fmt.Errorf("%s contains duplicate ruleNumber %d", label, rule.RuleNumber)
		}
		seen[rule.RuleNumber] = struct{}{}
		normalized = append(normalized, rule)
	}
	sortRules(normalized)
	return normalized, nil
}

func normalizeAndValidateRule(rule NetworkACLRule) (NetworkACLRule, error) {
	if rule.RuleNumber < 1 || rule.RuleNumber > 32766 {
		return NetworkACLRule{}, fmt.Errorf("ruleNumber %d must be between 1 and 32766", rule.RuleNumber)
	}
	protocol, err := normalizeProtocol(rule.Protocol)
	if err != nil {
		return NetworkACLRule{}, err
	}
	rule.Protocol = protocol
	rule.RuleAction = strings.ToLower(strings.TrimSpace(rule.RuleAction))
	if rule.RuleAction != "allow" && rule.RuleAction != "deny" {
		return NetworkACLRule{}, fmt.Errorf("rule %d has invalid ruleAction %q", rule.RuleNumber, rule.RuleAction)
	}
	rule.CidrBlock = strings.TrimSpace(rule.CidrBlock)
	if rule.CidrBlock == "" {
		return NetworkACLRule{}, fmt.Errorf("rule %d requires cidrBlock", rule.RuleNumber)
	}
	switch protocol {
	case "-1":
		if rule.FromPort != 0 || rule.ToPort != 0 {
			return NetworkACLRule{}, fmt.Errorf("rule %d with protocol -1 must use fromPort=0 and toPort=0", rule.RuleNumber)
		}
	case "1":
		if rule.FromPort < -1 || rule.FromPort > 255 || rule.ToPort < -1 || rule.ToPort > 255 {
			return NetworkACLRule{}, fmt.Errorf("rule %d ICMP type/code must be between -1 and 255", rule.RuleNumber)
		}
	default:
		if rule.FromPort < 0 || rule.FromPort > 65535 || rule.ToPort < 0 || rule.ToPort > 65535 {
			return NetworkACLRule{}, fmt.Errorf("rule %d ports must be between 0 and 65535", rule.RuleNumber)
		}
		if rule.ToPort < rule.FromPort {
			return NetworkACLRule{}, fmt.Errorf("rule %d toPort must be >= fromPort", rule.RuleNumber)
		}
	}
	return rule, nil
}

func specFromObserved(obs ObservedState) NetworkACLSpec {
	return NetworkACLSpec{
		VpcId:              obs.VpcId,
		IngressRules:       append([]NetworkACLRule(nil), obs.IngressRules...),
		EgressRules:        append([]NetworkACLRule(nil), obs.EgressRules...),
		SubnetAssociations: subnetIDsFromAssociations(obs.Associations),
		Tags:               filterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) NetworkACLOutputs {
	ingress := append([]NetworkACLRule(nil), obs.IngressRules...)
	egress := append([]NetworkACLRule(nil), obs.EgressRules...)
	associations := append([]NetworkACLAssociation(nil), obs.Associations...)
	sortRules(ingress)
	sortRules(egress)
	sortAssociations(associations)
	return NetworkACLOutputs{
		NetworkAclId: obs.NetworkAclId,
		VpcId:        obs.VpcId,
		IsDefault:    obs.IsDefault,
		IngressRules: ingress,
		EgressRules:  egress,
		Associations: associations,
	}
}

func subnetIDsFromAssociations(associations []NetworkACLAssociation) []string {
	ids := make([]string, 0, len(associations))
	for _, association := range associations {
		if association.SubnetId != "" {
			ids = append(ids, association.SubnetId)
		}
	}
	sort.Strings(ids)
	return ids
}

func defaultNetworkACLImportMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeObserved
	}
	return m
}

func formatManagedKeyConflict(managedKey, networkAclID string) error {
	return fmt.Errorf("network ACL name %q in this VPC is already managed by Praxis (networkAclId: %s); remove the existing resource or use a different metadata.name", managedKey, networkAclID)
}
