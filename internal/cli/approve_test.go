package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestApproveCmd_Success(t *testing.T) {
	var gotPath string
	var gotReq types.ApprovalRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ApprovalResponse{
			DeploymentKey: gotReq.DeploymentKey,
			Status:        types.DeploymentRunning,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"approve", "my-app", "--comment", "ship it"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/PraxisCommandService/Approve", gotPath)
	assert.Equal(t, "my-app", gotReq.DeploymentKey)
	assert.Equal(t, "ship it", gotReq.Comment)
	assert.NotEmpty(t, gotReq.DecidedBy, "decided-by should default to the local username")
}

func TestRejectCmd_Success(t *testing.T) {
	var gotPath string
	var gotReq types.ApprovalRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ApprovalResponse{
			DeploymentKey: gotReq.DeploymentKey,
			Status:        types.DeploymentCancelled,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"reject", "my-app", "--decided-by", "release-bot", "--comment", "freeze"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/PraxisCommandService/Reject", gotPath)
	assert.Equal(t, "release-bot", gotReq.DecidedBy)
	assert.Equal(t, "freeze", gotReq.Comment)
}

func TestApproveCmd_NotAwaitingApproval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":409,"message":"deployment \"my-app\" is Complete, not awaiting approval"}`))
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"approve", "my-app"}, srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not awaiting approval")
}

func TestApproveCmd_JSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ApprovalResponse{DeploymentKey: "my-app", Status: types.DeploymentRunning})
	}))
	defer srv.Close()

	stdout := captureStdout(t, func() {
		_, _, err := executeCmd(t, []string{"approve", "my-app", "-o", "json"}, srv.URL)
		require.NoError(t, err)
	})
	var resp types.ApprovalResponse
	require.NoError(t, json.Unmarshal([]byte(stdout), &resp))
	assert.Equal(t, "my-app", resp.DeploymentKey)
	assert.Equal(t, types.DeploymentRunning, resp.Status)
}
