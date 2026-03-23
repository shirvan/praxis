package iampolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/praxiscloud/praxis/internal/infra/ratelimit"
)

var errNotFound = errors.New("iam policy not found")

type IAMPolicyAPI interface {
	CreatePolicy(ctx context.Context, spec IAMPolicySpec) (arn, policyID string, err error)
	DescribePolicy(ctx context.Context, policyArn string) (ObservedState, error)
	DescribePolicyByName(ctx context.Context, policyName, path string) (ObservedState, error)
	DeletePolicy(ctx context.Context, policyArn string) error
	CreatePolicyVersion(ctx context.Context, policyArn, policyDocument string) error
	GetPolicyDocument(ctx context.Context, policyArn, versionID string) (string, error)
	ListPolicyVersions(ctx context.Context, policyArn string) ([]PolicyVersionInfo, error)
	DeletePolicyVersion(ctx context.Context, policyArn, versionID string) error
	DetachAllPrincipals(ctx context.Context, policyArn string) error
	UpdateTags(ctx context.Context, policyArn string, tags map[string]string) error
}

type PolicyVersionInfo struct {
	VersionID        string
	IsDefaultVersion bool
	CreateDate       time.Time
}

type realIAMPolicyAPI struct {
	client  *iamsdk.Client
	limiter *ratelimit.Limiter
}

func NewIAMPolicyAPI(client *iamsdk.Client) IAMPolicyAPI {
	return &realIAMPolicyAPI{
		client:  client,
		limiter: ratelimit.New("iam", 15, 8),
	}
}

func (r *realIAMPolicyAPI) CreatePolicy(ctx context.Context, spec IAMPolicySpec) (string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", err
	}
	input := &iamsdk.CreatePolicyInput{
		PolicyName:     aws.String(spec.PolicyName),
		Path:           aws.String(spec.Path),
		PolicyDocument: aws.String(spec.PolicyDocument),
	}
	if spec.Description != "" {
		input.Description = aws.String(spec.Description)
	}
	if len(spec.Tags) > 0 {
		input.Tags = toIAMTags(spec.Tags)
	}
	out, err := r.client.CreatePolicy(ctx, input)
	if err != nil {
		return "", "", err
	}
	return aws.ToString(out.Policy.Arn), aws.ToString(out.Policy.PolicyId), nil
}

func (r *realIAMPolicyAPI) DescribePolicy(ctx context.Context, policyArn string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	policyOut, err := r.client.GetPolicy(ctx, &iamsdk.GetPolicyInput{PolicyArn: aws.String(policyArn)})
	if err != nil {
		return ObservedState{}, err
	}
	pol := policyOut.Policy

	doc, err := r.GetPolicyDocument(ctx, policyArn, aws.ToString(pol.DefaultVersionId))
	if err != nil {
		return ObservedState{}, err
	}
	tags, err := r.listPolicyTags(ctx, policyArn)
	if err != nil {
		return ObservedState{}, err
	}

	observed := ObservedState{
		Arn:              aws.ToString(pol.Arn),
		PolicyId:         aws.ToString(pol.PolicyId),
		PolicyName:       aws.ToString(pol.PolicyName),
		Path:             aws.ToString(pol.Path),
		Description:      aws.ToString(pol.Description),
		PolicyDocument:   doc,
		DefaultVersionId: aws.ToString(pol.DefaultVersionId),
		AttachmentCount:  aws.ToInt32(pol.AttachmentCount),
		Tags:             tags,
	}
	if pol.CreateDate != nil {
		observed.CreateDate = pol.CreateDate.UTC().Format(time.RFC3339)
	}
	if pol.UpdateDate != nil {
		observed.UpdateDate = pol.UpdateDate.UTC().Format(time.RFC3339)
	}
	return observed, nil
}

func (r *realIAMPolicyAPI) DescribePolicyByName(ctx context.Context, policyName, path string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	input := &iamsdk.ListPoliciesInput{Scope: iamtypes.PolicyScopeTypeLocal}
	if strings.TrimSpace(path) != "" {
		input.PathPrefix = aws.String(path)
	}
	paginator := iamsdk.NewListPoliciesPaginator(r.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return ObservedState{}, err
		}
		for _, policy := range page.Policies {
			if aws.ToString(policy.PolicyName) == policyName {
				return r.DescribePolicy(ctx, aws.ToString(policy.Arn))
			}
		}
	}
	return ObservedState{}, fmt.Errorf("policy %q not found: %w", policyName, errNotFound)
}

func (r *realIAMPolicyAPI) DeletePolicy(ctx context.Context, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeletePolicy(ctx, &iamsdk.DeletePolicyInput{PolicyArn: aws.String(policyArn)})
	return err
}

