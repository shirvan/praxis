package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shirvan/praxis/internal/core/workspace"
	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// import
// ---------------------------------------------------------------------------

func TestImportCmd_Success(t *testing.T) {
	var gotReq types.ImportRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ImportResponse{
			Key:    "my-bucket",
			Status: "Ready",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"import", "S3Bucket", "--id", "my-bucket", "--region", "us-east-1"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "S3Bucket", gotReq.Kind)
	assert.Equal(t, "my-bucket", gotReq.ResourceID)
	assert.Equal(t, "us-east-1", gotReq.Region)
	assert.Equal(t, types.ModeManaged, gotReq.Mode)
}

func TestImportCmd_ObservedMode(t *testing.T) {
	var gotReq types.ImportRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ImportResponse{Key: "my-bucket"})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"import", "S3Bucket", "--id", "my-bucket", "--region", "us-east-1", "--observe"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, gotReq.Mode)
}

func TestImportCmd_MissingID(t *testing.T) {
	_, _, err := executeCmd(t, []string{"import", "S3Bucket", "--region", "us-east-1"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required flag")
}

func TestImportCmd_MissingRegion(t *testing.T) {
	// Unset env to ensure no default region.
	t.Setenv("PRAXIS_REGION", "")
	_, _, err := executeCmd(t, []string{"import", "S3Bucket", "--id", "my-bucket"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--region is required")
}

func TestImportCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"import"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// template register
// ---------------------------------------------------------------------------

func TestTemplateRegisterCmd_Success(t *testing.T) {
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

	_, _, err := executeCmd(t, []string{"template", "register", tpl}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "mystack", gotReq.Name)
	assert.Equal(t, `{name: "test"}`, gotReq.Source)
}

func TestTemplateRegisterCmd_CustomName(t *testing.T) {
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

	_, _, err := executeCmd(t, []string{"template", "register", tpl, "--name", "custom-name"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "custom-name", gotReq.Name)
}

func TestTemplateRegisterCmd_MissingFile(t *testing.T) {
	_, _, err := executeCmd(t, []string{"template", "register", "/nonexistent.cue"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// template list
// ---------------------------------------------------------------------------

func TestTemplateListCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/PraxisCommandService/ListTemplates", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.TemplateSummary{
			{Name: "stack1", Description: "A stack"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"template", "list"}, srv.URL)
	require.NoError(t, err)
}

func TestTemplateListCmd_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.TemplateSummary{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"template", "list"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// template describe
// ---------------------------------------------------------------------------

func TestTemplateDescribeCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.TemplateRecord{
			Metadata: types.TemplateMetadata{Name: "stack1"},
			Digest:   "abc123def456abc123def456",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"template", "describe", "stack1"}, srv.URL)
	require.NoError(t, err)
}

func TestTemplateDescribeCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"template", "describe"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// template delete
// ---------------------------------------------------------------------------

func TestTemplateDeleteCmd_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"template", "delete", "mystack"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/PraxisCommandService/DeleteTemplate", gotPath)
}

func TestTemplateDeleteCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"template", "delete"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// workspace create
// ---------------------------------------------------------------------------

func TestWorkspaceCreateCmd_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/WorkspaceService/dev/Configure":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/WorkspaceIndex/global/List":
			_ = json.NewEncoder(w).Encode([]string{"dev"})
		default:
			t.Logf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"workspace", "create", "dev", "--account", "myaccount", "--region", "us-east-1"}, srv.URL)
	require.NoError(t, err)
}

func TestWorkspaceCreateCmd_MissingAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := executeCmd(t, []string{"workspace", "create", "dev", "--region", "us-east-1"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--account is required")
}

func TestWorkspaceCreateCmd_MissingRegion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := executeCmd(t, []string{"workspace", "create", "dev", "--account", "myaccount"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--region is required")
}

func TestWorkspaceCreateCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"workspace", "create"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// workspace list
// ---------------------------------------------------------------------------

func TestWorkspaceListCmd_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/WorkspaceIndex/global/List":
			_ = json.NewEncoder(w).Encode([]string{"dev", "prod"})
		case r.URL.Path == "/WorkspaceService/dev/Get":
			_ = json.NewEncoder(w).Encode(workspace.WorkspaceInfo{
				Name: "dev", Account: "dev-acct", Region: "us-east-1",
			})
		case r.URL.Path == "/WorkspaceService/prod/Get":
			_ = json.NewEncoder(w).Encode(workspace.WorkspaceInfo{
				Name: "prod", Account: "prod-acct", Region: "us-west-2",
			})
		}
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"workspace", "list"}, srv.URL)
	require.NoError(t, err)
}

func TestWorkspaceListCmd_Empty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]string{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"workspace", "list"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// workspace select
// ---------------------------------------------------------------------------

func TestWorkspaceSelectCmd_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workspace.WorkspaceInfo{
			Name: "prod", Account: "prod-acct", Region: "us-west-2",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"workspace", "select", "prod"}, srv.URL)
	require.NoError(t, err)

	// Verify the config was persisted.
	cfg := LoadCLIConfig()
	assert.Equal(t, "prod", cfg.ActiveWorkspace)
}

func TestWorkspaceSelectCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"workspace", "select"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// workspace show
// ---------------------------------------------------------------------------

func TestWorkspaceShowCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/WorkspaceService/dev/Get", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workspace.WorkspaceInfo{
			Name: "dev", Account: "dev-acct", Region: "us-east-1",
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"workspace", "show", "dev"}, srv.URL)
	require.NoError(t, err)
}

func TestWorkspaceShowCmd_NoActiveWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := executeCmd(t, []string{"workspace", "show"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace specified")
}

// ---------------------------------------------------------------------------
// workspace delete
// ---------------------------------------------------------------------------

func TestWorkspaceDeleteCmd_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"workspace", "delete", "old"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/WorkspaceService/old/Delete", gotPath)
}

func TestWorkspaceDeleteCmd_ClearsActiveWorkspace(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Pre-set active workspace to the one we'll delete.
	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "doomed"}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"workspace", "delete", "doomed"}, srv.URL)
	require.NoError(t, err)

	cfg := LoadCLIConfig()
	assert.Empty(t, cfg.ActiveWorkspace)
}

func TestWorkspaceDeleteCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"workspace", "delete"}, "http://unused")
	require.Error(t, err)
}
