package s3

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// S3API abstracts the AWS S3 SDK operations that the driver uses.
// In production and integration tests, this is realS3API (backed by the real SDK client).
// In unit tests, this is mockS3API (backed by testify/mock).
//
// All methods receive a plain context.Context, NOT a restate.RunContext.
// The caller in driver.go wraps these calls inside restate.Run() which provides
// the journaling guarantee. The AWS wrapper should not know about Restate.
type S3API interface {
	// HeadBucket checks if a bucket exists and is accessible.
	// Returns nil if the bucket exists, an error otherwise.
	HeadBucket(ctx context.Context, name string) error

	// CreateBucket creates a new S3 bucket with the given name and region.
	CreateBucket(ctx context.Context, name, region string) error

	// ConfigureBucket applies versioning, encryption, and tagging to an existing bucket.
	// This is called on both create and update paths, making Provision convergent.
	ConfigureBucket(ctx context.Context, spec S3BucketSpec) error

	// DescribeBucket returns the observed state of a bucket by inspecting
	// its versioning, encryption, and tagging configuration.
	DescribeBucket(ctx context.Context, name string) (ObservedState, error)

	// DeleteBucket removes a bucket. Fails if the bucket is not empty.
	DeleteBucket(ctx context.Context, name string) error

	// FindByTags returns the single bucket whose tags exactly match the provided selectors.
	FindByTags(ctx context.Context, tags map[string]string) (string, error)
}

// realS3API implements S3API using the actual AWS SDK v2 S3 client.
type realS3API struct {
	client  *s3sdk.Client
	limiter *ratelimit.Limiter
}

// NewS3API creates a new S3API backed by the given S3 SDK client.
func NewS3API(client *s3sdk.Client) S3API {
	return &realS3API{
		client:  client,
		limiter: ratelimit.Shared("s3", 100, 20),
	}
}

// HeadBucket calls s3:HeadBucket to check bucket existence.
func (r *realS3API) HeadBucket(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.HeadBucket(ctx, &s3sdk.HeadBucketInput{
		Bucket: aws.String(name),
	})
	return err
}

// CreateBucket creates a new S3 bucket. For regions other than us-east-1,
// the CreateBucketConfiguration must specify the LocationConstraint.
// us-east-1 requires NO LocationConstraint (AWS API quirk).
func (r *realS3API) CreateBucket(ctx context.Context, name, region string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &s3sdk.CreateBucketInput{
		Bucket: aws.String(name),
	}
	// AWS requires explicit LocationConstraint for non-us-east-1 regions.
	// Specifying us-east-1 as LocationConstraint returns an error.
	if region != "us-east-1" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(region),
		}
	}
	_, err := r.client.CreateBucket(ctx, input)
	return err
}

