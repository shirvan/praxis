package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// import (unchanged — top-level verb)
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
// create template (verb-first — edge cases)
// ---------------------------------------------------------------------------

func TestCreateTemplateCmd_NameDefaultsToFileStem(t *testing.T) {
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

// ---------------------------------------------------------------------------
// list templates (verb-first — empty list)
// ---------------------------------------------------------------------------

func TestListTemplatesCmd_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.TemplateSummary{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "templates"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// list workspaces (verb-first — empty list)
// ---------------------------------------------------------------------------

func TestListWorkspacesCmd_Empty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]string{})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "workspaces"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// delete workspace clears active workspace
// ---------------------------------------------------------------------------

func TestDeleteWorkspaceCmd_ClearsActiveWorkspace(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Pre-set active workspace to the one we'll delete.
	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "doomed"}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"delete", "workspace/doomed"}, srv.URL)
	require.NoError(t, err)

	cfg := LoadCLIConfig()
	assert.Empty(t, cfg.ActiveWorkspace)
}
