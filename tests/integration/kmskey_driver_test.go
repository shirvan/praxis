//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	kmssdk "github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/kmskey"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// uniqueKMSAliasName derives a Moto-safe, collision-free alias short name from
// the test name plus a nanosecond suffix. Moto keeps no cross-test state
// guarantees, so every test provisions under its own alias.
func uniqueKMSAliasName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 80 {
		name = name[:80]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupKMSKeyDriver(t *testing.T) (*ingress.Client, *kmssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	kmsClient := awsclient.NewKMSClient(awsCfg)
	driver := kmskey.NewKMSKeyDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, kmsClient
}

// baseKMSKeySpec builds a spec that Moto accepts.
func baseKMSKeySpec(name string) kmskey.KMSKeySpec {
	return kmskey.KMSKeySpec{
		Account:           integrationAccountName,
		Region:            "us-east-1",
		Name:              name,
		Description:       "praxis integration key",
		EnableKeyRotation: false,
		Tags:              map[string]string{"env": "test"},
	}
}

func provisionKMSKey(t *testing.T, client *ingress.Client, key string, spec kmskey.KMSKeySpec) kmskey.KMSKeyOutputs {
	t.Helper()
	out, err := ingress.Object[kmskey.KMSKeySpec, kmskey.KMSKeyOutputs](
		client, kmskey.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	return out
}

func TestKMSKeyProvision_CreatesKey(t *testing.T) {
	client, kmsClient := setupKMSKeyDriver(t)
	name := uniqueKMSAliasName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	outputs := provisionKMSKey(t, client, key, baseKMSKeySpec(name))
	assert.NotEmpty(t, outputs.KeyID)
	assert.NotEmpty(t, outputs.ARN)
	assert.Equal(t, "alias/"+name, outputs.AliasName)

	got, err := kmsClient.DescribeKey(context.Background(), &kmssdk.DescribeKeyInput{KeyId: aws.String("alias/" + name)})
	require.NoError(t, err)
	assert.Equal(t, outputs.KeyID, aws.ToString(got.KeyMetadata.KeyId))

	tags, err := kmsClient.ListResourceTags(context.Background(), &kmssdk.ListResourceTagsInput{KeyId: aws.String(outputs.KeyID)})
	require.NoError(t, err)
	found := map[string]string{}
	for _, tag := range tags.Tags {
		found[aws.ToString(tag.TagKey)] = aws.ToString(tag.TagValue)
	}
	assert.Equal(t, "test", found["env"])
	assert.Contains(t, found, "praxis:managed-key", "provisioning should stamp the managed-key marker")
}

func TestKMSKeyProvision_Idempotent(t *testing.T) {
	client, _ := setupKMSKeyDriver(t)
	name := uniqueKMSAliasName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	spec := baseKMSKeySpec(name)

	out1 := provisionKMSKey(t, client, key, spec)
	out2 := provisionKMSKey(t, client, key, spec)
	assert.Equal(t, out1.KeyID, out2.KeyID, "re-provisioning an in-sync key must be a no-op")
	assert.Equal(t, out1.ARN, out2.ARN)
}

func TestKMSKeyImport_ExistingKey(t *testing.T) {
	client, kmsClient := setupKMSKeyDriver(t)
	name := uniqueKMSAliasName(t)

	created, err := kmsClient.CreateKey(context.Background(), &kmssdk.CreateKeyInput{
		Description: aws.String("preexisting"),
		Tags:        []kmstypes.Tag{{TagKey: aws.String("env"), TagValue: aws.String("preexisting")}},
	})
	require.NoError(t, err)
	_, err = kmsClient.CreateAlias(context.Background(), &kmssdk.CreateAliasInput{
		AliasName:   aws.String("alias/" + name),
		TargetKeyId: created.KeyMetadata.KeyId,
	})
	require.NoError(t, err)

	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	outputs, err := ingress.Object[types.ImportRef, kmskey.KMSKeyOutputs](
		client, kmskey.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: name, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(created.KeyMetadata.KeyId), outputs.KeyID)
	assert.Equal(t, "alias/"+name, outputs.AliasName)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, kmskey.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode, "default import mode is Observed")
}

func TestKMSKeyDelete_SchedulesDeletion(t *testing.T) {
	client, kmsClient := setupKMSKeyDriver(t)
	name := uniqueKMSAliasName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionKMSKey(t, client, key, baseKMSKeySpec(name))

	_, err := ingress.Object[restate.Void, restate.Void](
		client, kmskey.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// The alias is removed, so lookup by alias must now fail.
	_, err = kmsClient.DescribeKey(context.Background(), &kmssdk.DescribeKeyInput{KeyId: aws.String("alias/" + name)})
	require.Error(t, err, "alias should be deleted from AWS")
}

func TestKMSKeyReconcile_DetectsAndCorrectsTagDrift(t *testing.T) {
	client, kmsClient := setupKMSKeyDriver(t)
	name := uniqueKMSAliasName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	out := provisionKMSKey(t, client, key, baseKMSKeySpec(name))

	// Externally mutate the tag set to introduce drift.
	_, err := kmsClient.TagResource(context.Background(), &kmssdk.TagResourceInput{
		KeyId: aws.String(out.KeyID),
		Tags: []kmstypes.Tag{
			{TagKey: aws.String("env"), TagValue: aws.String("hijacked")},
			{TagKey: aws.String("rogue"), TagValue: aws.String("1")},
		},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, kmskey.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "tag drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct tag drift")

	tags, err := kmsClient.ListResourceTags(context.Background(), &kmssdk.ListResourceTagsInput{KeyId: aws.String(out.KeyID)})
	require.NoError(t, err)
	found := map[string]string{}
	for _, tag := range tags.Tags {
		found[aws.ToString(tag.TagKey)] = aws.ToString(tag.TagValue)
	}
	assert.Equal(t, "test", found["env"], "reconcile should restore the desired tag value")
	assert.NotContains(t, found, "rogue", "reconcile should remove externally-added tags")
}

func TestKMSKeyReconcile_DetectsAndCorrectsRotationDrift(t *testing.T) {
	client, kmsClient := setupKMSKeyDriver(t)
	name := uniqueKMSAliasName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	out := provisionKMSKey(t, client, key, baseKMSKeySpec(name))

	// Externally enable rotation (desired is disabled) to introduce drift.
	_, err := kmsClient.EnableKeyRotation(context.Background(), &kmssdk.EnableKeyRotationInput{KeyId: aws.String(out.KeyID)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, kmskey.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "rotation drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct rotation drift")

	status, err := kmsClient.GetKeyRotationStatus(context.Background(), &kmssdk.GetKeyRotationStatusInput{KeyId: aws.String(out.KeyID)})
	require.NoError(t, err)
	assert.False(t, status.KeyRotationEnabled, "reconcile should restore rotation to desired (disabled)")
}

func TestKMSKeyReconcile_DetectsExternalDelete(t *testing.T) {
	client, kmsClient := setupKMSKeyDriver(t)
	name := uniqueKMSAliasName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionKMSKey(t, client, key, baseKMSKeySpec(name))

	// Delete the alias externally; identity is established by alias lookup.
	_, err := kmsClient.DeleteAlias(context.Background(), &kmssdk.DeleteAliasInput{AliasName: aws.String("alias/" + name)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, kmskey.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally", "external deletion should be surfaced as an error")

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, kmskey.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusError, status.Status)
}
