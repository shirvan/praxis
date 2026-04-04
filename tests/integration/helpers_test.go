//go:build integration

// Package integration contains end-to-end tests for Praxis drivers.
// These tests require Docker (Testcontainers starts Restate automatically)
// and a running Moto instance with S3 support.
//
// Run with: go test ./tests/integration/... -v -tags=integration -timeout=5m
package integration

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/s3"
	"github.com/shirvan/praxis/internal/infra/awsclient"
)

// localstackEndpoint is the URL for the Moto (mock AWS) instance.
// In CI, this is the same host; in Docker-based tests, it's the container network.
const localstackEndpoint = "http://localhost:4566"

const integrationAccountName = "local"

func configureLocalAccount(t *testing.T) {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", integrationAccountName)
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_ENDPOINT_URL", localstackEndpoint)
}

func accountVariables() map[string]any {
	return map[string]any{"account": integrationAccountName}
}

// setupS3Driver starts a Restate test environment with the S3 driver registered.
// It returns an ingress client for invoking handlers and a raw S3 client for
// directly inspecting state in Moto.
func setupS3Driver(t *testing.T) (*ingress.Client, *s3sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	s3Client := awsclient.NewS3Client(awsCfg)
	driver := s3.NewS3BucketDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), s3Client
}

// localstackAWSConfig returns an AWS config pointing at Moto
// with dummy credentials (Moto accepts any credentials).
func localstackAWSConfig(t *testing.T) aws.Config {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(
			func(ctx context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     "test",
					SecretAccessKey: "test",
				}, nil
			},
		)),
	)
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseEndpoint = aws.String(localstackEndpoint)
	return cfg
}
