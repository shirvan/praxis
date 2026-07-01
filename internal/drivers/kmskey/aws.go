// Package kmskey – aws.go
//
// This file contains the AWS API abstraction layer for KMS keys.
// It defines the KMSKeyAPI interface (used for testing with mocks) and the real
// implementation that calls AWS KMS through the AWS SDK. All AWS calls are
// rate-limited to prevent throttling.
//
// A KMS key's observable state is spread across three APIs: DescribeKey supplies
// the metadata (ARN, description, usage, spec, state), GetKeyRotationStatus
// supplies the rotation flag, and ListResourceTags supplies the tag set.
// DescribeKey (the composite read below) stitches them together, keyed by the
// alias so callers never need the raw key ID.
package kmskey

import (
	"context"
	"maps"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// KMSKeyAPI abstracts all AWS KMS SDK operations needed to manage a key and its
// alias. The real implementation calls AWS; tests supply a mock to verify driver
// logic without network calls.
type KMSKeyAPI interface {
	CreateKey(ctx context.Context, spec KMSKeySpec) (string, string, error)
	CreateAlias(ctx context.Context, alias, keyID string) error
	DescribeKey(ctx context.Context, alias string) (ObservedState, bool, error)
	UpdateDescription(ctx context.Context, keyID, description string) error
	EnableKeyRotation(ctx context.Context, keyID string) error
	DisableKeyRotation(ctx context.Context, keyID string) error
	TagResource(ctx context.Context, keyID string, tags map[string]string) error
	UntagResource(ctx context.Context, keyID string, tagKeys []string) error
	DeleteAlias(ctx context.Context, alias string) error
	ScheduleKeyDeletion(ctx context.Context, keyID string, windowInDays int32) error
}

type realKMSKeyAPI struct {
	client  *kms.Client
	limiter *ratelimit.Limiter
}

// NewKMSKeyAPI constructs a production KMSKeyAPI backed by the given AWS SDK
// client, with built-in rate limiting to avoid throttling.
func NewKMSKeyAPI(client *kms.Client) KMSKeyAPI {
	return &realKMSKeyAPI{
		client:  client,
		limiter: ratelimit.Shared("kms-key", 10, 5),
	}
}

// CreateKey provisions a new KMS key from the given spec and returns its key ID
// and ARN. Tags (including the praxis managed-key marker) are attached at
// creation via the KMS CreateKey Tags parameter.
func (r *realKMSKeyAPI) CreateKey(ctx context.Context, spec KMSKeySpec) (string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", err
	}
	input := &kms.CreateKeyInput{
		KeyUsage: kmstypes.KeyUsageType(spec.KeyUsage),
		KeySpec:  kmstypes.KeySpec(spec.KeySpec),
		Tags:     tagList(managedTags(spec.Tags, spec.ManagedKey)),
	}
	if spec.Description != "" {
		input.Description = aws.String(spec.Description)
	}
	out, err := r.client.CreateKey(ctx, input)
	if err != nil {
		return "", "", err
	}
	return aws.ToString(out.KeyMetadata.KeyId), aws.ToString(out.KeyMetadata.Arn), nil
}

// CreateAlias binds the alias name to the given key ID.
func (r *realKMSKeyAPI) CreateAlias(ctx context.Context, alias, keyID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.CreateAlias(ctx, &kms.CreateAliasInput{
		AliasName:   aws.String(alias),
		TargetKeyId: aws.String(keyID),
	})
	return err
}

// DescribeKey reads the current state of the KMS key identified by its alias.
// DescribeKey (metadata) supplies the ARN, description, usage, spec, and state;
// GetKeyRotationStatus supplies the rotation flag; ListResourceTags supplies the
// tag set. The second return value is false when the alias does not exist.
func (r *realKMSKeyAPI) DescribeKey(ctx context.Context, alias string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	out, err := r.client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(alias)})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	meta := out.KeyMetadata
	observed := ObservedState{
		ARN:         aws.ToString(meta.Arn),
		KeyID:       aws.ToString(meta.KeyId),
		AliasName:   alias,
		Description: aws.ToString(meta.Description),
		KeyUsage:    string(meta.KeyUsage),
		KeySpec:     string(meta.KeySpec),
		KeyState:    string(meta.KeyState),
		Enabled:     meta.Enabled,
		Tags:        map[string]string{},
	}

	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	rot, err := r.client.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: meta.KeyId})
	if err != nil {
		return ObservedState{}, false, err
	}
	observed.EnableKeyRotation = rot.KeyRotationEnabled

	tags, err := r.listTags(ctx, aws.ToString(meta.KeyId))
	if err != nil {
		return ObservedState{}, false, err
	}
	observed.Tags = tags
	return observed, true, nil
}

