package workspace

// Handler-level unit tests for the WorkspaceService and WorkspaceIndex Restate
// Virtual Objects. These drive the real handlers against the Restate SDK's
// mock context (restate.WithMockContext + mocks.NewMockContext), following the
// same pattern as internal/core/authservice/service_test.go, so no Restate
// container is needed. The AuthService GetStatus dependency in Configure is
// intercepted via MockObjectClient.

import (
	"path/filepath"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/orchestrator"
)

// testSchemaDir points at the repository's real CUE schema directory so
// SetEventRetention validates against the shipped retention.cue schema.
var testSchemaDir = filepath.Join("..", "..", "..", "schemas")

// expectNoConfig makes Get("config") report no stored workspace config.
func expectNoConfig(mockCtx *mocks.MockContext) {
	mockCtx.EXPECT().Get("config", mock.Anything).Return(false, nil)
}

// expectGetStatus mocks the AuthService GetStatus object call made by
// Configure to verify the account alias.
func expectGetStatus(mockCtx *mocks.MockContext, alias string, status authservice.CredentialStatus, err error) {
	mockCtx.EXPECT().MockObjectClient(authservice.ServiceName, alias, "GetStatus").
		RequestAndReturn(restate.Void{}, status, err)
}

// ---------------------------------------------------------------------------
// Configure
// ---------------------------------------------------------------------------

func TestConfigure_ValidationFailures_Terminal400(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		cfg     WorkspaceConfig
		wantMsg string
	}{
		{
			name:    "name does not match object key",
			key:     "dev",
			cfg:     WorkspaceConfig{Name: "prod", Account: "acct", Region: "us-east-1"},
			wantMsg: "does not match object key",
		},
		{
			name:    "invalid workspace name",
			key:     "Bad.Name",
			cfg:     WorkspaceConfig{Name: "Bad.Name", Account: "acct", Region: "us-east-1"},
			wantMsg: "must match",
		},
		{
			name:    "missing account",
			key:     "dev",
			cfg:     WorkspaceConfig{Name: "dev", Account: "  ", Region: "us-east-1"},
			wantMsg: "account is required",
		},
		{
			name:    "missing region",
			key:     "dev",
			cfg:     WorkspaceConfig{Name: "dev", Account: "acct", Region: ""},
			wantMsg: "region is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewWorkspaceService(testSchemaDir)
			mockCtx := mocks.NewMockContext(t)
			mockCtx.EXPECT().Key().Return(tc.key)
			expectNoConfig(mockCtx)

			err := svc.Configure(restate.WithMockContext(mockCtx), tc.cfg)
			require.Error(t, err)
			assert.True(t, restate.IsTerminalError(err), "validation failures must be terminal")
			assert.Equal(t, restate.Code(400), restate.ErrorCode(err))
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestConfigure_UnknownAccountAlias_Terminal400(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("dev")
	expectNoConfig(mockCtx)
	// GetStatus succeeds for any key; an unregistered alias is signalled by an
	// empty CredentialSource in the response.
	expectGetStatus(mockCtx, "ghost", authservice.CredentialStatus{AccountAlias: "ghost"}, nil)

	err := svc.Configure(restate.WithMockContext(mockCtx),
		WorkspaceConfig{Name: "dev", Account: "ghost", Region: "us-east-1"})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, restate.Code(400), restate.ErrorCode(err))
	assert.Contains(t, err.Error(), `unknown account alias "ghost"`)
}

func TestConfigure_AccountCheckTransportError_StaysRetryable(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("dev")
	expectNoConfig(mockCtx)
	expectGetStatus(mockCtx, "acct", authservice.CredentialStatus{}, assert.AnError)

	err := svc.Configure(restate.WithMockContext(mockCtx),
		WorkspaceConfig{Name: "dev", Account: "acct", Region: "us-east-1"})
	require.Error(t, err)
	assert.False(t, restate.IsTerminalError(err),
		"transport errors must stay retryable so Restate can retry")
	assert.Contains(t, err.Error(), `verify account alias "acct"`)
}

func TestConfigure_AccountCheckTerminalError_Propagates(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	terminal := restate.TerminalError(assert.AnError, 403)

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("dev")
	expectNoConfig(mockCtx)
	expectGetStatus(mockCtx, "acct", authservice.CredentialStatus{}, terminal)

	err := svc.Configure(restate.WithMockContext(mockCtx),
		WorkspaceConfig{Name: "dev", Account: "acct", Region: "us-east-1"})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, restate.Code(403), restate.ErrorCode(err))
}

