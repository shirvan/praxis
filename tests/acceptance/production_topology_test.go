//go:build acceptance

// Package acceptance verifies Praxis through the same process boundaries used
// by operators: the compiled CLI, Restate ingress, Core, production driver
// packs, and the provider API.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

const (
	defaultIngressURL = "http://localhost:8080"
	defaultAdminURL   = "http://localhost:9070"
	defaultMotoURL    = "http://localhost:4566"
)

type topology struct {
	repoRoot   string
	cliPath    string
	ingressURL string
	adminURL   string
	motoURL    string
	ec2        *ec2sdk.Client
	s3         *s3sdk.Client
}

type servicesResponse struct {
	Services []struct {
		Name string `json:"name"`
	} `json:"services"`
}

type deploymentJSON struct {
	Deployment types.DeploymentDetail    `json:"deployment"`
	Inputs     map[string]map[string]any `json:"inputs"`
}

func TestProductionTopology(t *testing.T) {
	env := newTopology(t)
	env.requireHealthy(t)

	t.Run("all production services are registered", env.requireProductionServices)
	t.Run("CLI plan deploy inspect delete reaches provider", env.requireCLILifecycle)
	t.Run("cross-pack dependency graph reaches provider", env.requireCrossPackLifecycle)
	t.Run("drift policy update and rollback are observable", env.requireDriftUpdateRollback)
	t.Run("observed import never owns provider deletion", env.requireObservedImport)
}

func newTopology(t *testing.T) *topology {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "resolve acceptance test location")
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	cliPath := envOrDefault("PRAXIS_ACCEPTANCE_CLI", filepath.Join(repoRoot, "bin", "praxis"))

	_, err := os.Stat(cliPath)
	require.NoError(t, err, "compiled CLI not found at %s; run `just build-cli`", cliPath)

	motoURL := envOrDefault("PRAXIS_ACCEPTANCE_MOTO_URL", defaultMotoURL)
	awsCfg, err := awsconfig.LoadDefaultConfig(t.Context(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	require.NoError(t, err)
	awsCfg.BaseEndpoint = aws.String(motoURL)

	return &topology{
		repoRoot:   repoRoot,
		cliPath:    cliPath,
		ingressURL: envOrDefault("PRAXIS_ACCEPTANCE_INGRESS_URL", defaultIngressURL),
		adminURL:   envOrDefault("PRAXIS_ACCEPTANCE_ADMIN_URL", defaultAdminURL),
		motoURL:    motoURL,
		ec2:        ec2sdk.NewFromConfig(awsCfg),
		s3:         s3sdk.NewFromConfig(awsCfg, func(options *s3sdk.Options) { options.UsePathStyle = true }),
	}
}

func (env *topology) requireHealthy(t *testing.T) {
	t.Helper()
	requireHTTPStatus(t, env.adminURL+"/health", http.StatusOK)
	requireHTTPStatus(t, env.motoURL+"/moto-api/", http.StatusOK)

	var version struct {
		Version string `json:"version"`
	}
	env.runCLIJSON(t, &version, "version")
	require.Equal(t, "alpha", version.Version, "compiled CLI version contract")
}

func (env *topology) requireProductionServices(t *testing.T) {
	resp, err := http.Get(env.adminURL + "/services") //nolint:gosec,noctx // fixed test endpoint selected by the operator
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var inventory servicesResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inventory))
	actual := make(map[string]struct{}, len(inventory.Services))
	for _, service := range inventory.Services {
		actual[service.Name] = struct{}{}
	}

	expectedDrivers := provider.NewRegistry(nil).All()
	require.Len(t, expectedDrivers, 51, "production provider inventory")
	missing := make([]string, 0)
	for name := range expectedDrivers {
		if _, ok := actual[name]; !ok {
			missing = append(missing, name)
		}
	}
	// Keep this system-boundary inventory explicit. It should fail if a service
	// is added to the production binary without also being verified here.
	expectedCore := []string{
		"AuthService",
		"WorkspaceService",
		"WorkspaceIndex",
		"PraxisCommandService",
		"DeploymentWorkflow",
		"DeploymentDeleteWorkflow",
		"DeploymentRollbackWorkflow",
		"DeploymentStateObj",
		"DeploymentIndex",
		"ResourceIndex",
		"TemplateRegistry",
		"TemplateIndex",
		"PolicyRegistry",
		"EventBus",
		"DeploymentEventStore",
		"ResourceEventOwner",
		"ResourceEventBridge",
		"SinkRouter",
		"NotificationSinkConfig",
	}
	for _, name := range expectedCore {
		if _, ok := actual[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing, "production services missing from Restate")
}

