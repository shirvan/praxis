package authservice

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
)

// AuthClient is the interface that drivers and Core components use to
// resolve AWS credentials. It replaces *auth.Registry across the codebase.
type AuthClient interface {
	GetCredentials(ctx restate.Context, accountAlias string) (aws.Config, error)
}

// RestateAuthClient resolves credentials via Restate RPC to the Auth Service.
type RestateAuthClient struct{}

// NewAuthClient creates a new RestateAuthClient.
func NewAuthClient() *RestateAuthClient {
	return &RestateAuthClient{}
}

func (c *RestateAuthClient) GetCredentials(ctx restate.Context, accountAlias string) (aws.Config, error) {
	alias := accountAlias
	if alias == "" {
		alias = "default"
	}

	creds, err := restate.Object[CredentialResponse](ctx, ServiceName, alias, "GetCredentials").
		Request(alias)
	if err != nil {
		return aws.Config{}, fmt.Errorf("auth: get credentials for %q: %w", alias, err)
	}

	return buildAWSConfig(creds)
}

// LocalAuthClient resolves credentials directly via auth.Registry.
// Used in tests that don't run a full Restate environment.
type LocalAuthClient struct {
	registry *auth.Registry
}

// NewLocalAuthClient wraps an existing auth.Registry as an AuthClient.
func NewLocalAuthClient(registry *auth.Registry) *LocalAuthClient {
	return &LocalAuthClient{registry: registry}
}

func (c *LocalAuthClient) GetCredentials(_ restate.Context, accountAlias string) (aws.Config, error) {
	return c.registry.Resolve(accountAlias)
}

// buildAWSConfig reconstructs an aws.Config from a CredentialResponse.
func buildAWSConfig(creds CredentialResponse) (aws.Config, error) {
	provider := credentials.NewStaticCredentialsProvider(
		creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken,
	)

	cfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(creds.Region),
		awsconfig.WithCredentialsProvider(provider),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("build aws.Config from credentials: %w", err)
	}

	if creds.EndpointURL != "" {
		cfg.BaseEndpoint = aws.String(creds.EndpointURL)
	}

	return cfg, nil
}