func TestConfigure_Success_StoresConfigAndRegisters(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	cfg := WorkspaceConfig{
		Name:      "dev",
		Account:   "acct",
		Region:    "ca-central-1",
		Variables: map[string]string{"env": "dev"},
	}

	var captured WorkspaceConfig
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("dev")
	expectNoConfig(mockCtx)
	expectGetStatus(mockCtx, "acct", authservice.CredentialStatus{CredentialSource: "static"}, nil)
	mockCtx.On("Set", "config", mock.Anything).Run(func(args mock.Arguments) {
		captured = args.Get(1).(WorkspaceConfig)
	})
	mockCtx.EXPECT().MockObjectClient(WorkspaceIndexServiceName, WorkspaceIndexGlobalKey, "Register").
		MockSend("dev")

	err := svc.Configure(restate.WithMockContext(mockCtx), cfg)
	require.NoError(t, err)
	assert.Equal(t, cfg, captured, "stored config must match the request")
	// No retention configured: no sweep scheduling (enforced by mock expectations).
}

func TestConfigure_Update_PreservesExistingEventsAndSchedulesSweep(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	existing := &WorkspaceConfig{
		Name:    "dev",
		Account: "old-acct",
		Region:  "us-east-1",
		Events:  &EventSettings{Retention: &EventRetentionPolicy{MaxAge: "30d"}},
	}
	update := WorkspaceConfig{Name: "dev", Account: "acct", Region: "eu-west-1"} // Events nil

	var captured WorkspaceConfig
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("dev")
	mockCtx.EXPECT().GetAndReturn("config", existing)
	expectGetStatus(mockCtx, "acct", authservice.CredentialStatus{CredentialSource: "role"}, nil)
	mockCtx.On("Set", "config", mock.Anything).Run(func(args mock.Arguments) {
		captured = args.Get(1).(WorkspaceConfig)
	})
	mockCtx.EXPECT().MockObjectClient(WorkspaceIndexServiceName, WorkspaceIndexGlobalKey, "Register").
		MockSend("dev")
	mockCtx.EXPECT().MockObjectClient(orchestrator.EventBusServiceName, orchestrator.EventBusGlobalKey, "ScheduleRetentionSweep").
		MockSend(orchestrator.RetentionSweepRequest{Workspace: "dev"})

	err := svc.Configure(restate.WithMockContext(mockCtx), update)
	require.NoError(t, err)

	assert.Equal(t, "acct", captured.Account)
	assert.Equal(t, "eu-west-1", captured.Region)
	require.NotNil(t, captured.Events, "update without Events must preserve existing event settings")
	assert.Equal(t, existing.Events.Retention, captured.Events.Retention)
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestGet_MissingWorkspace_Terminal404(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("ghost")
	expectNoConfig(mockCtx)

	_, err := svc.Get(restate.WithMockContext(mockCtx), restate.Void{})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, restate.Code(404), restate.ErrorCode(err))
	assert.Contains(t, err.Error(), `workspace "ghost" is not configured`)
}

func TestGet_ReturnsStoredConfig(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	stored := &WorkspaceConfig{
		Name:      "dev",
		Account:   "acct",
		Region:    "ca-central-1",
		Variables: map[string]string{"env": "dev"},
		Events:    &EventSettings{Retention: &EventRetentionPolicy{MaxAge: "30d"}},
	}

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().GetAndReturn("config", stored)

	info, err := svc.Get(restate.WithMockContext(mockCtx), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, WorkspaceInfo{
		Name:      stored.Name,
		Account:   stored.Account,
		Region:    stored.Region,
		Variables: stored.Variables,
		Events:    stored.Events,
	}, info)
}

