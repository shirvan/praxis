package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// concierge configure
// ---------------------------------------------------------------------------

func TestConciergeConfigureCmd_Success(t *testing.T) {
	var gotReq conciergeConfigureRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ConciergeConfig/global/Configure", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "configure",
		"--provider", "openai",
		"--model", "gpt-4o",
		"--api-key", "sk-test",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "openai", gotReq.Provider)
	assert.Equal(t, "gpt-4o", gotReq.Model)
	assert.Equal(t, "sk-test", gotReq.APIKey)
}

func TestConciergeConfigureCmd_MissingProvider(t *testing.T) {
	_, _, err := executeCmd(t, []string{"concierge", "configure"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--provider is required")
}

// ---------------------------------------------------------------------------
// concierge status
// ---------------------------------------------------------------------------

func TestConciergeStatusCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ConciergeSession/default/GetStatus", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(conciergeSessionStatus{
			Provider:  "openai",
			Model:     "gpt-4o",
			TurnCount: 5,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"concierge", "status"}, srv.URL)
	require.NoError(t, err)
}

func TestConciergeStatusCmd_CustomSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ConciergeSession/my-session/GetStatus", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(conciergeSessionStatus{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"concierge", "status", "--session", "my-session"}, srv.URL)
	require.NoError(t, err)
}

func TestConciergeStatusCmd_WithPendingApproval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(conciergeSessionStatus{
			Provider:  "claude",
			Model:     "claude-sonnet-4-20250514",
			TurnCount: 3,
			PendingApproval: &conciergeApproval{
				AwakeableID: "awk-123",
				Action:      "delete",
				Description: "Delete S3 bucket",
				RequestedAt: "2024-01-01T00:00:00Z",
			},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"concierge", "status"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// concierge history
// ---------------------------------------------------------------------------

func TestConciergeHistoryCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ConciergeSession/default/GetHistory", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]conciergeMessage{
			{Role: "user", Content: "deploy app", Timestamp: "2024-01-01T00:00:00Z"},
			{Role: "assistant", Content: "I'll plan that for you", Timestamp: "2024-01-01T00:00:01Z"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"concierge", "history"}, srv.URL)
	require.NoError(t, err)
}

func TestConciergeHistoryCmd_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]conciergeMessage{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"concierge", "history"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// concierge reset
// ---------------------------------------------------------------------------

func TestConciergeResetCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ConciergeSession/default/Reset", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"concierge", "reset"}, srv.URL)
	require.NoError(t, err)
}

func TestConciergeResetCmd_CustomSession(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"concierge", "reset", "--session", "sess-42"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/ConciergeSession/sess-42/Reset", gotPath)
}

// ---------------------------------------------------------------------------
// concierge approve
// ---------------------------------------------------------------------------

func TestConciergeApproveCmd_Success(t *testing.T) {
	var gotReq conciergeApprovalRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ApprovalRelay/Resolve", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "approve",
		"--awakeable-id", "awk-123",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "awk-123", gotReq.AwakeableID)
	assert.True(t, gotReq.Approved)
}

func TestConciergeApproveCmd_Reject(t *testing.T) {
	var gotReq conciergeApprovalRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "approve",
		"--awakeable-id", "awk-456",
		"--reject",
		"--reason", "too risky",
	}, srv.URL)
	require.NoError(t, err)
	assert.False(t, gotReq.Approved)
	assert.Equal(t, "too risky", gotReq.Reason)
}

func TestConciergeApproveCmd_MissingAwakeableID(t *testing.T) {
	_, _, err := executeCmd(t, []string{"concierge", "approve"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--awakeable-id is required")
}

// ---------------------------------------------------------------------------
// isConciergeUnavailable helper
// ---------------------------------------------------------------------------

func TestIsConciergeUnavailable(t *testing.T) {
	assert.False(t, isConciergeUnavailable(nil))
	assert.True(t, isConciergeUnavailable(errContaining("service not found")))
	assert.True(t, isConciergeUnavailable(errContaining("connection refused")))
	assert.True(t, isConciergeUnavailable(errContaining("no such host")))
	assert.False(t, isConciergeUnavailable(errContaining("timeout")))
	assert.False(t, isConciergeUnavailable(errContaining("internal error")))
}

type textError string

func (e textError) Error() string  { return string(e) }
func errContaining(s string) error { return textError(s) }

// ---------------------------------------------------------------------------
// slack configure
// ---------------------------------------------------------------------------

func TestSlackConfigureCmd_Success(t *testing.T) {
	var gotReq slackConfigRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackGatewayConfig/global/Configure", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "configure",
		"--bot-token", "xoxb-test",
		"--app-token", "xapp-test",
		"--event-channel", "#ops",
		"--allowed-users", "U001,U002",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "xoxb-test", gotReq.BotToken)
	assert.Equal(t, "xapp-test", gotReq.AppToken)
	assert.Equal(t, "#ops", gotReq.EventChannel)
	assert.Equal(t, []string{"U001", "U002"}, gotReq.AllowedUsers)
}

// ---------------------------------------------------------------------------
// slack get-config
// ---------------------------------------------------------------------------

func TestSlackGetConfigCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackGatewayConfig/global/Get", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(slackConfiguration{
			EventChannel: "#ops",
			Version:      2,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"concierge", "slack", "get-config"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// slack allowed-users set
// ---------------------------------------------------------------------------

func TestSlackAllowedUsersSetCmd_Success(t *testing.T) {
	var gotReq slackSetAllowedUsersRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackGatewayConfig/global/SetAllowedUsers", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "allowed-users", "set", "U001,U002",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, []string{"U001", "U002"}, gotReq.UserIDs)
}

func TestSlackAllowedUsersSetCmd_ClearList(t *testing.T) {
	var gotReq slackSetAllowedUsersRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "allowed-users", "set", "",
	}, srv.URL)
	require.NoError(t, err)
	assert.Nil(t, gotReq.UserIDs)
}

