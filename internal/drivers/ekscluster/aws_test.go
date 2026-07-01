package ekscluster

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestLoggingRequest_EmptyIsNil(t *testing.T) {
	assert.Nil(t, loggingRequest(nil), "no requested log types should omit the Logging block on create")
}

func TestLoggingRoundTrip(t *testing.T) {
	// fullLoggingRequest enables the requested types and disables the rest;
	// observedLoggingTypes should recover exactly the requested set.
	req := fullLoggingRequest([]string{"api", "audit"})
	logging := &ekstypes.Logging{ClusterLogging: req.ClusterLogging}
	assert.Equal(t, []string{"api", "audit"}, observedLoggingTypes(logging))
}

func TestFullLoggingRequest_DisablesUnselected(t *testing.T) {
	req := fullLoggingRequest([]string{"api"})
	var enabled, disabled []string
	for _, setup := range req.ClusterLogging {
		for _, lt := range setup.Types {
			if aws.ToBool(setup.Enabled) {
				enabled = append(enabled, string(lt))
			} else {
				disabled = append(disabled, string(lt))
			}
		}
	}
	assert.Equal(t, []string{"api"}, enabled)
	assert.ElementsMatch(t, []string{"audit", "authenticator", "controllerManager", "scheduler"}, disabled,
		"every unselected log type must be explicitly disabled so updates converge")
}

func TestManagedTags(t *testing.T) {
	out := managedTags(map[string]string{"env": "prod"}, "us-east-1~prod")
	assert.Equal(t, "prod", out["env"])
	assert.Equal(t, "us-east-1~prod", out["praxis:managed-key"])

	noKey := managedTags(map[string]string{"env": "prod"}, "")
	assert.NotContains(t, noKey, "praxis:managed-key")
}

func TestClusterToObserved(t *testing.T) {
	obs := clusterToObserved(&ekstypes.Cluster{
		Arn:             aws.String("arn:aws:eks:us-east-1:123456789012:cluster/prod"),
		Name:            aws.String("prod"),
		Status:          ekstypes.ClusterStatusActive,
		Version:         aws.String("1.29"),
		PlatformVersion: aws.String("eks.5"),
		Endpoint:        aws.String("https://example.eks.amazonaws.com"),
		RoleArn:         aws.String("arn:aws:iam::123456789012:role/eks"),
		ResourcesVpcConfig: &ekstypes.VpcConfigResponse{
			SubnetIds:             []string{"subnet-a", "subnet-b"},
			EndpointPublicAccess:  true,
			EndpointPrivateAccess: false,
			PublicAccessCidrs:     []string{"0.0.0.0/0"},
		},
		Logging: fullLoggingRequest([]string{"api"}),
		Tags:    map[string]string{"env": "prod"},
	})
	assert.Equal(t, "prod", obs.Name)
	assert.Equal(t, "ACTIVE", obs.Status)
	assert.Equal(t, []string{"subnet-a", "subnet-b"}, obs.SubnetIds)
	assert.True(t, obs.EndpointPublicAccess)
	assert.Equal(t, []string{"api"}, obs.EnabledLoggingTypes)
	assert.Equal(t, "prod", obs.Tags["env"])
}

func TestErrorClassifiers(t *testing.T) {
	notFound := &smithy.GenericAPIError{Code: "ResourceNotFoundException"}
	inUse := &smithy.GenericAPIError{Code: "ResourceInUseException"}
	invalid := &smithy.GenericAPIError{Code: "InvalidParameterException"}
	limit := &smithy.GenericAPIError{Code: "ResourceLimitExceededException"}

	assert.True(t, IsNotFound(notFound))
	assert.False(t, IsNotFound(inUse))
	assert.True(t, IsConflict(inUse))
	assert.True(t, IsInvalidParam(invalid))
	assert.True(t, IsLimitExceeded(limit))

	// String fallback: Restate wraps errors and loses the typed code, so the
	// classifiers must still match on the wrapped message.
	wrapped := errors.New("operation error EKS: DescribeCluster, ResourceNotFoundException: No cluster found")
	assert.True(t, IsNotFound(wrapped), "classifier must survive error wrapping via string fallback")
}
