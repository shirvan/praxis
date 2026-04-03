// Package auth provides AWS credential management for Praxis.
//
// Praxis supports multiple AWS authentication strategies configured via
// environment variables. The auth package abstracts these into a Registry
// that maps account names to AWS SDK configurations.
//
// # Credential Sources
//
// Three credential sources are supported:
//
//   - "static": explicit access key ID + secret access key (for CI/CD or testing).
//   - "role": STS AssumeRole with an optional external ID (for cross-account access).
//   - "default": AWS SDK default credential chain (env vars, shared config, IMDS, etc.).
//
// # Account Registry
//
// The Registry holds named accounts. Currently a single account is loaded from
// PRAXIS_ACCOUNT_* env vars. The Lookup method resolves an account by name
// (falling back to the default), and Resolve returns a fully-configured
// aws.Config ready for use by any AWS service client.
//
// # LocalStack Support
//
// When AWS_ENDPOINT_URL is set, all AWS API calls are redirected to that
// endpoint. This is how Praxis integrates with LocalStack for local development
// and integration testing.
package auth

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Credential source constants identify which authentication strategy an account uses.
const (
	CredentialSourceStatic  = "static"  // Explicit access key ID + secret key.
	CredentialSourceRole    = "role"    // STS AssumeRole (cross-account or elevated privileges).
	CredentialSourceDefault = "default" // AWS SDK default credential chain.

	defaultAccountName   = "default"
	defaultAccountRegion = "us-east-1"
)

// Account represents a single AWS account configuration with its authentication
// details. Each field maps to a PRAXIS_ACCOUNT_* environment variable.
type Account struct {
	Name             string // Logical name for this account (e.g. "production", "staging").
	Region           string // AWS region for API calls (e.g. "us-east-1").
	CredentialSource string // One of: "static", "role", "default".
	AccessKeyID      string // For static credentials only.
	SecretAccessKey  string // For static credentials only.
	RoleARN          string // For role-based credentials: the ARN to assume.
	ExternalID       string // Optional external ID for STS AssumeRole (cross-account security).
	EndpointURL      string // AWS endpoint override (e.g. LocalStack URL).
}

// Registry maps account names to their configurations and resolves them to
// aws.Config instances. The fallback field holds the name of the default
// account used when callers pass an empty account name.
type Registry struct {
	accounts map[string]Account
	fallback string // Name of the default account.
}

// LoadFromEnv constructs a Registry from PRAXIS_ACCOUNT_* environment variables.
// A single account is created from the current environment. The account name
// defaults to "default" if PRAXIS_ACCOUNT_NAME is not set.
func LoadFromEnv() *Registry {
	name := envOr("PRAXIS_ACCOUNT_NAME", defaultAccountName)
	account := Account{
		Name:             name,
		Region:           envOr("PRAXIS_ACCOUNT_REGION", defaultAccountRegion),
		CredentialSource: envOr("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", CredentialSourceDefault),
		AccessKeyID:      os.Getenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID"),
		SecretAccessKey:  os.Getenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY"),
		RoleARN:          os.Getenv("PRAXIS_ACCOUNT_ROLE_ARN"),
		ExternalID:       os.Getenv("PRAXIS_ACCOUNT_EXTERNAL_ID"),
		EndpointURL:      os.Getenv("AWS_ENDPOINT_URL"),
	}

	return &Registry{
		accounts: map[string]Account{name: account},
		fallback: name,
	}
}

// Lookup retrieves an Account by name. If accountName is empty, the fallback
// (default) account is used. Returns an error if the account does not exist
// or the registry is nil.
func (r *Registry) Lookup(accountName string) (Account, error) {
	if r == nil {
		return Account{}, fmt.Errorf("account registry is nil")
	}

	name := strings.TrimSpace(accountName)
	if name == "" {
		name = r.fallback
	}
	if name == "" {
		return Account{}, fmt.Errorf("no default account is configured")
	}

	account, ok := r.accounts[name]
	if !ok {
		return Account{}, fmt.Errorf("unknown account %q", name)
	}
	return account, nil
}