func (r *realIAMPolicyAPI) CreatePolicyVersion(ctx context.Context, policyArn, policyDocument string) error {
	versions, err := r.ListPolicyVersions(ctx, policyArn)
	if err != nil {
		return err
	}
	if len(versions) >= 5 {
		oldest := findOldestNonDefault(versions)
		if oldest != "" {
			if err := r.DeletePolicyVersion(ctx, policyArn, oldest); err != nil {
				return err
			}
		}
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.CreatePolicyVersion(ctx, &iamsdk.CreatePolicyVersionInput{
		PolicyArn:      aws.String(policyArn),
		PolicyDocument: aws.String(policyDocument),
		SetAsDefault:   true,
	})
	return err
}

func (r *realIAMPolicyAPI) GetPolicyDocument(ctx context.Context, policyArn, versionID string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.GetPolicyVersion(ctx, &iamsdk.GetPolicyVersionInput{
		PolicyArn: aws.String(policyArn),
		VersionId: aws.String(versionID),
	})
	if err != nil {
		return "", err
	}
	return normalizePolicyDocument(aws.ToString(out.PolicyVersion.Document)), nil
}

func (r *realIAMPolicyAPI) ListPolicyVersions(ctx context.Context, policyArn string) ([]PolicyVersionInfo, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListPolicyVersions(ctx, &iamsdk.ListPolicyVersionsInput{PolicyArn: aws.String(policyArn)})
	if err != nil {
		return nil, err
	}
	versions := make([]PolicyVersionInfo, 0, len(out.Versions))
	for _, version := range out.Versions {
		createDate := time.Time{}
		if version.CreateDate != nil {
			createDate = version.CreateDate.UTC()
		}
		versions = append(versions, PolicyVersionInfo{
			VersionID:        aws.ToString(version.VersionId),
			IsDefaultVersion: version.IsDefaultVersion,
			CreateDate:       createDate,
		})
	}
	return versions, nil
}

func (r *realIAMPolicyAPI) DeletePolicyVersion(ctx context.Context, policyArn, versionID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeletePolicyVersion(ctx, &iamsdk.DeletePolicyVersionInput{
		PolicyArn: aws.String(policyArn),
		VersionId: aws.String(versionID),
	})
	return err
}

func (r *realIAMPolicyAPI) DetachAllPrincipals(ctx context.Context, policyArn string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	paginator := iamsdk.NewListEntitiesForPolicyPaginator(r.client, &iamsdk.ListEntitiesForPolicyInput{
		PolicyArn: aws.String(policyArn),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, role := range page.PolicyRoles {
			if err := r.detachRolePolicy(ctx, policyArn, aws.ToString(role.RoleName)); err != nil {
				return err
			}
		}
		for _, user := range page.PolicyUsers {
			if err := r.detachUserPolicy(ctx, policyArn, aws.ToString(user.UserName)); err != nil {
				return err
			}
		}
		for _, group := range page.PolicyGroups {
			if err := r.detachGroupPolicy(ctx, policyArn, aws.ToString(group.GroupName)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *realIAMPolicyAPI) UpdateTags(ctx context.Context, policyArn string, tags map[string]string) error {
	existing, err := r.listPolicyTags(ctx, policyArn)
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
		_, err := r.client.UntagPolicy(ctx, &iamsdk.UntagPolicyInput{
			PolicyArn: aws.String(policyArn),
			TagKeys:   remove,
		})
		if err != nil {
			return err
		}
	}

	add := make(map[string]string)
	for key, value := range desired {
		if observedValue, ok := existing[key]; !ok || observedValue != value {
			add[key] = value
		}
	}
	if len(add) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err := r.client.TagPolicy(ctx, &iamsdk.TagPolicyInput{
			PolicyArn: aws.String(policyArn),
			Tags:      toIAMTags(add),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *realIAMPolicyAPI) listPolicyTags(ctx context.Context, policyArn string) (map[string]string, error) {
	tags := map[string]string{}
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	paginator := iamsdk.NewListPolicyTagsPaginator(r.client, &iamsdk.ListPolicyTagsInput{PolicyArn: aws.String(policyArn)})
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

func (r *realIAMPolicyAPI) detachRolePolicy(ctx context.Context, policyArn, roleName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DetachRolePolicy(ctx, &iamsdk.DetachRolePolicyInput{
		PolicyArn: aws.String(policyArn),
		RoleName:  aws.String(roleName),
	})
	return err
}

func (r *realIAMPolicyAPI) detachUserPolicy(ctx context.Context, policyArn, userName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DetachUserPolicy(ctx, &iamsdk.DetachUserPolicyInput{
		PolicyArn: aws.String(policyArn),
		UserName:  aws.String(userName),
	})
	return err
}

func (r *realIAMPolicyAPI) detachGroupPolicy(ctx context.Context, policyArn, groupName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DetachGroupPolicy(ctx, &iamsdk.DetachGroupPolicyInput{
		PolicyArn: aws.String(policyArn),
		GroupName: aws.String(groupName),
	})
	return err
}

func findOldestNonDefault(versions []PolicyVersionInfo) string {
	var oldest *PolicyVersionInfo
	for i := range versions {
		version := versions[i]
		if version.IsDefaultVersion {
			continue
		}
		if oldest == nil || version.CreateDate.Before(oldest.CreateDate) {
			oldest = &version
		}
	}
	if oldest == nil {
		return ""
	}
	return oldest.VersionID
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
	if err == nil {
		return false
	}
	if errors.Is(err, errNotFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "NoSuchEntity"
	}
	return strings.Contains(err.Error(), "NoSuchEntity")
}

func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "EntityAlreadyExists"
	}
	return strings.Contains(err.Error(), "EntityAlreadyExists")
}

func IsDeleteConflict(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "DeleteConflict"
	}
	return strings.Contains(err.Error(), "DeleteConflict")
}

func IsMalformedPolicy(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "MalformedPolicyDocument"
	}
	return strings.Contains(err.Error(), "MalformedPolicyDocument")
}

func IsVersionLimitExceeded(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "LimitExceeded"
	}
	return strings.Contains(err.Error(), "LimitExceeded")
}