func (env *topology) requireCLILifecycle(t *testing.T) {
	suffix := acceptanceSuffix()
	bucketName := "praxis-acceptance-" + suffix
	deploymentKey := "acceptance-" + suffix
	template := fmt.Sprintf(`resources: bucket: {
	apiVersion: "praxis.io/alpha"
	kind:       "S3Bucket"
	metadata: name: %q
	spec: {
		region:     "us-east-1"
		versioning: false
		acl:        "private"
		encryption: {
			enabled:   true
			algorithm: "AES256"
		}
		tags: acceptance: "true"
	}
}
`, bucketName)
	env.runManagedDeploymentScenario(t, managedDeploymentScenario{
		DeploymentKey: deploymentKey,
		Template:      template,
		Resources:     map[string]string{"bucket": "S3Bucket"},
		AssertAbsent: func(t *testing.T) {
			assertBucketMissing(t, env.s3, bucketName)
		},
		AssertPresent: func(t *testing.T, _ types.DeploymentDetail) {
			_, err := env.s3.HeadBucket(t.Context(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
			require.NoError(t, err, "deployed bucket must exist in the provider")
		},
	})
}

func acceptanceSuffix() string {
	suffix := time.Now().UTC().Format("20060102-150405.000000000")
	return strings.ReplaceAll(suffix, ".", "-")
}

func (env *topology) runCLIJSON(t *testing.T, target any, args ...string) {
	t.Helper()
	output := env.runCLI(t, args...)
	require.NoError(t, json.Unmarshal([]byte(output), target), "decode CLI JSON:\n%s", output)
}

func (env *topology) runCLI(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	output, err := env.runCLIContext(ctx, args...)
	require.NoError(t, err, "praxis %s:\n%s", strings.Join(args, " "), output)
	return output
}

func (env *topology) runCLIContext(ctx context.Context, args ...string) (string, error) {
	rootArgs := []string{"--endpoint", env.ingressURL, "--plain", "--output", "json"}
	cmd := exec.CommandContext(ctx, env.cliPath, append(rootArgs, args...)...) //nolint:gosec // executable path and args are explicit test inputs
	cmd.Dir = env.repoRoot
	cmd.Env = append(os.Environ(),
		"PRAXIS_ACCOUNT=local",
		"PRAXIS_REGION=us-east-1",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	combined := stdout.String()
	if stderr.Len() > 0 {
		combined += "\nstderr:\n" + stderr.String()
	}
	return strings.TrimSpace(combined), err
}

func requireHTTPStatus(t *testing.T, url string, status int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "%s is unavailable; start the production topology first", url)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	require.Equal(t, status, resp.StatusCode, url)
}

func assertBucketMissing(t *testing.T, client *s3sdk.Client, bucket string) {
	t.Helper()
	_, err := client.HeadBucket(t.Context(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucket)})
	require.Error(t, err, "bucket %q must not exist", bucket)
	var responseErr *smithyhttp.ResponseError
	require.True(t, errors.As(err, &responseErr), "HeadBucket for %q failed without an HTTP response: %v", bucket, err)
	require.Equal(t, http.StatusNotFound, responseErr.HTTPStatusCode(), "HeadBucket for %q must return 404", bucket)
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
