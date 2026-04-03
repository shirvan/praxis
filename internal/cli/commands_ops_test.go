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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// events list
// ---------------------------------------------------------------------------

func TestEventsListCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/DeploymentEventStore/my-app/ListSince", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.SequencedCloudEvent{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"events", "list", "Deployment/my-app"}, srv.URL)
	require.NoError(t, err)
}

func TestEventsListCmd_NonDeployment(t *testing.T) {
	_, _, err := executeCmd(t, []string{"events", "list", "S3Bucket/my-bucket"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only supports Deployment")
}

func TestEventsListCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"events", "list"}, "http://unused")
	require.Error(t, err)
}

func TestEventsListCmd_InvalidArg(t *testing.T) {
	_, _, err := executeCmd(t, []string{"events", "list", "bad-format"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected Kind/Key")
}

// ---------------------------------------------------------------------------
// events query
// ---------------------------------------------------------------------------

func TestEventsQueryCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/EventIndex/global/Query", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.SequencedCloudEvent{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"events", "query", "--severity", "error"}, srv.URL)
	require.NoError(t, err)
}

func TestEventsQueryCmd_InvalidDuration(t *testing.T) {
	_, _, err := executeCmd(t, []string{"events", "query", "--since", "notaduration"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid duration")
}

// ---------------------------------------------------------------------------
// parseLookback / parseFlexibleDuration
// ---------------------------------------------------------------------------

func TestParseFlexibleDuration_Days(t *testing.T) {
	d, err := parseFlexibleDuration("7d")
	require.NoError(t, err)
	assert.Equal(t, 7*24, int(d.Hours()))
}

func TestParseFlexibleDuration_Hours(t *testing.T) {
	d, err := parseFlexibleDuration("3h")
	require.NoError(t, err)
	assert.Equal(t, 3, int(d.Hours()))
}

func TestParseFlexibleDuration_Invalid(t *testing.T) {
	_, err := parseFlexibleDuration("nope")
	require.Error(t, err)
}

func TestParseLookback_Empty(t *testing.T) {
	ts, err := parseLookback("")
	require.NoError(t, err)
	assert.True(t, ts.IsZero())
}

// ---------------------------------------------------------------------------
// notifications add-sink
// ---------------------------------------------------------------------------

func TestNotificationAddSinkCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/NotificationSinkConfig/global/Upsert", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{
		"notifications", "add-sink",
		"--name", "my-webhook",
		"--type", "webhook",
		"--url", "https://hooks.example.com/deploy",
	}, srv.URL)
	require.NoError(t, err)
}

func TestNotificationAddSinkCmd_FromFile(t *testing.T) {
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
		"notifications", "add-sink", "--from-file", sinkFile,
	}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "file-sink", gotSink.Name)
}

// ---------------------------------------------------------------------------
// notifications list-sinks
// ---------------------------------------------------------------------------

func TestNotificationListSinksCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.NotificationSink{
			{Name: "slack", Type: "webhook", URL: "https://hooks.slack.com/xxx"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"notifications", "list-sinks"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// notifications health
// ---------------------------------------------------------------------------

func TestNotificationHealthCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/NotificationSinkConfig/global/Health", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(orchestrator.NotificationSinkHealth{
			Total: 2, Healthy: 1, Degraded: 1,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"notifications", "health"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// notifications get-sink
// ---------------------------------------------------------------------------

func TestNotificationGetSinkCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&orchestrator.NotificationSink{
			Name: "my-webhook",
			Type: "webhook",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"notifications", "get-sink", "my-webhook"}, srv.URL)
	require.NoError(t, err)
}

func TestNotificationGetSinkCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"notifications", "get-sink"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// notifications remove-sink
// ---------------------------------------------------------------------------

func TestNotificationRemoveSinkCmd_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"notifications", "remove-sink", "old-sink"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/NotificationSinkConfig/global/Remove", gotPath)
}

func TestNotificationRemoveSinkCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"notifications", "remove-sink"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// notifications test-sink
// ---------------------------------------------------------------------------

