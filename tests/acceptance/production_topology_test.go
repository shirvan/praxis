//go:build acceptance

// Package acceptance verifies Praxis through the same process boundaries used
// by operators: the compiled CLI, Restate ingress, Core, production driver
// packs, and the provider API.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
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
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
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
	suffix := time.Now().UTC().Format("20060102-150405.000000000")
	suffix = strings.ReplaceAll(suffix, ".", "-")
	bucketName := "praxis-acceptance-" + suffix
	deploymentKey := "acceptance-" + suffix
	templatePath := filepath.Join(t.TempDir(), "acceptance.cue")
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
	require.NoError(t, os.WriteFile(templatePath, []byte(template), 0o600))

	assertBucketMissing(t, env.s3, bucketName)

	var plan types.PlanResponse
	env.runCLIJSON(t, &plan, "plan", templatePath, "--account", "local", "--key", deploymentKey)
	require.NotNil(t, plan.Plan)
	assert.Equal(t, 1, plan.Plan.Summary.ToCreate)
	assert.Equal(t, 0, plan.Plan.Summary.ToUpdate)
	assert.Equal(t, 0, plan.Plan.Summary.ToDelete)
	assertBucketMissing(t, env.s3, bucketName)

	cleanupNeeded := false
	t.Cleanup(func() {
		if !cleanupNeeded {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = env.runCLIContext(ctx, "delete", "Deployment/"+deploymentKey, "--yes", "--wait", "--timeout", "90s")
	})

	cleanupNeeded = true
	var deployedDetail types.DeploymentDetail
	env.runCLIJSON(t, &deployedDetail,
		"deploy", templatePath,
		"--account", "local",
		"--key", deploymentKey,
		"--yes", "--wait",
		"--poll-interval", "100ms",
		"--timeout", "2m",
	)
	require.Equal(t, types.DeploymentComplete, deployedDetail.Status)
	require.Len(t, deployedDetail.Resources, 1)
	assert.Equal(t, types.DeploymentResourceReady, deployedDetail.Resources[0].Status)
	assert.Equal(t, "S3Bucket", deployedDetail.Resources[0].Kind)

	_, err := env.s3.HeadBucket(t.Context(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
	require.NoError(t, err, "deployed bucket must exist in the provider")

	var inspected deploymentJSON
	env.runCLIJSON(t, &inspected, "get", "Deployment/"+deploymentKey)
	assert.Equal(t, deploymentKey, inspected.Deployment.Key)
	assert.Equal(t, types.DeploymentComplete, inspected.Deployment.Status)
	require.Contains(t, inspected.Inputs, "bucket")

	var deletedDetail types.DeploymentDetail
	env.runCLIJSON(t, &deletedDetail,
		"delete", "Deployment/"+deploymentKey,
		"--yes", "--wait", "--timeout", "2m",
	)
	require.Equal(t, types.DeploymentDeleted, deletedDetail.Status)
	assertBucketMissing(t, env.s3, bucketName)
	cleanupNeeded = false
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
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
