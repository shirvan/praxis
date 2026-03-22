package routetable

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/praxiscloud/praxis/internal/infra/ratelimit"
)

type RouteTableAPI interface {
	CreateRouteTable(ctx context.Context, spec RouteTableSpec) (string, error)
	DescribeRouteTable(ctx context.Context, routeTableId string) (ObservedState, error)
	DeleteRouteTable(ctx context.Context, routeTableId string) error
	CreateRoute(ctx context.Context, routeTableId string, route Route) error
	DeleteRoute(ctx context.Context, routeTableId string, destinationCidr string) error
	ReplaceRoute(ctx context.Context, routeTableId string, route Route) error
	AssociateSubnet(ctx context.Context, routeTableId string, subnetId string) (string, error)
	DisassociateSubnet(ctx context.Context, associationId string) error
	UpdateTags(ctx context.Context, routeTableId string, tags map[string]string) error
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

type realRouteTableAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

func NewRouteTableAPI(client *ec2sdk.Client) RouteTableAPI {
	return &realRouteTableAPI{
		client:  client,
		limiter: ratelimit.New("route-table", 20, 10),
	}
}

func (r *realRouteTableAPI) CreateRouteTable(ctx context.Context, spec RouteTableSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for key, value := range spec.Tags {
		tags = append(tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	out, err := r.client.CreateRouteTable(ctx, &ec2sdk.CreateRouteTableInput{
		VpcId: aws.String(spec.VpcId),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeRouteTable,
			Tags:         tags,
		}},
	})
	if err != nil {
		return "", err
	}
	if out.RouteTable == nil {
		return "", fmt.Errorf("CreateRouteTable returned nil route table")
	}
	return aws.ToString(out.RouteTable.RouteTableId), nil
}

func (r *realRouteTableAPI) DescribeRouteTable(ctx context.Context, routeTableId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeRouteTables(ctx, &ec2sdk.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableId},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.RouteTables) == 0 {
		return ObservedState{}, fmt.Errorf("route table %s not found", routeTableId)
	}
	routeTable := out.RouteTables[0]
	observed := ObservedState{
		RouteTableId: aws.ToString(routeTable.RouteTableId),
		VpcId:        aws.ToString(routeTable.VpcId),
		OwnerId:      aws.ToString(routeTable.OwnerId),
		Tags:         make(map[string]string, len(routeTable.Tags)),
	}
	for _, tag := range routeTable.Tags {
		observed.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	for _, route := range routeTable.Routes {
		destination := strings.TrimSpace(aws.ToString(route.DestinationCidrBlock))
		if destination == "" {
			continue
		}
		observed.Routes = append(observed.Routes, ObservedRoute{
			DestinationCidrBlock:   destination,
			GatewayId:              aws.ToString(route.GatewayId),
			NatGatewayId:           aws.ToString(route.NatGatewayId),
			VpcPeeringConnectionId: aws.ToString(route.VpcPeeringConnectionId),
			TransitGatewayId:       aws.ToString(route.TransitGatewayId),
			NetworkInterfaceId:     aws.ToString(route.NetworkInterfaceId),
			State:                  string(route.State),
			Origin:                 string(route.Origin),
		})
	}
	for _, association := range routeTable.Associations {
		observed.Associations = append(observed.Associations, ObservedAssociation{
			AssociationId: aws.ToString(association.RouteTableAssociationId),
			SubnetId:      aws.ToString(association.SubnetId),
			Main:          aws.ToBool(association.Main),
		})
	}
	sort.Slice(observed.Routes, func(i, j int) bool {
		return observed.Routes[i].DestinationCidrBlock < observed.Routes[j].DestinationCidrBlock
	})
	sort.Slice(observed.Associations, func(i, j int) bool {
		if observed.Associations[i].Main != observed.Associations[j].Main {
			return observed.Associations[i].Main && !observed.Associations[j].Main
		}
		return observed.Associations[i].SubnetId < observed.Associations[j].SubnetId
	})
	return observed, nil
}

func (r *realRouteTableAPI) DeleteRouteTable(ctx context.Context, routeTableId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRouteTable(ctx, &ec2sdk.DeleteRouteTableInput{
		RouteTableId: aws.String(routeTableId),
	})
	return err
}

func (r *realRouteTableAPI) CreateRoute(ctx context.Context, routeTableId string, route Route) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &ec2sdk.CreateRouteInput{
		RouteTableId:         aws.String(routeTableId),
		DestinationCidrBlock: aws.String(route.DestinationCidrBlock),
	}
	applyRouteTargetToCreateInput(input, route)
	_, err := r.client.CreateRoute(ctx, input)
	return err
}

func (r *realRouteTableAPI) DeleteRoute(ctx context.Context, routeTableId string, destinationCidr string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRoute(ctx, &ec2sdk.DeleteRouteInput{
		RouteTableId:         aws.String(routeTableId),
		DestinationCidrBlock: aws.String(destinationCidr),
	})
	return err
}

func (r *realRouteTableAPI) ReplaceRoute(ctx context.Context, routeTableId string, route Route) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &ec2sdk.ReplaceRouteInput{
		RouteTableId:         aws.String(routeTableId),
		DestinationCidrBlock: aws.String(route.DestinationCidrBlock),
	}
	applyRouteTargetToReplaceInput(input, route)
	_, err := r.client.ReplaceRoute(ctx, input)
	return err
}

