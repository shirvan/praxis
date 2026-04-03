package natgw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// NATGatewayAPI abstracts the AWS EC2 SDK operations for NAT Gateways.
// Creation and deletion are asynchronous; WaitUntilAvailable and
// WaitUntilDeleted poll with 10-minute timeouts. DescribeNATGateway
// treats the "deleted" state as not-found to simplify driver logic.
type NATGatewayAPI interface {
	CreateNATGateway(ctx context.Context, spec NATGatewaySpec) (string, error)          // Creates a NAT GW; returns nat-xxxx.
	DescribeNATGateway(ctx context.Context, natGatewayId string) (ObservedState, error) // Fetches live state; "deleted" → not-found.
	DeleteNATGateway(ctx context.Context, natGatewayId string) error                    // Initiates async delete.
	WaitUntilAvailable(ctx context.Context, natGatewayId string) error                  // Polls until state=available (10min timeout).
	WaitUntilDeleted(ctx context.Context, natGatewayId string) error                    // Polls until state=deleted (10min timeout).
	UpdateTags(ctx context.Context, natGatewayId string, tags map[string]string) error  // Replaces user tags.
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)            // Finds by managed-key; filters terminal states.
}

// realNATGatewayAPI implements NATGatewayAPI using the actual AWS SDK v2 EC2 client.
type realNATGatewayAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter // Token-bucket: 20 burst, 10/s refill.
}

// NewNATGatewayAPI creates a new NATGatewayAPI backed by the given EC2 SDK client.
func NewNATGatewayAPI(client *ec2sdk.Client) NATGatewayAPI {
	return &realNATGatewayAPI{
		client:  client,
		limiter: ratelimit.New("nat-gateway", 20, 10),
	}
}

func (r *realNATGatewayAPI) CreateNATGateway(ctx context.Context, spec NATGatewaySpec) (string, error) {
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

	input := &ec2sdk.CreateNatGatewayInput{
		SubnetId:         aws.String(spec.SubnetId),
		ConnectivityType: ec2types.ConnectivityType(spec.ConnectivityType),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeNatgateway,
			Tags:         tags,
		}},
	}
	if spec.AllocationId != "" {
		input.AllocationId = aws.String(spec.AllocationId)
	}

	out, err := r.client.CreateNatGateway(ctx, input)
	if err != nil {
		return "", err
	}
	if out.NatGateway == nil {
		return "", fmt.Errorf("CreateNatGateway returned nil NAT gateway")
	}
	return aws.ToString(out.NatGateway.NatGatewayId), nil
}

func (r *realNATGatewayAPI) DescribeNATGateway(ctx context.Context, natGatewayId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeNatGateways(ctx, &ec2sdk.DescribeNatGatewaysInput{
		NatGatewayIds: []string{natGatewayId},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.NatGateways) == 0 {
		return ObservedState{}, fmt.Errorf("nat gateway %s not found", natGatewayId)
	}

	gw := out.NatGateways[0]
	if gw.State == ec2types.NatGatewayStateDeleted {
		return ObservedState{}, &notFoundError{id: natGatewayId}
	}
	return observedStateFromNATGateway(gw), nil
}

func (r *realNATGatewayAPI) DeleteNATGateway(ctx context.Context, natGatewayId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteNatGateway(ctx, &ec2sdk.DeleteNatGatewayInput{
		NatGatewayId: aws.String(natGatewayId),
	})
	return err
}

func (r *realNATGatewayAPI) WaitUntilAvailable(ctx context.Context, natGatewayId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	waiter := ec2sdk.NewNatGatewayAvailableWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeNatGatewaysInput{NatGatewayIds: []string{natGatewayId}}, 10*time.Minute)
}

func (r *realNATGatewayAPI) WaitUntilDeleted(ctx context.Context, natGatewayId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	waiter := ec2sdk.NewNatGatewayDeletedWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeNatGatewaysInput{NatGatewayIds: []string{natGatewayId}}, 10*time.Minute)
}

