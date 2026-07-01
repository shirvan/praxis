// Package ecscluster – aws.go
//
// This file contains the AWS API abstraction layer for ECS clusters.
// It defines the ECSClusterAPI interface (used for testing with mocks)
// and the real implementation that calls AWS ECS through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package ecscluster

import (
	"context"
	"maps"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// defaultContainerInsights is the AWS default when the setting is unspecified.
const defaultContainerInsights = "disabled"

// ECSClusterAPI abstracts all AWS ECS SDK operations needed to manage a cluster.
// The real implementation calls AWS; tests supply a mock to verify driver logic
// without network calls.
type ECSClusterAPI interface {
	CreateCluster(ctx context.Context, spec ECSClusterSpec) (ObservedState, error)
	DescribeCluster(ctx context.Context, name string) (ObservedState, bool, error)
	UpdateCluster(ctx context.Context, name, containerInsights string) error
	PutCapacityProviders(ctx context.Context, name string, capacityProviders []string) error
	DeleteCluster(ctx context.Context, name string) error
	TagResource(ctx context.Context, arn string, tags map[string]string) error
	UntagResource(ctx context.Context, arn string, tagKeys []string) error
}

type realECSClusterAPI struct {
	client  *ecs.Client
	limiter *ratelimit.Limiter
}

// NewECSClusterAPI constructs a production ECSClusterAPI backed by the given AWS
// SDK client, with built-in rate limiting to avoid throttling.
func NewECSClusterAPI(client *ecs.Client) ECSClusterAPI {
	return &realECSClusterAPI{
		client:  client,
		limiter: ratelimit.Shared("ecs-cluster", 10, 5),
	}
}

// CreateCluster provisions a new ECS cluster from the given spec and returns the
// observed state read back from the create response.
func (r *realECSClusterAPI) CreateCluster(ctx context.Context, spec ECSClusterSpec) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	input := &ecs.CreateClusterInput{
		ClusterName: aws.String(spec.Name),
		Settings:    settingsRequest(spec.ContainerInsights),
		Tags:        toECSTags(managedTags(spec.Tags, spec.ManagedKey)),
	}
	if len(spec.CapacityProviders) > 0 {
		input.CapacityProviders = spec.CapacityProviders
	}
	out, err := r.client.CreateCluster(ctx, input)
	if err != nil {
		return ObservedState{}, err
	}
	return clusterToObserved(out.Cluster), nil
}

// DescribeCluster reads the current state of the ECS cluster from AWS. The
// second return value is false when the cluster does not exist or has been
// deleted (ECS retains deleted clusters in an INACTIVE status).
func (r *realECSClusterAPI) DescribeCluster(ctx context.Context, name string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	out, err := r.client.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{name},
		Include:  []ecstypes.ClusterField{ecstypes.ClusterFieldTags, ecstypes.ClusterFieldSettings},
	})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	if len(out.Clusters) == 0 {
		return ObservedState{}, false, nil
	}
	cluster := out.Clusters[0]
	// Deleted clusters linger in ECS with an INACTIVE status; treat them as
	// absent so create/delete flows behave as if the cluster is gone.
	if aws.ToString(cluster.Status) == "INACTIVE" {
		return ObservedState{}, false, nil
	}
	return clusterToObserved(&cluster), true, nil
}

// UpdateCluster converges the mutable cluster settings (Container Insights).
func (r *realECSClusterAPI) UpdateCluster(ctx context.Context, name, containerInsights string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateCluster(ctx, &ecs.UpdateClusterInput{
		Cluster:  aws.String(name),
		Settings: settingsRequest(containerInsights),
	})
	return err
}

// PutCapacityProviders converges the capacity providers associated with the
// cluster. ECS requires a default capacity-provider strategy alongside the
// provider list; Praxis does not manage the strategy, so an empty strategy is
// passed to leave it unset.
func (r *realECSClusterAPI) PutCapacityProviders(ctx context.Context, name string, capacityProviders []string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.PutClusterCapacityProviders(ctx, &ecs.PutClusterCapacityProvidersInput{
		Cluster:                         aws.String(name),
		CapacityProviders:               append([]string{}, capacityProviders...),
		DefaultCapacityProviderStrategy: []ecstypes.CapacityProviderStrategyItem{},
	})
	return err
}

// DeleteCluster removes the ECS cluster from AWS.
func (r *realECSClusterAPI) DeleteCluster(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)})
	return err
}

// TagResource attaches or overwrites tags on the cluster identified by ARN.
func (r *realECSClusterAPI) TagResource(ctx context.Context, arn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String(arn),
		Tags:        toECSTags(tags),
	})
	return err
}

// UntagResource removes the given tag keys from the cluster identified by ARN.
func (r *realECSClusterAPI) UntagResource(ctx context.Context, arn string, tagKeys []string) error {
	if len(tagKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UntagResource(ctx, &ecs.UntagResourceInput{
		ResourceArn: aws.String(arn),
		TagKeys:     tagKeys,
	})
	return err
}

// settingsRequest builds the cluster settings list carrying the Container
// Insights toggle. An empty value falls back to the AWS default (disabled).
func settingsRequest(containerInsights string) []ecstypes.ClusterSetting {
	value := containerInsights
	if value == "" {
		value = defaultContainerInsights
	}
	return []ecstypes.ClusterSetting{{
		Name:  ecstypes.ClusterSettingNameContainerInsights,
		Value: aws.String(value),
	}}
}

// toECSTags converts a tag map into the ECS SDK's key/value tag list, in
// deterministic key order.
func toECSTags(tags map[string]string) []ecstypes.Tag {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ecstypes.Tag, 0, len(tags))
	for _, key := range keys {
		out = append(out, ecstypes.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	return out
}

// clusterToObserved projects an ECS SDK Cluster into the driver's ObservedState.
func clusterToObserved(c *ecstypes.Cluster) ObservedState {
	obs := ObservedState{
		ARN:               aws.ToString(c.ClusterArn),
		Name:              aws.ToString(c.ClusterName),
		Status:            aws.ToString(c.Status),
		ContainerInsights: observedContainerInsights(c.Settings),
		CapacityProviders: append([]string{}, c.CapacityProviders...),
		Tags:              map[string]string{},
	}
	for _, tag := range c.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return obs
}

// observedContainerInsights reads the Container Insights value out of the
// cluster settings, defaulting to disabled when the setting is absent.
func observedContainerInsights(settings []ecstypes.ClusterSetting) string {
	for _, setting := range settings {
		if setting.Name == ecstypes.ClusterSettingNameContainerInsights {
			return aws.ToString(setting.Value)
		}
	}
	return defaultContainerInsights
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

// IsNotFound reports whether the AWS error indicates the cluster does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ClusterNotFoundException")
}

// IsInvalidParam reports whether the AWS error indicates an invalid request
// that a retry cannot fix.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterException", "ClientException")
}
