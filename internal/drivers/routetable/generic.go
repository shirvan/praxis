package routetable

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

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) RouteTableAPI
}

const routeTableManagedKeyTag = "praxis:managed-key"

// NewGenericRouteTableDriver returns the EC2 route-table lifecycle
// implementation backed by the shared generic kernel.
func NewGenericRouteTableDriver(auth authservice.AuthClient) *kernel.Driver[RouteTableSpec, RouteTableOutputs, ObservedState] {
	return newGenericRouteTableDriverWithFactory(auth, nil)
}

func newGenericRouteTableDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RouteTableAPI) *kernel.Driver[RouteTableSpec, RouteTableOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) RouteTableAPI {
			return NewRouteTableAPI(awsclient.NewEC2Client(cfg))
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[RouteTableSpec, RouteTableOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec RouteTableSpec) (RouteTableSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return RouteTableSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Region = region
			normalized, err := normalizeSpec(spec)
			if err != nil {
				return RouteTableSpec{}, restate.TerminalError(err, 400)
			}
			// Internal ownership metadata is derived only from the Restate object
			// key; user tags cannot override or duplicate it.
			normalized.Tags = drivers.FilterPraxisTags(normalized.Tags)
			return normalized, nil
		},
		Validate: func(spec RouteTableSpec) error {
			if spec.Region == "" {
				return fmt.Errorf("region is required")
			}
			if spec.VpcId == "" {
				return fmt.Errorf("vpcId is required")
			}
			return nil
		},
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) RouteTableSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ RouteTableOutputs) RouteTableOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired RouteTableSpec, outputs RouteTableOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	id := strings.TrimSpace(outputs.RouteTableId)
	recoveredByManagedKey := false
	if id == "" && desired.ManagedKey != "" {
		id, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyRouteTableFind)
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		recoveredByManagedKey = id != ""
	}
	if id == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	observation, err := observeRouteTable(ctx, api, id)
	if err != nil || !observation.Exists {
		return observation, err
	}
	currentOwner := strings.TrimSpace(observation.Value.Tags[routeTableManagedKeyTag])
	if currentOwner != "" && currentOwner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"route table %s is owned by Praxis object %q, not %q", id, currentOwner, desired.ManagedKey,
		), 409)
	}
	if recoveredByManagedKey && currentOwner != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
			"refusing to adopt route table %s without exact Praxis ownership tag %q", id, desired.ManagedKey,
		), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired RouteTableSpec) (kernel.CreateResult[RouteTableOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[RouteTableOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	id, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// CreateRouteTable has no client token. Recheck the atomically-applied
		// ownership tag inside the durable callback so a retry after an
		// ambiguous response adopts the first table instead of duplicating it.
		if desired.ManagedKey != "" {
			existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil || existing != "" {
				return existing, findErr
			}
		}
		return api.CreateRouteTable(rc, desired)
	}, classifyRouteTableCreate)
	return kernel.CreateResult[RouteTableOutputs]{SeedOutputs: RouteTableOutputs{RouteTableId: id, VpcId: desired.VpcId}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired RouteTableSpec, observed ObservedState) error {
	if desired.VpcId != observed.VpcId {
		return restate.TerminalError(fmt.Errorf(
			"vpcId is immutable; delete and reprovision the route table to move it from %s to %s",
			observed.VpcId, desired.VpcId,
		), 409)
	}
	if currentOwner := strings.TrimSpace(observed.Tags[routeTableManagedKeyTag]); currentOwner != "" && currentOwner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf(
			"route table %s is owned by Praxis object %q, not %q", observed.RouteTableId, currentOwner, desired.ManagedKey,
		), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	return convergeRouteTable(ctx, api, observed.RouteTableId, desired, observed)
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired RouteTableSpec, outputs RouteTableOutputs) error {
	id := outputs.RouteTableId
	if id == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observation, err := observeRouteTable(ctx, api, id)
	if err != nil || !observation.Exists {
		return err
	}
	observed := observation.Value
	if currentOwner := strings.TrimSpace(observed.Tags[routeTableManagedKeyTag]); currentOwner != "" && currentOwner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf(
			"refusing to delete route table %s owned by Praxis object %q, not %q", id, currentOwner, desired.ManagedKey,
		), 409)
	}
	if containsMainAssociation(observed.Associations) {
		return restate.TerminalError(fmt.Errorf("cannot delete route table %s: it is the main route table for its VPC", id), 409)
	}
	for _, association := range observed.Associations {
		if association.Main || association.AssociationId == "" {
			continue
		}
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			disassociateErr := api.DisassociateSubnet(rc, association.AssociationId)
			if IsAssociationNotFound(disassociateErr) {
				disassociateErr = nil
			}
			return restate.Void{}, disassociateErr
		}, classifyRouteTableMutation); err != nil {
			return err
		}
	}
	managedRoutes := filterManagedRoutes(observed.Routes)
	for i := range managedRoutes {
		route := &managedRoutes[i]
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			deleteErr := api.DeleteRoute(rc, id, route.DestinationCidrBlock)
			if IsRouteNotFound(deleteErr) {
				deleteErr = nil
			}
			return restate.Void{}, deleteErr
		}, classifyRouteTableMutation); err != nil {
			return err
		}
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteRouteTable(rc, id)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyRouteTableMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeRouteTable(ctx, api, ref.ResourceID)
}

