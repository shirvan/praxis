package iamuser

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type IAMUserAPI interface {
	CreateUser(ctx context.Context, spec IAMUserSpec) (arn, userID string, err error)
	DescribeUser(ctx context.Context, userName string) (ObservedState, error)
	DeleteUser(ctx context.Context, userName string) error
	UpdateUserPath(ctx context.Context, userName, newPath string) error
	PutUserPermissionsBoundary(ctx context.Context, userName, policyArn string) error
	DeleteUserPermissionsBoundary(ctx context.Context, userName string) error
	PutInlinePolicy(ctx context.Context, userName, policyName, document string) error
	DeleteInlinePolicy(ctx context.Context, userName, policyName string) error
	AttachManagedPolicy(ctx context.Context, userName, policyArn string) error
	DetachManagedPolicy(ctx context.Context, userName, policyArn string) error
	AddUserToGroup(ctx context.Context, userName, groupName string) error
	RemoveUserFromGroup(ctx context.Context, userName, groupName string) error
	UpdateTags(ctx context.Context, userName string, tags map[string]string) error
	DeleteLoginProfile(ctx context.Context, userName string) error
	DeleteAllAccessKeys(ctx context.Context, userName string) error
}

type realIAMUserAPI struct {
	client  *iamsdk.Client
	limiter *ratelimit.Limiter
}

func NewIAMUserAPI(client *iamsdk.Client) IAMUserAPI {
	return &realIAMUserAPI{
		client:  client,
		limiter: ratelimit.New("iam", 15, 8),
	}
}

func (r *realIAMUserAPI) CreateUser(ctx context.Context, spec IAMUserSpec) (string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", err
	}
	input := &iamsdk.CreateUserInput{
		UserName: aws.String(spec.UserName),
		Path:     aws.String(spec.Path),
	}
	if spec.PermissionsBoundary != "" {
		input.PermissionsBoundary = aws.String(spec.PermissionsBoundary)
	}
	if len(spec.Tags) > 0 {
		input.Tags = toIAMTags(spec.Tags)
	}
	out, err := r.client.CreateUser(ctx, input)
	if err != nil {
		return "", "", err
	}
	return aws.ToString(out.User.Arn), aws.ToString(out.User.UserId), nil
}

func (r *realIAMUserAPI) DescribeUser(ctx context.Context, userName string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetUser(ctx, &iamsdk.GetUserInput{UserName: aws.String(userName)})
	if err != nil {
		return ObservedState{}, err
	}
	user := out.User

	inlinePolicies, err := r.listInlinePolicies(ctx, userName)
	if err != nil {
		return ObservedState{}, err
	}
	managedPolicyArns, err := r.listManagedPolicies(ctx, userName)
	if err != nil {
		return ObservedState{}, err
	}
	groups, err := r.listGroups(ctx, userName)
	if err != nil {
		return ObservedState{}, err
	}
	tags, err := r.listUserTags(ctx, userName)
	if err != nil {
		return ObservedState{}, err
	}

	observed := ObservedState{
		Arn:               aws.ToString(user.Arn),
		UserId:            aws.ToString(user.UserId),
		UserName:          aws.ToString(user.UserName),
		Path:              aws.ToString(user.Path),
		InlinePolicies:    inlinePolicies,
		ManagedPolicyArns: managedPolicyArns,
		Groups:            groups,
		Tags:              tags,
	}
	if user.PermissionsBoundary != nil {
		observed.PermissionsBoundary = aws.ToString(user.PermissionsBoundary.PermissionsBoundaryArn)
	}
	if user.CreateDate != nil {
		observed.CreateDate = user.CreateDate.UTC().Format(time.RFC3339)
	}
	return observed, nil
}

func (r *realIAMUserAPI) DeleteUser(ctx context.Context, userName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteUser(ctx, &iamsdk.DeleteUserInput{UserName: aws.String(userName)})
	return err
}

func (r *realIAMUserAPI) UpdateUserPath(ctx context.Context, userName, newPath string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateUser(ctx, &iamsdk.UpdateUserInput{UserName: aws.String(userName), NewPath: aws.String(newPath)})
	return err
}

func (r *realIAMUserAPI) PutUserPermissionsBoundary(ctx context.Context, userName, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.PutUserPermissionsBoundary(ctx, &iamsdk.PutUserPermissionsBoundaryInput{
		UserName:            aws.String(userName),
		PermissionsBoundary: aws.String(policyArn),
	})
	return err
}

func (r *realIAMUserAPI) DeleteUserPermissionsBoundary(ctx context.Context, userName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteUserPermissionsBoundary(ctx, &iamsdk.DeleteUserPermissionsBoundaryInput{UserName: aws.String(userName)})
	return err
}

