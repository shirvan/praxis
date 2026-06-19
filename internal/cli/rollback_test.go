package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

func TestRollbackCmd_Success(t *testing.T) {
	var gotPath string
	var gotReq types.RollbackToRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.DeployResponse{
			DeploymentKey: gotReq.DeploymentKey,
			Status:        types.DeploymentPending,
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"rollback", "my-app", "--to", "3"}, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "/PraxisCommandService/RollbackTo", gotPath)
	assert.Equal(t, "my-app", gotReq.DeploymentKey)
	assert.Equal(t, int64(3), gotReq.ToGeneration)
}

func TestRollbackCmd_RequiresTo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request should be sent when --to is missing")
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"rollback", "my-app"}, srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--to <generation> is required")
}

func TestRollbackCmd_NotKnownGood(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":409,"message":"generation 2 of deployment \"my-app\" is not a known-good target (final status: Failed)"}`))
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"rollback", "my-app", "--to", "2"}, srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a known-good target")
}

func TestListCmd_Generations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/DeploymentStateObj/my-app/ListGenerations", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.GenerationRecord{
			{Generation: 1, CreatedAt: time.Now().UTC(), FinalStatus: types.DeploymentComplete, Resources: 2, TemplatePath: "inline://template.cue"},
			{Generation: 2, CreatedAt: time.Now().UTC(), FinalStatus: types.DeploymentFailed, Resources: 3, TemplatePath: "inline://template.cue"},
		})
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"list", "generations", "my-app"}, srv.URL)
	require.NoError(t, err)

	// Deployment/<key> scope form works too.
	_, _, err = executeCmd(t, []string{"list", "generations", "Deployment/my-app"}, srv.URL)
	require.NoError(t, err)
}

func TestListCmd_Generations_JSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]orchestrator.GenerationRecord{
			{Generation: 1, FinalStatus: types.DeploymentComplete, Resources: 1},
		})
	}))
	defer srv.Close()

	stdout := captureStdout(t, func() {
		_, _, err := executeCmd(t, []string{"list", "generations", "my-app", "-o", "json"}, srv.URL)
		require.NoError(t, err)
	})
	var records []orchestrator.GenerationRecord
	require.NoError(t, json.Unmarshal([]byte(stdout), &records))
	require.Len(t, records, 1)
	assert.Equal(t, int64(1), records[0].Generation)
}
