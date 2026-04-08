package ebs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// EBSAPI abstracts the AWS EC2 SDK operations for EBS volume management.
// In production, this is realEBSAPI (backed by the real SDK client).
// In unit tests, this is mockEBSAPI (backed by testify/mock).
//
// All methods receive a plain context.Context, NOT a restate.RunContext.
// The caller in driver.go wraps these calls inside restate.Run() for journaling.
type EBSAPI interface {
	// CreateVolume creates a new EBS volume with the given spec and returns the volume ID.
	CreateVolume(ctx context.Context, spec EBSVolumeSpec) (string, error)

	// DescribeVolume returns the observed state of a volume by calling ec2:DescribeVolumes.
	DescribeVolume(ctx context.Context, volumeID string) (ObservedState, error)

	// DeleteVolume deletes a volume. Fails if the volume is attached to an instance.
	DeleteVolume(ctx context.Context, volumeID string) error

	// ModifyVolume modifies volume type, size, IOPS, and/or throughput in-place.
	// Subject to AWS's 6-hour modification cooldown between changes.
	ModifyVolume(ctx context.Context, volumeID string, spec EBSVolumeSpec) error

	// WaitUntilAvailable polls until the volume reaches the "available" state.
	// Times out after 5 minutes.
	WaitUntilAvailable(ctx context.Context, volumeID string) error

	// UpdateTags replaces all user tags on a volume (preserving praxis: prefixed tags).
	UpdateTags(ctx context.Context, volumeID string, tags map[string]string) error

	// FindByManagedKey looks up a volume by its praxis:managed-key tag.
	// Returns empty string if none found, error if multiple found (ownership corruption).
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

// realEBSAPI implements EBSAPI using the actual AWS SDK v2 EC2 client.
type realEBSAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

// NewEBSAPI creates a new EBSAPI backed by the given EC2 SDK client.
// Rate limited to 20 req/s with burst of 10 for the "ebs-volume" category.
func NewEBSAPI(client *ec2sdk.Client) EBSAPI {
	return &realEBSAPI{
		client:  client,
		limiter: ratelimit.New("ebs-volume", 20, 10),
	}
}

// CreateVolume calls ec2:CreateVolume with the specified parameters.
// Tags (including the praxis:managed-key tag) are applied atomically at creation
// time via TagSpecifications, avoiding a separate CreateTags call.
func (r *realEBSAPI) CreateVolume(ctx context.Context, spec EBSVolumeSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	input := &ec2sdk.CreateVolumeInput{
		AvailabilityZone: aws.String(spec.AvailabilityZone),
		VolumeType:       ec2types.VolumeType(spec.VolumeType),
		Size:             aws.Int32(spec.SizeGiB),
		Encrypted:        aws.Bool(spec.Encrypted),
	}
	if spec.Iops > 0 {
		input.Iops = aws.Int32(spec.Iops)
	}
	if spec.Throughput > 0 {
		input.Throughput = aws.Int32(spec.Throughput)
	}
	if spec.KmsKeyId != "" {
		input.KmsKeyId = aws.String(spec.KmsKeyId)
	}
	if spec.SnapshotId != "" {
		input.SnapshotId = aws.String(spec.SnapshotId)
	}

	ec2Tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for key, value := range spec.Tags {
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	input.TagSpecifications = []ec2types.TagSpecification{{
		ResourceType: ec2types.ResourceTypeVolume,
		Tags:         ec2Tags,
	}}

	out, err := r.client.CreateVolume(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.VolumeId), nil
}

// DescribeVolume calls ec2:DescribeVolumes and maps the response to ObservedState.
func (r *realEBSAPI) DescribeVolume(ctx context.Context, volumeID string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeVolumes(ctx, &ec2sdk.DescribeVolumesInput{VolumeIds: []string{volumeID}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Volumes) == 0 {
		return ObservedState{}, awserr.NotFound(fmt.Sprintf("volume %s not found", volumeID))
	}
	vol := out.Volumes[0]

	observed := ObservedState{
		VolumeId:         aws.ToString(vol.VolumeId),
		AvailabilityZone: aws.ToString(vol.AvailabilityZone),
		VolumeType:       string(vol.VolumeType),
		SizeGiB:          aws.ToInt32(vol.Size),
		Encrypted:        aws.ToBool(vol.Encrypted),
		KmsKeyId:         aws.ToString(vol.KmsKeyId),
		State:            string(vol.State),
		SnapshotId:       aws.ToString(vol.SnapshotId),
		Tags:             make(map[string]string, len(vol.Tags)),
	}
	if vol.Iops != nil {
		observed.Iops = aws.ToInt32(vol.Iops)
	}
	if vol.Throughput != nil {
		observed.Throughput = aws.ToInt32(vol.Throughput)
	}
	for _, tag := range vol.Tags {
		observed.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	return observed, nil
}

// DeleteVolume calls ec2:DeleteVolume. Fails with VolumeInUse if attached.
func (r *realEBSAPI) DeleteVolume(ctx context.Context, volumeID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteVolume(ctx, &ec2sdk.DeleteVolumeInput{VolumeId: aws.String(volumeID)})
	return err
}

// ModifyVolume calls ec2:ModifyVolume to change volume type, size, IOPS, or throughput.
// AWS enforces a 6-hour cooldown between modifications.
func (r *realEBSAPI) ModifyVolume(ctx context.Context, volumeID string, spec EBSVolumeSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &ec2sdk.ModifyVolumeInput{
		VolumeId:   aws.String(volumeID),
		VolumeType: ec2types.VolumeType(spec.VolumeType),
		Size:       aws.Int32(spec.SizeGiB),
	}
	if spec.Iops > 0 {
		input.Iops = aws.Int32(spec.Iops)
	}
	if spec.Throughput > 0 {
		input.Throughput = aws.Int32(spec.Throughput)
	}
	_, err := r.client.ModifyVolume(ctx, input)
	return err
}

// WaitUntilAvailable uses the SDK's built-in waiter to poll until the volume
// reaches "available" state. Times out after 5 minutes.
func (r *realEBSAPI) WaitUntilAvailable(ctx context.Context, volumeID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	waiter := ec2sdk.NewVolumeAvailableWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeVolumesInput{VolumeIds: []string{volumeID}}, 5*time.Minute)
}

// UpdateTags performs a diff-based tag update: removes old user tags, then adds new ones.
// Tags prefixed with "praxis:" are preserved — they are internal ownership markers.
func (r *realEBSAPI) UpdateTags(ctx context.Context, volumeID string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.DescribeVolumes(ctx, &ec2sdk.DescribeVolumesInput{VolumeIds: []string{volumeID}})
	if err != nil {
		return err
	}
	if len(out.Volumes) > 0 {
		vol := out.Volumes[0]
		var oldTags []ec2types.Tag
		for _, tag := range vol.Tags {
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
			_, err = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{Resources: []string{volumeID}, Tags: oldTags})
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
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{Resources: []string{volumeID}, Tags: ec2Tags})
	return err
}

// FindByManagedKey searches for a volume tagged with praxis:managed-key matching
// the given key. Returns the volume ID if exactly one match is found.
// Returns error if multiple volumes claim the same managed key (ownership corruption).
func (r *realEBSAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.DescribeVolumes(ctx, &ec2sdk.DescribeVolumesInput{
		Filters: []ec2types.Filter{{Name: aws.String("tag:praxis:managed-key"), Values: []string{managedKey}}},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for i := range out.Volumes {
		if id := aws.ToString(out.Volumes[i].VolumeId); id != "" {
			matches = append(matches, id)
		}
	}

	return singleManagedKeyMatch(managedKey, matches)
}

// singleManagedKeyMatch validates that at most one resource claims a managed key.
// Multiple matches indicate ownership corruption requiring manual intervention.
func singleManagedKeyMatch(managedKey string, matches []string) (string, error) {
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d live volumes claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

// IsNotFound returns true if the AWS error indicates the volume does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidVolume.NotFound") || awserr.IsNotFoundErr(err)
}

// IsVolumeInUse returns true if a DeleteVolume call failed because the volume
// is still attached to an EC2 instance.
func IsVolumeInUse(err error) bool {
	return awserr.HasCode(err, "VolumeInUse")
}

// IsModificationCooldown returns true if a ModifyVolume call failed because
// the volume was modified within the last 6 hours (AWS enforced cooldown).
func IsModificationCooldown(err error) bool {
	if err == nil {
		return false
	}
	errText := err.Error()
	return strings.Contains(errText, "currently being modified") ||
		strings.Contains(errText, "modification cooldown") ||
		strings.Contains(errText, "has been modified within the last 6 hours")
}

// IsInvalidParam returns true if the error indicates invalid API parameters.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameterCombination", "UnsupportedOperation", "VolumeTypeNotAvailableInZone")
}
