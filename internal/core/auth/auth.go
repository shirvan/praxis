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

const (
	CredentialSourceStatic  = "static"
	CredentialSourceRole    = "role"
	CredentialSourceDefault = "default"

	defaultAccountName   = "default"
	defaultAccountRegion = "us-east-1"
)

type Account struct {
	Name             string
	Region           string
	CredentialSource string
	AccessKeyID      string
	SecretAccessKey  string
	RoleARN          string
	ExternalID       string
	EndpointURL      string
}

type Registry struct {
	accounts map[string]Account
	fallback string
}

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

func applyEndpointOverride(cfg *aws.Config, endpoint string) {
	if cfg == nil || strings.TrimSpace(endpoint) == "" {
		return
	}
	cfg.BaseEndpoint = aws.String(strings.TrimSpace(endpoint))
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
