package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/workspace"
	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// create workspace
// ---------------------------------------------------------------------------

func TestCreateWorkspaceCmd_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/WorkspaceService/staging/Configure":
			w.WriteHeader(http.StatusOK)
		case "/WorkspaceIndex/global/List":
			_ = json.NewEncoder(w).Encode([]string{"staging"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"create", "workspace", "staging", "--account", "myaccount", "--region", "us-east-1"}, srv.URL)
	require.NoError(t, err)
}

func TestCreateWorkspaceCmd_MissingAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := executeCmd(t, []string{"create", "workspace", "dev", "--region", "us-east-1"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--account is required")
}

func TestCreateWorkspaceCmd_MissingRegion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := executeCmd(t, []string{"create", "workspace", "dev", "--account", "myaccount"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--region is required")
}

func TestCreateWorkspaceCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"create", "workspace"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// create template
// ---------------------------------------------------------------------------

func TestCreateTemplateCmd_Success(t *testing.T) {
	tmp := t.TempDir()
	tpl := filepath.Join(tmp, "mystack.cue")
	require.NoError(t, os.WriteFile(tpl, []byte(`{name: "test"}`), 0644))

	var gotReq types.RegisterTemplateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.RegisterTemplateResponse{
			Name:   "mystack",
			Digest: "abc123def456abc123def456",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"create", "template", tpl}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "mystack", gotReq.Name)
	assert.Equal(t, `{name: "test"}`, gotReq.Source)
}

func TestCreateTemplateCmd_CustomName(t *testing.T) {
	tmp := t.TempDir()
	tpl := filepath.Join(tmp, "file.cue")
	require.NoError(t, os.WriteFile(tpl, []byte(`{}`), 0644))

	var gotReq types.RegisterTemplateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.RegisterTemplateResponse{
			Name:   "custom-name",
			Digest: "abc123def456abc123def456",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"create", "template", tpl, "--name", "custom-name"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "custom-name", gotReq.Name)
}

func TestCreateTemplateCmd_MissingFile(t *testing.T) {
	_, _, err := executeCmd(t, []string{"create", "template", "/nonexistent.cue"}, "http://unused")
	require.Error(t, err)
}

func TestCreateTemplateCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"create", "template"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// create sink
// ---------------------------------------------------------------------------

func TestCreateSinkCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/NotificationSinkConfig/global/Upsert", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"create", "sink",
		"--name", "my-webhook",
		"--type", "webhook",
		"--url", "https://hooks.example.com/deploy",
	}, srv.URL)
	require.NoError(t, err)
}

func TestCreateSinkCmd_FromFile(t *testing.T) {
	tmp := t.TempDir()
	sinkFile := filepath.Join(tmp, "sink.json")
	sink := orchestrator.NotificationSink{
		Name: "file-sink",
		Type: "webhook",
		URL:  "https://hooks.example.com",
	}
	data, _ := json.Marshal(sink)
	require.NoError(t, os.WriteFile(sinkFile, data, 0644))

	var gotSink orchestrator.NotificationSink
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotSink)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"create", "sink", "--file", sinkFile,
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "file-sink", gotSink.Name)
}

// ---------------------------------------------------------------------------
// set workspace
// ---------------------------------------------------------------------------

func TestSetWorkspaceCmd_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workspace.WorkspaceInfo{
			Name: "prod", Account: "prod-acct", Region: "us-west-2",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"set", "workspace", "prod"}, srv.URL)
	require.NoError(t, err)

	cfg := LoadCLIConfig()
	assert.Equal(t, "prod", cfg.ActiveWorkspace)
}

func TestSetWorkspaceCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"set", "workspace"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// set config
// ---------------------------------------------------------------------------

func TestSetConfigRetentionFieldCmd_MaxAge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "dev"}))

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_ = json.NewEncoder(w).Encode(workspace.EventRetentionPolicy{
				MaxAge:                 "90d",
				MaxEventsPerDeployment: 10000,
			})
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"set", "config", "events.retention.max-age", "180d"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// set concierge
// ---------------------------------------------------------------------------

func TestSetConciergeCmd_Success(t *testing.T) {
	var gotReq conciergeConfigureRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ConciergeConfig/global/Configure", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"set", "concierge",
		"--provider", "openai",
		"--model", "gpt-4o",
		"--api-key", "sk-test",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "openai", gotReq.Provider)
	assert.Equal(t, "gpt-4o", gotReq.Model)
}

