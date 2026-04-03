// Package config provides environment-based configuration for all Praxis services.
//
// All configuration is read from environment variables with sensible defaults.
// This follows the twelve-factor app methodology: configuration that varies
// between deployments is stored in the environment, not in code or config files.
//
// Each Praxis microservice (praxis-core, praxis-compute, praxis-network, etc.)
// calls config.Load() at startup to obtain its configuration. The Config struct
// covers the settings shared across all services; service-specific configuration
// (like AWS credentials) is handled by the auth package.
package config

import (
	"os"

	"github.com/shirvan/praxis/internal/core/auth"
)

// Config holds all configuration for Praxis services.
// Every field maps to a single environment variable.
//
// This struct is intentionally flat — no nested config groups — so that each
// field has a 1:1 correspondence with an env var, making configuration
// transparent and easy to audit.
type Config struct {
	// ListenAddr is the address the Restate HTTP2 server binds to.
	// PRAXIS_LISTEN_ADDR — default: "0.0.0.0:9080"
	ListenAddr string

	// RestateEndpoint is the Restate ingress URL for SDK clients.
	// PRAXIS_RESTATE_ENDPOINT — default: "http://localhost:8080"
	RestateEndpoint string

	// SchemaDir is the path to the CUE base template schemas.
	// PRAXIS_SCHEMA_DIR — default: "./schemas"
	SchemaDir string

	// PolicyDir seeds global policy files during startup when set.
	// PRAXIS_POLICY_DIR — default: "" (disabled)
	PolicyDir string
}

// Load reads configuration from environment variables with sensible defaults.
// This is the primary entry point — called once at service startup.
// Missing or empty env vars fall back to the defaults documented on each Config field.
func Load() Config {
	return Config{
		ListenAddr:      envOr("PRAXIS_LISTEN_ADDR", "0.0.0.0:9080"),
		RestateEndpoint: envOr("PRAXIS_RESTATE_ENDPOINT", "http://localhost:8080"),
		SchemaDir:       envOr("PRAXIS_SCHEMA_DIR", "./schemas"),
		PolicyDir:       os.Getenv("PRAXIS_POLICY_DIR"),
	}
}

// Auth returns the configured account registry for AWS access.
// The registry is built from PRAXIS_ACCOUNT_* environment variables and
// provides aws.Config instances to drivers for making AWS API calls.
// See the auth package for details on credential source types.
func (c Config) Auth() *auth.Registry {
	return auth.LoadFromEnv()
}

// envOr returns the value of the environment variable key, or fallback if unset/empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
