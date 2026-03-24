package iamrole

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type IAMRoleAPI interface {
	CreateRole(ctx context.Context, spec IAMRoleSpec) (arn, roleID string, err error)
	DescribeRole(ctx context.Context, roleName string) (ObservedState, error)
	FindByTags(ctx context.Context, tags map[string]string) (string, error)
	DeleteRole(ctx context.Context, roleName string) error
	UpdateAssumeRolePolicy(ctx context.Context, roleName, policyDocument string) error
	UpdateRole(ctx context.Context, roleName, description string, maxSessionDuration int32) error
	PutPermissionsBoundary(ctx context.Context, roleName, policyArn string) error
	DeletePermissionsBoundary(ctx context.Context, roleName string) error
	PutInlinePolicy(ctx context.Context, roleName, policyName, policyDocument string) error
	DeleteInlinePolicy(ctx context.Context, roleName, policyName string) error
	AttachManagedPolicy(ctx context.Context, roleName, policyArn string) error
	DetachManagedPolicy(ctx context.Context, roleName, policyArn string) error
	UpdateTags(ctx context.Context, roleName string, tags map[string]string) error
	ListInstanceProfilesForRole(ctx context.Context, roleName string) ([]string, error)
	RemoveRoleFromInstanceProfile(ctx context.Context, roleName, profileName string) error
}

type realIAMRoleAPI struct {
	client  *iamsdk.Client
	limiter *ratelimit.Limiter
}

func NewIAMRoleAPI(client *iamsdk.Client) IAMRoleAPI {
	return &realIAMRoleAPI{
		client:  client,
		limiter: ratelimit.New("iam", 15, 8),
	}
}

func (r *realIAMRoleAPI) CreateRole(ctx context.Context, spec IAMRoleSpec) (string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", err
	}
	input := &iamsdk.CreateRoleInput{
		RoleName:                 aws.String(spec.RoleName),
		Path:                     aws.String(spec.Path),
		AssumeRolePolicyDocument: aws.String(spec.AssumeRolePolicyDocument),
		MaxSessionDuration:       aws.Int32(spec.MaxSessionDuration),
	}
	if spec.Description != "" {
		input.Description = aws.String(spec.Description)
	}
	if spec.PermissionsBoundary != "" {
		input.PermissionsBoundary = aws.String(spec.PermissionsBoundary)
	}
	if len(spec.Tags) > 0 {
		input.Tags = toIAMTags(spec.Tags)
	}
	out, err := r.client.CreateRole(ctx, input)
	if err != nil {
		return "", "", err
	}
	return aws.ToString(out.Role.Arn), aws.ToString(out.Role.RoleId), nil
}

func (r *realIAMRoleAPI) DescribeRole(ctx context.Context, roleName string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetRole(ctx, &iamsdk.GetRoleInput{RoleName: aws.String(roleName)})
	if err != nil {
		return ObservedState{}, err
	}
	role := out.Role

	inlinePolicies, err := r.listInlinePolicies(ctx, roleName)
	if err != nil {
		return ObservedState{}, err
	}
	managedPolicyArns, err := r.listManagedPolicies(ctx, roleName)
	if err != nil {
		return ObservedState{}, err
	}
	tags, err := r.listRoleTags(ctx, roleName)
	if err != nil {
		return ObservedState{}, err
	}

	observed := ObservedState{
		Arn:                      aws.ToString(role.Arn),
		RoleId:                   aws.ToString(role.RoleId),
		RoleName:                 aws.ToString(role.RoleName),
		Path:                     aws.ToString(role.Path),
		AssumeRolePolicyDocument: normalizePolicyDocument(aws.ToString(role.AssumeRolePolicyDocument)),
		Description:              aws.ToString(role.Description),
		MaxSessionDuration:       aws.ToInt32(role.MaxSessionDuration),
		InlinePolicies:           inlinePolicies,
		ManagedPolicyArns:        managedPolicyArns,
		Tags:                     tags,
	}
	if role.PermissionsBoundary != nil {
		observed.PermissionsBoundary = aws.ToString(role.PermissionsBoundary.PermissionsBoundaryArn)
	}
	if role.CreateDate != nil {
		observed.CreateDate = role.CreateDate.UTC().Format(time.RFC3339)
	}
	return observed, nil
}

func (r *realIAMRoleAPI) FindByTags(ctx context.Context, tags map[string]string) (string, error) {
	paginator := iamsdk.NewListRolesPaginator(r.client, &iamsdk.ListRolesInput{})
	var matches []string
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return "", err
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return "", err
		}
		for _, role := range page.Roles {
			roleName := aws.ToString(role.RoleName)
			roleTags, err := r.listRoleTags(ctx, roleName)
			if err != nil {
				return "", err
			}
			matched := true
			for key, value := range tags {
				if roleTags[key] != value {
					matched = false
					break
				}
			}
			if matched {
				matches = append(matches, roleName)
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous lookup: %d IAM roles match the given tag filters", len(matches))
	}
}

