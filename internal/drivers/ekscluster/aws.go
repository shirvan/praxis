// Package ekscluster – aws.go
//
// This file contains the AWS API abstraction layer for EKS clusters.
// It defines the EKSClusterAPI interface (used for testing with mocks)
// and the real implementation that calls AWS EKS through the AWS SDK.
// All AWS calls are rate-limited to prevent throttling.
package ekscluster

import (
	"context"
	"maps"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// EKSClusterAPI abstracts all AWS EKS SDK operations needed to manage a cluster.
// The real implementation calls AWS; tests supply a mock to verify driver logic
// without network calls.
type EKSClusterAPI interface {
	CreateCluster(ctx context.Context, spec EKSClusterSpec) (ObservedState, error)
	DescribeCluster(ctx context.Context, name string) (ObservedState, bool, error)
	UpdateClusterConfig(ctx context.Context, spec EKSClusterSpec) error
	UpdateClusterLogging(ctx context.Context, name string, enabled []string) error
	UpdateClusterVersion(ctx context.Context, name, version string) error
	DeleteCluster(ctx context.Context, name string) error
	TagResource(ctx context.Context, arn string, tags map[string]string) error
	UntagResource(ctx context.Context, arn string, tagKeys []string) error
}

type realEKSClusterAPI struct {
	client  *eks.Client
	limiter *ratelimit.Limiter
}

// NewEKSClusterAPI constructs a production EKSClusterAPI backed by the given AWS
// SDK client, with built-in rate limiting to avoid throttling.
func NewEKSClusterAPI(client *eks.Client) EKSClusterAPI {
	return &realEKSClusterAPI{
		client:  client,
		limiter: ratelimit.Shared("eks-cluster", 10, 5),
	}
}

// CreateCluster provisions a new EKS cluster from the given spec and returns the
// observed state read back from the create response.
func (r *realEKSClusterAPI) CreateCluster(ctx context.Context, spec EKSClusterSpec) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	input := &eks.CreateClusterInput{
		Name:               aws.String(spec.Name),
		RoleArn:            aws.String(spec.RoleArn),
		ResourcesVpcConfig: vpcConfigRequest(spec),
		Tags:               managedTags(spec.Tags, spec.ManagedKey),
	}
	if spec.Version != "" {
		input.Version = aws.String(spec.Version)
	}
	if logging := loggingRequest(spec.EnabledLoggingTypes); logging != nil {
		input.Logging = logging
	}
	out, err := r.client.CreateCluster(ctx, input)
	if err != nil {
		return ObservedState{}, err
	}
	return clusterToObserved(out.Cluster), nil
}

// DescribeCluster reads the current state of the EKS cluster from AWS. The
// second return value is false when the cluster does not exist.
func (r *realEKSClusterAPI) DescribeCluster(ctx context.Context, name string) (ObservedState, bool, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, false, err
	}
	out, err := r.client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
	if err != nil {
		if IsNotFound(err) {
			return ObservedState{}, false, nil
		}
		return ObservedState{}, false, err
	}
	return clusterToObserved(out.Cluster), true, nil
}

// UpdateClusterConfig converges the mutable VPC endpoint access configuration
// and control-plane logging for an existing cluster.
func (r *realEKSClusterAPI) UpdateClusterConfig(ctx context.Context, spec EKSClusterSpec) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &eks.UpdateClusterConfigInput{
		Name: aws.String(spec.Name),
		ResourcesVpcConfig: &ekstypes.VpcConfigRequest{
			EndpointPublicAccess:  aws.Bool(spec.EndpointPublicAccess),
			EndpointPrivateAccess: aws.Bool(spec.EndpointPrivateAccess),
			PublicAccessCidrs:     spec.PublicAccessCidrs,
		},
	}
	_, err := r.client.UpdateClusterConfig(ctx, input)
	return err
}

func (r *realEKSClusterAPI) UpdateClusterLogging(ctx context.Context, name string, enabled []string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateClusterConfig(ctx, &eks.UpdateClusterConfigInput{
		Name: aws.String(name), Logging: fullLoggingRequest(enabled),
	})
	return err
}

// UpdateClusterVersion upgrades the Kubernetes control-plane version.
func (r *realEKSClusterAPI) UpdateClusterVersion(ctx context.Context, name, version string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UpdateClusterVersion(ctx, &eks.UpdateClusterVersionInput{
		Name:    aws.String(name),
		Version: aws.String(version),
	})
	return err
}

// DeleteCluster removes the EKS cluster from AWS.
func (r *realEKSClusterAPI) DeleteCluster(ctx context.Context, name string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteCluster(ctx, &eks.DeleteClusterInput{Name: aws.String(name)})
	return err
}

// TagResource attaches or overwrites tags on the cluster identified by ARN.
func (r *realEKSClusterAPI) TagResource(ctx context.Context, arn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.TagResource(ctx, &eks.TagResourceInput{
		ResourceArn: aws.String(arn),
		Tags:        tags,
	})
	return err
}

