// config.go manages the CLI's local configuration file (~/.praxis/config.json).
//
// This file stores per-user state that persists across CLI invocations:
//   - ActiveWorkspace: the currently selected workspace injected into commands
//   - Endpoint: cached Restate ingress URL (overridden by --endpoint flag)
//
// The config file is separate from workspace configuration (which lives in
// Restate state) and from environment variables (which override config values).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// CLIConfig is the on-disk representation of user-local CLI state.
// Stored at ~/.praxis/config.json.
type CLIConfig struct {
	// ActiveWorkspace is the currently selected workspace name.
	// Empty string means no workspace is active.
	ActiveWorkspace string `json:"activeWorkspace,omitempty"`

	// Endpoint is the Restate ingress URL. Overridden by --endpoint flag.
	Endpoint string `json:"endpoint,omitempty"`
}

// ConfigPath returns the path to the CLI config file.
func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".praxis", "config.json")
}

// LoadCLIConfig reads ~/.praxis/config.json and returns its contents.
// Returns a zero-value CLIConfig if the file does not exist or is unreadable.
func LoadCLIConfig() CLIConfig {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return CLIConfig{}
	}
	var cfg CLIConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return CLIConfig{}
	}
	return cfg
}

// SaveCLIConfig writes the config to ~/.praxis/config.json, creating the
// directory if needed. The file is written with 0600 permissions.
func SaveCLIConfig(cfg CLIConfig) error {
	p := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}
