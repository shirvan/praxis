package ebs

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

	"github.com/praxiscloud/praxis/internal/infra/ratelimit"
)

// EBSAPI abstracts the AWS EC2 SDK operations for EBS volume management.
type EBSAPI interface {
	CreateVolume(ctx context.Context, spec EBSVolumeSpec) (string, error)
	DescribeVolume(ctx context.Context, volumeID string) (ObservedState, error)
	DeleteVolume(ctx context.Context, volumeID string) error
	ModifyVolume(ctx context.Context, volumeID string, spec EBSVolumeSpec) error
	WaitUntilAvailable(ctx context.Context, volumeID string) error
	UpdateTags(ctx context.Context, volumeID string, tags map[string]string) error
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

type realEBSAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

func NewEBSAPI(client *ec2sdk.Client) EBSAPI {
	return &realEBSAPI{
		client:  client,
		limiter: ratelimit.New("ebs-volume", 20, 10),
	}
}

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

func (r *realEBSAPI) DescribeVolume(ctx context.Context, volumeID string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeVolumes(ctx, &ec2sdk.DescribeVolumesInput{VolumeIds: []string{volumeID}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Volumes) == 0 {
		return ObservedState{}, fmt.Errorf("volume %s not found", volumeID)
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

func (r *realEBSAPI) DeleteVolume(ctx context.Context, volumeID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteVolume(ctx, &ec2sdk.DeleteVolumeInput{VolumeId: aws.String(volumeID)})
	return err
}

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

func (r *realEBSAPI) WaitUntilAvailable(ctx context.Context, volumeID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	waiter := ec2sdk.NewVolumeAvailableWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeVolumesInput{VolumeIds: []string{volumeID}}, 5*time.Minute)
}

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
	for _, volume := range out.Volumes {
		if id := aws.ToString(volume.VolumeId); id != "" {
			matches = append(matches, id)
		}
	}

	return singleManagedKeyMatch(managedKey, matches)
}

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

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidVolume.NotFound"
	}
	errText := err.Error()
	return strings.Contains(errText, "InvalidVolume.NotFound")
}

func IsVolumeInUse(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "VolumeInUse"
	}
	errText := err.Error()
	return strings.Contains(errText, "VolumeInUse")
}

func IsModificationCooldown(err error) bool {
	if err == nil {
		return false
	}
	errText := err.Error()
	return strings.Contains(errText, "currently being modified") ||
		strings.Contains(errText, "modification cooldown") ||
		strings.Contains(errText, "has been modified within the last 6 hours")
}

func IsInvalidParam(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "InvalidParameterValue" ||
			code == "InvalidParameterCombination" ||
			code == "UnsupportedOperation" ||
			code == "VolumeTypeNotAvailableInZone"
	}
	return false
}