// ---------------------------------------------------------------------------
// SetEventRetention / GetEventRetention
// ---------------------------------------------------------------------------

func TestSetEventRetention_UnconfiguredWorkspace_Terminal404(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("ghost")
	expectNoConfig(mockCtx)

	err := svc.SetEventRetention(restate.WithMockContext(mockCtx), EventRetentionPolicy{MaxAge: "30d"})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, restate.Code(404), restate.ErrorCode(err))
}

func TestSetEventRetention_InvalidPolicies_Terminal400(t *testing.T) {
	tests := []struct {
		name   string
		policy EventRetentionPolicy
	}{
		{"maxAge wrong unit", EventRetentionPolicy{MaxAge: "2w"}},
		{"maxAge not a duration", EventRetentionPolicy{MaxAge: "ninety days"}},
		{"sweepInterval minutes not allowed", EventRetentionPolicy{SweepInterval: "30m"}},
		{"maxEventsPerDeployment below minimum", EventRetentionPolicy{MaxEventsPerDeployment: 5}},
		{"maxEventsPerDeployment above maximum", EventRetentionPolicy{MaxEventsPerDeployment: 2_000_000}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewWorkspaceService(testSchemaDir)
			mockCtx := mocks.NewMockContext(t)
			mockCtx.EXPECT().GetAndReturn("config", &WorkspaceConfig{Name: "dev", Account: "acct", Region: "us-east-1"})

			err := svc.SetEventRetention(restate.WithMockContext(mockCtx), tc.policy)
			require.Error(t, err)
			assert.True(t, restate.IsTerminalError(err))
			assert.Equal(t, restate.Code(400), restate.ErrorCode(err))
			assert.Contains(t, err.Error(), "invalid retention policy")
		})
	}
}

func TestSetEventRetention_ValidPolicy_NormalizesStoresAndSchedules(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)

	var captured *WorkspaceConfig
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("dev")
	mockCtx.EXPECT().GetAndReturn("config", &WorkspaceConfig{Name: "dev", Account: "acct", Region: "us-east-1"})
	mockCtx.On("Set", "config", mock.Anything).Run(func(args mock.Arguments) {
		captured = args.Get(1).(*WorkspaceConfig)
	})
	mockCtx.EXPECT().MockObjectClient(orchestrator.EventBusServiceName, orchestrator.EventBusGlobalKey, "ScheduleRetentionSweep").
		MockSend(orchestrator.RetentionSweepRequest{Workspace: "dev"})

	// Only maxAge supplied; CUE defaults must fill the rest.
	err := svc.SetEventRetention(restate.WithMockContext(mockCtx), EventRetentionPolicy{MaxAge: "30d"})
	require.NoError(t, err)

	require.NotNil(t, captured)
	require.NotNil(t, captured.Events)
	require.NotNil(t, captured.Events.Retention)
	assert.Equal(t, EventRetentionPolicy{
		MaxAge:                 "30d",
		MaxEventsPerDeployment: 10000,
		SweepInterval:          "24h",
	}, *captured.Events.Retention, "unset fields must be filled from CUE defaults")
}

