// Package secret – aws.go
//
// This file contains the AWS API abstraction layer for Secrets Manager secrets.
// It defines the SecretsManagerSecretAPI interface (used for testing with mocks)
// and the real implementation that calls AWS Secrets Manager through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package secret

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// SecretsManagerSecretAPI abstracts all AWS Secrets Manager SDK operations
// needed to manage a secret. The real implementation calls AWS; tests supply a
// mock to verify driver logic without network calls.
type SecretsManagerSecretAPI interface {
	CreateSecret(ctx context.Context, spec SecretsManagerSecretSpec, clientRequestToken string) (SecretsManagerSecretOutputs, error)
	DescribeSecret(ctx context.Context, name string) (ObservedState, bool, error)
	UpdateSecret(ctx context.Context, name, description, kmsKeyID string) error
	PutSecretValue(ctx context.Context, name, value, clientRequestToken string) error
	DeleteSecret(ctx context.Context, name string, force bool) error
	RestoreSecret(ctx context.Context, name string) error
	AddTags(ctx context.Context, name string, tags map[string]string) error
	RemoveTags(ctx context.Context, name string, tagKeys []string) error
}

// SecretsManagerSecretMetadataAPI is the least-privilege read surface used by
// data-source lookup. It never requests the secret value.
type SecretsManagerSecretMetadataAPI interface {
	DescribeSecretMetadata(ctx context.Context, name string) (ObservedState, bool, error)
}

type realSecretsManagerSecretAPI struct {
	client  *secretsmanager.Client
	limiter *ratelimit.Limiter
}

// NewSecretsManagerSecretAPI constructs a production SecretsManagerSecretAPI
// backed by the given AWS SDK client, with built-in rate limiting to avoid
// throttling.
func NewSecretsManagerSecretAPI(client *secretsmanager.Client) SecretsManagerSecretAPI {
	return newRealSecretsManagerSecretAPI(client)
}

// NewSecretsManagerSecretMetadataAPI constructs the metadata-only read surface
// used by provider lookups.
func NewSecretsManagerSecretMetadataAPI(client *secretsmanager.Client) SecretsManagerSecretMetadataAPI {
	return newRealSecretsManagerSecretAPI(client)
}

func newRealSecretsManagerSecretAPI(client *secretsmanager.Client) *realSecretsManagerSecretAPI {
	return &realSecretsManagerSecretAPI{
		client:  client,
		limiter: ratelimit.Shared("secretsmanager", 10, 5),
	}
}

// CreateSecret creates a new secret from the given spec, stamping the managed
// tags on create. Observation is deliberately a separate driver operation so
// one Restate journal entry contains exactly one AWS request.
func (r *realSecretsManagerSecretAPI) CreateSecret(ctx context.Context, spec SecretsManagerSecretSpec, clientRequestToken string) (SecretsManagerSecretOutputs, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return SecretsManagerSecretOutputs{}, err
	}
	input := &secretsmanager.CreateSecretInput{
		Name:         aws.String(spec.Name),
		SecretString: aws.String(spec.SecretString),
		Tags:         tagList(managedTags(spec.Tags, spec.ManagedKey)),
	}
	if clientRequestToken != "" {
		input.ClientRequestToken = aws.String(clientRequestToken)
	}
	if spec.Description != "" {
		input.Description = aws.String(spec.Description)
	}
	if spec.KmsKeyID != "" {
		input.KmsKeyId = aws.String(spec.KmsKeyID)
	}
	out, err := r.client.CreateSecret(ctx, input)
	if err != nil {
		return SecretsManagerSecretOutputs{}, err
	}
	return SecretsManagerSecretOutputs{
		ARN: aws.ToString(out.ARN), Name: spec.Name, VersionID: aws.ToString(out.VersionId),
	}, nil
}

// DescribeSecret reads the current state of the secret from AWS. DescribeSecret
// supplies the ARN, description, KMS key, and tags; GetSecretValue supplies the
// current secret value and version so value drift is detectable.
func (r *realSecretsManagerSecretAPI) DescribeSecret(ctx context.Context, name string) (ObservedState, bool, error) {
	observed, found, err := r.DescribeSecretMetadata(ctx, name)
	if err != nil || !found || observed.ScheduledForDeletion {
		return observed, found, err
	}

	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	val, err := r.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	observed.SecretString = aws.ToString(val.SecretString)
	observed.VersionID = aws.ToString(val.VersionId)
	return observed, true, nil
}

