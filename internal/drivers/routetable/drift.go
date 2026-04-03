package routetable

import (
	"fmt"
	"sort"
	"strings"
)

// FieldDiffEntry represents a single field difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift returns true if the desired spec and observed state differ.
//
// Route table drift rules:
//   - Routes are compared by destination CIDR, checking that each desired
//     route exists with the correct target. Routes with Origin=CreateRouteTable
//     (the implicit VPC local route) and EnableVgwRoutePropagation (VPN
//     propagated routes) are excluded from comparison since they are AWS-managed.
//   - Subnet associations are compared as sets (excluding main associations).
//   - Tags are compared (excluding praxis:-prefixed tags).
func HasDrift(desired RouteTableSpec, observed ObservedState) bool {
	if !routesMatch(desired.Routes, observed.Routes) {
		return true
	}
	if !associationsMatch(desired.Associations, observed.Associations) {
		return true
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

// ComputeFieldDiffs returns a human-readable list of differences for drift
// event reporting, including route target changes and association differences.
func ComputeFieldDiffs(desired RouteTableSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.VpcId != observed.VpcId && observed.VpcId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.vpcId (immutable, ignored)",
			OldValue: observed.VpcId,
			NewValue: desired.VpcId,
		})
	}

	diffs = append(diffs, computeRouteDiffs(desired.Routes, observed.Routes)...)
	diffs = append(diffs, computeAssociationDiffs(desired.Associations, observed.Associations)...)
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)

	return diffs
}

func NormalizeRoute(r Route) string {
	return r.DestinationCidrBlock + "|" + normalizeDesiredRouteTarget(r)
}

func filterManagedRoutes(routes []ObservedRoute) []ObservedRoute {
	filtered := make([]ObservedRoute, 0, len(routes))
	for i := range routes {
		route := &routes[i]
		if route.Origin == "CreateRouteTable" || route.Origin == "EnableVgwRoutePropagation" {
			continue
		}
		if strings.TrimSpace(route.DestinationCidrBlock) == "" {
			continue
		}
		filtered = append(filtered, *route)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].DestinationCidrBlock < filtered[j].DestinationCidrBlock
	})
	return filtered
}

func normalizeSpec(spec RouteTableSpec) (RouteTableSpec, error) {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.VpcId = strings.TrimSpace(spec.VpcId)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}

	routes := make([]Route, 0, len(spec.Routes))
	seenDestinations := make(map[string]struct{}, len(spec.Routes))
	for _, route := range spec.Routes {
		normalized, err := normalizeDesiredRoute(route)
		if err != nil {
			return RouteTableSpec{}, err
		}
		if _, exists := seenDestinations[normalized.DestinationCidrBlock]; exists {
			return RouteTableSpec{}, fmt.Errorf("duplicate route destination %q", normalized.DestinationCidrBlock)
		}
		seenDestinations[normalized.DestinationCidrBlock] = struct{}{}
		routes = append(routes, normalized)
	}
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].DestinationCidrBlock < routes[j].DestinationCidrBlock
	})
	spec.Routes = routes

	associations := make([]Association, 0, len(spec.Associations))
	seenSubnets := make(map[string]struct{}, len(spec.Associations))
	for _, association := range spec.Associations {
		subnetID := strings.TrimSpace(association.SubnetId)
		if subnetID == "" {
			return RouteTableSpec{}, fmt.Errorf("association subnetId is required")
		}
		if _, exists := seenSubnets[subnetID]; exists {
			return RouteTableSpec{}, fmt.Errorf("duplicate association subnetId %q", subnetID)
		}
		seenSubnets[subnetID] = struct{}{}
		associations = append(associations, Association{SubnetId: subnetID})
	}
	sort.Slice(associations, func(i, j int) bool {
		return associations[i].SubnetId < associations[j].SubnetId
	})
	spec.Associations = associations

	return spec, nil
}

func normalizeDesiredRoute(route Route) (Route, error) {
	route.DestinationCidrBlock = strings.TrimSpace(route.DestinationCidrBlock)
	route.GatewayId = strings.TrimSpace(route.GatewayId)
	route.NatGatewayId = strings.TrimSpace(route.NatGatewayId)
	route.VpcPeeringConnectionId = strings.TrimSpace(route.VpcPeeringConnectionId)
	route.TransitGatewayId = strings.TrimSpace(route.TransitGatewayId)
	route.NetworkInterfaceId = strings.TrimSpace(route.NetworkInterfaceId)
	route.VpcEndpointId = strings.TrimSpace(route.VpcEndpointId)
	if route.DestinationCidrBlock == "" {
		return Route{}, fmt.Errorf("route destinationCidrBlock is required")
	}
	if countRouteTargets(route) != 1 {
		return Route{}, fmt.Errorf("route %q must specify exactly one target", route.DestinationCidrBlock)
	}
	return route, nil
}

