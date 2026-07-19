package dynamodbtable

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
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

// retryDynamoDBAPI is deliberately small: it models only enough DynamoDB
// behavior to prove that a retryable create error stays inside restate.Run.
// The mutex makes the fake safe when the SDK handler and the test assertion
// execute on different goroutines under the race detector.
type retryDynamoDBAPI struct {
	mu             sync.Mutex
	observed       ObservedState
	createAttempts int
	readAttempts   int
	updateAttempts int
	deleteAttempts int
}

func (f *retryDynamoDBAPI) CreateTable(_ context.Context, spec DynamoDBTableSpec) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createAttempts++
	if f.createAttempts == 1 {
		return ObservedState{}, &smithy.GenericAPIError{
			Code:    "LimitExceededException",
			Message: "account control-plane rate is temporarily exceeded",
		}
	}
	f.observed = ObservedState{
		ARN:           "arn:aws:dynamodb:us-east-1:123456789012:table/" + spec.Name,
		Name:          spec.Name,
		Status:        "ACTIVE",
		BillingMode:   spec.BillingMode,
		HashKey:       spec.HashKey,
		HashKeyType:   spec.HashKeyType,
		RangeKey:      spec.RangeKey,
		RangeKeyType:  spec.RangeKeyType,
		ReadCapacity:  spec.ReadCapacity,
		WriteCapacity: spec.WriteCapacity,
		Tags:          map[string]string{"praxis:managed-key": spec.ManagedKey},
	}
	maps.Copy(f.observed.Tags, spec.Tags)
	return f.observed, nil
}

func (f *retryDynamoDBAPI) DescribeTable(_ context.Context, name string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readAttempts++
	if f.observed.Name == "" || f.observed.Name != name {
		return ObservedState{}, false, nil
	}
	return f.observed, true, nil
}

func (f *retryDynamoDBAPI) UpdateTable(_ context.Context, spec DynamoDBTableSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateAttempts++
	f.observed.BillingMode = spec.BillingMode
	f.observed.ReadCapacity = spec.ReadCapacity
	f.observed.WriteCapacity = spec.WriteCapacity
	return nil
}

func (f *retryDynamoDBAPI) DeleteTable(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteAttempts++
	f.observed = ObservedState{}
	return nil
}

func (f *retryDynamoDBAPI) TagResource(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateAttempts++
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	maps.Copy(f.observed.Tags, tags)
	return nil
}

func (f *retryDynamoDBAPI) UntagResource(_ context.Context, _ string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateAttempts++
	for _, key := range keys {
		delete(f.observed.Tags, key)
	}
	return nil
}

func (f *retryDynamoDBAPI) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createAttempts
}

func (f *retryDynamoDBAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{
		Creates: f.createAttempts,
		Reads:   f.readAttempts,
		Updates: f.updateAttempts,
		Deletes: f.deleteAttempts,
	}
}

func setupRetryDynamoDBDriver(t *testing.T, api DynamoDBTableAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewGenericDynamoDBTableDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) DynamoDBTableAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver)).Ingress()
}

func TestServiceName(t *testing.T) {
	drv := NewGenericDynamoDBTableDriver(nil)
	assert.Equal(t, "DynamoDBTable", drv.ServiceName())
}

func baseSpec() DynamoDBTableSpec {
	return DynamoDBTableSpec{
		Region:      "us-east-1",
		Name:        "prod",
		BillingMode: BillingModePayPerRequest,
		HashKey:     "pk",
		HashKeyType: "S",
	}
}

func TestApplyDefaults_TrimsAndInitializes(t *testing.T) {
	spec := applyDefaults(DynamoDBTableSpec{
		Region:  "  us-east-1  ",
		Name:    "  prod  ",
		HashKey: "  pk  ",
	})
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "pk", spec.HashKey)
	assert.Equal(t, "S", spec.HashKeyType, "hashKeyType defaults to S")
	assert.Equal(t, BillingModePayPerRequest, spec.BillingMode, "billingMode defaults to on-demand")
	assert.NotNil(t, spec.Tags)
}

func TestApplyDefaults_RangeKeyTypeClearedWhenNoRangeKey(t *testing.T) {
	spec := applyDefaults(DynamoDBTableSpec{Region: "us-east-1", Name: "t", HashKey: "pk", RangeKeyType: "N"})
	assert.Empty(t, spec.RangeKeyType, "rangeKeyType is cleared when no range key is set")

	withRange := applyDefaults(DynamoDBTableSpec{Region: "us-east-1", Name: "t", HashKey: "pk", RangeKey: "sk"})
	assert.Equal(t, "S", withRange.RangeKeyType, "rangeKeyType defaults to S when a range key is set")
}

func TestValidateSpec(t *testing.T) {
	assert.NoError(t, validateSpec(baseSpec()))

	noRegion := baseSpec()
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := baseSpec()
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	noHash := baseSpec()
	noHash.HashKey = ""
	assert.Error(t, validateSpec(noHash))

	badHashType := baseSpec()
	badHashType.HashKeyType = "X"
	assert.Error(t, validateSpec(badHashType))

	badRangeType := baseSpec()
	badRangeType.RangeKey = "sk"
	badRangeType.RangeKeyType = "Z"
	assert.Error(t, validateSpec(badRangeType))

	badBilling := baseSpec()
	badBilling.BillingMode = "WHATEVER"
	assert.Error(t, validateSpec(badBilling))

	provisioned := baseSpec()
	provisioned.BillingMode = BillingModeProvisioned
	assert.NoError(t, validateSpec(provisioned))
}

