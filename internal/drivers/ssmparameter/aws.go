// Package ssmparameter – aws.go
//
// This file contains the AWS API abstraction layer for SSM parameters.
// It defines the SSMParameterAPI interface (used for testing with mocks)
// and the real implementation that calls AWS Systems Manager through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package ssmparameter

import (
	"context"
	"maps"
	"sort"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// SSMParameterAPI abstracts all AWS Systems Manager SDK operations needed
// to manage an SSM parameter. The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type SSMParameterAPI interface {
	PutParameter(ctx context.Context, spec SSMParameterSpec, overwrite bool) (int64, error)
	DescribeParameter(ctx context.Context, name string) (ObservedState, bool, error)
	DeleteParameter(ctx context.Context, name string) error
	AddTags(ctx context.Context, name string, tags map[string]string) error
	RemoveTags(ctx context.Context, name string, tagKeys []string) error
	ListTags(ctx context.Context, name string) (map[string]string, error)
}

// SSMParameterMetadataAPI is the least-privilege read surface used by
// data-source lookup. It never reads or decrypts the parameter value.
type SSMParameterMetadataAPI interface {
	DescribeParameterMetadata(ctx context.Context, name string) (ObservedState, bool, error)
}

type realSSMParameterAPI struct {
	client  *ssm.Client
	limiter *ratelimit.Limiter
}

// NewSSMParameterAPI constructs a production SSMParameterAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewSSMParameterAPI(client *ssm.Client) SSMParameterAPI {
	return newRealSSMParameterAPI(client)
}

// NewSSMParameterMetadataAPI constructs the metadata-only read surface used
// by provider lookups.
func NewSSMParameterMetadataAPI(client *ssm.Client) SSMParameterMetadataAPI {
	return newRealSSMParameterAPI(client)
}

func newRealSSMParameterAPI(client *ssm.Client) *realSSMParameterAPI {
	return &realSSMParameterAPI{
		client:  client,
		limiter: ratelimit.Shared("ssm-parameter", 10, 5),
	}
}

// PutParameter creates or overwrites the SSM parameter from the given spec.
// AWS rejects Tags combined with Overwrite, so tags are only attached on
// create; updates converge tags separately via AddTags/RemoveTags.
func (r *realSSMParameterAPI) PutParameter(ctx context.Context, spec SSMParameterSpec, overwrite bool) (int64, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return 0, err
	}
	input := &ssm.PutParameterInput{
		Name:      aws.String(spec.ParameterName),
		Value:     aws.String(spec.Value),
		Type:      ssmtypes.ParameterType(spec.Type),
		Overwrite: aws.Bool(overwrite),
	}
	if spec.Description != "" {
		input.Description = aws.String(spec.Description)
	}
	if spec.Tier != "" && spec.Tier != "Standard" {
		input.Tier = ssmtypes.ParameterTier(spec.Tier)
	}
	if spec.KmsKeyID != "" {
		input.KeyId = aws.String(spec.KmsKeyID)
	}
	if spec.AllowedPattern != "" {
		input.AllowedPattern = aws.String(spec.AllowedPattern)
	}
	if spec.DataType != "" && spec.DataType != "text" {
		input.DataType = aws.String(spec.DataType)
	}
	if !overwrite {
		input.Tags = tagList(managedTags(spec.Tags, spec.ManagedKey))
	}
	out, err := r.client.PutParameter(ctx, input)
	if err != nil {
		return 0, err
	}
	return out.Version, nil
}

