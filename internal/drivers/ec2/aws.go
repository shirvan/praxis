package ec2

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// EC2API abstracts all AWS EC2 API operations used by the EC2 instance driver.
// Each method maps to one or more EC2 SDK calls and is rate-limited via a shared token-bucket limiter.
// This interface enables unit testing by allowing injection of a mock implementation.
type EC2API interface {
	RunInstance(ctx context.Context, spec EC2InstanceSpec) (string, error)
	DescribeInstance(ctx context.Context, instanceId string) (ObservedState, error)
	TerminateInstance(ctx context.Context, instanceId string) error
	WaitUntilRunning(ctx context.Context, instanceId string) error
	ModifyInstanceType(ctx context.Context, instanceId, newType string) error
	ModifySecurityGroups(ctx context.Context, instanceId string, sgIds []string) error
	UpdateMonitoring(ctx context.Context, instanceId string, enabled bool) error
	UpdateTags(ctx context.Context, instanceId string, tags map[string]string) error
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

// realEC2API is the production implementation of EC2API backed by the AWS SDK v2 EC2 client.
// All calls go through a rate limiter configured at 20 tokens/sec with a burst of 10 to stay
// within EC2 API rate limits and avoid throttling.
type realEC2API struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

// NewEC2API creates a production EC2API backed by the given SDK client.
// The rate limiter is shared across all operations on this API instance.
func NewEC2API(client *ec2sdk.Client) EC2API {
	return &realEC2API{
		client:  client,
		limiter: ratelimit.New("ec2-instance", 20, 10),
	}
}

// RunInstance launches a single EC2 instance via the RunInstances API.
// It maps all spec fields to the RunInstances input, including optional fields like
// KeyName, SecurityGroupIds, UserData (base64-encoded), IamInstanceProfile (supports
// both ARN and name), RootVolume (BlockDeviceMappings), and Monitoring.
// A praxis:managed-key tag is always applied for idempotent lookup.
// Returns the new instance ID on success.
func (r *realEC2API) RunInstance(ctx context.Context, spec EC2InstanceSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	input := &ec2sdk.RunInstancesInput{
		ImageId:      aws.String(spec.ImageId),
		InstanceType: ec2types.InstanceType(spec.InstanceType),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     aws.String(spec.SubnetId),
	}

	if spec.KeyName != "" {
		input.KeyName = aws.String(spec.KeyName)
	}
	if len(spec.SecurityGroupIds) > 0 {
		input.SecurityGroupIds = spec.SecurityGroupIds
	}
	if spec.UserData != "" {
		input.UserData = aws.String(base64Encode(spec.UserData))
	}
	if spec.IamInstanceProfile != "" {
		profile := &ec2types.IamInstanceProfileSpecification{}
		if strings.HasPrefix(spec.IamInstanceProfile, "arn:") {
			profile.Arn = aws.String(spec.IamInstanceProfile)
		} else {
			profile.Name = aws.String(spec.IamInstanceProfile)
		}
		input.IamInstanceProfile = profile
	}
	if spec.RootVolume != nil {
		input.BlockDeviceMappings = []ec2types.BlockDeviceMapping{{
			DeviceName: aws.String("/dev/xvda"),
			Ebs: &ec2types.EbsBlockDevice{
				VolumeSize: aws.Int32(spec.RootVolume.SizeGiB),
				VolumeType: ec2types.VolumeType(spec.RootVolume.VolumeType),
				Encrypted:  aws.Bool(spec.RootVolume.Encrypted),
			},
		}}
	}
	if spec.Monitoring {
		input.Monitoring = &ec2types.RunInstancesMonitoringEnabled{Enabled: aws.Bool(true)}
	}

	ec2Tags := []ec2types.Tag{{
		Key:   aws.String("praxis:managed-key"),
		Value: aws.String(spec.ManagedKey),
	}}
	for key, value := range spec.Tags {
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	input.TagSpecifications = []ec2types.TagSpecification{{
		ResourceType: ec2types.ResourceTypeInstance,
		Tags:         ec2Tags,
	}}

	out, err := r.client.RunInstances(ctx, input)
	if err != nil {
		return "", err
	}
	if len(out.Instances) == 0 {
		return "", fmt.Errorf("RunInstances returned no instances")
	}
	return aws.ToString(out.Instances[0].InstanceId), nil
}

// DescribeInstance fetches the full observed state of an EC2 instance.
// It calls DescribeInstances for the core instance metadata, then DescribeVolumes
// for the root volume details (type, size, encryption). Security group IDs are
// sorted for deterministic drift comparison. The IAM instance profile ARN is
// parsed to extract just the profile name.
func (r *realEC2API) DescribeInstance(ctx context.Context, instanceId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeInstances(ctx, &ec2sdk.DescribeInstancesInput{InstanceIds: []string{instanceId}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
		return ObservedState{}, fmt.Errorf("instance %s not found", instanceId)
	}
	inst := out.Reservations[0].Instances[0]

	obs := ObservedState{
		InstanceId:       aws.ToString(inst.InstanceId),
		ImageId:          aws.ToString(inst.ImageId),
		InstanceType:     string(inst.InstanceType),
		KeyName:          aws.ToString(inst.KeyName),
		SubnetId:         aws.ToString(inst.SubnetId),
		VpcId:            aws.ToString(inst.VpcId),
		State:            string(inst.State.Name),
		PrivateIpAddress: aws.ToString(inst.PrivateIpAddress),
		PublicIpAddress:  aws.ToString(inst.PublicIpAddress),
		PrivateDnsName:   aws.ToString(inst.PrivateDnsName),
		Tags:             make(map[string]string, len(inst.Tags)),
	}
	if inst.Monitoring != nil {
		obs.Monitoring = inst.Monitoring.State == ec2types.MonitoringStateEnabled
	}
	if inst.IamInstanceProfile != nil {
		obs.IamInstanceProfile = extractProfileName(aws.ToString(inst.IamInstanceProfile.Arn))
	}
	for _, sg := range inst.SecurityGroups {
		obs.SecurityGroupIds = append(obs.SecurityGroupIds, aws.ToString(sg.GroupId))
	}
	for _, tag := range inst.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	if inst.RootDeviceName != nil {
		for _, bdm := range inst.BlockDeviceMappings {
			if aws.ToString(bdm.DeviceName) == aws.ToString(inst.RootDeviceName) && bdm.Ebs != nil {
				if err := r.limiter.Wait(ctx); err != nil {
					return ObservedState{}, err
				}
				volOut, volErr := r.client.DescribeVolumes(ctx, &ec2sdk.DescribeVolumesInput{
					VolumeIds: []string{aws.ToString(bdm.Ebs.VolumeId)},
				})
				if volErr == nil && len(volOut.Volumes) > 0 {
					vol := volOut.Volumes[0]
					obs.RootVolumeType = string(vol.VolumeType)
					obs.RootVolumeSizeGiB = aws.ToInt32(vol.Size)
					obs.RootVolumeEncrypted = aws.ToBool(vol.Encrypted)
				}
				break
			}
		}
	}

	sort.Strings(obs.SecurityGroupIds)
	return obs, nil
}

// TerminateInstance destroys the EC2 instance via the TerminateInstances API.
// This is irreversible — the instance transitions to shutting-down then terminated.
func (r *realEC2API) TerminateInstance(ctx context.Context, instanceId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TerminateInstances(ctx, &ec2sdk.TerminateInstancesInput{InstanceIds: []string{instanceId}})
	return err
}

// WaitUntilRunning blocks until the instance reaches the "running" state.
// Uses the SDK's built-in InstanceRunningWaiter with a 5-minute timeout.
func (r *realEC2API) WaitUntilRunning(ctx context.Context, instanceId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	waiter := ec2sdk.NewInstanceRunningWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeInstancesInput{InstanceIds: []string{instanceId}}, 5*time.Minute)
}

// ModifyInstanceType changes the instance type in-place. This requires a full
// stop → modify → start cycle because EC2 does not support hot-resizing.
// Steps: (1) check if already stopped, (2) StopInstances + wait, (3) ModifyInstanceAttribute,
// (4) StartInstances + wait until running. Handles IncorrectInstanceState gracefully
// in case the instance is already stopping/stopped.
func (r *realEC2API) ModifyInstanceType(ctx context.Context, instanceId, newType string) error {
	obs, err := r.DescribeInstance(ctx, instanceId)
	if err != nil {
		return err
	}

	if obs.State != "stopped" {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err = r.client.StopInstances(ctx, &ec2sdk.StopInstancesInput{InstanceIds: []string{instanceId}})
		if err != nil {
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "IncorrectInstanceState" {
				return fmt.Errorf("stop instance for type change: %w", err)
			}
		}

		stoppedWaiter := ec2sdk.NewInstanceStoppedWaiter(r.client)
		if err := stoppedWaiter.Wait(ctx, &ec2sdk.DescribeInstancesInput{InstanceIds: []string{instanceId}}, 5*time.Minute); err != nil {
			return fmt.Errorf("wait for instance stop: %w", err)
		}
	}

	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.ModifyInstanceAttribute(ctx, &ec2sdk.ModifyInstanceAttributeInput{
		InstanceId: aws.String(instanceId),
		InstanceType: &ec2types.AttributeValue{
			Value: aws.String(newType),
		},
	})
	if err != nil {
		return fmt.Errorf("modify instance type: %w", err)
	}

	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.StartInstances(ctx, &ec2sdk.StartInstancesInput{InstanceIds: []string{instanceId}})
	if err != nil {
		return fmt.Errorf("start instance after type change: %w", err)
	}

	runningWaiter := ec2sdk.NewInstanceRunningWaiter(r.client)
	if err := runningWaiter.Wait(ctx, &ec2sdk.DescribeInstancesInput{InstanceIds: []string{instanceId}}, 5*time.Minute); err != nil {
		return fmt.Errorf("wait for instance start: %w", err)
	}

	return nil
}

// ModifySecurityGroups replaces the security groups on the instance's primary ENI.
// This is a hot operation — no stop/start required. Uses ModifyInstanceAttribute.
func (r *realEC2API) ModifySecurityGroups(ctx context.Context, instanceId string, sgIds []string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ModifyInstanceAttribute(ctx, &ec2sdk.ModifyInstanceAttributeInput{
		InstanceId: aws.String(instanceId),
		Groups:     sgIds,
	})
	return err
}

// UpdateMonitoring toggles detailed CloudWatch monitoring on the instance.
// Calls MonitorInstances to enable or UnmonitorInstances to disable.
func (r *realEC2API) UpdateMonitoring(ctx context.Context, instanceId string, enabled bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	if enabled {
		_, err := r.client.MonitorInstances(ctx, &ec2sdk.MonitorInstancesInput{InstanceIds: []string{instanceId}})
		return err
	}
	_, err := r.client.UnmonitorInstances(ctx, &ec2sdk.UnmonitorInstancesInput{InstanceIds: []string{instanceId}})
	return err
}

// UpdateTags performs a full tag sync: first deletes all user tags (excluding praxis:-prefixed ones),
// then applies the desired tags via CreateTags. This delete-then-create approach ensures
// stale tags are removed. praxis: tags are never touched to preserve internal metadata.
func (r *realEC2API) UpdateTags(ctx context.Context, instanceId string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.DescribeInstances(ctx, &ec2sdk.DescribeInstancesInput{InstanceIds: []string{instanceId}})
	if err != nil {
		return err
	}
	if len(out.Reservations) > 0 && len(out.Reservations[0].Instances) > 0 {
		inst := out.Reservations[0].Instances[0]
		var oldTags []ec2types.Tag
		for _, tag := range inst.Tags {
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
			_, err = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{Resources: []string{instanceId}, Tags: oldTags})
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
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{Resources: []string{instanceId}, Tags: ec2Tags})
	return err
}

// FindByManagedKey searches for an existing instance with the given praxis:managed-key tag.
// This powers the idempotent create-or-converge pattern: if a previous Provision created
// an instance but the handler was interrupted before saving state, we can find and adopt it.
// Only considers non-terminated instances (pending, running, stopping, stopped).
// Returns "" if no match, the instance ID if exactly one match, or an error if multiple
// instances claim the same managed key (ownership corruption requiring manual intervention).
func (r *realEC2API) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.DescribeInstances(ctx, &ec2sdk.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:praxis:managed-key"), Values: []string{managedKey}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for _, reservation := range out.Reservations {
		for i := range reservation.Instances {
			if id := aws.ToString(reservation.Instances[i].InstanceId); id != "" {
				matches = append(matches, id)
			}
		}
	}

	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d live instances claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

// IsNotFound returns true if the error indicates the instance does not exist.
// Matches both NotFound and Malformed instance ID error codes.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidInstanceID.NotFound", "InvalidInstanceID.Malformed")
}

// IsInvalidParam returns true if the error is due to invalid parameters in the request
// (bad AMI ID, invalid subnet, missing security group, etc.). These are terminal — retrying won't help.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidAMIID.Malformed", "InvalidAMIID.NotFound", "InvalidSubnetID.NotFound", "InvalidGroup.NotFound")
}

// IsInsufficientCapacity returns true if the error indicates AWS capacity limits
// (no capacity in AZ, account instance limit reached, or unsupported instance type).
// These are terminal — the user must change their spec or wait for capacity.
func IsInsufficientCapacity(err error) bool {
	return awserr.HasCode(err, "InsufficientInstanceCapacity", "InstanceLimitExceeded", "Unsupported")
}

// base64Encode encodes a string to standard base64, used for EC2 UserData.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// extractProfileName extracts the instance profile name from its full ARN.
// e.g. "arn:aws:iam::123456:instance-profile/my-profile" → "my-profile"
func extractProfileName(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return arn
}