// DescribeSecretMetadata reads identity, configuration, and tags without
// requesting secret material.
func (r *realSecretsManagerSecretAPI) DescribeSecretMetadata(ctx context.Context, name string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	desc, err := r.client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(name),
	})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	observed := ObservedState{
		ARN:         aws.ToString(desc.ARN),
		Name:        aws.ToString(desc.Name),
		Description: aws.ToString(desc.Description),
		KmsKeyID:    aws.ToString(desc.KmsKeyId),
		Tags:        map[string]string{},
	}
	for _, tag := range desc.Tags {
		observed.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	currentVersions := make([]string, 0, 1)
	for versionID, stages := range desc.VersionIdsToStages {
		if slices.Contains(stages, "AWSCURRENT") {
			currentVersions = append(currentVersions, versionID)
		}
	}
	sort.Strings(currentVersions)
	if len(currentVersions) > 0 {
		observed.VersionID = currentVersions[0]
	}

	// A secret scheduled for deletion (recovery window) still describes
	// successfully but rejects GetSecretValue with InvalidRequestException.
	// Surface the condition structurally so the driver can restore it instead
	// of failing on the value read.
	if desc.DeletedDate != nil {
		observed.ScheduledForDeletion = true
	}
	return observed, true, nil
}

// UpdateSecret converges the description and KMS key on an existing secret.
func (r *realSecretsManagerSecretAPI) UpdateSecret(ctx context.Context, name, description, kmsKeyID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &secretsmanager.UpdateSecretInput{SecretId: aws.String(name)}
	if description != "" {
		input.Description = aws.String(description)
	}
	if kmsKeyID != "" {
		input.KmsKeyId = aws.String(kmsKeyID)
	}
	_, err := r.client.UpdateSecret(ctx, input)
	return err
}

// PutSecretValue stores a new version of the secret value, becoming AWSCURRENT.
func (r *realSecretsManagerSecretAPI) PutSecretValue(ctx context.Context, name, value, clientRequestToken string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String(value),
	}
	if clientRequestToken != "" {
		input.ClientRequestToken = aws.String(clientRequestToken)
	}
	_, err := r.client.PutSecretValue(ctx, input)
	return err
}

// DeleteSecret removes the secret. By default it schedules deletion with a
// 7-day recovery window so an accidental delete can be undone via RestoreSecret;
// when force is true it deletes immediately with no recovery window. The two
// options are mutually exclusive in the AWS API.
func (r *realSecretsManagerSecretAPI) DeleteSecret(ctx context.Context, name string, force bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &secretsmanager.DeleteSecretInput{SecretId: aws.String(name)}
	if force {
		input.ForceDeleteWithoutRecovery = aws.Bool(true)
	} else {
		input.RecoveryWindowInDays = aws.Int64(7)
	}
	_, err := r.client.DeleteSecret(ctx, input)
	return err
}

// RestoreSecret cancels a scheduled deletion, bringing a recovery-window
// secret back to active so a re-provision under the same name can converge it
// instead of failing on "scheduled for deletion".
func (r *realSecretsManagerSecretAPI) RestoreSecret(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RestoreSecret(ctx, &secretsmanager.RestoreSecretInput{
		SecretId: aws.String(name),
	})
	return err
}

func (r *realSecretsManagerSecretAPI) AddTags(ctx context.Context, name string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TagResource(ctx, &secretsmanager.TagResourceInput{
		SecretId: aws.String(name),
		Tags:     tagList(tags),
	})
	return err
}

func (r *realSecretsManagerSecretAPI) RemoveTags(ctx context.Context, name string, tagKeys []string) error {
	if len(tagKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UntagResource(ctx, &secretsmanager.UntagResourceInput{
		SecretId: aws.String(name),
		TagKeys:  tagKeys,
	})
	return err
}

// IsNotFound returns true if the AWS error indicates the secret does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException")
}

func IsAlreadyExists(err error) bool {
	return awserr.HasCode(err, "ResourceExistsException")
}

func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterException", "InvalidRequestException", "ValidationException")
}

// IsScheduledForDeletion matches the InvalidRequestException returned when
// operating on a secret inside its recovery window. Wording varies by
// operation and implementation: AWS uses "scheduled for deletion" /
// "marked for deletion", Moto uses "currently marked deleted". Used to make
// Delete idempotent when the secret is already scheduled.
func IsScheduledForDeletion(err error) bool {
	if err == nil || !awserr.HasCode(err, "InvalidRequestException") {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "scheduled for deletion") ||
		strings.Contains(msg, "marked for deletion") ||
		strings.Contains(msg, "marked deleted")
}

func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "LimitExceededException")
}

func tagList(tags map[string]string) []smtypes.Tag {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]smtypes.Tag, 0, len(tags))
	for _, key := range keys {
		out = append(out, smtypes.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
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

// secretClientRequestToken creates the 64-character token accepted by Secrets
// Manager from stable resource and Restate invocation identities. Callback
// retries reuse it; a later Provision or Reconcile invocation receives a new
// token and may legitimately create a new secret version.
func secretClientRequestToken(managedKey, invocationID string) string {
	if strings.TrimSpace(invocationID) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(managedKey + "\x00" + invocationID))
	return hex.EncodeToString(sum[:])
}