// UpdateDescription converges the mutable key description.
func (r *realKMSKeyAPI) UpdateDescription(ctx context.Context, keyID, description string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateKeyDescription(ctx, &kms.UpdateKeyDescriptionInput{
		KeyId:       aws.String(keyID),
		Description: aws.String(description),
	})
	return err
}

// EnableKeyRotation turns on automatic annual key rotation.
func (r *realKMSKeyAPI) EnableKeyRotation(ctx context.Context, keyID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{KeyId: aws.String(keyID)})
	return err
}

// DisableKeyRotation turns off automatic annual key rotation.
func (r *realKMSKeyAPI) DisableKeyRotation(ctx context.Context, keyID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DisableKeyRotation(ctx, &kms.DisableKeyRotationInput{KeyId: aws.String(keyID)})
	return err
}

// TagResource attaches or overwrites tags on the key identified by key ID.
func (r *realKMSKeyAPI) TagResource(ctx context.Context, keyID string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TagResource(ctx, &kms.TagResourceInput{
		KeyId: aws.String(keyID),
		Tags:  tagList(tags),
	})
	return err
}

// UntagResource removes the given tag keys from the key identified by key ID.
func (r *realKMSKeyAPI) UntagResource(ctx context.Context, keyID string, tagKeys []string) error {
	if len(tagKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UntagResource(ctx, &kms.UntagResourceInput{
		KeyId:   aws.String(keyID),
		TagKeys: tagKeys,
	})
	return err
}

// DeleteAlias removes the alias binding. The underlying key is scheduled for
// deletion separately via ScheduleKeyDeletion.
func (r *realKMSKeyAPI) DeleteAlias(ctx context.Context, alias string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: aws.String(alias)})
	return err
}

// ScheduleKeyDeletion schedules the key for deletion after the given waiting
// period (in days).
func (r *realKMSKeyAPI) ScheduleKeyDeletion(ctx context.Context, keyID string, windowInDays int32) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
		KeyId:               aws.String(keyID),
		PendingWindowInDays: aws.Int32(windowInDays),
	})
	return err
}

// listTags enumerates the tags attached to the KMS key.
func (r *realKMSKeyAPI) listTags(ctx context.Context, keyID string) (map[string]string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListResourceTags(ctx, &kms.ListResourceTagsInput{KeyId: aws.String(keyID)})
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string, len(out.Tags))
	for _, tag := range out.Tags {
		tags[aws.ToString(tag.TagKey)] = aws.ToString(tag.TagValue)
	}
	return tags, nil
}

// tagList converts a tag map into the sorted KMS Tag slice the SDK expects.
func tagList(tags map[string]string) []kmstypes.Tag {
	keys := sortedKeys(tags)
	out := make([]kmstypes.Tag, 0, len(tags))
	for _, key := range keys {
		out = append(out, kmstypes.Tag{TagKey: aws.String(key), TagValue: aws.String(tags[key])})
	}
	return out
}

// managedTags merges the user's tags with the praxis managed-key marker.
func managedTags(tags map[string]string, managedKey string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, tags)
	if managedKey != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

// tagDiff computes the tag additions and removals needed to converge the
// observed tag set to the desired one, preserving the praxis managed-key marker.
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
	sortStrings(toRemove)
	return toAdd, toRemove
}

// IsNotFound reports whether the AWS error indicates the key or alias does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "NotFoundException")
}

// IsConflict reports whether the AWS error indicates the alias already exists or
// the key is in a state that forbids the requested operation.
func IsConflict(err error) bool {
	return awserr.HasCode(err, "AlreadyExistsException", "KMSInvalidStateException", "ConflictException")
}

// IsInvalidParam reports whether the AWS error indicates an invalid request that
// a retry cannot fix.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidArnException", "InvalidAliasNameException",
		"InvalidKeyUsageException", "MalformedPolicyDocumentException", "TagException", "DisabledException")
}

// IsLimitExceeded reports whether the AWS error indicates a service quota was hit.
func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "LimitExceededException")
}