// ---------------------------------------------------------------------------
// slack allowed-users add
// ---------------------------------------------------------------------------

func TestSlackAllowedUsersAddCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackGatewayConfig/global/AddAllowedUser", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "allowed-users", "add", "U003",
	}, srv.URL)
	require.NoError(t, err)
}

func TestSlackAllowedUsersAddCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "allowed-users", "add",
	}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// slack allowed-users remove
// ---------------------------------------------------------------------------

func TestSlackAllowedUsersRemoveCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackGatewayConfig/global/RemoveAllowedUser", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "allowed-users", "remove", "U001",
	}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// slack allowed-users list
// ---------------------------------------------------------------------------

func TestSlackAllowedUsersListCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(slackConfiguration{
			AllowedUsers: []string{"U001", "U002"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "allowed-users", "list",
	}, srv.URL)
	require.NoError(t, err)
}

func TestSlackAllowedUsersListCmd_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(slackConfiguration{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "allowed-users", "list",
	}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// slack watch add
// ---------------------------------------------------------------------------

func TestSlackWatchAddCmd_Success(t *testing.T) {
	var gotReq slackAddWatchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackWatchConfig/global/AddWatch", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(slackWatchRule{
			ID:   "w-001",
			Name: "deploy-alerts",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "watch", "add",
		"--name", "deploy-alerts",
		"--channel", "#deploys",
		"--types", "deploy.ready,deploy.failed",
		"--severities", "error",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "deploy-alerts", gotReq.Name)
	assert.Equal(t, "#deploys", gotReq.Channel)
	assert.Equal(t, []string{"deploy.ready", "deploy.failed"}, gotReq.Filter.Types)
	assert.Equal(t, []string{"error"}, gotReq.Filter.Severities)
}

func TestSlackWatchAddCmd_MissingName(t *testing.T) {
	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "watch", "add",
	}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--name is required")
}

// ---------------------------------------------------------------------------
// slack watch list
// ---------------------------------------------------------------------------

func TestSlackWatchListCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackWatchConfig/global/ListWatches", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]slackWatchRule{
			{ID: "w-001", Name: "deploy-alerts", Enabled: true, Channel: "#deploys"},
			{ID: "w-002", Name: "errors", Enabled: false, Channel: "#errors"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "watch", "list",
	}, srv.URL)
	require.NoError(t, err)
}

func TestSlackWatchListCmd_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]slackWatchRule{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "watch", "list",
	}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// slack watch remove
// ---------------------------------------------------------------------------

func TestSlackWatchRemoveCmd_Success(t *testing.T) {
	var gotReq slackRemoveWatchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackWatchConfig/global/RemoveWatch", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "watch", "remove",
		"--id", "w-001",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "w-001", gotReq.ID)
}

func TestSlackWatchRemoveCmd_MissingID(t *testing.T) {
	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "watch", "remove",
	}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

// ---------------------------------------------------------------------------
// slack watch update
// ---------------------------------------------------------------------------

func TestSlackWatchUpdateCmd_Success(t *testing.T) {
	var gotReq slackUpdateWatchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/SlackWatchConfig/global/UpdateWatch", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(slackWatchRule{
			ID:      "w-001",
			Name:    "renamed",
			Enabled: false,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "watch", "update",
		"--id", "w-001",
		"--name", "renamed",
		"--enabled", "false",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "w-001", gotReq.ID)
	assert.NotNil(t, gotReq.Name)
	assert.Equal(t, "renamed", *gotReq.Name)
	assert.NotNil(t, gotReq.Enabled)
	assert.False(t, *gotReq.Enabled)
}

func TestSlackWatchUpdateCmd_MissingID(t *testing.T) {
	_, _, err := executeCmd(t, []string{
		"concierge", "slack", "watch", "update",
	}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

// ---------------------------------------------------------------------------
// buildWatchFilter helper
// ---------------------------------------------------------------------------

func TestBuildWatchFilter(t *testing.T) {
	f := buildWatchFilter("deploy.*,resource.ready", "lifecycle", "error,warn", "prod", "web-app")
	assert.Equal(t, []string{"deploy.*", "resource.ready"}, f.Types)
	assert.Equal(t, []string{"lifecycle"}, f.Categories)
	assert.Equal(t, []string{"error", "warn"}, f.Severities)
	assert.Equal(t, []string{"prod"}, f.Workspaces)
	assert.Equal(t, []string{"web-app"}, f.Deployments)
}

func TestBuildWatchFilter_Empty(t *testing.T) {
	f := buildWatchFilter("", "", "", "", "")
	assert.Nil(t, f.Types)
	assert.Nil(t, f.Categories)
	assert.Nil(t, f.Severities)
	assert.Nil(t, f.Workspaces)
	assert.Nil(t, f.Deployments)
}