func (r *realNATGatewayAPI) UpdateTags(ctx context.Context, natGatewayId string, tags map[string]string) error {
	observed, err := r.DescribeNATGateway(ctx, natGatewayId)
	if err != nil {
		return err
	}

	var stale []ec2types.Tag
	for key := range filterPraxisTags(observed.Tags) {
		stale = append(stale, ec2types.Tag{Key: aws.String(key)})
	}
	if len(stale) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, err := r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{Resources: []string{natGatewayId}, Tags: stale}); err != nil {
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
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{Resources: []string{natGatewayId}, Tags: ec2Tags})
	return err
}

func (r *realNATGatewayAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	out, err := r.client.DescribeNatGateways(ctx, &ec2sdk.DescribeNatGatewaysInput{
		Filter: []ec2types.Filter{{
			Name:   aws.String("tag:praxis:managed-key"),
			Values: []string{managedKey},
		}},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for i := range out.NatGateways {
		if managedKeyTagValue(out.NatGateways[i].Tags) != managedKey {
			continue
		}
		state := string(out.NatGateways[i].State)
		if state != string(ec2types.NatGatewayStatePending) && state != string(ec2types.NatGatewayStateAvailable) {
			continue
		}
		if id := aws.ToString(out.NatGateways[i].NatGatewayId); id != "" {
			matches = append(matches, id)
		}
	}

	return singleManagedKeyMatch(managedKey, matches)
}

func managedKeyTagValue(tags []ec2types.Tag) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == "praxis:managed-key" {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

func observedStateFromNATGateway(gw ec2types.NatGateway) ObservedState {
	obs := ObservedState{
		NatGatewayId:     aws.ToString(gw.NatGatewayId),
		SubnetId:         aws.ToString(gw.SubnetId),
		VpcId:            aws.ToString(gw.VpcId),
		ConnectivityType: string(gw.ConnectivityType),
		State:            string(gw.State),
		FailureCode:      aws.ToString(gw.FailureCode),
		FailureMessage:   aws.ToString(gw.FailureMessage),
		Tags:             make(map[string]string, len(gw.Tags)),
	}
	for _, tag := range gw.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	addr := selectPrimaryAddress(gw.NatGatewayAddresses)
	obs.AllocationId = aws.ToString(addr.AllocationId)
	obs.PublicIp = aws.ToString(addr.PublicIp)
	obs.PrivateIp = aws.ToString(addr.PrivateIp)
	obs.NetworkInterfaceId = aws.ToString(addr.NetworkInterfaceId)
	return obs
}

func selectPrimaryAddress(addresses []ec2types.NatGatewayAddress) ec2types.NatGatewayAddress {
	if len(addresses) == 0 {
		return ec2types.NatGatewayAddress{}
	}
	for _, addr := range addresses {
		if aws.ToBool(addr.IsPrimary) {
			return addr
		}
	}
	return addresses[0]
}

func singleManagedKeyMatch(managedKey string, matches []string) (string, error) {
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d NAT gateways claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

// Error classifiers — used by the driver to decide between retryable
// errors, terminal errors, and idempotent success paths.

// IsNotFound returns true when the NAT Gateway does not exist. Checks the
// custom notFoundError (used when Describe sees "deleted" state), the
// standard API error codes, and message string matching as a last resort.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nf *notFoundError
	if errors.As(err, &nf) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "NatGatewayNotFound" || code == "InvalidNatGatewayID.NotFound"
	}
	msg := err.Error()
	return strings.Contains(msg, "NatGatewayNotFound") || strings.Contains(msg, "InvalidNatGatewayID.NotFound")
}

// IsInvalidParam returns true for invalid parameter errors (terminal).
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination")
}

// IsAllocationInUse returns true when the Elastic IP allocation is already
// associated with another resource, or the allocation ID does not exist.
func IsAllocationInUse(err error) bool {
	return awserr.HasCode(err, "Resource.AlreadyAssociated", "InvalidAllocationID.NotFound")
}

// IsSubnetNotFound returns true when the target subnet does not exist (terminal).
func IsSubnetNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidSubnetID.NotFound")
}

// IsFailed returns true when the NAT Gateway state string is "failed".
func IsFailed(state string) bool {
	return strings.TrimSpace(state) == "failed"
}

// notFoundError is a sentinel error returned by DescribeNATGateway when the
// NAT Gateway exists but is in the "deleted" terminal state.
type notFoundError struct {
	id string
}

func (e *notFoundError) Error() string {
	return fmt.Sprintf("NatGatewayNotFound: %s", e.id)
}