func (r *realIAMUserAPI) PutInlinePolicy(ctx context.Context, userName, policyName, document string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.PutUserPolicy(ctx, &iamsdk.PutUserPolicyInput{
		UserName:       aws.String(userName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(document),
	})
	return err
}

func (r *realIAMUserAPI) DeleteInlinePolicy(ctx context.Context, userName, policyName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteUserPolicy(ctx, &iamsdk.DeleteUserPolicyInput{
		UserName:   aws.String(userName),
		PolicyName: aws.String(policyName),
	})
	return err
}

func (r *realIAMUserAPI) AttachManagedPolicy(ctx context.Context, userName, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AttachUserPolicy(ctx, &iamsdk.AttachUserPolicyInput{
		UserName:  aws.String(userName),
		PolicyArn: aws.String(policyArn),
	})
	return err
}

func (r *realIAMUserAPI) DetachManagedPolicy(ctx context.Context, userName, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DetachUserPolicy(ctx, &iamsdk.DetachUserPolicyInput{
		UserName:  aws.String(userName),
		PolicyArn: aws.String(policyArn),
	})
	return err
}

func (r *realIAMUserAPI) AddUserToGroup(ctx context.Context, userName, groupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AddUserToGroup(ctx, &iamsdk.AddUserToGroupInput{
		UserName:  aws.String(userName),
		GroupName: aws.String(groupName),
	})
	return err
}

func (r *realIAMUserAPI) RemoveUserFromGroup(ctx context.Context, userName, groupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RemoveUserFromGroup(ctx, &iamsdk.RemoveUserFromGroupInput{
		UserName:  aws.String(userName),
		GroupName: aws.String(groupName),
	})
	return err
}

func (r *realIAMUserAPI) UpdateTags(ctx context.Context, userName string, tags map[string]string) error {
	existing, err := r.listUserTags(ctx, userName)
	if err != nil {
		return err
	}
	desired := filterPraxisTags(tags)

	var remove []string
	for key := range existing {
		if _, ok := desired[key]; !ok {
			remove = append(remove, key)
		}
	}
	sort.Strings(remove)
	if len(remove) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err := r.client.UntagUser(ctx, &iamsdk.UntagUserInput{UserName: aws.String(userName), TagKeys: remove})
		if err != nil {
			return err
		}
	}

	add := make(map[string]string)
	for key, value := range desired {
		if current, ok := existing[key]; !ok || current != value {
			add[key] = value
		}
	}
	if len(add) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err := r.client.TagUser(ctx, &iamsdk.TagUserInput{UserName: aws.String(userName), Tags: toIAMTags(add)})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *realIAMUserAPI) DeleteLoginProfile(ctx context.Context, userName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteLoginProfile(ctx, &iamsdk.DeleteLoginProfileInput{UserName: aws.String(userName)})
	return err
}

func (r *realIAMUserAPI) DeleteAllAccessKeys(ctx context.Context, userName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	paginator := iamsdk.NewListAccessKeysPaginator(r.client, &iamsdk.ListAccessKeysInput{UserName: aws.String(userName)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, metadata := range page.AccessKeyMetadata {
			if err := r.limiter.Wait(ctx); err != nil {
				return err
			}
			_, err := r.client.DeleteAccessKey(ctx, &iamsdk.DeleteAccessKeyInput{
				UserName:    aws.String(userName),
				AccessKeyId: metadata.AccessKeyId,
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *realIAMUserAPI) listInlinePolicies(ctx context.Context, userName string) (map[string]string, error) {
	policies := map[string]string{}
	paginator := iamsdk.NewListUserPoliciesPaginator(r.client, &iamsdk.ListUserPoliciesInput{UserName: aws.String(userName)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, policyName := range page.PolicyNames {
			if err := r.limiter.Wait(ctx); err != nil {
				return nil, err
			}
			policyOut, err := r.client.GetUserPolicy(ctx, &iamsdk.GetUserPolicyInput{
				UserName:   aws.String(userName),
				PolicyName: aws.String(policyName),
			})
			if err != nil {
				return nil, err
			}
			policies[policyName] = normalizePolicyDocument(aws.ToString(policyOut.PolicyDocument))
		}
	}
	return policies, nil
}

func (r *realIAMUserAPI) listManagedPolicies(ctx context.Context, userName string) ([]string, error) {
	var arns []string
	paginator := iamsdk.NewListAttachedUserPoliciesPaginator(r.client, &iamsdk.ListAttachedUserPoliciesInput{UserName: aws.String(userName)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, policy := range page.AttachedPolicies {
			arns = append(arns, aws.ToString(policy.PolicyArn))
		}
	}
	sort.Strings(arns)
	return arns, nil
}

func (r *realIAMUserAPI) listGroups(ctx context.Context, userName string) ([]string, error) {
	var groups []string
	paginator := iamsdk.NewListGroupsForUserPaginator(r.client, &iamsdk.ListGroupsForUserInput{UserName: aws.String(userName)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, group := range page.Groups {
			groups = append(groups, aws.ToString(group.GroupName))
		}
	}
	sort.Strings(groups)
	return groups, nil
}

func (r *realIAMUserAPI) listUserTags(ctx context.Context, userName string) (map[string]string, error) {
	tags := map[string]string{}
	paginator := iamsdk.NewListUserTagsPaginator(r.client, &iamsdk.ListUserTagsInput{UserName: aws.String(userName)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, tag := range page.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}
	return tags, nil
}

func toIAMTags(tags map[string]string) []iamtypes.Tag {
	filtered := filterPraxisTags(tags)
	awsTags := make([]iamtypes.Tag, 0, len(filtered))
	for key, value := range filtered {
		awsTags = append(awsTags, iamtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	sort.Slice(awsTags, func(i, j int) bool {
		return aws.ToString(awsTags[i].Key) < aws.ToString(awsTags[j].Key)
	})
	return awsTags
}

func normalizePolicyDocument(doc string) string {
	decoded, err := url.QueryUnescape(doc)
	if err != nil {
		decoded = doc
	}
	var parsed any
	if err := json.Unmarshal([]byte(decoded), &parsed); err != nil {
		return decoded
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return decoded
	}
	return string(canonical)
}

func IsNotFound(err error) bool {
	return awserr.HasCode(err, "NoSuchEntity")
}

func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "EntityAlreadyExists")
}

func IsDeleteConflict(err error) bool {
	return awserr.HasCode(err, "DeleteConflict")
}

func IsMalformedPolicy(err error) bool {
	return awserr.HasCode(err, "MalformedPolicyDocument")
}

func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "LimitExceeded")
}
