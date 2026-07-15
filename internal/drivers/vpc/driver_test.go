package vpc

import (
	"context"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

// retryDeleteVPCAPI embeds the full interface so this regression fixture only
// implements the two AWS operations exercised by Import followed by Delete.
// Any unexpected call fails immediately through the nil embedded interface.
type retryDeleteVPCAPI struct {
	VPCAPI

	mu             sync.Mutex
	deleteAttempts int
}

func (f *retryDeleteVPCAPI) DescribeVpc(context.Context, string) (ObservedState, error) {
	return ObservedState{
		VpcId:           "vpc-123",
		CidrBlock:       "10.0.0.0/16",
		State:           "available",
		InstanceTenancy: "default",
		OwnerId:         "123456789012",
		IsDefault:       false,
	}, nil
}

func (f *retryDeleteVPCAPI) DeleteVpc(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteAttempts++
	if f.deleteAttempts == 1 {
		return &mockAPIError{code: "DependencyViolation", message: "dependent resource is still attached"}
	}
	return nil
}

func (f *retryDeleteVPCAPI) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleteAttempts
}

func setupRetryDeleteVPCDriver(t *testing.T, api VPCAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewVPCDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) VPCAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver)).Ingress()
}

func TestServiceName(t *testing.T) {
	drv := NewVPCDriver(nil)
	assert.Equal(t, "VPC", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		VpcId:              "vpc-123",
		CidrBlock:          "10.0.0.0/16",
		EnableDnsHostnames: true,
		EnableDnsSupport:   true,
		InstanceTenancy:    "default",
		Tags:               map[string]string{"Name": "my-vpc", "praxis:managed-key": "us-east-1~my-vpc"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.CidrBlock, spec.CidrBlock)
	assert.Equal(t, obs.EnableDnsHostnames, spec.EnableDnsHostnames)
	assert.Equal(t, obs.EnableDnsSupport, spec.EnableDnsSupport)
	assert.Equal(t, obs.InstanceTenancy, spec.InstanceTenancy)
	assert.Equal(t, obs.Tags, spec.Tags)
}

func TestSpecFromObserved_Empty(t *testing.T) {
	obs := ObservedState{
		CidrBlock: "10.0.0.0/16",
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "10.0.0.0/16", spec.CidrBlock)
	assert.False(t, spec.EnableDnsHostnames)
	assert.False(t, spec.EnableDnsSupport)
}

func TestSpecFromObserved_NilTags(t *testing.T) {
	obs := ObservedState{
		CidrBlock: "10.0.0.0/16",
		Tags:      nil,
	}
	spec := specFromObserved(obs)
	assert.Nil(t, spec.Tags)
}

func TestDefaultVPCImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultVPCImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultVPCImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultVPCImportMode(types.ModeObserved))
}

func TestDelete_RetriesDependencyViolationInsideDurableCallback(t *testing.T) {
	api := &retryDeleteVPCAPI{}
	client := setupRetryDeleteVPCDriver(t, api)
	key := "us-east-1~network"

	_, err := ingress.Object[types.ImportRef, VPCOutputs](client, ServiceName, key, "Import").Request(
		t.Context(),
		types.ImportRef{ResourceID: "vpc-123", Mode: types.ModeManaged, Account: "test"},
	)
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 2, api.attempts(), "DependencyViolation must be retried by Restate instead of becoming terminal")
}
