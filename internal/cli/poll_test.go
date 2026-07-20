package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestPollDeployment_DeleteWaitsPastStaleComplete(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/GetDetail"))
		status := types.DeploymentComplete
		if calls.Add(1) > 1 {
			status = types.DeploymentDeleted
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(types.DeploymentDetail{Key: "app", Status: status}))
	}))
	defer srv.Close()

	var out bytes.Buffer
	renderer := newRendererWithWriters(false, &out, &bytes.Buffer{})
	err := pollDeployment(
		context.Background(),
		NewClient(srv.URL),
		"app",
		time.Millisecond,
		OutputTable,
		renderer,
		types.DeploymentDeleted,
	)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "stale Complete must not finish a delete wait")
	assert.Contains(t, out.String(), "Deleted")
}
