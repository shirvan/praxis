package ami

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type AMIAPI interface {
	RegisterImage(ctx context.Context, spec AMISpec) (string, error)
	CopyImage(ctx context.Context, spec AMISpec) (string, error)
	DescribeImage(ctx context.Context, imageId string) (ObservedState, error)
	DescribeImageByName(ctx context.Context, name string) (ObservedState, error)
	DeregisterImage(ctx context.Context, imageId string) error
	UpdateTags(ctx context.Context, imageId string, tags map[string]string) error
	ModifyDescription(ctx context.Context, imageId, description string) error
	ModifyLaunchPermissions(ctx context.Context, imageId string, perms *LaunchPermsSpec) error
	EnableDeprecation(ctx context.Context, imageId, deprecateAt string) error
	DisableDeprecation(ctx context.Context, imageId string) error
	WaitUntilAvailable(ctx context.Context, imageId string, timeout time.Duration) error
	FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}

type realAMIAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

func NewAMIAPI(client *ec2sdk.Client) AMIAPI {
	return &realAMIAPI{
		client:  client,
		limiter: ratelimit.New("ami", 20, 10),
	}
}

func (r *realAMIAPI) RegisterImage(ctx context.Context, spec AMISpec) (string, error) {
	snapshot := spec.Source.FromSnapshot
	if snapshot == nil {
		return "", fmt.Errorf("RegisterImage requires fromSnapshot source")
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	blockMapping := ec2types.BlockDeviceMapping{
		DeviceName: aws.String(snapshot.RootDeviceName),
		Ebs: &ec2types.EbsBlockDevice{
			SnapshotId:          aws.String(snapshot.SnapshotId),
			VolumeType:          ec2types.VolumeType(snapshot.VolumeType),
			DeleteOnTermination: aws.Bool(true),
		},
	}
	if snapshot.VolumeSize > 0 {
		blockMapping.Ebs.VolumeSize = aws.Int32(snapshot.VolumeSize)
	}

	input := &ec2sdk.RegisterImageInput{
		Name:                aws.String(spec.Name),
		Description:         aws.String(spec.Description),
		Architecture:        ec2types.ArchitectureValues(snapshot.Architecture),
		VirtualizationType:  aws.String(snapshot.VirtualizationType),
		RootDeviceName:      aws.String(snapshot.RootDeviceName),
		BlockDeviceMappings: []ec2types.BlockDeviceMapping{blockMapping},
	}
	if snapshot.EnaSupport != nil {
		input.EnaSupport = aws.Bool(*snapshot.EnaSupport)
	}

	out, err := r.client.RegisterImage(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.ImageId), nil
}

func (r *realAMIAPI) CopyImage(ctx context.Context, spec AMISpec) (string, error) {
	fromAMI := spec.Source.FromAMI
	if fromAMI == nil {
		return "", fmt.Errorf("CopyImage requires fromAMI source")
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}

	sourceRegion := strings.TrimSpace(fromAMI.SourceRegion)
	if sourceRegion == "" {
		sourceRegion = spec.Region
	}

	input := &ec2sdk.CopyImageInput{
		Name:          aws.String(spec.Name),
		Description:   aws.String(spec.Description),
		SourceImageId: aws.String(fromAMI.SourceImageId),
		SourceRegion:  aws.String(sourceRegion),
		Encrypted:     aws.Bool(fromAMI.Encrypted),
	}
	if fromAMI.KmsKeyId != "" {
		input.KmsKeyId = aws.String(fromAMI.KmsKeyId)
	}

	out, err := r.client.CopyImage(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.ImageId), nil
}

func (r *realAMIAPI) DescribeImage(ctx context.Context, imageId string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	return r.describeSingleImage(ctx, &ec2sdk.DescribeImagesInput{ImageIds: []string{imageId}})
}

func (r *realAMIAPI) DescribeImageByName(ctx context.Context, name string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	return r.describeSingleImage(ctx, &ec2sdk.DescribeImagesInput{
		Owners: []string{"self"},
		Filters: []ec2types.Filter{{
			Name:   aws.String("name"),
			Values: []string{name},
		}},
	})
}

func (r *realAMIAPI) describeSingleImage(ctx context.Context, input *ec2sdk.DescribeImagesInput) (ObservedState, error) {
	out, err := r.client.DescribeImages(ctx, input)
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Images) == 0 {
		return ObservedState{}, fmt.Errorf("AMI not found")
	}
	if len(out.Images) > 1 {
		var ids []string
		for i := range out.Images {
			ids = append(ids, aws.ToString(out.Images[i].ImageId))
		}
		sort.Strings(ids)
		return ObservedState{}, fmt.Errorf("multiple AMIs matched query: %v", ids)
	}

	image := out.Images[0]
	observed := ObservedState{
		ImageId:            aws.ToString(image.ImageId),
		Name:               aws.ToString(image.Name),
		Description:        aws.ToString(image.Description),
		State:              string(image.State),
		Architecture:       string(image.Architecture),
		VirtualizationType: string(image.VirtualizationType),
		RootDeviceName:     aws.ToString(image.RootDeviceName),
		OwnerId:            aws.ToString(image.OwnerId),
		CreationDate:       aws.ToString(image.CreationDate),
		Tags:               extractTags(image.Tags),
	}
	if image.DeprecationTime != nil {
		observed.DeprecationTime = aws.ToString(image.DeprecationTime)
	}

	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	attr, err := r.client.DescribeImageAttribute(ctx, &ec2sdk.DescribeImageAttributeInput{
		ImageId:   image.ImageId,
		Attribute: ec2types.ImageAttributeNameLaunchPermission,
	})
	if err != nil {
		return ObservedState{}, err
	}
	for _, permission := range attr.LaunchPermissions {
		if permission.Group == ec2types.PermissionGroupAll {
			observed.LaunchPermPublic = true
		}
		if permission.UserId != nil {
			observed.LaunchPermAccounts = append(observed.LaunchPermAccounts, aws.ToString(permission.UserId))
		}
	}
	sort.Strings(observed.LaunchPermAccounts)
	observed.LaunchPermAccounts = dedupe(observed.LaunchPermAccounts)

	return observed, nil
}