func (r *realIAMRoleAPI) DeleteRole(ctx context.Context, roleName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRole(ctx, &iamsdk.DeleteRoleInput{RoleName: aws.String(roleName)})
	return err
}

func (r *realIAMRoleAPI) UpdateAssumeRolePolicy(ctx context.Context, roleName, policyDocument string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateAssumeRolePolicy(ctx, &iamsdk.UpdateAssumeRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyDocument: aws.String(policyDocument),
	})
	return err
}

func (r *realIAMRoleAPI) UpdateRole(ctx context.Context, roleName, description string, maxSessionDuration int32) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateRole(ctx, &iamsdk.UpdateRoleInput{
		RoleName:           aws.String(roleName),
		Description:        aws.String(description),
		MaxSessionDuration: aws.Int32(maxSessionDuration),
	})
	return err
}

func (r *realIAMRoleAPI) PutPermissionsBoundary(ctx context.Context, roleName, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.PutRolePermissionsBoundary(ctx, &iamsdk.PutRolePermissionsBoundaryInput{
		RoleName:            aws.String(roleName),
		PermissionsBoundary: aws.String(policyArn),
	})
	return err
}

func (r *realIAMRoleAPI) DeletePermissionsBoundary(ctx context.Context, roleName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRolePermissionsBoundary(ctx, &iamsdk.DeleteRolePermissionsBoundaryInput{RoleName: aws.String(roleName)})
	return err
}

func (r *realIAMRoleAPI) PutInlinePolicy(ctx context.Context, roleName, policyName, policyDocument string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.PutRolePolicy(ctx, &iamsdk.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(policyDocument),
	})
	return err
}

func (r *realIAMRoleAPI) DeleteInlinePolicy(ctx context.Context, roleName, policyName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRolePolicy(ctx, &iamsdk.DeleteRolePolicyInput{
		RoleName:   aws.String(roleName),
		PolicyName: aws.String(policyName),
	})
	return err
}

func (r *realIAMRoleAPI) AttachManagedPolicy(ctx context.Context, roleName, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AttachRolePolicy(ctx, &iamsdk.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(policyArn),
	})
	return err
}

func (r *realIAMRoleAPI) DetachManagedPolicy(ctx context.Context, roleName, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DetachRolePolicy(ctx, &iamsdk.DetachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(policyArn),
	})
	return err
}

func (r *realIAMRoleAPI) UpdateTags(ctx context.Context, roleName string, tags map[string]string) error {
	existing, err := r.listRoleTags(ctx, roleName)
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
		_, err := r.client.UntagRole(ctx, &iamsdk.UntagRoleInput{RoleName: aws.String(roleName), TagKeys: remove})
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
		_, err := r.client.TagRole(ctx, &iamsdk.TagRoleInput{RoleName: aws.String(roleName), Tags: toIAMTags(add)})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *realIAMRoleAPI) ListInstanceProfilesForRole(ctx context.Context, roleName string) ([]string, error) {
	paginator := iamsdk.NewListInstanceProfilesForRolePaginator(r.client, &iamsdk.ListInstanceProfilesForRoleInput{RoleName: aws.String(roleName)})
	var names []string
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, profile := range page.InstanceProfiles {
			names = append(names, aws.ToString(profile.InstanceProfileName))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (r *realIAMRoleAPI) RemoveRoleFromInstanceProfile(ctx context.Context, roleName, profileName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RemoveRoleFromInstanceProfile(ctx, &iamsdk.RemoveRoleFromInstanceProfileInput{
		RoleName:            aws.String(roleName),
		InstanceProfileName: aws.String(profileName),
	})
	return err
}

func (r *realIAMRoleAPI) listInlinePolicies(ctx context.Context, roleName string) (map[string]string, error) {
	paginator := iamsdk.NewListRolePoliciesPaginator(r.client, &iamsdk.ListRolePoliciesInput{RoleName: aws.String(roleName)})
	policies := map[string]string{}
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, policyName := range page.PolicyNames {
			if err := r.limiter.Wait(ctx); err != nil {
				return nil, err
			}
			policyOut, err := r.client.GetRolePolicy(ctx, &iamsdk.GetRolePolicyInput{
				RoleName:   aws.String(roleName),
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

func (r *realIAMRoleAPI) listManagedPolicies(ctx context.Context, roleName string) ([]string, error) {
	paginator := iamsdk.NewListAttachedRolePoliciesPaginator(r.client, &iamsdk.ListAttachedRolePoliciesInput{RoleName: aws.String(roleName)})
	var arns []string
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
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

func (r *realIAMRoleAPI) listRoleTags(ctx context.Context, roleName string) (map[string]string, error) {
	paginator := iamsdk.NewListRoleTagsPaginator(r.client, &iamsdk.ListRoleTagsInput{RoleName: aws.String(roleName)})
	tags := map[string]string{}
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
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
