// Package ecrrepo – aws.go
//
// This file contains the AWS API abstraction layer for AWS ECR Repository.
// It defines the ECRRepositoryAPI interface (used for testing with mocks)
// and the real implementation that calls Amazon Elastic Container Registry (ECR) through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package ecrrepo

import (
	"context"
	"errors"
	"fmt"
	"github.com/shirvan/praxis/internal/drivers"
	"maps"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/smithy-go"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// RepositoryAPI abstracts all Amazon Elastic Container Registry (ECR) SDK operations needed
// to manage a AWS ECR Repository. The real implementation calls AWS;
// tests supply a mock to verify driver logic without network calls.
type RepositoryAPI interface {
	CreateRepository(ctx context.Context, spec ECRRepositorySpec) (ObservedState, error)
	DescribeRepository(ctx context.Context, name string) (ObservedState, error)
	DeleteRepository(ctx context.Context, name string, force bool) error
	UpdateImageTagMutability(ctx context.Context, name, value string) error
	UpdateScanningConfiguration(ctx context.Context, name string, cfg *ImageScanningConfiguration) error
	PutRepositoryPolicy(ctx context.Context, name, policy string) error
	DeleteRepositoryPolicy(ctx context.Context, name string) error
	UpdateTags(ctx context.Context, arn string, tags map[string]string) error
}

type realRepositoryAPI struct {
	client  *ecrsdk.Client
	limiter *ratelimit.Limiter
}

// NewRepositoryAPI constructs a production ECRRepositoryAPI backed by the given
// AWS SDK client, with built-in rate limiting to avoid throttling.
func NewRepositoryAPI(client *ecrsdk.Client) RepositoryAPI {
	return &realRepositoryAPI{client: client, limiter: ratelimit.New("ecr-repository", 15, 5)}
}

// CreateRepository calls Amazon Elastic Container Registry (ECR) to create a new AWS ECR Repository from the given spec.
func (r *realRepositoryAPI) CreateRepository(ctx context.Context, spec ECRRepositorySpec) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	input := &ecrsdk.CreateRepositoryInput{
		RepositoryName:     aws.String(spec.RepositoryName),
		ImageTagMutability: imageTagMutability(spec.ImageTagMutability),
		Tags:               awsTags(tagsForApply(spec.Tags, spec.ManagedKey)),
	}
	if spec.ImageScanningConfiguration != nil {
		input.ImageScanningConfiguration = &ecrtypes.ImageScanningConfiguration{ScanOnPush: spec.ImageScanningConfiguration.ScanOnPush}
	}
	if spec.EncryptionConfiguration != nil {
		input.EncryptionConfiguration = &ecrtypes.EncryptionConfiguration{EncryptionType: ecrtypes.EncryptionType(spec.EncryptionConfiguration.EncryptionType)}
		if spec.EncryptionConfiguration.KmsKey != "" {
			input.EncryptionConfiguration.KmsKey = aws.String(spec.EncryptionConfiguration.KmsKey)
		}
	}
	_, err := r.client.CreateRepository(ctx, input)
	if err != nil {
		return ObservedState{}, err
	}
	return r.DescribeRepository(ctx, spec.RepositoryName)
}

// DescribeRepository reads the current state of the AWS ECR Repository from Amazon Elastic Container Registry (ECR).
func (r *realRepositoryAPI) DescribeRepository(ctx context.Context, name string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.DescribeRepositories(ctx, &ecrsdk.DescribeRepositoriesInput{RepositoryNames: []string{name}})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.Repositories) == 0 {
		return ObservedState{}, fmt.Errorf("repository %s not found", name)
	}
	repo := out.Repositories[0]
	observed := observedFromRepository(repo)
	if observed.RepositoryArn != "" {
		tags, tagErr := r.listTags(ctx, observed.RepositoryArn)
		if tagErr != nil {
			return ObservedState{}, tagErr
		}
		observed.Tags = tags
	}
	policy, policyErr := r.getRepositoryPolicy(ctx, name)
	if policyErr != nil && !IsRepositoryPolicyNotFound(policyErr) {
		return ObservedState{}, policyErr
	}
	if policyErr == nil {
		observed.RepositoryPolicy = policy
	}
	return observed, nil
}

// DeleteRepository removes the AWS ECR Repository from AWS via Amazon Elastic Container Registry (ECR).
func (r *realRepositoryAPI) DeleteRepository(ctx context.Context, name string, force bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRepository(ctx, &ecrsdk.DeleteRepositoryInput{RepositoryName: aws.String(name), Force: force})
	return err
}

// UpdateImageTagMutability updates mutable properties of the AWS ECR Repository via Amazon Elastic Container Registry (ECR).
func (r *realRepositoryAPI) UpdateImageTagMutability(ctx context.Context, name, value string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.PutImageTagMutability(ctx, &ecrsdk.PutImageTagMutabilityInput{RepositoryName: aws.String(name), ImageTagMutability: imageTagMutability(value)})
	return err
}

// UpdateScanningConfiguration updates mutable properties of the AWS ECR Repository via Amazon Elastic Container Registry (ECR).
func (r *realRepositoryAPI) UpdateScanningConfiguration(ctx context.Context, name string, cfg *ImageScanningConfiguration) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	scanOnPush := false
	if cfg != nil {
		scanOnPush = cfg.ScanOnPush
	}
	_, err := r.client.PutImageScanningConfiguration(ctx, &ecrsdk.PutImageScanningConfigurationInput{
		RepositoryName:             aws.String(name),
		ImageScanningConfiguration: &ecrtypes.ImageScanningConfiguration{ScanOnPush: scanOnPush},
	})
	return err
}

