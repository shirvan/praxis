package routetable

import (
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// RouteTableDriver is a Restate Virtual Object that manages EC2 Route Table lifecycle.
type RouteTableDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) RouteTableAPI
}

// NewRouteTableDriver creates a production RouteTableDriver.
func NewRouteTableDriver(auth authservice.AuthClient) *RouteTableDriver {
	return NewRouteTableDriverWithFactory(auth, func(cfg aws.Config) RouteTableAPI {
		return NewRouteTableAPI(awsclient.NewEC2Client(cfg))
	})
}

// NewRouteTableDriverWithFactory allows tests to inject a custom RouteTableAPI factory.
func NewRouteTableDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RouteTableAPI) *RouteTableDriver {
	if factory == nil {
		factory = func(cfg aws.Config) RouteTableAPI {
			return NewRouteTableAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &RouteTableDriver{auth: auth, apiFactory: factory}
}

func (d *RouteTableDriver) ServiceName() string {
	return ServiceName
}

// Provision implements idempotent create-or-converge for a Route Table.
//
// Flow: normalize spec (validate routes, dedup) → load state → ownership check →
// create if missing → apply desired state (routes + associations + tags) →
// describe final state → commit state → schedule reconcile.
//
// Route convergence: add (with fallback to replace if already exists) →
// replace changed routes (with fallback to create if not found) →
// delete removed routes (ignoring not-found).
func (d *RouteTableDriver) Provision(ctx restate.ObjectContext, spec RouteTableSpec) (RouteTableOutputs, error) {
	ctx.Log().Info("provisioning route table", "key", restate.Key(ctx))
	if spec.ManagedKey == "" {
		spec.ManagedKey = restate.Key(ctx)
	}
	normalizedSpec, err := normalizeSpec(spec)
	if err != nil {
		return RouteTableOutputs{}, restate.TerminalError(err, 400)
	}
	spec = normalizedSpec
	if spec.Region == "" {
		return RouteTableOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	if spec.VpcId == "" {
		return RouteTableOutputs{}, restate.TerminalError(fmt.Errorf("vpcId is required"), 400)
	}

	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return RouteTableOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[RouteTableState](ctx, drivers.StateKey)
	if err != nil {
		return RouteTableOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	routeTableID := state.Outputs.RouteTableId
	currentObserved := state.Observed
	if routeTableID != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeRouteTable(rc, routeTableID)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil {
			routeTableID = ""
			currentObserved = ObservedState{}
		} else {
			currentObserved = described
		}
	}

	if routeTableID == "" && spec.ManagedKey != "" {
		conflictID, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, spec.ManagedKey)
		})
		if conflictErr != nil {
			state.Status = types.StatusError
			state.Error = conflictErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return RouteTableOutputs{}, conflictErr
		}
		if conflictID != "" {
			err := fmt.Errorf("route table %q in VPC %s is already managed by Praxis (routeTableId: %s); remove the existing resource or use a different metadata.name", spec.ManagedKey, spec.VpcId, conflictID)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return RouteTableOutputs{}, restate.TerminalError(err, 409)
		}
	}

	if routeTableID == "" {
		createdID, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateRouteTable(rc, spec)
			if runErr != nil {
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
			return RouteTableOutputs{}, createErr
		}
		routeTableID = createdID
		currentObserved = ObservedState{}
	}

	if err := d.applyDesiredState(ctx, api, routeTableID, spec, currentObserved); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = RouteTableOutputs{RouteTableId: routeTableID, VpcId: spec.VpcId}
		state.Observed = currentObserved
		restate.Set(ctx, drivers.StateKey, state)
		return RouteTableOutputs{}, err
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRouteTable(rc, routeTableID)
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
		state.Outputs = RouteTableOutputs{RouteTableId: routeTableID, VpcId: spec.VpcId}
		restate.Set(ctx, drivers.StateKey, state)
		return RouteTableOutputs{}, err
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

// Import captures an existing Route Table's live state as the baseline.
func (d *RouteTableDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (RouteTableOutputs, error) {
	ctx.Log().Info("importing route table", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return RouteTableOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[RouteTableState](ctx, drivers.StateKey)
	if err != nil {
		return RouteTableOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRouteTable(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: route table %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return RouteTableOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	outputs := outputsFromObserved(observed)

	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = defaultRouteTableImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes the Route Table. Flow: check for main association (cannot
// delete) → disassociate all subnets → delete managed routes → delete table.
func (d *RouteTableDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting route table", "key", restate.Key(ctx))
	state, err := restate.Get[RouteTableState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete route table %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.RouteTableId), 409)
	}

	routeTableID := state.Outputs.RouteTableId
	if routeTableID == "" {
		restate.Set(ctx, drivers.StateKey, RouteTableState{Status: types.StatusDeleted})
		return nil
	}

	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRouteTable(rc, routeTableID)
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
	if observed.RouteTableId == "" {
		restate.Set(ctx, drivers.StateKey, RouteTableState{Status: types.StatusDeleted})
		return nil
	}
	if hasMainAssociation(observed.Associations) {
		err := fmt.Errorf("cannot delete route table %s: it is the main route table for its VPC", routeTableID)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return restate.TerminalError(err, 409)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	for _, association := range observed.Associations {
		if association.Main || association.AssociationId == "" {
			continue
		}
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DisassociateSubnet(rc, association.AssociationId); runErr != nil {
				if IsAssociationNotFound(runErr) {
					return restate.Void{}, nil
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("disassociate subnet %s: %v", association.SubnetId, err)
			restate.Set(ctx, drivers.StateKey, state)
			return err
		}
	}

	routes := filterManagedRoutes(observed.Routes)
	for i := range routes {

		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DeleteRoute(rc, routeTableID, routes[i].DestinationCidrBlock); runErr != nil {
				if IsRouteNotFound(runErr) {
					return restate.Void{}, nil
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("delete route %s: %v", routes[i].DestinationCidrBlock, err)
			restate.Set(ctx, drivers.StateKey, state)
			return err
		}
	}

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if runErr := api.DeleteRouteTable(rc, routeTableID); runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsMainRouteTable(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("cannot delete route table %s: it is the main route table for its VPC", routeTableID), 409)
			}
			if IsDependencyViolation(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("cannot delete route table %s: subnets or other dependencies still reference it", routeTableID), 409)
			}
			if IsInvalidParam(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
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

	restate.Set(ctx, drivers.StateKey, RouteTableState{Status: types.StatusDeleted})
	return nil
}

// Reconcile checks actual state against desired and corrects drift (Managed)
// or reports it (Observed). Routes with Origin=CreateRouteTable are excluded.
func (d *RouteTableDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[RouteTableState](ctx, drivers.StateKey)
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
	if state.Outputs.RouteTableId == "" {
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
		obs, runErr := api.DescribeRouteTable(rc, state.Outputs.RouteTableId)
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
		state.Error = fmt.Sprintf("route table %s was deleted externally", state.Outputs.RouteTableId)
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
		ctx.Log().Info("drift detected, correcting route table", "routeTableId", state.Outputs.RouteTableId)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.applyDesiredState(ctx, api, state.Outputs.RouteTableId, state.Desired, observed); correctionErr != nil {
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
		ctx.Log().Info("drift detected (observed mode, not correcting)", "routeTableId", state.Outputs.RouteTableId)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *RouteTableDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[RouteTableState](ctx, drivers.StateKey)
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

func (d *RouteTableDriver) GetOutputs(ctx restate.ObjectSharedContext) (RouteTableOutputs, error) {
	state, err := restate.Get[RouteTableState](ctx, drivers.StateKey)
	if err != nil {
		return RouteTableOutputs{}, err
	}
	return state.Outputs, nil
}

// scheduleReconcile sends a delayed self-invocation after ReconcileInterval.
func (d *RouteTableDriver) scheduleReconcile(ctx restate.ObjectContext, state *RouteTableState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

// applyDesiredState converges routes, associations, and tags to match the
// desired spec. Route errors use fallback patterns: CreateRoute catches
// RouteAlreadyExists and falls back to ReplaceRoute; ReplaceRoute catches
// RouteNotFound and falls back to CreateRoute; DeleteRoute ignores not-found.
func (d *RouteTableDriver) applyDesiredState(ctx restate.ObjectContext, api RouteTableAPI, routeTableID string, desired RouteTableSpec, observed ObservedState) error {
	observedManagedRoutes := filterManagedRoutes(observed.Routes)
	desiredRoutes := desiredRouteMap(desired.Routes)
	observedRoutes := observedRouteMap(observedManagedRoutes)

	var toAdd []Route
	var toReplace []Route
	var toRemove []ObservedRoute
	for destination, route := range desiredRoutes {
		observedRoute, ok := observedRoutes[destination]
		if !ok {
			toAdd = append(toAdd, route)
			continue
		}
		if !routeTargetsEqual(route, observedRoute) {
			toReplace = append(toReplace, route)
		}
	}
	for destination := range observedRoutes {
		if _, ok := desiredRoutes[destination]; !ok {
			toRemove = append(toRemove, observedRoutes[destination])
		}
	}
	sort.Slice(toAdd, func(i, j int) bool {
		return toAdd[i].DestinationCidrBlock < toAdd[j].DestinationCidrBlock
	})
	sort.Slice(toReplace, func(i, j int) bool {
		return toReplace[i].DestinationCidrBlock < toReplace[j].DestinationCidrBlock
	})
	sort.Slice(toRemove, func(i, j int) bool {
		return toRemove[i].DestinationCidrBlock < toRemove[j].DestinationCidrBlock
	})

	for _, route := range toAdd {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.CreateRoute(rc, routeTableID, route); runErr != nil {
				if IsRouteAlreadyExists(runErr) {
					if replaceErr := api.ReplaceRoute(rc, routeTableID, route); replaceErr != nil {
						if IsInvalidRoute(replaceErr) || IsInvalidParam(replaceErr) {
							return restate.Void{}, restate.TerminalError(replaceErr, 400)
						}
						return restate.Void{}, replaceErr
					}
					return restate.Void{}, nil
				}
				if IsInvalidRoute(runErr) || IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("create route %s: %w", route.DestinationCidrBlock, err)
		}
	}

	for _, route := range toReplace {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.ReplaceRoute(rc, routeTableID, route); runErr != nil {
				if IsRouteNotFound(runErr) {
					if createErr := api.CreateRoute(rc, routeTableID, route); createErr != nil {
						if IsInvalidRoute(createErr) || IsInvalidParam(createErr) {
							return restate.Void{}, restate.TerminalError(createErr, 400)
						}
						return restate.Void{}, createErr
					}
					return restate.Void{}, nil
				}
				if IsInvalidRoute(runErr) || IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("replace route %s: %w", route.DestinationCidrBlock, err)
		}
	}

	for i := range toRemove {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DeleteRoute(rc, routeTableID, toRemove[i].DestinationCidrBlock); runErr != nil {
				if IsRouteNotFound(runErr) {
					return restate.Void{}, nil
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("delete route %s: %w", toRemove[i].DestinationCidrBlock, err)
		}
	}

	desiredAssociations := make(map[string]struct{}, len(desired.Associations))
	for _, association := range desired.Associations {
		desiredAssociations[association.SubnetId] = struct{}{}
	}
	observedAssociations := make(map[string]ObservedAssociation, len(observed.Associations))
	for _, association := range observed.Associations {
		if association.Main || association.SubnetId == "" {
			continue
		}
		observedAssociations[association.SubnetId] = association
	}
	var toAssociate []string
	for _, association := range desired.Associations {
		if _, ok := observedAssociations[association.SubnetId]; !ok {
			toAssociate = append(toAssociate, association.SubnetId)
		}
	}
	var toDisassociate []ObservedAssociation
	for subnetID, association := range observedAssociations {
		if _, ok := desiredAssociations[subnetID]; !ok {
			toDisassociate = append(toDisassociate, association)
		}
	}
	sort.Strings(toAssociate)
	sort.Slice(toDisassociate, func(i, j int) bool {
		return toDisassociate[i].SubnetId < toDisassociate[j].SubnetId
	})

	for _, subnetID := range toAssociate {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			associationID, runErr := api.AssociateSubnet(rc, routeTableID, subnetID)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				return "", runErr
			}
			return associationID, nil
		})
		if err != nil {
			return fmt.Errorf("associate subnet %s: %w", subnetID, err)
		}
	}

	for _, association := range toDisassociate {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DisassociateSubnet(rc, association.AssociationId); runErr != nil {
				if IsAssociationNotFound(runErr) {
					return restate.Void{}, nil
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("disassociate subnet %s: %w", association.SubnetId, err)
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.UpdateTags(rc, routeTableID, desired.Tags); runErr != nil {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}

	return nil
}

func hasMainAssociation(associations []ObservedAssociation) bool {
	for _, association := range associations {
		if association.Main {
			return true
		}
	}
	return false
}

func defaultRouteTableImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func (d *RouteTableDriver) apiForAccount(ctx restate.ObjectContext, account string) (RouteTableAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("RouteTableDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RouteTable account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}
