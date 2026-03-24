package route53zone

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"

	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/pkg/types"
)

type fakeHostedZoneAPI struct {
	mu sync.Mutex

	nextID        string
	zones         map[string]ObservedState
	nameIndex     map[string]string
	createCalls   int
	deleteCalls   int
	commentCalls  int
	tagCalls      int
	assocCalls    int
	disassocCalls int

	createFunc   func(context.Context, HostedZoneSpec) (string, error)
	describeFunc func(context.Context, string) (ObservedState, error)
	findFunc     func(context.Context, string) (string, error)
	deleteFunc   func(context.Context, string) error
}

func newFakeHostedZoneAPI() *fakeHostedZoneAPI {
	return &fakeHostedZoneAPI{
		nextID:    "Z123456",
		zones:     map[string]ObservedState{},
		nameIndex: map[string]string{},
	}
}

func (f *fakeHostedZoneAPI) CreateHostedZone(ctx context.Context, spec HostedZoneSpec) (string, error) {
	if f.createFunc != nil {
		return f.createFunc(ctx, spec)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	id := f.nextID
	if id == "" {
		id = fmt.Sprintf("Z%d", f.createCalls)
	}
	f.zones[id] = ObservedState{
		HostedZoneId: id,
		Name:         normalizeZoneName(spec.Name),
		Comment:      spec.Comment,
		IsPrivate:    spec.IsPrivate,
		VPCs:         normalizeHostedZoneVPCs(spec.VPCs),
		Tags:         copyTags(spec.Tags),
		NameServers:  []string{"ns-1.example.com", "ns-2.example.com"},
		RecordCount:  2,
	}
	f.nameIndex[normalizeZoneName(spec.Name)] = id
	return id, nil
}

func (f *fakeHostedZoneAPI) DescribeHostedZone(ctx context.Context, hostedZoneID string) (ObservedState, error) {
	if f.describeFunc != nil {
		return f.describeFunc(ctx, hostedZoneID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.zones[hostedZoneID]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "NoSuchHostedZone", message: "not found"}
	}
	return cloneObserved(obs), nil
}

func (f *fakeHostedZoneAPI) FindHostedZoneByName(ctx context.Context, name string) (string, error) {
	if f.findFunc != nil {
		return f.findFunc(ctx, name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nameIndex[normalizeZoneName(name)], nil
}

func (f *fakeHostedZoneAPI) FindHostedZoneByTags(ctx context.Context, tags map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matches []string
	for id, observed := range f.zones {
		matched := true
		for key, value := range tags {
			if observed.Tags[key] != value {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous lookup")
	}
}

func (f *fakeHostedZoneAPI) UpdateComment(ctx context.Context, hostedZoneID, comment string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commentCalls++
	obs, ok := f.zones[hostedZoneID]
	if !ok {
		return &mockAPIError{code: "NoSuchHostedZone", message: "not found"}
	}
	obs.Comment = comment
	f.zones[hostedZoneID] = obs
	return nil
}

func (f *fakeHostedZoneAPI) AssociateVPC(ctx context.Context, hostedZoneID string, vpc HostedZoneVPC) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assocCalls++
	obs, ok := f.zones[hostedZoneID]
	if !ok {
		return &mockAPIError{code: "NoSuchHostedZone", message: "not found"}
	}
	obs.VPCs = append(obs.VPCs, vpc)
	obs.VPCs = normalizeHostedZoneVPCs(obs.VPCs)
	f.zones[hostedZoneID] = obs
	return nil
}

func (f *fakeHostedZoneAPI) DisassociateVPC(ctx context.Context, hostedZoneID string, vpc HostedZoneVPC) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disassocCalls++
	obs, ok := f.zones[hostedZoneID]
	if !ok {
		return &mockAPIError{code: "NoSuchHostedZone", message: "not found"}
	}
	targetKey := hostedZoneVPCKey(vpc)
	filtered := make([]HostedZoneVPC, 0, len(obs.VPCs))
	for _, v := range obs.VPCs {
		if hostedZoneVPCKey(v) != targetKey {
			filtered = append(filtered, v)
		}
	}
	obs.VPCs = normalizeHostedZoneVPCs(filtered)
	f.zones[hostedZoneID] = obs
	return nil
}

func (f *fakeHostedZoneAPI) UpdateTags(ctx context.Context, hostedZoneID string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagCalls++
	obs, ok := f.zones[hostedZoneID]
	if !ok {
		return &mockAPIError{code: "NoSuchHostedZone", message: "not found"}
	}
	praxisTags := map[string]string{}
	for key, value := range obs.Tags {
		if len(key) >= 7 && key[:7] == "praxis:" {
			praxisTags[key] = value
		}
	}
	obs.Tags = map[string]string{}
	for key, value := range praxisTags {
		obs.Tags[key] = value
	}
	for key, value := range tags {
		obs.Tags[key] = value
	}
	f.zones[hostedZoneID] = obs
	return nil
}

func (f *fakeHostedZoneAPI) DeleteHostedZone(ctx context.Context, hostedZoneID string) error {
	if f.deleteFunc != nil {
		return f.deleteFunc(ctx, hostedZoneID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	obs, ok := f.zones[hostedZoneID]
	if !ok {
		return &mockAPIError{code: "NoSuchHostedZone", message: "not found"}
	}
	delete(f.zones, hostedZoneID)
	delete(f.nameIndex, obs.Name)
	return nil
}

func cloneObserved(obs ObservedState) ObservedState {
	clone := obs
	if obs.Tags != nil {
		clone.Tags = make(map[string]string, len(obs.Tags))
		for key, value := range obs.Tags {
			clone.Tags[key] = value
		}
	}
	if obs.VPCs != nil {
		clone.VPCs = make([]HostedZoneVPC, len(obs.VPCs))
		copy(clone.VPCs, obs.VPCs)
	}
	if obs.NameServers != nil {
		clone.NameServers = make([]string, len(obs.NameServers))
		copy(clone.NameServers, obs.NameServers)
	}
	return clone
}

func copyTags(tags map[string]string) map[string]string {
	if tags == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		out[key] = value
	}
	return out
}

func setupHostedZoneDriver(t *testing.T, api HostedZoneAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewHostedZoneDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(cfg aws.Config) HostedZoneAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
}

func testZoneSpec(name string, tags map[string]string) HostedZoneSpec {
	if tags == nil {
		tags = map[string]string{"env": "dev"}
	}
	return HostedZoneSpec{
		Account: "test",
		Name:    name,
		Comment: "test hosted zone",
		Tags:    tags,
	}
}

func TestServiceName(t *testing.T) {
	drv := NewHostedZoneDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestProvision_CreatesPublicZone(t *testing.T) {
	api := newFakeHostedZoneAPI()
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	outputs, err := ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testZoneSpec(key, map[string]string{"env": "dev"}))
	require.NoError(t, err)
	assert.Equal(t, "Z123456", outputs.HostedZoneId)
	assert.Equal(t, "example.com", outputs.Name)
	assert.False(t, outputs.IsPrivate)
	assert.NotEmpty(t, outputs.NameServers)
	assert.Equal(t, 1, api.createCalls)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}

func TestProvision_IdempotentReprovision(t *testing.T) {
	api := newFakeHostedZoneAPI()
	client := setupHostedZoneDriver(t, api)
	key := "example.com"
	spec := testZoneSpec(key, map[string]string{"env": "dev"})

	out1, err := ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.HostedZoneId, out2.HostedZoneId)
	assert.Equal(t, 1, api.createCalls)
}

func TestProvision_TagUpdate(t *testing.T) {
	api := newFakeHostedZoneAPI()
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	_, err := ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testZoneSpec(key, map[string]string{"env": "dev"}))
	require.NoError(t, err)

	_, err = ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testZoneSpec(key, map[string]string{"env": "prod"}))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, api.tagCalls, 1)
	assert.Equal(t, "prod", api.zones["Z123456"].Tags["env"])
}

