package authservice

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	CredentialSourceStatic  = "static"
	CredentialSourceRole    = "role"
	CredentialSourceDefault = "default"

	defaultAccountName     = "default"
	defaultAccountRegion   = "us-east-1"
	minSessionDuration     = 15 * time.Minute
	maxSessionDuration     = 12 * time.Hour
	defaultSessionDuration = 1 * time.Hour
)

var aliasRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// AccountConfig holds the credential configuration for a single AWS account alias.
type AccountConfig struct {
	Region           string        `json:"region"`
	CredentialSource string        `json:"credentialSource"`
	AccessKeyID      string        `json:"accessKeyId,omitempty"`
	SecretAccessKey  string        `json:"secretAccessKey,omitempty"`
	RoleARN          string        `json:"roleArn,omitempty"`
	ExternalID       string        `json:"externalId,omitempty"`
	SessionDuration  time.Duration `json:"sessionDuration,omitempty"`
	EndpointURL      string        `json:"endpointUrl,omitempty"`
}

// AccountsConfig is the top-level config holding a map of account aliases.
type AccountsConfig struct {
	Accounts map[string]AccountConfig
}

// Validate checks that the account config is well-formed.
func (c *AccountConfig) Validate(alias string) error {
	if !aliasRegex.MatchString(alias) {
		return fmt.Errorf("account alias %q must match %s", alias, aliasRegex.String())
	}

	source := strings.ToLower(strings.TrimSpace(c.CredentialSource))
	if source == "" {
		source = CredentialSourceDefault
	}

	switch source {
	case CredentialSourceStatic:
		if strings.TrimSpace(c.AccessKeyID) == "" {
			return fmt.Errorf("account %q: static source requires accessKeyId", alias)
		}
		if strings.TrimSpace(c.SecretAccessKey) == "" {
			return fmt.Errorf("account %q: static source requires secretAccessKey", alias)
		}
	case CredentialSourceRole:
		if strings.TrimSpace(c.RoleARN) == "" {
			return fmt.Errorf("account %q: role source requires roleArn", alias)
		}
	case CredentialSourceDefault:
		// no extra fields required
	default:
		return fmt.Errorf("account %q: unsupported credential source %q (use static, role, or default)", alias, c.CredentialSource)
	}

	if c.SessionDuration != 0 {
		if c.SessionDuration < minSessionDuration {
			return fmt.Errorf("account %q: sessionDuration %s is below minimum %s", alias, c.SessionDuration, minSessionDuration)
		}
		if c.SessionDuration > maxSessionDuration {
			return fmt.Errorf("account %q: sessionDuration %s exceeds maximum %s", alias, c.SessionDuration, maxSessionDuration)
		}
	}

	return nil
}

// Redacted returns a copy with secrets masked for safe logging.
func (c AccountConfig) Redacted() AccountConfig {
	r := c
	if r.AccessKeyID != "" {
		r.AccessKeyID = "***"
	}
	if r.SecretAccessKey != "" {
		r.SecretAccessKey = "***"
	}
	return r
}

// ValidateAlias checks that a string is a valid account alias.
func ValidateAlias(alias string) error {
	if !aliasRegex.MatchString(alias) {
		return fmt.Errorf("alias %q must match %s", alias, aliasRegex.String())
	}
	return nil
}

// LoadBootstrapFromEnv creates an AccountsConfig from PRAXIS_ACCOUNT_* env vars.
func LoadBootstrapFromEnv() *AccountsConfig {
	name := envOr("PRAXIS_ACCOUNT_NAME", defaultAccountName)
	return &AccountsConfig{
		Accounts: map[string]AccountConfig{
			name: {
				Region:           envOr("PRAXIS_ACCOUNT_REGION", defaultAccountRegion),
				CredentialSource: envOr("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", CredentialSourceDefault),
				AccessKeyID:      os.Getenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID"),
				SecretAccessKey:  os.Getenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY"),
				RoleARN:          os.Getenv("PRAXIS_ACCOUNT_ROLE_ARN"),
				ExternalID:       os.Getenv("PRAXIS_ACCOUNT_EXTERNAL_ID"),
				EndpointURL:      os.Getenv("AWS_ENDPOINT_URL"),
			},
		},
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
