package vpcpeering

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// VPCPeeringAPI abstracts the AWS EC2 SDK operations for VPC Peering Connections.
// Peering creation returns immediately, but the connection starts in
// "pending-acceptance" state until the peer accepts. ModifyPeeringOptions
// can only be called once the peering is active.
type VPCPeeringAPI interface {
	CreateVPCPeeringConnection(ctx context.Context, spec VPCPeeringSpec) (string, error)                                   // Creates peering request; returns pcx-xxxx.
	AcceptVPCPeeringConnection(ctx context.Context, peeringID string) error                                                // Accepts a pending peering (same-account only).
	DescribeVPCPeeringConnection(ctx context.Context, peeringID string) (ObservedState, error)                             // Fetches live state including CIDRs and options.
	DeleteVPCPeeringConnection(ctx context.Context, peeringID string) error                                                // Deletes/rejects the peering.
	ModifyPeeringOptions(ctx context.Context, peeringID string, requester *PeeringOptions, accepter *PeeringOptions) error // Sets DNS resolution options.
	UpdateTags(ctx context.Context, peeringID string, tags map[string]string) error                                        // Replaces user tags.
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)                                               // Finds by managed-key; filters terminal statuses.
}

// realVPCPeeringAPI implements VPCPeeringAPI using the actual AWS SDK v2 EC2 client.
type realVPCPeeringAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter // Token-bucket: 20 burst, 10/s refill.
}

// NewVPCPeeringAPI creates a new VPCPeeringAPI backed by the given EC2 SDK client.
func NewVPCPeeringAPI(client *ec2sdk.Client) VPCPeeringAPI {
	return &realVPCPeeringAPI{
		client:  client,
		limiter: ratelimit.New("vpc-peering", 20, 10),
	}
}

func (r *realVPCPeeringAPI) CreateVPCPeeringConnection(ctx context.Context, spec VPCPeeringSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	input := &ec2sdk.CreateVpcPeeringConnectionInput{
		VpcId:     aws.String(spec.RequesterVpcId),
		PeerVpcId: aws.String(spec.AccepterVpcId),
	}
	if spec.PeerOwnerId != "" {
		input.PeerOwnerId = aws.String(spec.PeerOwnerId)
	}
	if spec.PeerRegion != "" {
		input.PeerRegion = aws.String(spec.PeerRegion)
	}

	tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for key, value := range spec.Tags {
		tags = append(tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	input.TagSpecifications = []ec2types.TagSpecification{{
		ResourceType: ec2types.ResourceTypeVpcPeeringConnection,
		Tags:         tags,
	}}

	out, err := r.client.CreateVpcPeeringConnection(ctx, input)
	if err != nil {
		return "", err
	}
	if out.VpcPeeringConnection == nil {
		return "", fmt.Errorf("CreateVpcPeeringConnection returned nil peering connection")
	}
	return aws.ToString(out.VpcPeeringConnection.VpcPeeringConnectionId), nil
}

func (r *realVPCPeeringAPI) AcceptVPCPeeringConnection(ctx context.Context, peeringID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AcceptVpcPeeringConnection(ctx, &ec2sdk.AcceptVpcPeeringConnectionInput{
		VpcPeeringConnectionId: aws.String(peeringID),
	})
	return err
}

func (r *realVPCPeeringAPI) DescribeVPCPeeringConnection(ctx context.Context, peeringID string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeVpcPeeringConnections(ctx, &ec2sdk.DescribeVpcPeeringConnectionsInput{
		VpcPeeringConnectionIds: []string{peeringID},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.VpcPeeringConnections) == 0 {
		return ObservedState{}, awserr.NotFound(fmt.Sprintf("VPC peering connection %s not found", peeringID))
	}

	conn := out.VpcPeeringConnections[0]
	obs := ObservedState{
		VpcPeeringConnectionId: aws.ToString(conn.VpcPeeringConnectionId),
		Tags:                   make(map[string]string, len(conn.Tags)),
	}
	if conn.Status != nil {
		obs.Status = string(conn.Status.Code)
	}
	if conn.RequesterVpcInfo != nil {
		obs.RequesterVpcId = aws.ToString(conn.RequesterVpcInfo.VpcId)
		obs.RequesterOwnerId = aws.ToString(conn.RequesterVpcInfo.OwnerId)
		obs.RequesterCidrBlock = firstCIDR(conn.RequesterVpcInfo)
		obs.RequesterOptions = peeringOptionsFromDescription(conn.RequesterVpcInfo.PeeringOptions)
	}
	if conn.AccepterVpcInfo != nil {
		obs.AccepterVpcId = aws.ToString(conn.AccepterVpcInfo.VpcId)
		obs.AccepterOwnerId = aws.ToString(conn.AccepterVpcInfo.OwnerId)
		obs.AccepterCidrBlock = firstCIDR(conn.AccepterVpcInfo)
		obs.AccepterOptions = peeringOptionsFromDescription(conn.AccepterVpcInfo.PeeringOptions)
	}
	for _, tag := range conn.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return obs, nil
}

func (r *realVPCPeeringAPI) DeleteVPCPeeringConnection(ctx context.Context, peeringID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteVpcPeeringConnection(ctx, &ec2sdk.DeleteVpcPeeringConnectionInput{
		VpcPeeringConnectionId: aws.String(peeringID),
	})
	return err
}

func (r *realVPCPeeringAPI) ModifyPeeringOptions(ctx context.Context, peeringID string, requester *PeeringOptions, accepter *PeeringOptions) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ModifyVpcPeeringConnectionOptions(ctx, &ec2sdk.ModifyVpcPeeringConnectionOptionsInput{
		VpcPeeringConnectionId:            aws.String(peeringID),
		RequesterPeeringConnectionOptions: peeringOptionsRequest(requester),
		AccepterPeeringConnectionOptions:  peeringOptionsRequest(accepter),
	})
	return err
}

func (r *realVPCPeeringAPI) UpdateTags(ctx context.Context, peeringID string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}

	current, err := r.DescribeVPCPeeringConnection(ctx, peeringID)
	if err != nil {
		return err
	}

	var oldTags []ec2types.Tag
	for key := range current.Tags {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		oldTags = append(oldTags, ec2types.Tag{Key: aws.String(key)})
	}
	if len(oldTags) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
			Resources: []string{peeringID},
			Tags:      oldTags,
		})
		if err != nil {
			return err
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
		Resources: []string{peeringID},
		Tags:      ec2Tags,
	})
	return err
}

