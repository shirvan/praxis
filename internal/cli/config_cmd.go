// config_cmd.go contains shared helpers for workspace-scoped configuration.
//
// Config operations are now accessed through top-level verbs:
//   - `praxis get config <path>`   (get.go)
//   - `praxis set config <path>`   (set.go)
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/shirvan/praxis/internal/core/workspace"
)

// resolveWorkspaceName returns the explicitly provided workspace name, or
// falls back to the active workspace from ~/.praxis/config.json.
func resolveWorkspaceName(explicit string) (string, error) {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed, nil
	}
	cliCfg := LoadCLIConfig()
	if strings.TrimSpace(cliCfg.ActiveWorkspace) == "" {
		return "", fmt.Errorf("no workspace specified and no active workspace set")
	}
	return cliCfg.ActiveWorkspace, nil
}

// loadEventRetentionPolicy reads and deserialises a retention policy from
// a JSON file. Pass "-" to read from stdin.
func loadEventRetentionPolicy(path string) (workspace.EventRetentionPolicy, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path) //nolint:gosec // path is user-provided CLI argument
	}
	if err != nil {
		return workspace.EventRetentionPolicy{}, err
	}
	var policy workspace.EventRetentionPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return workspace.EventRetentionPolicy{}, fmt.Errorf("decode retention policy: %w", err)
	}
	return policy, nil
}

// configFieldMutator is a function that mutates a single field on an
// EventRetentionPolicy. Used by both config_cmd.go and set.go.
type configFieldMutator func(policy *workspace.EventRetentionPolicy, value string) error

func configMutateMaxAge(policy *workspace.EventRetentionPolicy, value string) error {
	policy.MaxAge = value
	return nil
}

func configMutateMaxEvents(policy *workspace.EventRetentionPolicy, value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid integer %q", value)
	}
	policy.MaxEventsPerDeployment = parsed
	return nil
}

func configMutateMaxIndex(policy *workspace.EventRetentionPolicy, value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid integer %q", value)
	}
	policy.MaxIndexEntries = parsed
	return nil
}

func configMutateSweepInterval(policy *workspace.EventRetentionPolicy, value string) error {
	policy.SweepInterval = value
	return nil
}

func configMutateShipBeforeDelete(policy *workspace.EventRetentionPolicy, value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("invalid bool %q", value)
	}
	policy.ShipBeforeDelete = parsed
	return nil
}

func configMutateDrainSink(policy *workspace.EventRetentionPolicy, value string) error {
	policy.DrainSink = value
	return nil
}

// printEventRetentionPolicy renders a retention policy as a label/value list.
func printEventRetentionPolicy(r *Renderer, policy *workspace.EventRetentionPolicy) {
	if policy == nil {
		_, _ = fmt.Fprintln(r.out, r.renderMuted("No event retention policy configured."))
		return
	}
	r.writeLabelValue("Max Age", 28, policy.MaxAge)
	r.writeLabelValue("Max Events/Deployment", 28, fmt.Sprintf("%d", policy.MaxEventsPerDeployment))
	r.writeLabelValue("Max Index Entries", 28, fmt.Sprintf("%d", policy.MaxIndexEntries))
	r.writeLabelValue("Sweep Interval", 28, policy.SweepInterval)
	r.writeLabelValue("Ship Before Delete", 28, fmt.Sprintf("%t", policy.ShipBeforeDelete))
	if policy.DrainSink != "" {
		r.writeLabelValue("Drain Sink", 28, policy.DrainSink)
	}
}
