package iamgroup

import (
	"context"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type IAMGroupAPI interface {
	CreateGroup(ctx context.Context, spec IAMGroupSpec) (arn, groupID string, err error)
	DescribeGroup(ctx context.Context, groupName string) (ObservedState, error)
	DeleteGroup(ctx context.Context, groupName string) error
	UpdateGroupPath(ctx context.Context, groupName, newPath string) error
	PutInlinePolicy(ctx context.Context, groupName, policyName, policyDocument string) error
	DeleteInlinePolicy(ctx context.Context, groupName, policyName string) error
	AttachManagedPolicy(ctx context.Context, groupName, policyArn string) error
	DetachManagedPolicy(ctx context.Context, groupName, policyArn string) error
	RemoveAllMembers(ctx context.Context, groupName string) error
}

type realIAMGroupAPI struct {
	client  *iamsdk.Client
	limiter *ratelimit.Limiter
}

func NewIAMGroupAPI(client *iamsdk.Client) IAMGroupAPI {
	return &realIAMGroupAPI{
		client:  client,
		limiter: ratelimit.New("iam", 15, 8),
	}
}

func (r *realIAMGroupAPI) CreateGroup(ctx context.Context, spec IAMGroupSpec) (string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", err
	}
	out, err := r.client.CreateGroup(ctx, &iamsdk.CreateGroupInput{
		GroupName: aws.String(spec.GroupName),
		Path:      aws.String(spec.Path),
	})
	if err != nil {
		return "", "", err
	}
	return aws.ToString(out.Group.Arn), aws.ToString(out.Group.GroupId), nil
}

func (r *realIAMGroupAPI) DescribeGroup(ctx context.Context, groupName string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetGroup(ctx, &iamsdk.GetGroupInput{GroupName: aws.String(groupName)})
	if err != nil {
		return ObservedState{}, err
	}

	inlinePolicies, err := r.listInlinePolicies(ctx, groupName)
	if err != nil {
		return ObservedState{}, err
	}
	managedPolicyArns, err := r.listManagedPolicies(ctx, groupName)
	if err != nil {
		return ObservedState{}, err
	}

	group := out.Group
	observed := ObservedState{
		Arn:               aws.ToString(group.Arn),
		GroupId:           aws.ToString(group.GroupId),
		GroupName:         aws.ToString(group.GroupName),
		Path:              aws.ToString(group.Path),
		InlinePolicies:    inlinePolicies,
		ManagedPolicyArns: managedPolicyArns,
	}
	if group.CreateDate != nil {
		observed.CreateDate = group.CreateDate.UTC().Format(time.RFC3339)
	}
	return observed, nil
}

func (r *realIAMGroupAPI) DeleteGroup(ctx context.Context, groupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteGroup(ctx, &iamsdk.DeleteGroupInput{GroupName: aws.String(groupName)})
	return err
}

func (r *realIAMGroupAPI) UpdateGroupPath(ctx context.Context, groupName, newPath string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateGroup(ctx, &iamsdk.UpdateGroupInput{GroupName: aws.String(groupName), NewPath: aws.String(newPath)})
	return err
}

func (r *realIAMGroupAPI) PutInlinePolicy(ctx context.Context, groupName, policyName, policyDocument string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.PutGroupPolicy(ctx, &iamsdk.PutGroupPolicyInput{
		GroupName:      aws.String(groupName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(policyDocument),
	})
	return err
}

func (r *realIAMGroupAPI) DeleteInlinePolicy(ctx context.Context, groupName, policyName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteGroupPolicy(ctx, &iamsdk.DeleteGroupPolicyInput{GroupName: aws.String(groupName), PolicyName: aws.String(policyName)})
	return err
}

func (r *realIAMGroupAPI) AttachManagedPolicy(ctx context.Context, groupName, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AttachGroupPolicy(ctx, &iamsdk.AttachGroupPolicyInput{GroupName: aws.String(groupName), PolicyArn: aws.String(policyArn)})
	return err
}

func (r *realIAMGroupAPI) DetachManagedPolicy(ctx context.Context, groupName, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DetachGroupPolicy(ctx, &iamsdk.DetachGroupPolicyInput{GroupName: aws.String(groupName), PolicyArn: aws.String(policyArn)})
	return err
}

func (r *realIAMGroupAPI) RemoveAllMembers(ctx context.Context, groupName string) error {
	paginator := iamsdk.NewGetGroupPaginator(r.client, &iamsdk.GetGroupInput{GroupName: aws.String(groupName)})
	for paginator.HasMorePages() {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, user := range page.Users {
			if err := r.limiter.Wait(ctx); err != nil {
				return err
			}
			_, err := r.client.RemoveUserFromGroup(ctx, &iamsdk.RemoveUserFromGroupInput{
				GroupName: aws.String(groupName),
				UserName:  user.UserName,
			})
			if err != nil && !IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func (r *realIAMGroupAPI) listInlinePolicies(ctx context.Context, groupName string) (map[string]string, error) {
	paginator := iamsdk.NewListGroupPoliciesPaginator(r.client, &iamsdk.ListGroupPoliciesInput{GroupName: aws.String(groupName)})
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
			policyOut, err := r.client.GetGroupPolicy(ctx, &iamsdk.GetGroupPolicyInput{GroupName: aws.String(groupName), PolicyName: aws.String(policyName)})
			if err != nil {
				return nil, err
			}
			policies[policyName] = normalizePolicyDocument(aws.ToString(policyOut.PolicyDocument))
		}
	}
	return policies, nil
}

func (r *realIAMGroupAPI) listManagedPolicies(ctx context.Context, groupName string) ([]string, error) {
	paginator := iamsdk.NewListAttachedGroupPoliciesPaginator(r.client, &iamsdk.ListAttachedGroupPoliciesInput{GroupName: aws.String(groupName)})
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