// DescribeParameter reads the current state of the SSM parameter from AWS.
// GetParameter (with decryption) supplies the ARN, type, value, and version;
// DescribeParameters supplies metadata that GetParameter omits (description,
// tier, KMS key, allowed pattern); ListTags supplies the tag set.
func (r *realSSMParameterAPI) DescribeParameter(ctx context.Context, name string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	got, err := r.client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	observed := ObservedState{
		ARN:           aws.ToString(got.Parameter.ARN),
		ParameterName: aws.ToString(got.Parameter.Name),
		Type:          string(got.Parameter.Type),
		Value:         aws.ToString(got.Parameter.Value),
		DataType:      aws.ToString(got.Parameter.DataType),
		Version:       got.Parameter.Version,
		Tags:          map[string]string{},
	}

	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	desc, err := r.client.DescribeParameters(ctx, &ssm.DescribeParametersInput{
		ParameterFilters: []ssmtypes.ParameterStringFilter{{
			Key:    aws.String("Name"),
			Option: aws.String("Equals"),
			Values: []string{name},
		}},
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	for i := range desc.Parameters {
		meta := &desc.Parameters[i]
		if aws.ToString(meta.Name) != name {
			continue
		}
		observed.Description = aws.ToString(meta.Description)
		observed.Tier = string(meta.Tier)
		observed.KmsKeyID = aws.ToString(meta.KeyId)
		observed.AllowedPattern = aws.ToString(meta.AllowedPattern)
		if observed.DataType == "" {
			observed.DataType = aws.ToString(meta.DataType)
		}
	}

	tags, err := r.ListTags(ctx, name)
	if err != nil && !IsNotFound(err) {
		return ObservedState{}, false, err
	}
	if tags != nil {
		observed.Tags = tags
	}
	return observed, true, nil
}

// DescribeParameterMetadata reads parameter identity, configuration, and tags
// without requesting its value.
func (r *realSSMParameterAPI) DescribeParameterMetadata(ctx context.Context, name string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	desc, err := r.client.DescribeParameters(ctx, &ssm.DescribeParametersInput{
		ParameterFilters: []ssmtypes.ParameterStringFilter{{
			Key:    aws.String("Name"),
			Option: aws.String("Equals"),
			Values: []string{name},
		}},
	})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	var observed ObservedState
	found := false
	for i := range desc.Parameters {
		meta := &desc.Parameters[i]
		if aws.ToString(meta.Name) != name {
			continue
		}
		observed = ObservedState{
			ARN:            aws.ToString(meta.ARN),
			ParameterName:  aws.ToString(meta.Name),
			Type:           string(meta.Type),
			Description:    aws.ToString(meta.Description),
			Tier:           string(meta.Tier),
			KmsKeyID:       aws.ToString(meta.KeyId),
			AllowedPattern: aws.ToString(meta.AllowedPattern),
			DataType:       aws.ToString(meta.DataType),
			Version:        meta.Version,
			Tags:           map[string]string{},
		}
		found = true
		break
	}
	if !found {
		return ObservedState{}, false, nil
	}
	tags, err := r.ListTags(ctx, name)
	if err != nil && !IsNotFound(err) {
		return ObservedState{}, false, err
	}
	if tags != nil {
		observed.Tags = tags
	}
	return observed, true, nil
}

// DeleteParameter removes the SSM parameter from AWS.
func (r *realSSMParameterAPI) DeleteParameter(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)})
	return err
}

func (r *realSSMParameterAPI) AddTags(ctx context.Context, name string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AddTagsToResource(ctx, &ssm.AddTagsToResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String(name),
		Tags:         tagList(tags),
	})
	return err
}

func (r *realSSMParameterAPI) RemoveTags(ctx context.Context, name string, tagKeys []string) error {
	if len(tagKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RemoveTagsFromResource(ctx, &ssm.RemoveTagsFromResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String(name),
		TagKeys:      tagKeys,
	})
	return err
}

// ListTags enumerates the tags attached to the SSM parameter.
func (r *realSSMParameterAPI) ListTags(ctx context.Context, name string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String(name),
	})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string, len(out.TagList))
	for _, tag := range out.TagList {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags, nil
}

// IsNotFound returns true if the AWS error indicates the SSM parameter does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ParameterNotFound") || awserr.HasCode(err, "InvalidResourceId")
}

func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "ParameterAlreadyExists")
}

func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "ValidationException") || awserr.HasCode(err, "InvalidKeyId") ||
		awserr.HasCode(err, "InvalidAllowedPatternException") || awserr.HasCode(err, "UnsupportedParameterType")
}

func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "ParameterLimitExceeded") || awserr.HasCode(err, "ParameterMaxVersionLimitExceeded")
}

func tagList(tags map[string]string) []ssmtypes.Tag {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ssmtypes.Tag, 0, len(tags))
	for _, key := range keys {
		out = append(out, ssmtypes.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	return out
}

func managedTags(tags map[string]string, managedKey string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, tags)
	if strings.TrimSpace(managedKey) != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

func tagDiff(desired, observed map[string]string, managedKey string) (map[string]string, []string) {
	want := managedTags(drivers.FilterPraxisTags(desired), managedKey)
	have := managedTags(drivers.FilterPraxisTags(observed), managedKey)
	toAdd := map[string]string{}
	for key, value := range want {
		if current, ok := have[key]; !ok || current != value {
			toAdd[key] = value
		}
	}
	var toRemove []string
	for key := range have {
		if _, ok := want[key]; !ok {
			toRemove = append(toRemove, key)
		}
	}
	sort.Strings(toRemove)
	return toAdd, toRemove
}
