package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. This is needed because printJSON and the default
// renderer write to os.Stdout directly, not to the cobra command's out buffer
// that executeCmd captures. A reader goroutine drains the pipe concurrently
// so large outputs cannot block on the pipe buffer.
//
// Tests in this package do not use t.Parallel, so swapping the global
// os.Stdout is safe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	fn()

	require.NoError(t, w.Close())
	return <-done
}

// ---------------------------------------------------------------------------
// plan -o json
// ---------------------------------------------------------------------------

func TestPlanCmd_JSONOutput(t *testing.T) {
	tmp := t.TempDir()
	tpl := filepath.Join(tmp, "stack.cue")
	require.NoError(t, os.WriteFile(tpl, []byte(`{}`), 0644))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.PlanResponse{
			Plan: &types.PlanResult{
				Summary: types.PlanSummary{ToCreate: 2, ToUpdate: 1, ToDelete: 0, Unchanged: 3},
			},
		})
	}))
	defer srv.Close()

	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"plan", tpl, "-o", "json"}, srv.URL)
	})
	require.NoError(t, execErr)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded), "plan -o json must emit valid JSON, got: %s", out)

	plan, ok := decoded["plan"].(map[string]any)
	require.True(t, ok, "JSON output must contain a plan object, got: %s", out)
	summary, ok := plan["summary"].(map[string]any)
	require.True(t, ok, "plan object must contain a summary, got: %s", out)
	assert.Equal(t, float64(2), summary["toCreate"])
	assert.Equal(t, float64(1), summary["toUpdate"])
	assert.Equal(t, float64(0), summary["toDelete"])
	assert.Equal(t, float64(3), summary["unchanged"])
}

// ---------------------------------------------------------------------------
// get Deployment/<key> -o json
// ---------------------------------------------------------------------------

func TestGetCmd_Deployment_JSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetDetail"):
			_ = json.NewEncoder(w).Encode(types.DeploymentDetail{
				Key:    "my-app",
				Status: types.DeploymentComplete,
				Resources: []types.DeploymentResource{
					{Name: "bucket", Kind: "S3Bucket", Key: "my-bucket", Status: types.DeploymentResourceReady},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/GetInputs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"bucketName": "my-bucket"})
		default:
			_ = json.NewEncoder(w).Encode(nil)
		}
	}))
	defer srv.Close()

	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"get", "Deployment/my-app", "-o", "json"}, srv.URL)
	})
	require.NoError(t, execErr)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded), "get -o json must emit valid JSON, got: %s", out)

	deployment, ok := decoded["deployment"].(map[string]any)
	require.True(t, ok, "JSON output must contain a deployment object, got: %s", out)
	assert.Equal(t, "my-app", deployment["key"])
	assert.Equal(t, "Complete", deployment["status"])
}

// ---------------------------------------------------------------------------
// list deployments -o json
// ---------------------------------------------------------------------------

func TestListCmd_Deployments_JSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/DeploymentIndex/global/List", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.DeploymentSummary{
			{Key: "app-1", Status: types.DeploymentComplete, Resources: 3},
			{Key: "app-2", Status: types.DeploymentComplete, Resources: 1},
		})
	}))
	defer srv.Close()

	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"list", "deployments", "-o", "json"}, srv.URL)
	})
	require.NoError(t, execErr)

	var decoded []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded), "list -o json must emit a valid JSON array, got: %s", out)
	require.Len(t, decoded, 2)
	assert.Equal(t, "app-1", decoded[0]["key"])
	assert.Equal(t, "Complete", decoded[0]["status"])
	assert.Equal(t, "app-2", decoded[1]["key"])
}
