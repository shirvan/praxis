package ecscluster

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestSettingsRequest_DefaultsToDisabled(t *testing.T) {
	settings := settingsRequest("")
	assert.Len(t, settings, 1)
	assert.Equal(t, ecstypes.ClusterSettingNameContainerInsights, settings[0].Name)
	assert.Equal(t, "disabled", aws.ToString(settings[0].Value))
}

func TestSettingsRequest_PassesThroughValue(t *testing.T) {
	settings := settingsRequest("enabled")
	assert.Equal(t, "enabled", aws.ToString(settings[0].Value))
}

func TestToECSTags_SortedKeyOrder(t *testing.T) {
	tags := toECSTags(map[string]string{"b": "2", "a": "1"})
	assert.Equal(t, []ecstypes.Tag{
		{Key: aws.String("a"), Value: aws.String("1")},
		{Key: aws.String("b"), Value: aws.String("2")},
	}, tags)
}

func TestManagedTags(t *testing.T) {
	out := managedTags(map[string]string{"env": "prod"}, "us-east-1~prod")
	assert.Equal(t, "prod", out["env"])
	assert.Equal(t, "us-east-1~prod", out["praxis:managed-key"])

	noKey := managedTags(map[string]string{"env": "prod"}, "")
	assert.NotContains(t, noKey, "praxis:managed-key")
}

func TestClusterToObserved(t *testing.T) {
	obs := clusterToObserved(&ecstypes.Cluster{
		ClusterArn:        aws.String("arn:aws:ecs:us-east-1:123456789012:cluster/prod"),
		ClusterName:       aws.String("prod"),
		Status:            aws.String("ACTIVE"),
		CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"},
		Settings: []ecstypes.ClusterSetting{
			{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String("enabled")},
		},
		Tags: []ecstypes.Tag{{Key: aws.String("env"), Value: aws.String("prod")}},
	})
	assert.Equal(t, "prod", obs.Name)
	assert.Equal(t, "ACTIVE", obs.Status)
	assert.Equal(t, "enabled", obs.ContainerInsights)
	assert.Equal(t, []string{"FARGATE", "FARGATE_SPOT"}, obs.CapacityProviders)
	assert.Equal(t, "prod", obs.Tags["env"])
}

func TestObservedContainerInsights_DefaultsToDisabled(t *testing.T) {
	assert.Equal(t, "disabled", observedContainerInsights(nil))
	assert.Equal(t, "enabled", observedContainerInsights([]ecstypes.ClusterSetting{
		{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String("enabled")},
	}))
}

func TestErrorClassifiers(t *testing.T) {
	notFound := &smithy.GenericAPIError{Code: "ClusterNotFoundException"}
	invalid := &smithy.GenericAPIError{Code: "InvalidParameterException"}

	assert.True(t, IsNotFound(notFound))
	assert.False(t, IsNotFound(invalid))
	assert.True(t, IsInvalidParam(invalid))

	// String fallback: Restate wraps errors and loses the typed code, so the
	// classifiers must still match on the wrapped message.
	wrapped := errors.New("operation error ECS: DescribeClusters, ClusterNotFoundException: cluster was not found")
	assert.True(t, IsNotFound(wrapped), "classifier must survive error wrapping via string fallback")
}
