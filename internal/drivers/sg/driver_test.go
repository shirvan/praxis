package sg

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

// retryDeleteSGAPI embeds the full interface so this regression fixture only
// implements the two AWS operations exercised by Import followed by Delete.
// Any unexpected call fails immediately through the nil embedded interface.
type retryDeleteSGAPI struct {
	SGAPI

	mu             sync.Mutex
	deleteAttempts int
}

func (f *retryDeleteSGAPI) DescribeSecurityGroup(context.Context, string) (ObservedState, error) {
	return ObservedState{
		GroupId:     "sg-123",
		GroupName:   "web",
		Description: "web traffic",
		VpcId:       "vpc-123",
		OwnerId:     "123456789012",
	}, nil
}

func (f *retryDeleteSGAPI) DeleteSecurityGroup(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteAttempts++
	if f.deleteAttempts == 1 {
		return &mockAPIError{code: "DependencyViolation", message: "network interface is still attached"}
	}
	return nil
}

func (f *retryDeleteSGAPI) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleteAttempts
}

func setupRetryDeleteSGDriver(t *testing.T, api SGAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewSecurityGroupDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) SGAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver)).Ingress()
}

// --- specFromObserved tests ---

func TestSpecFromObserved_FullyPopulated(t *testing.T) {
	obs := ObservedState{
		GroupId:     "sg-123",
		GroupName:   "my-sg",
		Description: "Test SG",
		VpcId:       "vpc-abc",
		IngressRules: []NormalizedRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		},
		EgressRules: []NormalizedRule{
			{Direction: "egress", Protocol: "all", FromPort: 0, ToPort: 0, Target: "cidr:0.0.0.0/0"},
		},
		Tags: map[string]string{"env": "prod"},
	}

	spec := specFromObserved(obs)

	assert.Equal(t, "my-sg", spec.GroupName)
	assert.Equal(t, "Test SG", spec.Description)
	assert.Equal(t, "vpc-abc", spec.VpcId)
	assert.Len(t, spec.IngressRules, 1)
	assert.Equal(t, "tcp", spec.IngressRules[0].Protocol)
	assert.Equal(t, int32(80), spec.IngressRules[0].FromPort)
	assert.Equal(t, "0.0.0.0/0", spec.IngressRules[0].CidrBlock)
	assert.Len(t, spec.EgressRules, 1)
	assert.Equal(t, "-1", spec.EgressRules[0].Protocol) // "all" denormalized to "-1"
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags)
}

func TestSpecFromObserved_Empty(t *testing.T) {
	obs := ObservedState{
		GroupName:   "empty-sg",
		Description: "Empty",
		VpcId:       "vpc-123",
	}

	spec := specFromObserved(obs)

	assert.Equal(t, "empty-sg", spec.GroupName)
	assert.Empty(t, spec.IngressRules)
	assert.Empty(t, spec.EgressRules)
}

func TestSpecFromObserved_NilTags(t *testing.T) {
	obs := ObservedState{
		GroupName:   "no-tags",
		Description: "No tags",
		VpcId:       "vpc-123",
		Tags:        nil,
	}

	spec := specFromObserved(obs)
	assert.Nil(t, spec.Tags)
}

// --- ServiceName tests ---

func TestServiceName(t *testing.T) {
	drv := NewSecurityGroupDriver(nil)
	assert.Equal(t, "SecurityGroup", drv.ServiceName())
}

func TestDelete_RetriesDependencyViolationInsideDurableCallback(t *testing.T) {
	api := &retryDeleteSGAPI{}
	client := setupRetryDeleteSGDriver(t, api)
	key := "us-east-1~web"

	_, err := ingress.Object[types.ImportRef, SecurityGroupOutputs](client, ServiceName, key, "Import").Request(
		t.Context(),
		types.ImportRef{ResourceID: "sg-123", Mode: types.ModeManaged, Account: "test"},
	)
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 2, api.attempts(), "DependencyViolation must be retried by Restate instead of becoming terminal")
}

// --- extractCidr tests ---

func TestExtractCidr(t *testing.T) {
	assert.Equal(t, "10.0.0.0/8", extractCidr("cidr:10.0.0.0/8"))
	assert.Equal(t, "0.0.0.0/0", extractCidr("cidr:0.0.0.0/0"))
	assert.Equal(t, "raw", extractCidr("raw"))
}
