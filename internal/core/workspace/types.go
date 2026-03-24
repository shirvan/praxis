package workspace

import (
	"fmt"
	"regexp"
)

var nameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// WorkspaceConfig is the operator-supplied configuration for a workspace.
// It is sent to the Configure handler and stored in Restate state.
type WorkspaceConfig struct {
	// Name is the workspace identifier (also the Virtual Object key).
	Name string `json:"name"`

	// Account is the default account-alias for deployments in this workspace.
	// Must reference an alias previously registered via Auth.Configure.
	Account string `json:"account"`

	// Region is the default AWS region for this workspace.
	Region string `json:"region"`

	// Variables are default template variables inherited by deployments.
	// Explicit variables in an apply request override these.
	Variables map[string]string `json:"variables,omitempty"`
}

// WorkspaceInfo is the read-only view returned by the Get handler.
type WorkspaceInfo struct {
	Name      string            `json:"name"`
	Account   string            `json:"account"`
	Region    string            `json:"region"`
	Variables map[string]string `json:"variables,omitempty"`
}

// ValidateName checks that a string is a valid workspace name.
func ValidateName(name string) error {
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("workspace name %q must match %s", name, nameRegex.String())
	}
	return nil
}