func TestSpecFromObserved_FiltersPraxisTags(t *testing.T) {
	obs := ObservedState{
		Name:          "prod",
		BillingMode:   BillingModeProvisioned,
		HashKey:       "pk",
		HashKeyType:   "S",
		RangeKey:      "sk",
		RangeKeyType:  "N",
		ReadCapacity:  5,
		WriteCapacity: 7,
		Tags:          map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, BillingModeProvisioned, spec.BillingMode)
	assert.Equal(t, "pk", spec.HashKey)
	assert.Equal(t, "sk", spec.RangeKey)
	assert.Equal(t, int64(5), spec.ReadCapacity)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		ARN:       "arn:aws:dynamodb:us-east-1:123456789012:table/prod",
		Name:      "prod",
		Status:    "ACTIVE",
		ItemCount: 42,
	})
	assert.Equal(t, "arn:aws:dynamodb:us-east-1:123456789012:table/prod", out.ARN)
	assert.Equal(t, "prod", out.Name)
	assert.Equal(t, "ACTIVE", out.Status)
	assert.Equal(t, int64(42), out.ItemCount)
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
	toAdd, toRemove := tagDiff(map[string]string{}, map[string]string{}, "us-east-1~prod")
	assert.NotContains(t, toAdd, "praxis:managed-key")
	assert.Empty(t, toAdd)
	assert.Empty(t, toRemove)
}

func TestProvision_RetriesLimitExceededInsideDurableCallback(t *testing.T) {
	api := &retryDynamoDBAPI{}
	client := setupRetryDynamoDBDriver(t, api)
	key := "us-east-1~prod"

	outputs, err := ingress.Object[DynamoDBTableSpec, DynamoDBTableOutputs](
		client, ServiceName, key, "Provision",
	).Request(t.Context(), baseSpec())
	require.NoError(t, err)
	assert.Equal(t, "prod", outputs.Name)
	assert.Equal(t, "ACTIVE", outputs.Status)
	assert.Equal(t, 2, api.attempts(),
		"LimitExceededException must be retried by Restate instead of becoming terminal")

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
}

func TestGenericDynamoDBTableCoreLifecycle(t *testing.T) {
	api := &retryDynamoDBAPI{createAttempts: 1}
	client := setupRetryDynamoDBDriver(t, api)
	key := "us-east-1~generic-table"
	spec := DynamoDBTableSpec{
		Account: "test", Region: "us-east-1", Name: "generic-table",
		BillingMode: BillingModePayPerRequest, HashKey: "pk", HashKeyType: "S",
		Tags: map[string]string{"suite": "generic"},
	}
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[DynamoDBTableSpec, DynamoDBTableOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs DynamoDBTableSpec) {
			expected := spec
			expected.ManagedKey = key
			assert.Equal(t, expected, inputs)
		},
	})
}

func TestGenericDynamoDBTableObservedImportLifecycle(t *testing.T) {
	api := &retryDynamoDBAPI{observed: ObservedState{
		ARN: "arn:aws:dynamodb:us-east-1:123456789012:table/existing", Name: "existing", Status: "ACTIVE",
		BillingMode: BillingModePayPerRequest, HashKey: "pk", HashKeyType: "S",
		Tags: map[string]string{"suite": "generic"},
	}}
	client := setupRetryDynamoDBDriver(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[DynamoDBTableOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing",
		Ref: types.ImportRef{ResourceID: "existing", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericDynamoDBTableRejectsImmutableIdentityAndRetainsInputs(t *testing.T) {
	api := &retryDynamoDBAPI{createAttempts: 1}
	client := setupRetryDynamoDBDriver(t, api)
	key := "us-east-1~immutable-table"
	spec := DynamoDBTableSpec{
		Account: "test", Region: "us-east-1", Name: "immutable-table",
		BillingMode: BillingModePayPerRequest, HashKey: "pk", HashKeyType: "S",
		RangeKey: "sk", RangeKeyType: "S",
	}
	_, err := ingress.Object[DynamoDBTableSpec, DynamoDBTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, DynamoDBTableSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	tests := []struct {
		field  string
		mutate func(*DynamoDBTableSpec)
	}{
		{field: "name", mutate: func(s *DynamoDBTableSpec) { s.Name = "different-table" }},
		{field: "hashKey", mutate: func(s *DynamoDBTableSpec) { s.HashKey = "other-pk" }},
		{field: "hashKeyType", mutate: func(s *DynamoDBTableSpec) { s.HashKeyType = "N" }},
		{field: "rangeKey", mutate: func(s *DynamoDBTableSpec) { s.RangeKey = "other-sk" }},
		{field: "rangeKeyType", mutate: func(s *DynamoDBTableSpec) { s.RangeKeyType = "N" }},
	}
	for _, tt := range tests {
		changed := accepted
		tt.mutate(&changed)
		_, err = ingress.Object[DynamoDBTableSpec, DynamoDBTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), changed)
		require.Error(t, err)
		assert.Contains(t, err.Error(), tt.field+" is immutable")
		retained, getErr := ingress.Object[restate.Void, DynamoDBTableSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
		require.NoError(t, getErr)
		assert.Equal(t, accepted, retained)
	}
}