func (r *realRouteTableAPI) AssociateSubnet(ctx context.Context, routeTableId string, subnetId string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.AssociateRouteTable(ctx, &ec2sdk.AssociateRouteTableInput{
		RouteTableId: aws.String(routeTableId),
		SubnetId:     aws.String(subnetId),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.AssociationId), nil
}

func (r *realRouteTableAPI) DisassociateSubnet(ctx context.Context, associationId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DisassociateRouteTable(ctx, &ec2sdk.DisassociateRouteTableInput{
		AssociationId: aws.String(associationId),
	})
	return err
}

func (r *realRouteTableAPI) UpdateTags(ctx context.Context, routeTableId string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.DescribeRouteTables(ctx, &ec2sdk.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableId},
	})
	if err != nil {
		return err
	}
	if len(out.RouteTables) > 0 {
		var oldTags []ec2types.Tag
		for _, tag := range out.RouteTables[0].Tags {
			key := aws.ToString(tag.Key)
			if strings.HasPrefix(key, "praxis:") {
				continue
			}
			oldTags = append(oldTags, ec2types.Tag{Key: tag.Key})
		}
		if len(oldTags) > 0 {
			if err := r.limiter.Wait(ctx); err != nil {
				return err
			}
			_, err = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
				Resources: []string{routeTableId},
				Tags:      oldTags,
			})
			if err != nil {
				return err
			}
		}
	}
	var ec2Tags []ec2types.Tag
	for key, value := range tags {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	if len(ec2Tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{
		Resources: []string{routeTableId},
		Tags:      ec2Tags,
	})
	return err
}

func (r *realRouteTableAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.DescribeRouteTables(ctx, &ec2sdk.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("tag:praxis:managed-key"),
			Values: []string{managedKey},
		}},
	})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, routeTable := range out.RouteTables {
		if id := aws.ToString(routeTable.RouteTableId); id != "" {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d route tables claim managed-key %q: %v", len(matches), managedKey, matches)
	}
}

func applyRouteTargetToCreateInput(input *ec2sdk.CreateRouteInput, route Route) {
	switch {
	case route.GatewayId != "":
		input.GatewayId = aws.String(route.GatewayId)
	case route.NatGatewayId != "":
		input.NatGatewayId = aws.String(route.NatGatewayId)
	case route.VpcPeeringConnectionId != "":
		input.VpcPeeringConnectionId = aws.String(route.VpcPeeringConnectionId)
	case route.TransitGatewayId != "":
		input.TransitGatewayId = aws.String(route.TransitGatewayId)
	case route.NetworkInterfaceId != "":
		input.NetworkInterfaceId = aws.String(route.NetworkInterfaceId)
	case route.VpcEndpointId != "":
		input.VpcEndpointId = aws.String(route.VpcEndpointId)
	}
}

func applyRouteTargetToReplaceInput(input *ec2sdk.ReplaceRouteInput, route Route) {
	switch {
	case route.GatewayId != "":
		input.GatewayId = aws.String(route.GatewayId)
	case route.NatGatewayId != "":
		input.NatGatewayId = aws.String(route.NatGatewayId)
	case route.VpcPeeringConnectionId != "":
		input.VpcPeeringConnectionId = aws.String(route.VpcPeeringConnectionId)
	case route.TransitGatewayId != "":
		input.TransitGatewayId = aws.String(route.TransitGatewayId)
	case route.NetworkInterfaceId != "":
		input.NetworkInterfaceId = aws.String(route.NetworkInterfaceId)
	case route.VpcEndpointId != "":
		input.VpcEndpointId = aws.String(route.VpcEndpointId)
	}
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidRouteTableID.NotFound"
	}
	return strings.Contains(err.Error(), "InvalidRouteTableID.NotFound")
}

func IsRouteNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidRoute.NotFound"
	}
	errText := err.Error()
	return strings.Contains(errText, "InvalidRoute.NotFound") || strings.Contains(errText, "no route with destination-cidr-block")
}

func IsAssociationNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidAssociationID.NotFound"
	}
	return strings.Contains(err.Error(), "InvalidAssociationID.NotFound")
}

func IsRouteAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "RouteAlreadyExists"
	}
	return strings.Contains(err.Error(), "RouteAlreadyExists")
}

func IsMainRouteTable(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "main route table") || strings.Contains(errText, "cannot delete main route table")
}

func IsInvalidParam(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidParameterValue", "InvalidParameterCombination", "MissingParameter", "RouteTableLimitExceeded":
			return true
		}
	}
	return false
}

func IsInvalidRoute(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidRoute.InvalidState", "InvalidRoute.Malformed", "InvalidGatewayID.NotFound", "InvalidNatGatewayID.NotFound", "InvalidTransitGatewayID.NotFound", "InvalidVpcPeeringConnectionID.NotFound", "InvalidNetworkInterfaceID.NotFound", "InvalidVpcEndpointId.NotFound":
			return true
		}
	}
	errText := err.Error()
	return strings.Contains(errText, "InvalidRoute.") || strings.Contains(errText, "invalid route")
}

func IsDependencyViolation(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "DependencyViolation"
	}
	return strings.Contains(err.Error(), "DependencyViolation")
}
