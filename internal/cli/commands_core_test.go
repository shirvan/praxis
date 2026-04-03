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
// plan
// ---------------------------------------------------------------------------

func TestPlanCmd_Success(t *testing.T) {
	tmp := t.TempDir()
	tpl := filepath.Join(tmp, "stack.cue")
	require.NoError(t, os.WriteFile(tpl, []byte(`{}`), 0644))

	var gotReq types.PlanRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		resp := types.PlanResponse{
			Plan: &types.PlanResult{
				Summary: types.PlanSummary{ToCreate: 1},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"plan", tpl}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "{}", gotReq.Template)
}

func TestPlanCmd_MissingFile(t *testing.T) {
	_, _, err := executeCmd(t, []string{"plan", "/nonexistent/file.cue"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read template")
}

func TestPlanCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"plan"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestPlanCmd_WithVariables(t *testing.T) {
	tmp := t.TempDir()
	tpl := filepath.Join(tmp, "stack.cue")
	require.NoError(t, os.WriteFile(tpl, []byte(`{}`), 0644))

	var gotReq types.PlanRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.PlanResponse{Plan: &types.PlanResult{}})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"plan", tpl, "--var", "env=prod", "--var", "region=us-east-1"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "prod", gotReq.Variables["env"])
	assert.Equal(t, "us-east-1", gotReq.Variables["region"])
}

// ---------------------------------------------------------------------------
// get
// ---------------------------------------------------------------------------

func TestGetCmd_Deployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/DeploymentStateObj/my-app/GetDetail", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.DeploymentDetail{
			Key:    "my-app",
			Status: types.DeploymentComplete,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"get", "Deployment/my-app"}, srv.URL)
	require.NoError(t, err)
}

func TestGetCmd_InvalidArg(t *testing.T) {
	_, _, err := executeCmd(t, []string{"get", "noformat"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected Kind/Key")
}

func TestGetCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"get"}, "http://unused")
	require.Error(t, err)
}

func TestGetCmd_Resource(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "Ready"})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"get", "S3Bucket/my-bucket"}, srv.URL)
	require.NoError(t, err)
	// Should have called GetResourceStatus and GetResourceOutputs
	require.GreaterOrEqual(t, len(paths), 1)
}

// ---------------------------------------------------------------------------
// delete
// ---------------------------------------------------------------------------

func TestDeleteCmd_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.DeleteDeploymentResponse{
			DeploymentKey: "my-app",
			Status:        types.DeploymentDeleting,
		})
	}))
	defer srv.Close()

	// Use --yes to skip interactive prompt.
	_, _, err := executeCmd(t, []string{"delete", "Deployment/my-app", "--yes"}, srv.URL)
	require.NoError(t, err)
}

func TestDeleteCmd_Rollback(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.DeleteDeploymentResponse{
			DeploymentKey: "my-app",
			Status:        types.DeploymentDeleting,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"delete", "Deployment/my-app", "--yes", "--rollback"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/PraxisCommandService/RollbackDeployment", gotPath)
}

func TestDeleteCmd_NonDeployment(t *testing.T) {
	_, _, err := executeCmd(t, []string{"delete", "S3Bucket/my-bucket", "--yes"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only supports Deployment")
}

func TestDeleteCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"delete"}, "http://unused")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

func TestListCmd_Deployments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/DeploymentIndex/global/List", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.DeploymentSummary{
			{Key: "app-1", Status: types.DeploymentComplete, Resources: 3},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "deployments"}, srv.URL)
	require.NoError(t, err)
}

func TestListCmd_UnsupportedType(t *testing.T) {
	_, _, err := executeCmd(t, []string{"list", "pods"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported resource type")
}

func TestListCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"list"}, "http://unused")
	require.Error(t, err)
}

func TestListCmd_DeploymentAliases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.DeploymentSummary{})
	}))
	defer srv.Close()

	for _, alias := range []string{"deployments", "deployment", "deploy"} {
		_, _, err := executeCmd(t, []string{"list", alias}, srv.URL)
		require.NoError(t, err, "alias %q should work", alias)
	}
}

// ---------------------------------------------------------------------------
// apply (limited — requires file + confirmation prompt)
// ---------------------------------------------------------------------------

func TestApplyCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"apply"}, "http://unused")
	require.Error(t, err)
}

func TestApplyCmd_MissingFile(t *testing.T) {
	_, _, err := executeCmd(t, []string{"apply", "/nonexistent.cue"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read template")
}

func TestApplyCmd_NoChanges(t *testing.T) {
	tmp := t.TempDir()
	tpl := filepath.Join(tmp, "stack.cue")
	require.NoError(t, os.WriteFile(tpl, []byte(`{}`), 0644))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Plan returns no changes → apply should exit early.
		_ = json.NewEncoder(w).Encode(types.PlanResponse{
			Plan: &types.PlanResult{Summary: types.PlanSummary{}},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"apply", tpl, "--auto-approve"}, srv.URL)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// parseVariables
// ---------------------------------------------------------------------------

func TestParseVariables(t *testing.T) {
	vars, err := parseVariables([]string{"key1=value1", "key2=value2"})
	require.NoError(t, err)
	assert.Equal(t, "value1", vars["key1"])
	assert.Equal(t, "value2", vars["key2"])
}

func TestParseVariables_Malformed(t *testing.T) {
	_, err := parseVariables([]string{"noequals"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected key=value")
}

func TestParseVariables_EmptyKey(t *testing.T) {
	_, err := parseVariables([]string{"=value"})
	require.Error(t, err)
}

func TestParseVariables_NilSlice(t *testing.T) {
	vars, err := parseVariables(nil)
	require.NoError(t, err)
	assert.Nil(t, vars)
}
