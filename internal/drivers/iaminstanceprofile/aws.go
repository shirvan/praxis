package iaminstanceprofile

import (
	"context"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// IAMInstanceProfileAPI defines the interface for all AWS IAM instance profile operations.
// Supports profile CRUD, single-role association, and tag management.
type IAMInstanceProfileAPI interface {
	CreateInstanceProfile(ctx context.Context, spec IAMInstanceProfileSpec) (arn, profileID string, err error)
	DescribeInstanceProfile(ctx context.Context, name string) (ObservedState, error)
	DeleteInstanceProfile(ctx context.Context, name string) error
	AddRoleToInstanceProfile(ctx context.Context, name, roleName string) error
	RemoveRoleFromInstanceProfile(ctx context.Context, name, roleName string) error
	TagInstanceProfile(ctx context.Context, name string, tags map[string]string) error
	UntagInstanceProfile(ctx context.Context, name string, keys []string) error
}

type realIAMInstanceProfileAPI struct {
	client  *iamsdk.Client
	limiter *ratelimit.Limiter
}

// NewIAMInstanceProfileAPI constructs a production IAMInstanceProfileAPI with IAM rate limiting.
func NewIAMInstanceProfileAPI(client *iamsdk.Client) IAMInstanceProfileAPI {
	return &realIAMInstanceProfileAPI{
		client:  client,
		limiter: ratelimit.New("iam", 15, 8),
	}
}

func (r *realIAMInstanceProfileAPI) CreateInstanceProfile(ctx context.Context, spec IAMInstanceProfileSpec) (string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", err
	}
	input := &iamsdk.CreateInstanceProfileInput{
		InstanceProfileName: aws.String(spec.InstanceProfileName),
		Path:                aws.String(spec.Path),
	}
	if len(spec.Tags) > 0 {
		input.Tags = make([]iamtypes.Tag, 0, len(spec.Tags))
		for key, value := range spec.Tags {
			input.Tags = append(input.Tags, iamtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
		sort.Slice(input.Tags, func(i, j int) bool {
			return aws.ToString(input.Tags[i].Key) < aws.ToString(input.Tags[j].Key)
		})
	}
	out, err := r.client.CreateInstanceProfile(ctx, input)
	if err != nil {
		return "", "", err
	}
	return aws.ToString(out.InstanceProfile.Arn), aws.ToString(out.InstanceProfile.InstanceProfileId), nil
}

func (r *realIAMInstanceProfileAPI) DescribeInstanceProfile(ctx context.Context, name string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetInstanceProfile(ctx, &iamsdk.GetInstanceProfileInput{
		InstanceProfileName: aws.String(name),
	})
	if err != nil {
		return ObservedState{}, err
	}
	ip := out.InstanceProfile
	observed := ObservedState{
		Arn:                 aws.ToString(ip.Arn),
		InstanceProfileId:   aws.ToString(ip.InstanceProfileId),
		InstanceProfileName: aws.ToString(ip.InstanceProfileName),
		Path:                aws.ToString(ip.Path),
		Tags:                map[string]string{},
	}
	if ip.CreateDate != nil {
		observed.CreateDate = ip.CreateDate.UTC().Format(time.RFC3339)
	}
	if len(ip.Roles) > 0 {
		observed.RoleName = aws.ToString(ip.Roles[0].RoleName)
	}
	for _, tag := range ip.Tags {
		observed.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return observed, nil
}

func (r *realIAMInstanceProfileAPI) DeleteInstanceProfile(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteInstanceProfile(ctx, &iamsdk.DeleteInstanceProfileInput{
		InstanceProfileName: aws.String(name),
	})
	return err
}

func (r *realIAMInstanceProfileAPI) AddRoleToInstanceProfile(ctx context.Context, name, roleName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AddRoleToInstanceProfile(ctx, &iamsdk.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		RoleName:            aws.String(roleName),
	})
	return err
}

func (r *realIAMInstanceProfileAPI) RemoveRoleFromInstanceProfile(ctx context.Context, name, roleName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RemoveRoleFromInstanceProfile(ctx, &iamsdk.RemoveRoleFromInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		RoleName:            aws.String(roleName),
	})
	return err
}

func (r *realIAMInstanceProfileAPI) TagInstanceProfile(ctx context.Context, name string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	awsTags := make([]iamtypes.Tag, 0, len(tags))
	for key, value := range tags {
		awsTags = append(awsTags, iamtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	sort.Slice(awsTags, func(i, j int) bool {
		return aws.ToString(awsTags[i].Key) < aws.ToString(awsTags[j].Key)
	})
	_, err := r.client.TagInstanceProfile(ctx, &iamsdk.TagInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		Tags:                awsTags,
	})
	return err
}

func (r *realIAMInstanceProfileAPI) UntagInstanceProfile(ctx context.Context, name string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UntagInstanceProfile(ctx, &iamsdk.UntagInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		TagKeys:             sorted,
	})
	return err
}

// IsNotFound returns true when the IAM error indicates the entity does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "NoSuchEntity")
}

func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "EntityAlreadyExists")
}

func IsDeleteConflict(err error) bool {
	return awserr.HasCode(err, "DeleteConflict")
}

func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "LimitExceeded")
}
