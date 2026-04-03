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
// resolve AWS credentials. It abstracts over the credential resolution
// mechanism so production code uses Restate RPC (RestateAuthClient) while
// tests use a direct in-process registry (LocalAuthClient).
type AuthClient interface {
	// GetCredentials resolves an aws.Config with valid credentials for the
	// given account alias. The alias maps to a configured account in the
	// AuthService; "default" is used when alias is empty.
	GetCredentials(ctx restate.Context, accountAlias string) (aws.Config, error)
}

// RestateAuthClient resolves credentials via Restate RPC to the Auth Service.
// This is the production implementation. It sends an RPC to the AuthService
// Virtual Object keyed by account alias, which handles caching, refresh,
// and STS operations internally.
type RestateAuthClient struct{}

// NewAuthClient creates a new RestateAuthClient.
func NewAuthClient() *RestateAuthClient {
	return &RestateAuthClient{}
}

// GetCredentials calls the AuthService via Restate RPC and reconstructs an
// aws.Config from the returned credential data. The account alias defaults
// to "default" when empty, which maps to the bootstrap account from env vars.
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
// Used in tests that don't run a full Restate environment. This bypasses
// the durable caching and proactive refresh that the AuthService provides.
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
// Uses a static credentials provider because the CredentialResponse contains
// already-resolved credential strings from the AuthService. If EndpointURL
// is set (LocalStack), configures BaseEndpoint for local development.
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