func countRouteTargets(route Route) int {
	count := 0
	for _, candidate := range []string{
		route.GatewayId,
		route.NatGatewayId,
		route.VpcPeeringConnectionId,
		route.TransitGatewayId,
		route.NetworkInterfaceId,
		route.VpcEndpointId,
	} {
		if strings.TrimSpace(candidate) != "" {
			count++
		}
	}
	return count
}

func computeRouteDiffs(desired []Route, observed []ObservedRoute) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredMap := desiredRouteMap(desired)
	observedMap := observedRouteMap(filterManagedRoutes(observed))
	keys := make([]string, 0, len(desiredMap)+len(observedMap))
	seen := make(map[string]struct{}, len(desiredMap)+len(observedMap))
	for destination := range desiredMap {
		keys = append(keys, destination)
		seen[destination] = struct{}{}
	}
	for destination := range observedMap {
		if _, ok := seen[destination]; !ok {
			keys = append(keys, destination)
		}
	}
	sort.Strings(keys)
	for _, destination := range keys {
		desiredRoute, desiredOK := desiredMap[destination]
		observedRoute, observedOK := observedMap[destination]
		path := fmt.Sprintf("spec.routes[%s]", destination)
		switch {
		case desiredOK && !observedOK:
			diffs = append(diffs, FieldDiffEntry{Path: path, OldValue: nil, NewValue: desiredRoute})
		case !desiredOK && observedOK:
			diffs = append(diffs, FieldDiffEntry{Path: path, OldValue: observedRouteToRoute(observedRoute), NewValue: nil})
		case desiredOK && observedOK && !routeTargetsEqual(desiredRoute, observedRoute):
			diffs = append(diffs, FieldDiffEntry{Path: path, OldValue: observedRouteToRoute(observedRoute), NewValue: desiredRoute})
		}
	}
	return diffs
}