// Resolve looks up an account by name and returns a fully-configured aws.Config
// ready for use with any AWS service client.
//
// The method dispatches on the account's CredentialSource:
//   - "static": builds credentials from the explicit access key + secret key.
//   - "role": loads the default config, then wraps it with an STS AssumeRole
//     provider (with optional ExternalID for cross-account trust).
//   - "default" (or empty): uses the AWS SDK default credential chain, which
//     checks env vars, shared config files, and IMDS in order.
//
// If EndpointURL is set on the account (e.g. for LocalStack), all AWS API
// calls will be routed to that endpoint.
func (r *Registry) Resolve(accountName string) (aws.Config, error) {
	account, err := r.Lookup(accountName)
	if err != nil {
		return aws.Config{}, err
	}

	switch strings.ToLower(strings.TrimSpace(account.CredentialSource)) {
	case CredentialSourceStatic:
		if strings.TrimSpace(account.AccessKeyID) == "" || strings.TrimSpace(account.SecretAccessKey) == "" {
			return aws.Config{}, fmt.Errorf("account %q uses static credentials but PRAXIS_ACCOUNT_ACCESS_KEY_ID or PRAXIS_ACCOUNT_SECRET_ACCESS_KEY is missing", account.Name)
		}
		cfg, cfgErr := awsconfig.LoadDefaultConfig(
			context.Background(),
			awsconfig.WithRegion(account.Region),
			awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(account.AccessKeyID, account.SecretAccessKey, "")),
		)
		if cfgErr != nil {
			return aws.Config{}, fmt.Errorf("load AWS config for account %q: %w", account.Name, cfgErr)
		}
		applyEndpointOverride(&cfg, account.EndpointURL)
		return cfg, nil
	case CredentialSourceRole:
		if strings.TrimSpace(account.RoleARN) == "" {
			return aws.Config{}, fmt.Errorf("account %q uses role credentials but PRAXIS_ACCOUNT_ROLE_ARN is missing", account.Name)
		}
		cfg, cfgErr := awsconfig.LoadDefaultConfig(
			context.Background(),
			awsconfig.WithRegion(account.Region),
		)
		if cfgErr != nil {
			return aws.Config{}, fmt.Errorf("load AWS base config for account %q: %w", account.Name, cfgErr)
		}
		applyEndpointOverride(&cfg, account.EndpointURL)

		stsClient := sts.NewFromConfig(cfg)
		provider := stscreds.NewAssumeRoleProvider(stsClient, account.RoleARN, func(options *stscreds.AssumeRoleOptions) {
			if strings.TrimSpace(account.ExternalID) != "" {
				externalID := account.ExternalID
				options.ExternalID = &externalID
			}
		})
		cfg.Credentials = aws.NewCredentialsCache(provider)
		return cfg, nil
	case "", CredentialSourceDefault:
		cfg, cfgErr := awsconfig.LoadDefaultConfig(
			context.Background(),
			awsconfig.WithRegion(account.Region),
		)
		if cfgErr != nil {
			return aws.Config{}, fmt.Errorf("load AWS config for account %q: %w", account.Name, cfgErr)
		}
		applyEndpointOverride(&cfg, account.EndpointURL)
		return cfg, nil
	default:
		return aws.Config{}, fmt.Errorf("account %q has unsupported credential source %q", account.Name, account.CredentialSource)
	}
}

// applyEndpointOverride sets the BaseEndpoint on an AWS config if a non-empty
// endpoint URL is provided. This redirects all AWS API calls to the given
// endpoint — typically used for LocalStack during local dev and testing.
func applyEndpointOverride(cfg *aws.Config, endpoint string) {
	if cfg == nil || strings.TrimSpace(endpoint) == "" {
		return
	}
	cfg.BaseEndpoint = aws.String(strings.TrimSpace(endpoint))
}

// envOr returns the value of the named environment variable, trimmed of
// whitespace. If the variable is empty or unset, the fallback is returned.
func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