func TestGetEventRetention(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)

	t.Run("unconfigured workspace is terminal 404", func(t *testing.T) {
		mockCtx := mocks.NewMockContext(t)
		mockCtx.EXPECT().Key().Return("ghost")
		expectNoConfig(mockCtx)

		_, err := svc.GetEventRetention(restate.WithMockContext(mockCtx), restate.Void{})
		require.Error(t, err)
		assert.True(t, restate.IsTerminalError(err))
		assert.Equal(t, restate.Code(404), restate.ErrorCode(err))
	})

	t.Run("no override returns system defaults", func(t *testing.T) {
		mockCtx := mocks.NewMockContext(t)
		mockCtx.EXPECT().GetAndReturn("config", &WorkspaceConfig{Name: "dev", Account: "acct", Region: "us-east-1"})

		policy, err := svc.GetEventRetention(restate.WithMockContext(mockCtx), restate.Void{})
		require.NoError(t, err)
		assert.Equal(t, DefaultEventRetentionPolicy(), policy)
	})

	t.Run("configured override is returned", func(t *testing.T) {
		want := EventRetentionPolicy{MaxAge: "7d", MaxEventsPerDeployment: 500, SweepInterval: "12h"}
		mockCtx := mocks.NewMockContext(t)
		mockCtx.EXPECT().GetAndReturn("config", &WorkspaceConfig{
			Name: "dev", Account: "acct", Region: "us-east-1",
			Events: &EventSettings{Retention: &want},
		})

		policy, err := svc.GetEventRetention(restate.WithMockContext(mockCtx), restate.Void{})
		require.NoError(t, err)
		assert.Equal(t, want, policy)
	})
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDelete_ClearsStateAndDeregisters(t *testing.T) {
	svc := NewWorkspaceService(testSchemaDir)
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("dev")
	mockCtx.EXPECT().ClearAll().Return()
	mockCtx.EXPECT().MockObjectClient(WorkspaceIndexServiceName, WorkspaceIndexGlobalKey, "Deregister").
		MockSend("dev")

	err := svc.Delete(restate.WithMockContext(mockCtx), restate.Void{})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// WorkspaceIndex
// ---------------------------------------------------------------------------

func TestWorkspaceIndex_RegisterAndDeregister(t *testing.T) {
	idx := WorkspaceIndex{}

	t.Run("register on empty index creates the set", func(t *testing.T) {
		var captured map[string]bool
		mockCtx := mocks.NewMockContext(t)
		mockCtx.EXPECT().Get("names", mock.Anything).Return(false, nil)
		mockCtx.On("Set", "names", mock.Anything).Run(func(args mock.Arguments) {
			captured = args.Get(1).(map[string]bool)
		})

		require.NoError(t, idx.Register(restate.WithMockContext(mockCtx), "dev"))
		assert.Equal(t, map[string]bool{"dev": true}, captured)
	})

	t.Run("deregister removes the name", func(t *testing.T) {
		var captured map[string]bool
		mockCtx := mocks.NewMockContext(t)
		mockCtx.EXPECT().GetAndReturn("names", map[string]bool{"dev": true, "prod": true})
		mockCtx.On("Set", "names", mock.Anything).Run(func(args mock.Arguments) {
			captured = args.Get(1).(map[string]bool)
		})

		require.NoError(t, idx.Deregister(restate.WithMockContext(mockCtx), "dev"))
		assert.Equal(t, map[string]bool{"prod": true}, captured)
	})

	t.Run("deregister on empty index is a no-op", func(t *testing.T) {
		mockCtx := mocks.NewMockContext(t)
		mockCtx.EXPECT().Get("names", mock.Anything).Return(false, nil)

		require.NoError(t, idx.Deregister(restate.WithMockContext(mockCtx), "ghost"))
		// No Set expected; asserted by mock expectations on cleanup.
	})
}

func TestWorkspaceIndex_List(t *testing.T) {
	idx := WorkspaceIndex{}

	t.Run("empty index returns nil", func(t *testing.T) {
		mockCtx := mocks.NewMockContext(t)
		mockCtx.EXPECT().Get("names", mock.Anything).Return(false, nil)

		names, err := idx.List(restate.WithMockContext(mockCtx), restate.Void{})
		require.NoError(t, err)
		assert.Nil(t, names)
	})

	t.Run("names are returned sorted", func(t *testing.T) {
		mockCtx := mocks.NewMockContext(t)
		mockCtx.EXPECT().GetAndReturn("names", map[string]bool{"prod": true, "dev": true, "staging": true})

		names, err := idx.List(restate.WithMockContext(mockCtx), restate.Void{})
		require.NoError(t, err)
		assert.Equal(t, []string{"dev", "prod", "staging"}, names)
	})
}