func computeAssociationDiffs(desired []Association, observed []ObservedAssociation) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredSet := make(map[string]struct{}, len(desired))
	for _, association := range desired {
		desiredSet[association.SubnetId] = struct{}{}
	}
	observedSet := make(map[string]struct{}, len(observed))
	for _, association := range observed {
		if association.Main || association.SubnetId == "" {
			continue
		}
		observedSet[association.SubnetId] = struct{}{}
	}
	for _, association := range desired {
		if _, ok := observedSet[association.SubnetId]; !ok {
			diffs = append(diffs, FieldDiffEntry{
				Path:     fmt.Sprintf("spec.associations[%s]", association.SubnetId),
				OldValue: nil,
				NewValue: association,
			})
		}
	}
	for _, association := range observed {
		if association.Main || association.SubnetId == "" {
			continue
		}
		if _, ok := desiredSet[association.SubnetId]; !ok {
			diffs = append(diffs, FieldDiffEntry{
				Path:     fmt.Sprintf("spec.associations[%s]", association.SubnetId),
				OldValue: association,
				NewValue: nil,
			})
		}
	}
	return diffs
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredFiltered := filterPraxisTags(desired)
	observedFiltered := filterPraxisTags(observed)
	for key, value := range desiredFiltered {
		if observedValue, ok := observedFiltered[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range observedFiltered {
		if _, ok := desiredFiltered[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

func routesMatch(desired []Route, observed []ObservedRoute) bool {
	desiredMap := desiredRouteMap(desired)
	observedMap := observedRouteMap(filterManagedRoutes(observed))
	if len(desiredMap) != len(observedMap) {
		return false
	}
	for destination, desiredRoute := range desiredMap {
		observedRoute, ok := observedMap[destination]
		if !ok || !routeTargetsEqual(desiredRoute, observedRoute) {
			return false
		}
	}
	return true
}

func associationsMatch(desired []Association, observed []ObservedAssociation) bool {
	desiredSet := make(map[string]struct{}, len(desired))
	for _, association := range desired {
		desiredSet[association.SubnetId] = struct{}{}
	}
	count := 0
	for _, association := range observed {
		if association.Main || association.SubnetId == "" {
			continue
		}
		count++
		if _, ok := desiredSet[association.SubnetId]; !ok {
			return false
		}
	}
	return count == len(desiredSet)
}

func tagsMatch(desired, observed map[string]string) bool {
	desiredFiltered := filterPraxisTags(desired)
	observedFiltered := filterPraxisTags(observed)
	if len(desiredFiltered) != len(observedFiltered) {
		return false
	}
	for key, value := range desiredFiltered {
		if other, ok := observedFiltered[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func filterPraxisTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	filtered := make(map[string]string, len(tags))
	for key, value := range tags {
		if !strings.HasPrefix(key, "praxis:") {
			filtered[key] = value
		}
	}
	return filtered
}

func desiredRouteMap(routes []Route) map[string]Route {
	indexed := make(map[string]Route, len(routes))
	for _, route := range routes {
		indexed[route.DestinationCidrBlock] = route
	}
	return indexed
}

func observedRouteMap(routes []ObservedRoute) map[string]ObservedRoute {
	indexed := make(map[string]ObservedRoute, len(routes))
	for i := range routes {
		indexed[routes[i].DestinationCidrBlock] = routes[i]
	}
	return indexed
}

func routeTargetsEqual(desired Route, observed ObservedRoute) bool {
	return normalizeDesiredRouteTarget(desired) == normalizeObservedRouteTarget(observed)
}

func normalizeDesiredRouteTarget(target Route) string {
	switch {
	case strings.TrimSpace(target.GatewayId) != "":
		return "gateway:" + strings.TrimSpace(target.GatewayId)
	case strings.TrimSpace(target.NatGatewayId) != "":
		return "natGateway:" + strings.TrimSpace(target.NatGatewayId)
	case strings.TrimSpace(target.VpcPeeringConnectionId) != "":
		return "vpcPeering:" + strings.TrimSpace(target.VpcPeeringConnectionId)
	case strings.TrimSpace(target.TransitGatewayId) != "":
		return "transitGateway:" + strings.TrimSpace(target.TransitGatewayId)
	case strings.TrimSpace(target.NetworkInterfaceId) != "":
		return "networkInterface:" + strings.TrimSpace(target.NetworkInterfaceId)
	case strings.TrimSpace(target.VpcEndpointId) != "":
		return "vpcEndpoint:" + strings.TrimSpace(target.VpcEndpointId)
	default:
		return ""
	}
}

func normalizeObservedRouteTarget(target ObservedRoute) string {
	switch {
	case strings.TrimSpace(target.GatewayId) != "":
		return "gateway:" + strings.TrimSpace(target.GatewayId)
	case strings.TrimSpace(target.NatGatewayId) != "":
		return "natGateway:" + strings.TrimSpace(target.NatGatewayId)
	case strings.TrimSpace(target.VpcPeeringConnectionId) != "":
		return "vpcPeering:" + strings.TrimSpace(target.VpcPeeringConnectionId)
	case strings.TrimSpace(target.TransitGatewayId) != "":
		return "transitGateway:" + strings.TrimSpace(target.TransitGatewayId)
	case strings.TrimSpace(target.NetworkInterfaceId) != "":
		return "networkInterface:" + strings.TrimSpace(target.NetworkInterfaceId)
	case strings.TrimSpace(target.VpcEndpointId) != "":
		return "vpcEndpoint:" + strings.TrimSpace(target.VpcEndpointId)
	default:
		return ""
	}
}

func observedRouteToRoute(route ObservedRoute) Route {
	return Route{
		DestinationCidrBlock:   route.DestinationCidrBlock,
		GatewayId:              route.GatewayId,
		NatGatewayId:           route.NatGatewayId,
		VpcPeeringConnectionId: route.VpcPeeringConnectionId,
		TransitGatewayId:       route.TransitGatewayId,
		NetworkInterfaceId:     route.NetworkInterfaceId,
		VpcEndpointId:          route.VpcEndpointId,
	}
}

func specFromObserved(observed ObservedState) RouteTableSpec {
	managedRoutes := filterManagedRoutes(observed.Routes)
	routes := make([]Route, 0, len(managedRoutes))
	for i := range managedRoutes {
		routes = append(routes, observedRouteToRoute(managedRoutes[i]))
	}
	associations := make([]Association, 0, len(observed.Associations))
	for _, association := range observed.Associations {
		if association.Main || association.SubnetId == "" {
			continue
		}
		associations = append(associations, Association{SubnetId: association.SubnetId})
	}
	sort.Slice(associations, func(i, j int) bool {
		return associations[i].SubnetId < associations[j].SubnetId
	})
	return RouteTableSpec{
		VpcId:        observed.VpcId,
		Routes:       routes,
		Associations: associations,
		Tags:         observed.Tags,
	}
}

func outputsFromObserved(observed ObservedState) RouteTableOutputs {
	routes := append([]ObservedRoute(nil), observed.Routes...)
	associations := append([]ObservedAssociation(nil), observed.Associations...)
	return RouteTableOutputs{
		RouteTableId: observed.RouteTableId,
		VpcId:        observed.VpcId,
		OwnerId:      observed.OwnerId,
		Routes:       routes,
		Associations: associations,
	}
}
