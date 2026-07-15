package ekscluster

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

// retryEKSAPI models an existing ACTIVE cluster whose first mutable-config
// update collides with another in-flight EKS operation. That AWS response is
// transient for updates, so the Restate callback must retry it rather than
// making the object permanently fail.
type retryEKSAPI struct {
	mu                   sync.Mutex
	observed             ObservedState
	configUpdateAttempts int
}

func (f *retryEKSAPI) CreateCluster(context.Context, EKSClusterSpec) (ObservedState, error) {
	return ObservedState{}, nil
}

func (f *retryEKSAPI) DescribeCluster(_ context.Context, _ string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneEKSObserved(f.observed), true, nil
}

func (f *retryEKSAPI) UpdateClusterConfig(_ context.Context, spec EKSClusterSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configUpdateAttempts++
	if f.configUpdateAttempts == 1 {
		return &smithy.GenericAPIError{
			Code:    "ResourceInUseException",
			Message: "another cluster update is still active",
		}
	}
	f.observed.EndpointPublicAccess = spec.EndpointPublicAccess
	f.observed.EndpointPrivateAccess = spec.EndpointPrivateAccess
	f.observed.PublicAccessCidrs = append([]string{}, spec.PublicAccessCidrs...)
	f.observed.EnabledLoggingTypes = append([]string{}, spec.EnabledLoggingTypes...)
	return nil
}

func (f *retryEKSAPI) UpdateClusterVersion(context.Context, string, string) error {
	return nil
}

func (f *retryEKSAPI) DeleteCluster(context.Context, string) error { return nil }

func (f *retryEKSAPI) TagResource(context.Context, string, map[string]string) error {
	return nil
}

func (f *retryEKSAPI) UntagResource(context.Context, string, []string) error {
	return nil
}

func (f *retryEKSAPI) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configUpdateAttempts
}

func cloneEKSObserved(observed ObservedState) ObservedState {
	clone := observed
	clone.SubnetIds = append([]string{}, observed.SubnetIds...)
	clone.SecurityGroupIds = append([]string{}, observed.SecurityGroupIds...)
	clone.PublicAccessCidrs = append([]string{}, observed.PublicAccessCidrs...)
	clone.EnabledLoggingTypes = append([]string{}, observed.EnabledLoggingTypes...)
	clone.Tags = make(map[string]string, len(observed.Tags))
	maps.Copy(clone.Tags, observed.Tags)
	return clone
}

func setupRetryEKSDriver(t *testing.T, api EKSClusterAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewEKSClusterDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) EKSClusterAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver)).Ingress()
}

func TestServiceName(t *testing.T) {
	drv := NewEKSClusterDriver(nil)
	assert.Equal(t, "EKSCluster", drv.ServiceName())
}

func baseSpec() EKSClusterSpec {
	return EKSClusterSpec{
		Region:    "us-east-1",
		Name:      "prod",
		RoleArn:   "arn:aws:iam::123456789012:role/eks",
		SubnetIds: []string{"subnet-a", "subnet-b"},
	}
}

func TestApplyDefaults_TrimsAndInitializes(t *testing.T) {
	spec := applyDefaults(EKSClusterSpec{
		Region:  "  us-east-1  ",
		Name:    "  prod  ",
		RoleArn: "  arn:aws:iam::123456789012:role/eks  ",
		Version: "  1.29  ",
	})
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "arn:aws:iam::123456789012:role/eks", spec.RoleArn)
	assert.Equal(t, "1.29", spec.Version)
	assert.NotNil(t, spec.Tags)
}

func TestValidateSpec(t *testing.T) {
	assert.NoError(t, validateSpec(baseSpec()))

	noRegion := baseSpec()
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := baseSpec()
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	noRole := baseSpec()
	noRole.RoleArn = ""
	assert.Error(t, validateSpec(noRole))

	oneSubnet := baseSpec()
	oneSubnet.SubnetIds = []string{"subnet-a"}
	assert.Error(t, validateSpec(oneSubnet), "EKS requires at least two subnets")

	badLog := baseSpec()
	badLog.EnabledLoggingTypes = []string{"api", "bogus"}
	assert.Error(t, validateSpec(badLog))

	goodLog := baseSpec()
	goodLog.EnabledLoggingTypes = []string{"api", "audit", "scheduler"}
	assert.NoError(t, validateSpec(goodLog))
}

