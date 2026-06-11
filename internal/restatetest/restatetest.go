// Package restatetest stands up a per-test Restate container, replacing the
// Restate SDK's testing harness with settings that make the containers fast
// and stable on loaded hosts:
//
//   - The image is pinned to the version used by docker-compose.yaml instead
//     of restate:latest.
//   - A single partition instead of the default 24. Multi-partition leadership
//     elections continue after the health checks pass, and the first ingress
//     request could fail with "lost leadership" (the ingress does not retry
//     submissions that carry no idempotency key).
//   - A three-minute startup deadline. The SDK hardcodes one minute, which
//     Docker occasionally exceeds when a full suite churns hundreds of
//     containers.
//
// The startup sequence mirrors sdk-go/testing.StartWithOptions, which does
// not expose the wait deadline.
package restatetest

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/restatedev/sdk-go/server"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	image           = "docker.restate.dev/restatedev/restate:1.6"
	adminPort       = "9070"
	ingressPort     = "8080"
	startupDeadline = 3 * time.Minute
)

// TestEnvironment exposes the subset of the SDK test environment the driver
// tests use.
type TestEnvironment struct {
	ingressClient *ingress.Client
}

func (e *TestEnvironment) Ingress() *ingress.Client { return e.ingressClient }

// Start mirrors the signature of the SDK's testing.Start so call sites only
// swap the import path.
func Start(t *testing.T, services ...restate.ServiceDefinition) *TestEnvironment {
	t.Helper()

	restateSrv := server.NewRestate()
	for _, service := range services {
		restateSrv.Bind(service)
	}

	restateHandler, err := restateSrv.Handler()
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(restateHandler)
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &protocols
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)

	sdkPort, err := strconv.Atoi(strings.Split(srv.URL, ":")[2])
	require.NoError(t, err)

	restateC, err := testcontainers.Run(
		t.Context(), image,
		testcontainers.WithEnv(map[string]string{
			"RUST_LOG":                              "warn",
			"RESTATE_DEFAULT_NUM_PARTITIONS":        "1",
			"RESTATE_META__REST_ADDRESS":            "0.0.0.0:" + adminPort,
			"RESTATE_WORKER__INGRESS__BIND_ADDRESS": "0.0.0.0:" + ingressPort,
		}),
		testcontainers.WithExposedPorts(adminPort+"/tcp", ingressPort+"/tcp"),
		testcontainers.WithWaitStrategyAndDeadline(
			startupDeadline,
			wait.ForAll(
				wait.ForHTTP("/health").WithPort(adminPort+"/tcp"),
				wait.ForHTTP("/restate/health").WithPort(ingressPort+"/tcp"),
			),
		),
		testcontainers.WithHostPortAccess(sdkPort),
	)
	testcontainers.CleanupContainer(t, restateC)
	require.NoError(t, err)

	logReader, err := restateC.Logs(t.Context())
	require.NoError(t, err)
	go func() {
		scanner := bufio.NewScanner(logReader)
		for scanner.Scan() {
			t.Log(scanner.Text())
		}
	}()

	mappedAdmin, err := restateC.MappedPort(t.Context(), adminPort)
	require.NoError(t, err)
	mappedIngress, err := restateC.MappedPort(t.Context(), ingressPort)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost,
		fmt.Sprintf("http://localhost:%d/deployments", mappedAdmin.Int()),
		bytes.NewBufferString(fmt.Sprintf(`{"uri":"http://%s:%d"}`, testcontainers.HostInternal, sdkPort)),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, res.Body.Close()) }()
	require.Equal(t, http.StatusCreated, res.StatusCode)

	return &TestEnvironment{
		ingressClient: ingress.NewClient(fmt.Sprintf("http://localhost:%d", mappedIngress.Int())),
	}
}