// UntagResource removes the given tag keys from the cluster identified by ARN.
func (r *realEKSClusterAPI) UntagResource(ctx context.Context, arn string, tagKeys []string) error {
	if len(tagKeys) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.UntagResource(ctx, &eks.UntagResourceInput{
		ResourceArn: aws.String(arn),
		TagKeys:     tagKeys,
	})
	return err
}

// vpcConfigRequest builds the create-time VPC config from the spec.
func vpcConfigRequest(spec EKSClusterSpec) *ekstypes.VpcConfigRequest {
	return &ekstypes.VpcConfigRequest{
		SubnetIds:             spec.SubnetIds,
		SecurityGroupIds:      spec.SecurityGroupIds,
		EndpointPublicAccess:  aws.Bool(spec.EndpointPublicAccess),
		EndpointPrivateAccess: aws.Bool(spec.EndpointPrivateAccess),
		PublicAccessCidrs:     spec.PublicAccessCidrs,
	}
}

// loggingRequest builds a Logging request that enables only the requested log
// types. It returns nil when no log types are requested so create calls omit
// the field entirely (AWS defaults to all logging disabled).
func loggingRequest(enabled []string) *ekstypes.Logging {
	if len(enabled) == 0 {
		return nil
	}
	return fullLoggingRequest(enabled)
}

// fullLoggingRequest builds a Logging request that explicitly enables the
// requested log types and disables every other type. This form is required by
// UpdateClusterConfig, which needs the complete desired logging state to
// converge (enabling some types while leaving others implicitly on would drift).
func fullLoggingRequest(enabled []string) *ekstypes.Logging {
	want := map[string]bool{}
	for _, t := range enabled {
		want[t] = true
	}
	var on, off []ekstypes.LogType
	for _, t := range allLogTypes {
		if want[t] {
			on = append(on, ekstypes.LogType(t))
		} else {
			off = append(off, ekstypes.LogType(t))
		}
	}
	var setups []ekstypes.LogSetup
	if len(on) > 0 {
		setups = append(setups, ekstypes.LogSetup{Enabled: aws.Bool(true), Types: on})
	}
	if len(off) > 0 {
		setups = append(setups, ekstypes.LogSetup{Enabled: aws.Bool(false), Types: off})
	}
	return &ekstypes.Logging{ClusterLogging: setups}
}

// allLogTypes is the closed set of EKS control-plane log types, in the canonical
// order AWS documents them.
var allLogTypes = []string{"api", "audit", "authenticator", "controllerManager", "scheduler"}

// clusterToObserved projects an EKS SDK Cluster into the driver's ObservedState.
func clusterToObserved(c *ekstypes.Cluster) ObservedState {
	obs := ObservedState{
		ARN:             aws.ToString(c.Arn),
		Name:            aws.ToString(c.Name),
		Status:          string(c.Status),
		Version:         aws.ToString(c.Version),
		PlatformVersion: aws.ToString(c.PlatformVersion),
		Endpoint:        aws.ToString(c.Endpoint),
		RoleArn:         aws.ToString(c.RoleArn),
		Tags:            map[string]string{},
	}
	if vpc := c.ResourcesVpcConfig; vpc != nil {
		obs.SubnetIds = append([]string{}, vpc.SubnetIds...)
		obs.SecurityGroupIds = append([]string{}, vpc.SecurityGroupIds...)
		obs.EndpointPublicAccess = vpc.EndpointPublicAccess
		obs.EndpointPrivateAccess = vpc.EndpointPrivateAccess
		obs.PublicAccessCidrs = append([]string{}, vpc.PublicAccessCidrs...)
	}
	obs.EnabledLoggingTypes = observedLoggingTypes(c.Logging)
	maps.Copy(obs.Tags, c.Tags)
	return obs
}

// observedLoggingTypes flattens the cluster's Logging block into the sorted set
// of currently-enabled log types.
func observedLoggingTypes(logging *ekstypes.Logging) []string {
	if logging == nil {
		return nil
	}
	var enabled []string
	for _, setup := range logging.ClusterLogging {
		if !aws.ToBool(setup.Enabled) {
			continue
		}
		for _, t := range setup.Types {
			enabled = append(enabled, string(t))
		}
	}
	sort.Strings(enabled)
	return enabled
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
	return awserr.HasCode(err, "ResourceNotFoundException")
}

// IsConflict reports whether the AWS error indicates the cluster already exists
// or is otherwise in use (e.g. concurrent create/update/delete).
func IsConflict(err error) bool {
	return awserr.HasCode(err, "ResourceInUseException")
}

// IsInvalidParam reports whether the AWS error indicates an invalid request
// that a retry cannot fix.
func IsInvalidParam(err error) bool {
	return awserr.HasCode(err, "InvalidParameterException", "InvalidRequestException", "UnsupportedAvailabilityZoneException")
}

// IsLimitExceeded reports whether the AWS error indicates a service quota was hit.
func IsLimitExceeded(err error) bool {
	return awserr.HasCode(err, "ResourceLimitExceededException")
}