func TestNotificationTestSinkCmd_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"notifications", "test-sink", "my-webhook"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/SinkRouter/Test", gotPath)
}

func TestNotificationTestSinkCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"notifications", "test-sink"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// notification helpers
// ---------------------------------------------------------------------------

func TestBuildNotificationSink_Flags(t *testing.T) {
	sink, err := buildNotificationSink("", "test-sink", "webhook", "https://example.com",
		"deploy.ready,deploy.failed", "deployment", "error,warn", "prod", "my-app-*",
		[]string{"Authorization=Bearer tok"}, 5, 2000, "structured")
	require.NoError(t, err)
	assert.Equal(t, "test-sink", sink.Name)
	assert.Equal(t, "webhook", sink.Type)
	assert.Equal(t, []string{"deploy.ready", "deploy.failed"}, sink.Filter.Types)
	assert.Equal(t, []string{"error", "warn"}, sink.Filter.Severities)
	assert.Equal(t, "Bearer tok", sink.Headers["Authorization"])
	assert.Equal(t, 5, sink.Retry.MaxAttempts)
}

func TestSplitCSV(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, splitCSV("a, b"))
	assert.Nil(t, splitCSV(""))
	assert.Equal(t, []string{"x"}, splitCSV("x"))
}

func TestParseHeaders(t *testing.T) {
	h, err := parseHeaders([]string{"X-Key=value", "Auth=Bearer xyz"})
	require.NoError(t, err)
	assert.Equal(t, "value", h["X-Key"])
	assert.Equal(t, "Bearer xyz", h["Auth"])
}

func TestParseHeaders_Invalid(t *testing.T) {
	_, err := parseHeaders([]string{"noequals"})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// state mv
// ---------------------------------------------------------------------------

func TestStateMvCmd_SameDeployment(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"state", "mv", "Deployment/web-app/myBucket", "renamedBucket"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/DeploymentStateObj/web-app/MoveResource", gotPath)
}

func TestStateMvCmd_CrossDeployment(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// RemoveResource
			assert.Equal(t, "/DeploymentStateObj/source/RemoveResource", r.URL.Path)
			_ = json.NewEncoder(w).Encode(orchestrator.ResourceState{Name: "myBucket"})
		} else {
			// AddResource
			assert.Equal(t, "/DeploymentStateObj/dest/AddResource", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"state", "mv", "Deployment/source/myBucket", "Deployment/dest/newBucket"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestStateMvCmd_InvalidSource(t *testing.T) {
	_, _, err := executeCmd(t, []string{"state", "mv", "badpath", "dest"}, "http://unused")
	require.Error(t, err)
}

func TestStateMvCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"state", "mv"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// config get
// ---------------------------------------------------------------------------

func TestConfigGetCmd_EventsRetention(t *testing.T) {
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

	_, _, err := executeCmd(t, []string{"config", "get", "events.retention"}, srv.URL)
	require.NoError(t, err)
}

func TestConfigGetCmd_UnsupportedPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "dev"}))

	_, _, err := executeCmd(t, []string{"config", "get", "some.unknown.path"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported config path")
}

func TestConfigGetCmd_NoWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := executeCmd(t, []string{"config", "get", "events.retention"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace specified")
}

// ---------------------------------------------------------------------------
// config set
// ---------------------------------------------------------------------------

func TestConfigSetRetentionFieldCmd_MaxAge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "dev"}))

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// GetEventRetention
			_ = json.NewEncoder(w).Encode(workspace.EventRetentionPolicy{
				MaxAge:                 "90d",
				MaxEventsPerDeployment: 10000,
			})
		} else {
			// SetEventRetention
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"config", "set", "events.retention.max-age", "180d"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// resolveWorkspaceName
// ---------------------------------------------------------------------------

func TestResolveWorkspaceName_Explicit(t *testing.T) {
	name, err := resolveWorkspaceName("prod")
	require.NoError(t, err)
	assert.Equal(t, "prod", name)
}

func TestResolveWorkspaceName_Empty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := resolveWorkspaceName("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace specified")
}
