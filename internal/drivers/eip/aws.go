package eip

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

// EIPAPI abstracts the AWS EC2 SDK operations for Elastic IP management.
type EIPAPI interface {
	// AllocateAddress creates a new EIP and returns the allocation ID and public IP.
	AllocateAddress(ctx context.Context, spec ElasticIPSpec) (allocationID, publicIP string, err error)

	// DescribeAddress returns the observed state of an existing EIP.
	DescribeAddress(ctx context.Context, allocationID string) (ObservedState, error)

	// ReleaseAddress releases an EIP back to the pool.
	ReleaseAddress(ctx context.Context, allocationID string) error

	// UpdateTags performs a delete-then-create tag replacement (praxis: tags preserved).
	UpdateTags(ctx context.Context, allocationID string, tags map[string]string) error

	// FindByManagedKey searches for an EIP tagged with praxis:managed-key=<key>.
	// Returns "" if none found, or an error if multiple match (ownership corruption).
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

// realEIPAPI implements EIPAPI using the AWS SDK v2 EC2 client.
type realEIPAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

// NewEIPAPI creates a new API backed by the given EC2 client.
// Rate limited to 20 req/s with burst of 10 for the "elastic-ip" category.
func NewEIPAPI(client *ec2sdk.Client) EIPAPI {
	return &realEIPAPI{
		client:  client,
		limiter: ratelimit.New("elastic-ip", 20, 10),
	}
}

// AllocateAddress calls ec2:AllocateAddress. Sets praxis:managed-key tag at creation.
func (r *realEIPAPI) AllocateAddress(ctx context.Context, spec ElasticIPSpec) (string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", err
	}

	input := &ec2sdk.AllocateAddressInput{
		Domain: ec2types.DomainTypeVpc,
	}
	if spec.NetworkBorderGroup != "" {
		input.NetworkBorderGroup = aws.String(spec.NetworkBorderGroup)
	}
	if spec.PublicIpv4Pool != "" {
		input.PublicIpv4Pool = aws.String(spec.PublicIpv4Pool)
	}

	ec2Tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for key, value := range spec.Tags {
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	input.TagSpecifications = []ec2types.TagSpecification{{
		ResourceType: ec2types.ResourceTypeElasticIp,
		Tags:         ec2Tags,
	}}

	out, err := r.client.AllocateAddress(ctx, input)
	if err != nil {
		return "", "", err
	}
	return aws.ToString(out.AllocationId), aws.ToString(out.PublicIp), nil
}

// DescribeAddress calls ec2:DescribeAddresses and maps to ObservedState.
func (r *realEIPAPI) DescribeAddress(ctx context.Context, allocationID string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeAddresses(ctx, &ec2sdk.DescribeAddressesInput{
		AllocationIds: []string{allocationID},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Addresses) == 0 {
		return ObservedState{}, awserr.NotFound(fmt.Sprintf("elastic IP %s not found", allocationID))
	}
	addr := out.Addresses[0]

	observed := ObservedState{
		AllocationId:       aws.ToString(addr.AllocationId),
		PublicIp:           aws.ToString(addr.PublicIp),
		Domain:             string(addr.Domain),
		NetworkBorderGroup: aws.ToString(addr.NetworkBorderGroup),
		AssociationId:      aws.ToString(addr.AssociationId),
		InstanceId:         aws.ToString(addr.InstanceId),
		Tags:               make(map[string]string, len(addr.Tags)),
	}
	for _, tag := range addr.Tags {
		observed.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	return observed, nil
}

// ReleaseAddress calls ec2:ReleaseAddress to free the EIP.
func (r *realEIPAPI) ReleaseAddress(ctx context.Context, allocationID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ReleaseAddress(ctx, &ec2sdk.ReleaseAddressInput{AllocationId: aws.String(allocationID)})
	return err
}

// UpdateTags replaces user tags: deletes all non-praxis old tags, then creates new ones.
func (r *realEIPAPI) UpdateTags(ctx context.Context, allocationID string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.DescribeAddresses(ctx, &ec2sdk.DescribeAddressesInput{AllocationIds: []string{allocationID}})
	if err != nil {
		return err
	}
	if len(out.Addresses) > 0 {
		addr := out.Addresses[0]
		var oldTags []ec2types.Tag
		for _, tag := range addr.Tags {
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
				Resources: []string{allocationID},
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
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{Resources: []string{allocationID}, Tags: ec2Tags})
	return err
}

// FindByManagedKey searches for an EIP with the praxis:managed-key tag.
// Returns empty string if no match, or an error if multiple EIPs claim the same key.
func (r *realEIPAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.DescribeAddresses(ctx, &ec2sdk.DescribeAddressesInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("tag:praxis:managed-key"),
			Values: []string{managedKey},
		}},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for i := range out.Addresses {
		if id := aws.ToString(out.Addresses[i].AllocationId); id != "" {
			matches = append(matches, id)
		}
	}

	return singleManagedKeyMatch(managedKey, matches)
}

// singleManagedKeyMatch ensures at most one EIP owns a managed key.
func singleManagedKeyMatch(managedKey string, matches []string) (string, error) {
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d allocations claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

// IsNotFound returns true if the EIP does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidAllocationID.NotFound", "InvalidAddressID.NotFound") || awserr.IsNotFoundErr(err)
}

// IsAssociationExists returns true if the EIP is still associated (cannot release).
func IsAssociationExists(err error) bool {
	return awserr.HasCode(err, "InvalidIPAddress.InUse")
}

// IsAddressLimitExceeded returns true if the account has hit the EIP quota.
func IsAddressLimitExceeded(err error) bool {
	return awserr.HasCode(err, "AddressLimitExceeded")
}

// IsQuotaExceeded is an alias for IsAddressLimitExceeded.
func IsQuotaExceeded(err error) bool {
	return IsAddressLimitExceeded(err)
}
