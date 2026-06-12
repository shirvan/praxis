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
	"os/exec"
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
	container     testcontainers.Container
}

func (e *TestEnvironment) Ingress() *ingress.Client { return e.ingressClient }

// RestartRestate stops and restarts the Restate container in place. Container
// state (the journal and K/V store under /restate-data) and the host port
// bindings survive a stop/start cycle, so in-flight invocations resume from
// their journals once the server is healthy again. Crash-resume tests use
// this to verify Praxis's durability promise.
func (e *TestEnvironment) RestartRestate(t *testing.T) {
	t.Helper()
	// Plain docker stop/start instead of the testcontainers Stop/Start
	// methods: those re-run lifecycle hooks, and the host-port-access hook
	// tears down (and fails to re-establish) the SSH forwarder that lets the
	// container reach the SDK server on the host. A raw restart preserves the
	// container's state, port bindings, and the forwarder session.
	id := e.container.GetContainerID()
	stop := exec.Command("docker", "stop", "-t", "10", id)
	out, err := stop.CombinedOutput()
	require.NoError(t, err, "docker stop: %s", out)
	start := exec.Command("docker", "start", id)
	out, err = start.CombinedOutput()
	require.NoError(t, err, "docker start: %s", out)

	mappedAdmin, err := e.container.MappedPort(t.Context(), adminPort)
	require.NoError(t, err)
	mappedIngress, err := e.container.MappedPort(t.Context(), ingressPort)
	require.NoError(t, err)

	deadline := time.Now().Add(startupDeadline)
	for _, probe := range []string{
		fmt.Sprintf("http://localhost:%d/health", mappedAdmin.Int()),
		fmt.Sprintf("http://localhost:%d/restate/health", mappedIngress.Int()),
	} {
		for {
			res, err := http.Get(probe) //nolint:noctx // bounded by the deadline loop
			if err == nil {
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					break
				}
			}
			require.True(t, time.Now().Before(deadline), "restate did not become healthy after restart (probe %s)", probe)
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Docker assigns fresh ephemeral host ports on restart, so any ingress
	// client created before the restart points at a dead port. Re-bind; tests
	// must re-fetch the client via Ingress() after calling RestartRestate.
	e.ingressClient = ingress.NewClient(fmt.Sprintf("http://localhost:%d", mappedIngress.Int()))
}

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

	// One bounded retry: under heavy container churn Docker occasionally
	// fails a start with transient errors ("port not found" before the
	// mapping is visible). A single retry absorbs that class without
	// masking persistent environment problems.
	var restateC testcontainers.Container
	for attempt := 1; attempt <= 2; attempt++ {
		restateC, err = testcontainers.Run(
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
		if err == nil {
			break
		}
		t.Logf("restatetest: container start attempt %d failed: %v", attempt, err)
	}
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
		container:     restateC,
	}
}
