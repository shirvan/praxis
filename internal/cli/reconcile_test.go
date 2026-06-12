package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// reconcile <Kind>/<Key>
// ---------------------------------------------------------------------------

func TestReconcileCmd_Resource_NoDrift(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ReconcileResult{Drift: false, Correcting: false})
	}))
	defer srv.Close()

	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"reconcile", "S3Bucket/my-bucket"}, srv.URL)
	})
	require.NoError(t, execErr)
	assert.Equal(t, "/S3Bucket/my-bucket/Reconcile", gotPath)

	assert.Contains(t, out, "S3Bucket/my-bucket")
	assert.Contains(t, out, "no drift")
	assert.Contains(t, out, "Resource is in sync — no drift detected.")
}

func TestReconcileCmd_Resource_DriftDetected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/S3Bucket/my-bucket/Reconcile", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ReconcileResult{Drift: true, Correcting: true})
	}))
	defer srv.Close()

	var execErr error
	out := captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"reconcile", "S3Bucket/my-bucket"}, srv.URL)
	})
	require.NoError(t, execErr)

	assert.Contains(t, out, "resource has drifted")
	assert.Contains(t, out, "Correcting")
	assert.NotContains(t, out, "Resource is in sync")
}

// Canonical resource keys may contain slashes (e.g. hierarchical log group
// names). The ingress client splices the key into the URL path verbatim, so
// the CLI must percent-encode it — otherwise the key's slashes are parsed as
// extra path segments and Restate rejects the request.
func TestReconcileCmd_Resource_SlashKeyIsEscaped(t *testing.T) {
	var escapedPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escapedPaths = append(escapedPaths, r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ReconcileResult{})
	}))
	defer srv.Close()

	var execErr error
	captureStdout(t, func() {
		_, _, execErr = executeCmd(t, []string{"reconcile", "LogGroup/us-east-1~/a/b"}, srv.URL)
	})
	require.NoError(t, execErr)
	require.NotEmpty(t, escapedPaths)
	assert.Equal(t, "/LogGroup/us-east-1~%2Fa%2Fb/Reconcile", escapedPaths[0])
}

func TestReconcileCmd_Resource_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, err := executeCmd(t, []string{"reconcile", "S3Bucket/my-bucket"}, srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reconcile S3Bucket/my-bucket")
}

func TestReconcileCmd_NoArgs(t *testing.T) {
	_, _, err := executeCmd(t, []string{"reconcile"}, "http://unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}
