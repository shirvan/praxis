package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// route is a key for matching fake ingress HTTP requests.
// Service calls use "ServiceName/HandlerName".
// Object calls use "ServiceName/Key/HandlerName".
type route struct {
	Method string // "POST", "GET", etc.
	Path   string // e.g., "/PraxisCommandService/Plan"
}

// handler describes how the fake ingress should respond.
type handler struct {
	Status int
	Body   any // will be JSON-encoded; use nil for empty 200
}

// fakeIngress starts an httptest server that responds to Restate ingress-style
// routes (POST /{Service}/{Handler} or POST /{Service}/{Key}/{Handler}).
// Returns a *Client pointed at the fake server. The server is automatically
// closed when the test finishes.
func fakeIngress(t *testing.T, routes map[string]handler) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		h, ok := routes[key]
		if !ok {
			t.Logf("fakeIngress: unmatched route %s %s", r.Method, key)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"service not found","code":404}`))
			return
		}
		status := h.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if h.Body != nil {
			_ = json.NewEncoder(w).Encode(h.Body)
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

// executeCmd runs a cobra command tree with the given args, capturing stdout
// and stderr. Returns the combined output and any error.
func executeCmd(t *testing.T, args []string, endpoint string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)

	// Inject the fake endpoint.
	allArgs := append([]string{"--endpoint", endpoint, "--plain"}, args...)
	root.SetArgs(allArgs)

	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// executeCmdWithClient runs a cobra command using a pre-built fake ingress
// client. It sets up the endpoint from the client's URL.
func executeCmdWithClient(t *testing.T, args []string, routes map[string]handler) (stdout, stderr string, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		h, ok := routes[key]
		if !ok {
			t.Logf("fakeIngress: unmatched route %s %s", r.Method, key)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"service not found","code":404}`))
			return
		}
		status := h.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if h.Body != nil {
			_ = json.NewEncoder(w).Encode(h.Body)
		}
	}))
	t.Cleanup(srv.Close)

	return executeCmd(t, args, srv.URL)
}

// mustJSON marshals v to a compact JSON string. Panics on error.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// readBody reads and closes an HTTP request body, returning the bytes.
func readBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	_ = r.Body.Close()
	return b
}