func TestSetConciergeCmd_MissingProvider(t *testing.T) {
	_, _, err := executeCmd(t, []string{"set", "concierge"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--provider is required")
}

// ---------------------------------------------------------------------------
// ask
// ---------------------------------------------------------------------------

func TestAskCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(conciergeAskResponse{
			Response: "Here's the status of your deployment",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"ask", "how do I deploy a VPC"}, srv.URL)
	require.NoError(t, err)
}

func TestAskCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"ask"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// approve
// ---------------------------------------------------------------------------

func TestApproveCmd_Success(t *testing.T) {
	var gotReq conciergeApprovalRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ApprovalRelay/Resolve", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"approve",
		"--awakeable-id", "awk-123",
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "awk-123", gotReq.AwakeableID)
	assert.True(t, gotReq.Approved)
}

func TestApproveCmd_Reject(t *testing.T) {
	var gotReq conciergeApprovalRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"approve",
		"--awakeable-id", "awk-456",
		"--reject",
		"--reason", "too risky",
	}, srv.URL)
	require.NoError(t, err)
	assert.False(t, gotReq.Approved)
	assert.Equal(t, "too risky", gotReq.Reason)
}

func TestApproveCmd_MissingAwakeableID(t *testing.T) {
	_, _, err := executeCmd(t, []string{"approve"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--awakeable-id is required")
}

// ---------------------------------------------------------------------------
// test sink
// ---------------------------------------------------------------------------

func TestTestSinkCmd_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"test", "sink/my-webhook"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/SinkRouter/Test", gotPath)
}

func TestTestSinkCmd_UnsupportedType(t *testing.T) {
	_, _, err := executeCmd(t, []string{"test", "bucket/foo"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported test resource type")
}

func TestTestSinkCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"test"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// move
// ---------------------------------------------------------------------------

func TestMoveCmd_SameDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.StateMvResponse{
			SourceDeployment: "web-app",
			DestDeployment:   "web-app",
			OldName:          "myBucket",
			NewName:          "renamedBucket",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"move", "Deployment/web-app/myBucket", "renamedBucket"}, srv.URL)
	require.NoError(t, err)
}

func TestMoveCmd_CrossDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.StateMvResponse{
			SourceDeployment: "source",
			DestDeployment:   "dest",
			OldName:          "myBucket",
			NewName:          "newBucket",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"move", "Deployment/source/myBucket", "Deployment/dest/newBucket"}, srv.URL)
	require.NoError(t, err)
}

func TestMoveCmd_InvalidSource(t *testing.T) {
	_, _, err := executeCmd(t, []string{"move", "badpath", "dest"}, "http://unused")
	require.Error(t, err)
}

func TestMoveCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"move"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// get workspace (verb-first)
// ---------------------------------------------------------------------------

func TestGetWorkspaceCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/WorkspaceService/dev/Get", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workspace.WorkspaceInfo{
			Name: "dev", Account: "dev-acct", Region: "us-east-1",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"get", "workspace", "dev"}, srv.URL)
	require.NoError(t, err)
}

func TestGetWorkspaceCmd_NoActiveWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := executeCmd(t, []string{"get", "workspace"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace specified")
}

// ---------------------------------------------------------------------------
// get config (verb-first)
// ---------------------------------------------------------------------------

func TestGetConfigCmd_VerbFirst_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "dev"}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/WorkspaceService/dev/GetEventRetention", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workspace.EventRetentionPolicy{
			MaxAge:                 "90d",
			MaxEventsPerDeployment: 10000,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"get", "config", "events.retention"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// get concierge (verb-first)
// ---------------------------------------------------------------------------

func TestGetConciergeCmd_Success(t *testing.T) {
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

	_, _, err := executeCmd(t, []string{"get", "concierge"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// get notifications (verb-first)
// ---------------------------------------------------------------------------

func TestGetNotificationsCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/NotificationSinkConfig/global/Health", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(orchestrator.NotificationSinkHealth{
			Total: 2, Healthy: 1, Degraded: 1,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"get", "notifications"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// get template/<name> (verb-first via Kind/Key dispatch)
// ---------------------------------------------------------------------------

func TestGetTemplateCmd_VerbFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.TemplateRecord{
			Metadata: types.TemplateMetadata{Name: "stack1"},
			Digest:   "abc123def456abc123def456",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"get", "template/stack1"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// get sink/<name> (verb-first via Kind/Key dispatch)
// ---------------------------------------------------------------------------

func TestGetSinkCmd_VerbFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(orchestrator.NotificationSink{
			Name: "my-webhook",
			Type: "webhook",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"get", "sink/my-webhook"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// list templates (verb-first)
// ---------------------------------------------------------------------------

func TestListTemplatesCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/PraxisCommandService/ListTemplates", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.TemplateSummary{
			{Name: "stack1", Description: "A stack"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "templates"}, srv.URL)
	require.NoError(t, err)
}

func TestListTemplatesCmd_Aliases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.TemplateSummary{})
	}))
	defer srv.Close()

	for _, alias := range []string{"templates", "template"} {
		_, _, err := executeCmd(t, []string{"list", alias}, srv.URL)
		require.NoError(t, err, "alias %q should work", alias)
	}
}

