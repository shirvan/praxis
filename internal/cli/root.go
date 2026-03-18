// Package cli implements the Praxis command-line interface.
//
// The CLI binary (`praxis`) provides the primary human interaction surface for
// Praxis. It communicates with Praxis Core exclusively through the Restate
// ingress HTTP/JSON endpoint — it never talks to driver services or deployment
// state directly.
//
// Commands:
//
//   - apply   — Provision resources from a CUE template
//   - plan    — Preview what would change without provisioning
//   - get     — Show deployment or resource details
//   - delete  — Tear down a deployment
//   - list    — List active deployments
//   - import  — Adopt an existing cloud resource
//   - observe — Watch deployment progress in real time
//
// The CLI supports two output formats:
//
//   - table (default) — Human-friendly aligned columns
//   - json            — Machine-readable indented JSON for scripting and AI agents
//
// All commands respect the --endpoint flag (or PRAXIS_RESTATE_ENDPOINT env var)
// for pointing to the Restate ingress endpoint.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const (
	// defaultRestateEndpoint is the default Restate ingress URL when neither
	// --endpoint nor PRAXIS_RESTATE_ENDPOINT is set.
	defaultRestateEndpoint = "http://localhost:8080"

	// envRestateEndpoint is the environment variable that overrides the default
	// Restate ingress URL.
	envRestateEndpoint = "PRAXIS_RESTATE_ENDPOINT"

	// envRegion is the environment variable that sets the default AWS region
	// for resource key resolution. When set, users can refer to region-scoped
	// resources by name alone (e.g. "my-bucket") and the CLI prepends the region.
	envRegion = "PRAXIS_REGION"

	// envAccount is the environment variable that sets the default AWS account
	// selection for apply, plan, and import operations.
	envAccount = "PRAXIS_ACCOUNT"
)

// rootFlags holds the global flags shared by all commands.
type rootFlags struct {
	// endpoint is the Restate ingress URL.
	endpoint string
	// output selects the output format: "table" or "json".
	output string
	// region is the default AWS region for key resolution.
	region string
	// account is the default AWS account for requests that touch provider APIs.
	account string
}

// NewRootCmd constructs the top-level cobra command tree for the `praxis` binary.
//
// Every subcommand receives a lazily-constructed *Client that points at the
// configured Restate ingress endpoint.
func NewRootCmd() *cobra.Command {
	flags := &rootFlags{}

	root := &cobra.Command{
		Use:   "praxis",
		Short: "Praxis — declarative infrastructure without Kubernetes",
		Long: `Praxis is a declarative infrastructure automation platform that uses
Restate for durable execution instead of Kubernetes.

Define your cloud resources in CUE templates, and Praxis handles provisioning,
drift detection, dependency ordering, and lifecycle management.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Global flags available to every subcommand.
	defaultEndpoint := os.Getenv(envRestateEndpoint)
	if defaultEndpoint == "" {
		defaultEndpoint = defaultRestateEndpoint
	}
	root.PersistentFlags().StringVar(&flags.endpoint, "endpoint", defaultEndpoint,
		fmt.Sprintf("Restate ingress endpoint URL (env: %s)", envRestateEndpoint))
	root.PersistentFlags().StringVarP(&flags.output, "output", "o", "table",
		"Output format: table or json")
	root.PersistentFlags().StringVar(&flags.region, "region", os.Getenv(envRegion),
		fmt.Sprintf("Default AWS region for resource key resolution (env: %s)", envRegion))
	flags.account = os.Getenv(envAccount)

	// Register all subcommands.
	root.AddCommand(
		newApplyCmd(flags),
		newDeployCmd(flags),
		newPlanCmd(flags),
		newGetCmd(flags),
		newDeleteCmd(flags),
		newListCmd(flags),
		newImportCmd(flags),
		newObserveCmd(flags),
		newTemplateCmd(flags),
		newVersionCmd(),
	)

	return root
}

// outputFormat parses the --output flag into a typed OutputFormat constant.
func (f *rootFlags) outputFormat() OutputFormat {
	switch f.output {
	case "json":
		return OutputJSON
	default:
		return OutputTable
	}
}

// ---------------------------------------------------------------------------
// Resource key scope resolution
// ---------------------------------------------------------------------------

// keyScope mirrors provider.KeyScope without importing internal packages.
type keyScope int

const (
	scopeGlobal keyScope = iota
	scopeRegion
	scopeCustom
)

// kindScopes maps known resource kinds to their key scopes. The CLI uses this
// to decide whether user-supplied names need a region prefix. Unknown kinds
// default to scopeRegion (the safe fallback).
var kindScopes = map[string]keyScope{
	"S3Bucket":      scopeGlobal,
	"EC2Instance":   scopeRegion,
	"SecurityGroup": scopeCustom,
}

// resolveResourceKey assembles the canonical resource key from a user-supplied
// name and the ambient region context. For global resources the name is returned
// as-is; for region-scoped resources the region is prepended; for custom-scoped
// resources the name is returned as-is (the user supplies the full key).
func (f *rootFlags) resolveResourceKey(kind, userKey string) string {
	scope, ok := kindScopes[kind]
	if !ok {
		scope = scopeRegion
	}

	switch scope {
	case scopeGlobal, scopeCustom:
		return userKey
	case scopeRegion:
		if f.region != "" && !strings.Contains(userKey, "~") {
			return f.region + "~" + userKey
		}
		return userKey
	default:
		return userKey
	}
}

// newClient constructs a Restate ingress client from the global endpoint flag.
func (f *rootFlags) newClient() *Client {
	return NewClient(f.endpoint)
}
