package orchestrator

import (
	"testing"
	"time"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceIndex_UpsertAndQuery(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ResourceIndex{}))
	client := env.Ingress()
	ctx := t.Context()

	now := time.Now().UTC()

	// Upsert two resources of different kinds.
	_, err := ingress.Object[ResourceIndexEntry, restate.Void](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Upsert",
	).Request(ctx, ResourceIndexEntry{
		Kind:          "S3Bucket",
		Key:           "my-bucket",
		DeploymentKey: "dep-1",
		ResourceName:  "assetsBucket",
		Workspace:     "staging",
		Status:        "Ready",
		CreatedAt:     now,
	})
	require.NoError(t, err)

	_, err = ingress.Object[ResourceIndexEntry, restate.Void](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Upsert",
	).Request(ctx, ResourceIndexEntry{
		Kind:          "VPC",
		Key:           "us-east-1~main-vpc",
		DeploymentKey: "dep-2",
		ResourceName:  "mainVPC",
		Workspace:     "prod",
		Status:        "Ready",
		CreatedAt:     now,
	})
	require.NoError(t, err)

	// Query all — should return both.
	all, err := ingress.Object[ResourceQuery, []ResourceIndexEntry](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Query",
	).Request(ctx, ResourceQuery{})
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// Query by Kind.
	s3Only, err := ingress.Object[ResourceQuery, []ResourceIndexEntry](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Query",
	).Request(ctx, ResourceQuery{Kind: "S3Bucket"})
	require.NoError(t, err)
	assert.Len(t, s3Only, 1)
	assert.Equal(t, "my-bucket", s3Only[0].Key)

	// Query by Workspace.
	prodOnly, err := ingress.Object[ResourceQuery, []ResourceIndexEntry](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Query",
	).Request(ctx, ResourceQuery{Workspace: "prod"})
	require.NoError(t, err)
	assert.Len(t, prodOnly, 1)
	assert.Equal(t, "VPC", prodOnly[0].Kind)

	// Query by Kind + Workspace (no match).
	empty, err := ingress.Object[ResourceQuery, []ResourceIndexEntry](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Query",
	).Request(ctx, ResourceQuery{Kind: "S3Bucket", Workspace: "prod"})
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestResourceIndex_UpsertOverwritesExisting(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ResourceIndex{}))
	client := env.Ingress()
	ctx := t.Context()

	entry := ResourceIndexEntry{
		Kind:          "S3Bucket",
		Key:           "my-bucket",
		DeploymentKey: "dep-1",
		ResourceName:  "bucket",
		Status:        "Pending",
		CreatedAt:     time.Now().UTC(),
	}
	_, err := ingress.Object[ResourceIndexEntry, restate.Void](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Upsert",
	).Request(ctx, entry)
	require.NoError(t, err)

	// Update status.
	entry.Status = "Ready"
	_, err = ingress.Object[ResourceIndexEntry, restate.Void](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Upsert",
	).Request(ctx, entry)
	require.NoError(t, err)

	results, err := ingress.Object[ResourceQuery, []ResourceIndexEntry](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Query",
	).Request(ctx, ResourceQuery{Kind: "S3Bucket"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Ready", results[0].Status)
}

func TestResourceIndex_Remove(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ResourceIndex{}))
	client := env.Ingress()
	ctx := t.Context()

	_, err := ingress.Object[ResourceIndexEntry, restate.Void](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Upsert",
	).Request(ctx, ResourceIndexEntry{
		Kind:          "S3Bucket",
		Key:           "my-bucket",
		DeploymentKey: "dep-1",
		ResourceName:  "bucket",
		Status:        "Ready",
		CreatedAt:     time.Now().UTC(),
	})
	require.NoError(t, err)

	_, err = ingress.Object[ResourceIndexRemoveRequest, restate.Void](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Remove",
	).Request(ctx, ResourceIndexRemoveRequest{
		DeploymentKey: "dep-1",
		ResourceName:  "bucket",
	})
	require.NoError(t, err)

	results, err := ingress.Object[ResourceQuery, []ResourceIndexEntry](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Query",
	).Request(ctx, ResourceQuery{})
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestResourceIndex_RemoveByDeployment(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ResourceIndex{}))
	client := env.Ingress()
	ctx := t.Context()

	now := time.Now().UTC()

	// Two resources in dep-1, one in dep-2.
	for _, entry := range []ResourceIndexEntry{
		{Kind: "S3Bucket", Key: "bucket-a", DeploymentKey: "dep-1", ResourceName: "a", Status: "Ready", CreatedAt: now},
		{Kind: "S3Bucket", Key: "bucket-b", DeploymentKey: "dep-1", ResourceName: "b", Status: "Ready", CreatedAt: now},
		{Kind: "VPC", Key: "us-east-1~vpc", DeploymentKey: "dep-2", ResourceName: "vpc", Status: "Ready", CreatedAt: now},
	} {
		_, err := ingress.Object[ResourceIndexEntry, restate.Void](
			client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Upsert",
		).Request(ctx, entry)
		require.NoError(t, err)
	}

	// Remove all dep-1 entries.
	_, err := ingress.Object[string, restate.Void](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "RemoveByDeployment",
	).Request(ctx, "dep-1")
	require.NoError(t, err)

	results, err := ingress.Object[ResourceQuery, []ResourceIndexEntry](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Query",
	).Request(ctx, ResourceQuery{})
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "dep-2", results[0].DeploymentKey)
}

func TestResourceIndex_RemoveNonexistentIsNoop(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ResourceIndex{}))
	client := env.Ingress()
	ctx := t.Context()

	_, err := ingress.Object[ResourceIndexRemoveRequest, restate.Void](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Remove",
	).Request(ctx, ResourceIndexRemoveRequest{
		DeploymentKey: "nonexistent",
		ResourceName:  "ghost",
	})
	assert.NoError(t, err)
}

func TestResourceIndex_QueryEmptyReturnsNil(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ResourceIndex{}))
	client := env.Ingress()
	ctx := t.Context()

	results, err := ingress.Object[ResourceQuery, []ResourceIndexEntry](
		client, ResourceIndexServiceName, ResourceIndexGlobalKey, "Query",
	).Request(ctx, ResourceQuery{Kind: "S3Bucket"})
	assert.NoError(t, err)
	assert.Nil(t, results)
}