// ---------------------------------------------------------------------------
// list workspaces (verb-first)
// ---------------------------------------------------------------------------

func TestListWorkspacesCmd_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/WorkspaceIndex/global/List":
			_ = json.NewEncoder(w).Encode([]string{"dev", "prod"})
		case "/WorkspaceService/dev/Get":
			_ = json.NewEncoder(w).Encode(workspace.WorkspaceInfo{
				Name: "dev", Account: "dev-acct", Region: "us-east-1",
			})
		case "/WorkspaceService/prod/Get":
			_ = json.NewEncoder(w).Encode(workspace.WorkspaceInfo{
				Name: "prod", Account: "prod-acct", Region: "us-west-2",
			})
		}
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "workspaces"}, srv.URL)
	require.NoError(t, err)
}

func TestListWorkspacesCmd_Aliases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]string{})
	}))
	defer srv.Close()

	for _, alias := range []string{"workspaces", "workspace"} {
		_, _, err := executeCmd(t, []string{"list", alias}, srv.URL)
		require.NoError(t, err, "alias %q should work", alias)
	}
}

// ---------------------------------------------------------------------------
// list sinks (verb-first)
// ---------------------------------------------------------------------------

func TestListSinksCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.NotificationSink{
			{Name: "slack", Type: "webhook", URL: "https://hooks.slack.com/xxx"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "sinks"}, srv.URL)
	require.NoError(t, err)
}

func TestListSinksCmd_Aliases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.NotificationSink{})
	}))
	defer srv.Close()

	for _, alias := range []string{"sinks", "sink"} {
		_, _, err := executeCmd(t, []string{"list", alias}, srv.URL)
		require.NoError(t, err, "alias %q should work", alias)
	}
}

// ---------------------------------------------------------------------------
// list events (verb-first)
// ---------------------------------------------------------------------------

func TestListEventsCmd_CrossDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/EventIndex/global/Query", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.SequencedCloudEvent{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "events"}, srv.URL)
	require.NoError(t, err)
}

func TestListEventsCmd_PerDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/DeploymentEventStore/my-app/ListSince", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.SequencedCloudEvent{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "events", "Deployment/my-app"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// list concierge (verb-first)
// ---------------------------------------------------------------------------

func TestListConciergeCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ConciergeSession/default/GetHistory", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]conciergeMessage{
			{Role: "user", Content: "hello", Timestamp: "2024-01-01T00:00:00Z"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "concierge"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// delete workspace (verb-first)
// ---------------------------------------------------------------------------

func TestDeleteWorkspaceCmd_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"delete", "workspace/old"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/WorkspaceService/old/Delete", gotPath)
}

// ---------------------------------------------------------------------------
// delete template (verb-first)
// ---------------------------------------------------------------------------

func TestDeleteTemplateCmd_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"delete", "template/mystack"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/PraxisCommandService/DeleteTemplate", gotPath)
}

// ---------------------------------------------------------------------------
// delete sink (verb-first)
// ---------------------------------------------------------------------------

func TestDeleteSinkCmd_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"delete", "sink/old-hook"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/NotificationSinkConfig/global/Remove", gotPath)
}

// ---------------------------------------------------------------------------
// delete concierge (verb-first)
// ---------------------------------------------------------------------------

func TestDeleteConciergeCmd_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"delete", "concierge/my-session"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/ConciergeSession/my-session/Reset", gotPath)
}

// ---------------------------------------------------------------------------
// delete --yes short flag
// ---------------------------------------------------------------------------

func TestDeleteCmd_ShortYesFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.DeleteDeploymentResponse{
			DeploymentKey: "my-app",
			Status:        types.DeploymentDeleting,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"delete", "Deployment/my-app", "-y"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// observe (expanded to any Kind/Key)
// ---------------------------------------------------------------------------

func TestObserveCmd_Resource(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ResourceStatusResponse{
			Status:     types.StatusReady,
			Mode:       types.ModeManaged,
			Generation: 1,
		})
	}))
	defer srv.Close()

	// Observe should see StatusReady (terminal) and exit immediately.
	_, _, err := executeCmd(t, []string{"observe", "S3Bucket/my-bucket", "--timeout", "5s"}, srv.URL)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, callCount, 1)
}

func TestObserveCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"observe"}, "http://unused")
	require.Error(t, err)
}

func TestObserveCmd_InvalidKindKey(t *testing.T) {
	_, _, err := executeCmd(t, []string{"observe", "badformat"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected Kind/Key")
}
