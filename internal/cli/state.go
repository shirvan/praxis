// state.go contains shared helpers for deployment state operations.
//
// State commands are now accessed through top-level verbs:
//   - `praxis move <source> <dest>`  (move.go)
package cli

import (
	"fmt"
	"strings"
)

// parseStatePath splits "Deployment/<key>/<resource>" into deployment key and
// resource name.
func parseStatePath(arg string) (deploymentKey, resourceName string, err error) {
	parts := strings.SplitN(arg, "/", 3)
	if len(parts) != 3 || parts[0] != "Deployment" {
		return "", "", fmt.Errorf("expected Deployment/<key>/<resource>, got %q", arg)
	}
	if parts[1] == "" {
		return "", "", fmt.Errorf("deployment key cannot be empty in %q", arg)
	}
	if parts[2] == "" {
		return "", "", fmt.Errorf("resource name cannot be empty in %q", arg)
	}
	return parts[1], parts[2], nil
}

// parseDestination interprets the destination argument. It can be either:
//   - A bare name (rename within the same deployment)
//   - Deployment/<key>/<resource> (move to another deployment)
func parseDestination(arg, srcDeployment, srcResource string) (deploymentKey, resourceName string, err error) {
	if strings.HasPrefix(arg, "Deployment/") {
		return parseStatePath(arg)
	}
	// Bare name → rename within source deployment.
	if arg == "" {
		return "", "", fmt.Errorf("destination name cannot be empty")
	}
	if strings.Contains(arg, "/") {
		return "", "", fmt.Errorf("destination %q contains '/' but does not start with 'Deployment/'; use Deployment/<key>/<resource> for cross-deployment moves", arg)
	}
	return srcDeployment, arg, nil
}