func TestSpecFromObserved_FiltersPraxisTags(t *testing.T) {
	obs := ObservedState{
		Name:                 "prod",
		RoleArn:              "arn:aws:iam::123456789012:role/eks",
		SubnetIds:            []string{"subnet-a", "subnet-b"},
		Version:              "1.29",
		EndpointPublicAccess: true,
		EnabledLoggingTypes:  []string{"api"},
		Tags:                 map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "1.29", spec.Version)
	assert.Equal(t, []string{"subnet-a", "subnet-b"}, spec.SubnetIds)
	assert.Equal(t, []string{"api"}, spec.EnabledLoggingTypes)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		ARN:             "arn:aws:eks:us-east-1:123456789012:cluster/prod",
		Name:            "prod",
		Status:          "ACTIVE",
		Version:         "1.29",
		PlatformVersion: "eks.5",
		Endpoint:        "https://example.eks.amazonaws.com",
	})
	assert.Equal(t, "arn:aws:eks:us-east-1:123456789012:cluster/prod", out.ARN)
	assert.Equal(t, "prod", out.Name)
	assert.Equal(t, "ACTIVE", out.Status)
	assert.Equal(t, "1.29", out.Version)
	assert.Equal(t, "eks.5", out.PlatformVersion)
	assert.Equal(t, "https://example.eks.amazonaws.com", out.Endpoint)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestTagDiff_AddsRemovesPreservesManagedKey(t *testing.T) {
	desired := map[string]string{"env": "prod", "team": "core"}
	observed := map[string]string{"env": "dev", "old": "1", "praxis:managed-key": "k"}
	toAdd, toRemove := tagDiff(desired, observed, "k")

	assert.Equal(t, "prod", toAdd["env"], "changed value should be re-tagged")
	assert.Equal(t, "core", toAdd["team"], "new tag should be added")
	assert.NotContains(t, toAdd, "praxis:managed-key", "managed key already present, not re-added")
	assert.Equal(t, []string{"old"}, toRemove, "stale tag should be removed; managed key preserved")
}

func TestTagDiff_ManagedKeyNeverDiffed(t *testing.T) {
	// The managed-key marker is synthesized on both the desired and observed
	// sides, so it must never surface as an add or a removal — reconciling it as
	// drift would fight the create-time tagging on every pass.
	toAdd, toRemove := tagDiff(map[string]string{}, map[string]string{}, "us-east-1~prod")
	assert.NotContains(t, toAdd, "praxis:managed-key")
	assert.Empty(t, toAdd)
	assert.Empty(t, toRemove)
}

func TestProvision_RetriesResourceInUseDuringMutableUpdate(t *testing.T) {
	key := "us-east-1~prod"
	spec := baseSpec()
	spec.EndpointPublicAccess = true
	spec.PublicAccessCidrs = []string{"10.0.0.0/8"}
	api := &retryEKSAPI{observed: ObservedState{
		ARN:                   "arn:aws:eks:us-east-1:123456789012:cluster/prod",
		Name:                  spec.Name,
		Status:                "ACTIVE",
		Version:               spec.Version,
		RoleArn:               spec.RoleArn,
		SubnetIds:             append([]string{}, spec.SubnetIds...),
		EndpointPublicAccess:  false,
		EndpointPrivateAccess: spec.EndpointPrivateAccess,
		Tags:                  map[string]string{"praxis:managed-key": key},
	}}
	client := setupRetryEKSDriver(t, api)

	outputs, err := ingress.Object[EKSClusterSpec, EKSClusterOutputs](
		client, ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, "prod", outputs.Name)
	assert.Equal(t, "ACTIVE", outputs.Status)
	assert.Equal(t, 2, api.attempts(),
		"ResourceInUseException during update must be retried by Restate instead of becoming terminal")

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
}
