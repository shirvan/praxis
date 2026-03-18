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

	"github.com/praxiscloud/praxis/internal/infra/ratelimit"
)

// EC2API abstracts the AWS operations used by the driver.
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

type realEC2API struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

func NewEC2API(client *ec2sdk.Client) EC2API {
	return &realEC2API{
		client:  client,
		limiter: ratelimit.New("ec2-instance", 20, 10),
	}
}

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

func (r *realEC2API) TerminateInstance(ctx context.Context, instanceId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TerminateInstances(ctx, &ec2sdk.TerminateInstancesInput{InstanceIds: []string{instanceId}})
	return err
}

func (r *realEC2API) WaitUntilRunning(ctx context.Context, instanceId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	waiter := ec2sdk.NewInstanceRunningWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeInstancesInput{InstanceIds: []string{instanceId}}, 5*time.Minute)
}

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
			if !(errors.As(err, &apiErr) && apiErr.ErrorCode() == "IncorrectInstanceState") {
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
		for _, inst := range reservation.Instances {
			if id := aws.ToString(inst.InstanceId); id != "" {
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

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "InvalidInstanceID.NotFound" || code == "InvalidInstanceID.Malformed"
	}
	errText := err.Error()
	return strings.Contains(errText, "InvalidInstanceID.NotFound")
}

func IsTerminated(err error) bool {
	if err == nil {
		return false
	}
	errText := err.Error()
	return strings.Contains(errText, "terminated") || strings.Contains(errText, "InvalidInstanceID.NotFound")
}

func IsInvalidParam(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "InvalidParameterValue" ||
			code == "InvalidAMIID.Malformed" ||
			code == "InvalidAMIID.NotFound" ||
			code == "InvalidSubnetID.NotFound" ||
			code == "InvalidGroup.NotFound"
	}
	return false
}

func IsInsufficientCapacity(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "InsufficientInstanceCapacity" || code == "InstanceLimitExceeded" || code == "Unsupported"
	}
	return false
}

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func extractProfileName(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return arn
}