func observeRouteTable(ctx restate.ObjectContext, api RouteTableAPI, id string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeRouteTable(rc, id)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyRouteTableObserve)
}

func convergeRouteTable(ctx restate.ObjectContext, api RouteTableAPI, id string, desired RouteTableSpec, observed ObservedState) error {
	desiredRoutes := desiredRouteMap(desired.Routes)
	observedRoutes := observedRouteMap(filterManagedRoutes(observed.Routes))
	toAdd := make([]Route, 0)
	toReplace := make([]Route, 0)
	toRemove := make([]ObservedRoute, 0)
	for destination, route := range desiredRoutes {
		current, ok := observedRoutes[destination]
		if !ok {
			toAdd = append(toAdd, route)
		} else if !routeTargetsEqual(route, current) {
			toReplace = append(toReplace, route)
		}
	}
	for destination := range observedRoutes {
		if _, ok := desiredRoutes[destination]; !ok {
			toRemove = append(toRemove, observedRoutes[destination])
		}
	}
	sort.Slice(toAdd, func(i, j int) bool { return toAdd[i].DestinationCidrBlock < toAdd[j].DestinationCidrBlock })
	sort.Slice(toReplace, func(i, j int) bool { return toReplace[i].DestinationCidrBlock < toReplace[j].DestinationCidrBlock })
	sort.Slice(toRemove, func(i, j int) bool { return toRemove[i].DestinationCidrBlock < toRemove[j].DestinationCidrBlock })

	for _, route := range toAdd {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			createErr := api.CreateRoute(rc, id, route)
			if IsRouteAlreadyExists(createErr) {
				createErr = api.ReplaceRoute(rc, id, route)
			}
			return restate.Void{}, createErr
		}, classifyRouteTableMutation); err != nil {
			return err
		}
	}
	for _, route := range toReplace {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			replaceErr := api.ReplaceRoute(rc, id, route)
			if IsRouteNotFound(replaceErr) {
				replaceErr = api.CreateRoute(rc, id, route)
			}
			return restate.Void{}, replaceErr
		}, classifyRouteTableMutation); err != nil {
			return err
		}
	}
	for i := range toRemove {
		route := &toRemove[i]
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			deleteErr := api.DeleteRoute(rc, id, route.DestinationCidrBlock)
			if IsRouteNotFound(deleteErr) {
				deleteErr = nil
			}
			return restate.Void{}, deleteErr
		}, classifyRouteTableMutation); err != nil {
			return err
		}
	}

	desiredAssociations := make(map[string]struct{}, len(desired.Associations))
	for _, association := range desired.Associations {
		desiredAssociations[association.SubnetId] = struct{}{}
	}
	observedAssociations := make(map[string]ObservedAssociation, len(observed.Associations))
	for _, association := range observed.Associations {
		if !association.Main && association.SubnetId != "" {
			observedAssociations[association.SubnetId] = association
		}
	}
	toAssociate := make([]string, 0)
	toDisassociate := make([]ObservedAssociation, 0)
	for _, association := range desired.Associations {
		if _, ok := observedAssociations[association.SubnetId]; !ok {
			toAssociate = append(toAssociate, association.SubnetId)
		}
	}
	for subnetID, association := range observedAssociations {
		if _, ok := desiredAssociations[subnetID]; !ok {
			toDisassociate = append(toDisassociate, association)
		}
	}
	sort.Strings(toAssociate)
	sort.Slice(toDisassociate, func(i, j int) bool { return toDisassociate[i].SubnetId < toDisassociate[j].SubnetId })
	for _, subnetID := range toAssociate {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.AssociateSubnet(rc, id, subnetID)
		}, classifyRouteTableMutation); err != nil {
			return err
		}
	}
	for _, association := range toDisassociate {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			disassociateErr := api.DisassociateSubnet(rc, association.AssociationId)
			if IsAssociationNotFound(disassociateErr) {
				disassociateErr = nil
			}
			return restate.Void{}, disassociateErr
		}, classifyRouteTableMutation); err != nil {
			return err
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, id, desired.Tags)
		}, classifyRouteTableMutation)
		return err
	}
	return nil
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (RouteTableAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("RouteTable driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RouteTable account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyRouteTableObserve(err error) error {
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

func classifyRouteTableFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 409)
	}
	return classifyRouteTableObserve(err)
}

func classifyRouteTableCreate(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 409)
	}
	return classifyRouteTableMutation(err)
}

func classifyRouteTableMutation(err error) error {
	if err == nil {
		return nil
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParam(err) || IsInvalidRoute(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsMainRouteTable(err) || IsDependencyViolation(err) {
		return restate.TerminalError(err, 409)
	}
	return err
}

func containsMainAssociation(associations []ObservedAssociation) bool {
	for _, association := range associations {
		if association.Main {
			return true
		}
	}
	return false
}