func (r *realRepositoryAPI) PutRepositoryPolicy(ctx context.Context, name, policy string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.SetRepositoryPolicy(ctx, &ecrsdk.SetRepositoryPolicyInput{RepositoryName: aws.String(name), PolicyText: aws.String(policy), Force: true})
	return err
}

// DeleteRepositoryPolicy removes the AWS ECR Repository from AWS via Amazon Elastic Container Registry (ECR).
func (r *realRepositoryAPI) DeleteRepositoryPolicy(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteRepositoryPolicy(ctx, &ecrsdk.DeleteRepositoryPolicyInput{RepositoryName: aws.String(name)})
	return err
}

// UpdateTags updates mutable properties of the AWS ECR Repository via Amazon Elastic Container Registry (ECR).
func (r *realRepositoryAPI) UpdateTags(ctx context.Context, arn string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	current, err := r.listTags(ctx, arn)
	if err != nil {
		return err
	}
	desired := make(map[string]string, len(tags))
	maps.Copy(desired, tags)
	if managedKey := current["praxis:managed-key"]; managedKey != "" {
		desired["praxis:managed-key"] = managedKey
	}
	keysToRemove := make([]string, 0)
	for key := range current {
		if _, keep := desired[key]; !keep {
			keysToRemove = append(keysToRemove, key)
		}
	}
	if len(keysToRemove) > 0 {
		if _, err := r.client.UntagResource(ctx, &ecrsdk.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: keysToRemove}); err != nil {
			return err
		}
	}
	if len(desired) > 0 {
		if _, err := r.client.TagResource(ctx, &ecrsdk.TagResourceInput{ResourceArn: aws.String(arn), Tags: awsTags(desired)}); err != nil {
			return err
		}
	}
	return nil
}

func (r *realRepositoryAPI) getRepositoryPolicy(ctx context.Context, name string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.GetRepositoryPolicy(ctx, &ecrsdk.GetRepositoryPolicyInput{RepositoryName: aws.String(name)})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.PolicyText), nil
}

func (r *realRepositoryAPI) listTags(ctx context.Context, arn string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListTagsForResource(ctx, &ecrsdk.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string, len(out.Tags))
	for _, tag := range out.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags, nil
}

func observedFromRepository(repo ecrtypes.Repository) ObservedState {
	observed := ObservedState{
		RepositoryArn:      aws.ToString(repo.RepositoryArn),
		RepositoryName:     aws.ToString(repo.RepositoryName),
		RepositoryUri:      aws.ToString(repo.RepositoryUri),
		RegistryId:         aws.ToString(repo.RegistryId),
		ImageTagMutability: string(repo.ImageTagMutability),
		Tags:               map[string]string{},
	}
	if repo.ImageScanningConfiguration != nil {
		observed.ImageScanningConfiguration = &ImageScanningConfiguration{ScanOnPush: repo.ImageScanningConfiguration.ScanOnPush}
	}
	if repo.EncryptionConfiguration != nil {
		observed.EncryptionConfiguration = &EncryptionConfiguration{EncryptionType: string(repo.EncryptionConfiguration.EncryptionType), KmsKey: aws.ToString(repo.EncryptionConfiguration.KmsKey)}
	}
	return observed
}

func outputsFromObserved(observed ObservedState) ECRRepositoryOutputs {
	return ECRRepositoryOutputs{RepositoryArn: observed.RepositoryArn, RepositoryName: observed.RepositoryName, RepositoryUri: observed.RepositoryUri, RegistryId: observed.RegistryId}
}

func specFromObserved(observed ObservedState) ECRRepositorySpec {
	return applyDefaults(ECRRepositorySpec{
		Region:                     regionFromRepositoryARN(observed.RepositoryArn),
		RepositoryName:             observed.RepositoryName,
		ImageTagMutability:         observed.ImageTagMutability,
		ImageScanningConfiguration: observed.ImageScanningConfiguration,
		EncryptionConfiguration:    observed.EncryptionConfiguration,
		RepositoryPolicy:           observed.RepositoryPolicy,
		Tags:                       drivers.FilterPraxisTags(observed.Tags),
	})
}

func imageTagMutability(value string) ecrtypes.ImageTagMutability {
	if value == "IMMUTABLE" {
		return ecrtypes.ImageTagMutabilityImmutable
	}
	return ecrtypes.ImageTagMutabilityMutable
}

func awsTags(tags map[string]string) []ecrtypes.Tag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]ecrtypes.Tag, 0, len(tags))
	for key, value := range tags {
		keyCopy := key
		valueCopy := value
		out = append(out, ecrtypes.Tag{Key: &keyCopy, Value: &valueCopy})
	}
	return out
}

func tagsForApply(tags map[string]string, managedKey string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, tags)
	if managedKey != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

func regionFromRepositoryARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) > 3 {
		return parts[3]
	}
	return ""
}

// IsNotFound returns true if the AWS error indicates the AWS ECR Repository does not exist.
func IsNotFound(err error) bool {
	if awserr.HasCode(err, "RepositoryNotFoundException") {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "RepositoryNotFoundException"
	}
	return strings.Contains(err.Error(), "not found")
}

func IsConflict(err error) bool {
	return awserr.HasCode(err, "RepositoryAlreadyExistsException")
}

func IsInvalidParameter(err error) bool {
	return awserr.HasCode(err, "InvalidParameterException", "ValidationException", "InvalidTagParameterException")
}

func IsRepositoryNotEmpty(err error) bool {
	return awserr.HasCode(err, "RepositoryNotEmptyException")
}

func IsRepositoryPolicyNotFound(err error) bool {
	return awserr.HasCode(err, "RepositoryPolicyNotFoundException")
}