func TestProvision_CommentUpdate(t *testing.T) {
	api := newFakeHostedZoneAPI()
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	_, err := ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), HostedZoneSpec{Account: "test", Comment: "old comment", Tags: map[string]string{}})
	require.NoError(t, err)

	_, err = ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), HostedZoneSpec{Account: "test", Comment: "new comment", Tags: map[string]string{}})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, api.commentCalls, 1)
	assert.Equal(t, "new comment", api.zones["Z123456"].Comment)
}

func TestImport_ExistingZone(t *testing.T) {
	api := newFakeHostedZoneAPI()
	api.zones["Z123456"] = ObservedState{
		HostedZoneId: "Z123456",
		Name:         "example.com",
		Comment:      "imported zone",
		NameServers:  []string{"ns-1.example.com"},
		RecordCount:  5,
		Tags:         map[string]string{"env": "prod"},
	}
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	outputs, err := ingress.Object[types.ImportRef, HostedZoneOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "Z123456", Account: "test"})
	require.NoError(t, err)
	assert.Equal(t, "Z123456", outputs.HostedZoneId)
	assert.Equal(t, "example.com", outputs.Name)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestDelete_DeletesZone(t *testing.T) {
	api := newFakeHostedZoneAPI()
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	_, err := ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testZoneSpec(key, nil))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, api.deleteCalls)
	_, ok := api.zones["Z123456"]
	assert.False(t, ok)
}

func TestDelete_ObservedModeBlocked(t *testing.T) {
	api := newFakeHostedZoneAPI()
	api.zones["Z123456"] = ObservedState{HostedZoneId: "Z123456", Name: "example.com", Tags: map[string]string{}}
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	_, err := ingress.Object[types.ImportRef, HostedZoneOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "Z123456", Account: "test"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestReconcile_CommentDriftCorrected(t *testing.T) {
	api := newFakeHostedZoneAPI()
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	_, err := ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testZoneSpec(key, map[string]string{}))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.zones["Z123456"]
	obs.Comment = "drifted comment"
	api.zones["Z123456"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, "test hosted zone", api.zones["Z123456"].Comment)
}

func TestReconcile_ObservedModeReportsOnly(t *testing.T) {
	api := newFakeHostedZoneAPI()
	api.zones["Z123456"] = ObservedState{
		HostedZoneId: "Z123456",
		Name:         "example.com",
		Comment:      "original",
		Tags:         map[string]string{},
	}
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	_, err := ingress.Object[types.ImportRef, HostedZoneOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "Z123456", Account: "test"})
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.zones["Z123456"]
	obs.Comment = "drifted comment"
	api.zones["Z123456"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
	assert.Equal(t, "drifted comment", api.zones["Z123456"].Comment)
}

func TestGetOutputs_ReturnsCurrentState(t *testing.T) {
	api := newFakeHostedZoneAPI()
	client := setupHostedZoneDriver(t, api)
	key := "example.com"

	_, err := ingress.Object[HostedZoneSpec, HostedZoneOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testZoneSpec(key, nil))
	require.NoError(t, err)

	outputs, err := ingress.Object[restate.Void, HostedZoneOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "Z123456", outputs.HostedZoneId)
	assert.Equal(t, "example.com", outputs.Name)
}