// ConfigureBucket applies versioning, encryption, and tagging to an existing bucket.
// Split from CreateBucket because S3's CreateBucket API doesn't accept these
// settings in a single call — they are separate API operations.
func (r *realS3API) ConfigureBucket(ctx context.Context, spec S3BucketSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	// --- Versioning ---
	versioningStatus := s3types.BucketVersioningStatusSuspended
	if spec.Versioning {
		versioningStatus = s3types.BucketVersioningStatusEnabled
	}
	_, err := r.client.PutBucketVersioning(ctx, &s3sdk.PutBucketVersioningInput{
		Bucket: aws.String(spec.BucketName),
		VersioningConfiguration: &s3types.VersioningConfiguration{
			Status: versioningStatus,
		},
	})
	if err != nil {
		return fmt.Errorf("put versioning: %w", err)
	}

	// --- Encryption ---
	if spec.Encryption.Enabled {
		algo := s3types.ServerSideEncryptionAes256
		if spec.Encryption.Algorithm == "aws:kms" {
			algo = s3types.ServerSideEncryptionAwsKms
		}
		_, err = r.client.PutBucketEncryption(ctx, &s3sdk.PutBucketEncryptionInput{
			Bucket: aws.String(spec.BucketName),
			ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{
				Rules: []s3types.ServerSideEncryptionRule{{
					ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{
						SSEAlgorithm: algo,
					},
				}},
			},
		})
		if err != nil {
			return fmt.Errorf("put encryption: %w", err)
		}
	}

	// --- Tags ---
	// PutBucketTagging replaces the entire tag set, so converge to
	// observed praxis:-prefixed bookkeeping tags + desired user tags.
	// An empty final set requires DeleteBucketTagging — putting an empty
	// set is rejected by S3, which would leave tag drift uncorrectable.
	current, err := r.getBucketTags(ctx, spec.BucketName)
	if err != nil {
		return fmt.Errorf("get tagging: %w", err)
	}
	final := make(map[string]string, len(current)+len(spec.Tags))
	for k, v := range current {
		if strings.HasPrefix(k, "praxis:") {
			final[k] = v
		}
	}
	maps.Copy(final, spec.Tags)
	if len(final) == 0 {
		_, err = r.client.DeleteBucketTagging(ctx, &s3sdk.DeleteBucketTaggingInput{
			Bucket: aws.String(spec.BucketName),
		})
		if err != nil {
			return fmt.Errorf("delete tagging: %w", err)
		}
		return nil
	}
	var tagSet []s3types.Tag
	for k, v := range final {
		tagSet = append(tagSet, s3types.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}
	_, err = r.client.PutBucketTagging(ctx, &s3sdk.PutBucketTaggingInput{
		Bucket: aws.String(spec.BucketName),
		Tagging: &s3types.Tagging{
			TagSet: tagSet,
		},
	})
	if err != nil {
		return fmt.Errorf("put tagging: %w", err)
	}

	return nil
}

// getBucketTags returns the bucket's current tags, treating NoSuchTagSet
// (bucket has no tags) as an empty map rather than an error.
func (r *realS3API) getBucketTags(ctx context.Context, name string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.GetBucketTagging(ctx, &s3sdk.GetBucketTaggingInput{Bucket: aws.String(name)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchTagSet" {
			return map[string]string{}, nil
		}
		return nil, err
	}
	tags := make(map[string]string, len(out.TagSet))
	for _, tag := range out.TagSet {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags, nil
}

// DescribeBucket returns the observed state of a bucket by querying
// its versioning, encryption, and tagging configuration.
func (r *realS3API) DescribeBucket(ctx context.Context, name string) (ObservedState, error) {
	obs := ObservedState{
		BucketName: name,
	}

	if err := r.limiter.Wait(ctx); err != nil {
		return obs, err
	}

	// Check bucket exists first
	if err := r.HeadBucket(ctx, name); err != nil {
		return obs, err
	}

	// Get the bucket's region
	locResp, err := r.client.GetBucketLocation(ctx, &s3sdk.GetBucketLocationInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		return obs, fmt.Errorf("get location: %w", err)
	}
	// AWS returns empty string for us-east-1 (historical quirk).
	region := string(locResp.LocationConstraint)
	if region == "" {
		region = "us-east-1"
	}
	obs.Region = region

	// Versioning
	verResp, err := r.client.GetBucketVersioning(ctx, &s3sdk.GetBucketVersioningInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		return obs, fmt.Errorf("get versioning: %w", err)
	}
	obs.VersioningStatus = string(verResp.Status)

	// Encryption
	encResp, err := r.client.GetBucketEncryption(ctx, &s3sdk.GetBucketEncryptionInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		// Some buckets may not have explicit encryption configuration.
		// Treat this as no encryption rather than an error.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ServerSideEncryptionConfigurationNotFoundError" {
			obs.EncryptionAlgo = ""
		} else {
			return obs, fmt.Errorf("get encryption: %w", err)
		}
	} else if len(encResp.ServerSideEncryptionConfiguration.Rules) > 0 {
		rule := encResp.ServerSideEncryptionConfiguration.Rules[0]
		if rule.ApplyServerSideEncryptionByDefault != nil {
			obs.EncryptionAlgo = string(rule.ApplyServerSideEncryptionByDefault.SSEAlgorithm)
		}
	}

	// Tags
	tagResp, err := r.client.GetBucketTagging(ctx, &s3sdk.GetBucketTaggingInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		// Buckets with no tags return NoSuchTagSet — this is not an error.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchTagSet" {
			obs.Tags = map[string]string{}
		} else {
			return obs, fmt.Errorf("get tagging: %w", err)
		}
	} else {
		obs.Tags = make(map[string]string, len(tagResp.TagSet))
		for _, tag := range tagResp.TagSet {
			obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}

	return obs, nil
}

// DeleteBucket removes a bucket. Fails if the bucket contains objects.
// Praxis never auto-empties buckets — this is an intentional safety decision.
func (r *realS3API) DeleteBucket(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteBucket(ctx, &s3sdk.DeleteBucketInput{
		Bucket: aws.String(name),
	})
	return err
}

func (r *realS3API) FindByTags(ctx context.Context, tags map[string]string) (string, error) {
	if len(tags) == 0 {
		// An empty filter would match every bucket in the account.
		return "", restate.TerminalError(fmt.Errorf("tag lookup requires at least one tag filter"), 400)
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.ListBuckets(ctx, &s3sdk.ListBucketsInput{})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, bucket := range out.Buckets {
		name := aws.ToString(bucket.Name)
		if name == "" {
			continue
		}
		if err := r.limiter.Wait(ctx); err != nil {
			return "", err
		}
		tagResp, tagErr := r.client.GetBucketTagging(ctx, &s3sdk.GetBucketTaggingInput{Bucket: aws.String(name)})
		if tagErr != nil {
			var apiErr smithy.APIError
			if errors.As(tagErr, &apiErr) && apiErr.ErrorCode() == "NoSuchTagSet" {
				continue // bucket has no tags — cannot match a non-empty filter
			}
			return "", fmt.Errorf("get tagging for bucket %s: %w", name, tagErr)
		}
		bucketTags := make(map[string]string, len(tagResp.TagSet))
		for _, tag := range tagResp.TagSet {
			bucketTags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		matched := true
		for key, value := range tags {
			if bucketTags[key] != value {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, name)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous lookup: %d buckets match the given tag filters", len(matches))
	}
}

// ---------------------------------------------------------------------------
// Error Classification Helpers
// ---------------------------------------------------------------------------
// The driver must classify AWS SDK errors before deciding whether to return
// a regular error (retryable — Restate retries automatically) or
// restate.TerminalError (permanent — Restate stops retrying).

// IsNotFound returns true if the AWS error indicates the resource does not exist.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nsk *s3types.NoSuchKey
	var nsb *s3types.NoSuchBucket
	var nf *s3types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nsb) || errors.As(err, &nf) {
		return true
	}
	// HasCode covers typed API errors (e.g. HeadBucket's generic 404) and
	// journal-flattened "<Code>:" strings.
	return awserr.HasCode(err, "NotFound", "NoSuchBucket", "NoSuchKey")
}

// IsBucketNotEmpty returns true if a DeleteBucket call failed because the
// bucket still contains objects. Praxis never auto-empties buckets.
func IsBucketNotEmpty(err error) bool {
	return awserr.HasCode(err, "BucketNotEmpty")
}

// IsConflict returns true if the error indicates a resource state conflict
// (e.g., BucketAlreadyOwnedByYou, BucketAlreadyExists owned by another account).
func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	var bao *s3types.BucketAlreadyOwnedByYou
	var bae *s3types.BucketAlreadyExists
	if errors.As(err, &bao) || errors.As(err, &bae) {
		return true
	}
	return awserr.HasCode(err, "BucketAlreadyOwnedByYou", "BucketAlreadyExists")
}

func IsBucketLimitExceeded(err error) bool {
	return awserr.HasCode(err, "TooManyBuckets")
}