func (r *realVPCPeeringAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	out, err := r.client.DescribeVpcPeeringConnections(ctx, &ec2sdk.DescribeVpcPeeringConnectionsInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("tag:praxis:managed-key"),
			Values: []string{managedKey},
		}},
	})
	if err != nil {
		return "", err
	}

	seen := make(map[string]struct{})
	var matches []string
	for _, conn := range out.VpcPeeringConnections {
		actualManagedKey := ""
		for _, tag := range conn.Tags {
			if aws.ToString(tag.Key) == "praxis:managed-key" {
				actualManagedKey = aws.ToString(tag.Value)
				break
			}
		}
		if actualManagedKey != managedKey {
			continue
		}
		status := ""
		if conn.Status != nil {
			status = string(conn.Status.Code)
		}
		switch status {
		case "deleted", "rejected", "expired", "failed":
			continue
		}
		if id := aws.ToString(conn.VpcPeeringConnectionId); id != "" {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d VPC peering connections claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

func peeringOptionsRequest(options *PeeringOptions) *ec2types.PeeringConnectionOptionsRequest {
	if options == nil {
		return nil
	}
	return &ec2types.PeeringConnectionOptionsRequest{
		AllowDnsResolutionFromRemoteVpc: aws.Bool(options.AllowDnsResolutionFromRemoteVpc),
	}
}

func peeringOptionsFromDescription(desc *ec2types.VpcPeeringConnectionOptionsDescription) *PeeringOptions {
	if desc == nil {
		return nil
	}
	return &PeeringOptions{
		AllowDnsResolutionFromRemoteVpc: aws.ToBool(desc.AllowDnsResolutionFromRemoteVpc),
	}
}

func firstCIDR(info *ec2types.VpcPeeringConnectionVpcInfo) string {
	if info == nil {
		return ""
	}
	if cidr := aws.ToString(info.CidrBlock); cidr != "" {
		return cidr
	}
	if len(info.CidrBlockSet) > 0 {
		return aws.ToString(info.CidrBlockSet[0].CidrBlock)
	}
	return ""
}

// Error classifiers — used by the driver to decide between retryable
// errors, terminal errors, and idempotent success paths.

// IsNotFound returns true when the peering connection does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidVpcPeeringConnectionID.NotFound", "InvalidVpcPeeringConnectionId.NotFound") || awserr.IsNotFoundErr(err)
}

// IsVpcNotFound returns true when one of the VPCs in the peering request
// does not exist (terminal).
func IsVpcNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidVpcID.NotFound", "InvalidVpcID.Malformed")
}

// IsAlreadyExists returns true when a peering between the same VPC pair
// already exists.
func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "VpcPeeringConnectionAlreadyExists", "InvalidVpcPeeringConnection.Duplicate")
}

// IsCidrOverlap returns true when the two VPCs' CIDR blocks overlap (terminal).
func IsCidrOverlap(err error) bool {
	return awserr.HasCode(err, "OverlappingCidrBlock")
}

// IsPeeringLimitExceeded returns true when the account's peering limit is reached (terminal).
func IsPeeringLimitExceeded(err error) bool {
	return awserr.HasCode(err, "VpcPeeringConnectionLimitExceeded", "VpcLimitExceeded")
}

// IsInvalidParam returns true for invalid parameter errors (terminal).
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination")
}