func (r *realAMIAPI) DeregisterImage(ctx context.Context, imageId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeregisterImage(ctx, &ec2sdk.DeregisterImageInput{ImageId: aws.String(imageId)})
	return err
}

func (r *realAMIAPI) UpdateTags(ctx context.Context, imageId string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	observed, err := r.DescribeImage(ctx, imageId)
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
		if _, err := r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{Resources: []string{imageId}, Tags: stale}); err != nil {
			return err
		}
	}

	if len(tags) == 0 {
		return nil
	}
	var ec2Tags []ec2types.Tag
	for key, value := range tags {
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	sort.Slice(ec2Tags, func(i, j int) bool {
		return aws.ToString(ec2Tags[i].Key) < aws.ToString(ec2Tags[j].Key)
	})
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{Resources: []string{imageId}, Tags: ec2Tags})
	return err
}

func (r *realAMIAPI) ModifyDescription(ctx context.Context, imageId, description string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ModifyImageAttribute(ctx, &ec2sdk.ModifyImageAttributeInput{
		ImageId:     aws.String(imageId),
		Attribute:   aws.String("description"),
		Description: &ec2types.AttributeValue{Value: aws.String(description)},
	})
	return err
}

func (r *realAMIAPI) ModifyLaunchPermissions(ctx context.Context, imageId string, perms *LaunchPermsSpec) error {
	observed, err := r.DescribeImage(ctx, imageId)
	if err != nil {
		return err
	}
	desired := normalizeLaunchPermSpec(perms)
	current := normalizeLaunchPermSpec(launchPermsFromObserved(observed))

	add := make([]ec2types.LaunchPermission, 0)
	remove := make([]ec2types.LaunchPermission, 0)

	if desired.Public && !current.Public {
		add = append(add, ec2types.LaunchPermission{Group: ec2types.PermissionGroupAll})
	}
	if !desired.Public && current.Public {
		remove = append(remove, ec2types.LaunchPermission{Group: ec2types.PermissionGroupAll})
	}

	currentSet := make(map[string]struct{}, len(current.AccountIds))
	for _, account := range current.AccountIds {
		currentSet[account] = struct{}{}
	}
	desiredSet := make(map[string]struct{}, len(desired.AccountIds))
	for _, account := range desired.AccountIds {
		desiredSet[account] = struct{}{}
		if _, ok := currentSet[account]; !ok {
			add = append(add, ec2types.LaunchPermission{UserId: aws.String(account)})
		}
	}
	for _, account := range current.AccountIds {
		if _, ok := desiredSet[account]; !ok {
			remove = append(remove, ec2types.LaunchPermission{UserId: aws.String(account)})
		}
	}

	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.ModifyImageAttribute(ctx, &ec2sdk.ModifyImageAttributeInput{
		ImageId:   aws.String(imageId),
		Attribute: aws.String("launchPermission"),
		LaunchPermission: &ec2types.LaunchPermissionModifications{
			Add:    add,
			Remove: remove,
		},
	})
	return err
}

func (r *realAMIAPI) EnableDeprecation(ctx context.Context, imageId, deprecateAt string) error {
	parsed, err := time.Parse(time.RFC3339, deprecateAt)
	if err != nil {
		return fmt.Errorf("parse deprecation time: %w", err)
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.EnableImageDeprecation(ctx, &ec2sdk.EnableImageDeprecationInput{
		ImageId:     aws.String(imageId),
		DeprecateAt: aws.Time(parsed.UTC()),
	})
	return err
}

func (r *realAMIAPI) DisableDeprecation(ctx context.Context, imageId string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DisableImageDeprecation(ctx, &ec2sdk.DisableImageDeprecationInput{ImageId: aws.String(imageId)})
	return err
}

func (r *realAMIAPI) WaitUntilAvailable(ctx context.Context, imageId string, timeout time.Duration) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	waiter := ec2sdk.NewImageAvailableWaiter(r.client)
	return waiter.Wait(ctx, &ec2sdk.DescribeImagesInput{ImageIds: []string{imageId}}, timeout)
}

func (r *realAMIAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.DescribeImages(ctx, &ec2sdk.DescribeImagesInput{
		Owners: []string{"self"},
		Filters: []ec2types.Filter{{
			Name:   aws.String("tag:praxis:managed-key"),
			Values: []string{managedKey},
		}},
	})
	if err != nil {
		return "", err
	}

	var matches []string
	for i := range out.Images {
		if id := aws.ToString(out.Images[i].ImageId); id != "" {
			matches = append(matches, id)
		}
	}
	sort.Strings(matches)
	matches = dedupe(matches)

	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ownership corruption: %d AMIs claim managed-key %q: %v; manual intervention required", len(matches), managedKey, matches)
	}
}

func extractTags(tags []ec2types.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, tag := range tags {
		out[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return out
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidAMIID.NotFound", "InvalidAMIID.Unavailable")
}

func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValue", "InvalidParameter", "MissingParameter", "InvalidAMIID.Malformed", "InvalidAMIID.NotFound")
}

func IsSnapshotNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidSnapshot.NotFound")
}

func IsAMIQuotaExceeded(err error) bool {
	return awserr.HasCode(err, "AMIQuotaExceeded")
}
