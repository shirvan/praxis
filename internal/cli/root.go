// Package cli implements the Praxis command-line interface.
//
// The CLI binary (`praxis`) provides the primary human interaction surface for
// Praxis. It communicates with Praxis Core exclusively through the Restate
// ingress HTTP/JSON endpoint — it never talks to driver services or deployment
// state directly.
//
// Commands follow a verb-first grammar: praxis <VERB> [<RESOURCE>] [flags]
//
// Verb-first commands:
//
//   - deploy     — Provision resources from a CUE template or registered template
//   - plan       — Preview what would change without provisioning
//   - get        — Show deployment, resource, workspace, config, or concierge details
//   - list       — List deployments, templates, workspaces, sinks, events, or concierge history
//   - delete     — Tear down a deployment, workspace, template, sink, or session
//   - create     — Create a workspace, template, or notification sink
//   - set        — Set active workspace, config field, or concierge provider
//   - move       — Rename or move a resource between deployments
//   - import     — Adopt an existing cloud resource
//   - reconcile  — Trigger on-demand drift detection and correction
//   - observe    — Watch any resource in real time
//   - ask        — Send a natural language prompt to the concierge
//   - approve    — Approve or reject a pending concierge action
//   - test       — Test an integration (notification sinks)
//   - fmt        — Format CUE template files
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
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	// selection for deploy, plan, and import operations.
	envAccount = "PRAXIS_ACCOUNT"

	// envWorkspace is the environment variable that sets the default active
	// workspace. It overrides the locally selected workspace in ~/.praxis/config.json.
	envWorkspace = "PRAXIS_WORKSPACE"

	// envOutput is the environment variable that sets the default output format.
	envOutput = "PRAXIS_OUTPUT"
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
	// workspace is the default workspace resolved from env/config.
	workspace string
	// plain disables styled CLI output even when stdout is a terminal.
	plain bool
}

var currentRootFlags *rootFlags

// NewRootCmd constructs the top-level cobra command tree for the `praxis` binary.
//
// Every subcommand receives a lazily-constructed *Client that points at the
// configured Restate ingress endpoint.
func NewRootCmd() *cobra.Command {
	flags := &rootFlags{}
	currentRootFlags = flags

	root := &cobra.Command{
		Use:   "praxis",
		Short: "Praxis — declarative infrastructure without Kubernetes",
		Long: `Praxis is a declarative infrastructure automation platform that uses
Restate for durable execution instead of Kubernetes.

Define your cloud resources in CUE templates, and Praxis handles provisioning,
drift detection, dependency ordering, and lifecycle management.

When the concierge is running, you can also talk to Praxis directly:

  praxis "why did my deploy fail?"
  praxis "convert this terraform to praxis" --file main.tf
  praxis "deploy the orders API to staging"`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
	}

	// Concierge-specific flags — registered on root so they apply when the
	// root RunE handles an unmatched prompt.
	var (
		conciergeSession     string
		conciergeNewSession  bool
		conciergeFile        string
		conciergeWorkspace   string
		conciergeAccount     string
		conciergeAutoApprove bool
		conciergeJSON        bool
	)

	root.Flags().StringVar(&conciergeSession, "session", "", "Switch to a specific session ID (env: PRAXIS_SESSION)")
	root.Flags().BoolVar(&conciergeNewSession, "new-session", false, "Start a new session (ignores saved session)")
	root.Flags().StringVarP(&conciergeFile, "file", "f", "", "Attach file(s), directory, or glob to the prompt (concierge mode)")
	root.Flags().StringVar(&conciergeAccount, "account", "", "Override account (concierge mode)")
	root.Flags().StringVar(&conciergeWorkspace, "workspace", "", "Override workspace (concierge mode)")
	root.Flags().BoolVar(&conciergeAutoApprove, "auto-approve", false, "Skip approval prompts (concierge mode)")
	root.Flags().BoolVar(&conciergeJSON, "json", false, "Output AskResponse as JSON (concierge mode)")

	root.RunE = func(cmd *cobra.Command, args []string) error {
		// No arguments → show help.
		if len(args) == 0 {
			return cmd.Help()
		}

		// Concierge shorthand requires the prompt to be a single quoted
		// string (e.g. praxis "show my buckets").  The shell strips the
		// quotes, so a quoted prompt arrives as one arg that contains
		// spaces.  Multiple bare args (e.g. praxis praxis template list)
		// indicate a mistyped subcommand.
		if len(args) > 1 || !strings.Contains(args[0], " ") {
			return fmt.Errorf("unknown command %q for \"praxis\"\nRun 'praxis --help' for usage\nTip: use quotes for concierge — praxis \"your question here\"", args[0])
		}

		// Single quoted argument → treat as concierge prompt.
		prompt := args[0]

		// Attach file content if --file provided.
		if conciergeFile != "" {
			files, err := resolveFilePaths(conciergeFile)
			if err != nil {
				return fmt.Errorf("resolve %q: %w", conciergeFile, err)
			}
			var sb strings.Builder
			sb.WriteString(prompt)
			for _, f := range files {
				content, err := os.ReadFile(f) //nolint:gosec // G304 file path from CLI flag is intentional
				if err != nil {
					return fmt.Errorf("read file %q: %w", f, err)
				}
				fmt.Fprintf(&sb, "\n\n--- %s ---\n```\n%s\n```", f, string(content))
			}
			prompt = sb.String()
		}

		acct := conciergeAccount
		if acct == "" {
			acct = flags.account
		}

		workspace := conciergeWorkspace
		if workspace == "" {
			workspace = flags.activeWorkspace()
		}

		client := flags.newClient()
		ctx := context.Background()
		r := flags.renderer()

		resp, err := runConciergeAsk(ctx, conciergeAskOpts{
			Client:     client,
			Renderer:   r,
			Session:    conciergeSession,
			NewSession: conciergeNewSession,
			Prompt:     prompt,
			Account:    acct,
			Workspace:  workspace,
			JSON:       conciergeJSON,
		})
		if err != nil {
			if isConciergeUnavailable(err) {
				fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
				return nil
			}
			return err
		}

		if conciergeJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		return nil
	}

	// Global flags available to every subcommand.
	defaultEndpoint := os.Getenv(envRestateEndpoint)
	if defaultEndpoint == "" {
		defaultEndpoint = defaultRestateEndpoint
	}
	defaultOutput := os.Getenv(envOutput)
	if defaultOutput == "" {
		defaultOutput = "table"
	}
	root.PersistentFlags().StringVar(&flags.endpoint, "endpoint", defaultEndpoint,
		fmt.Sprintf("Restate ingress endpoint URL (env: %s)", envRestateEndpoint))
	root.PersistentFlags().StringVarP(&flags.output, "output", "o", defaultOutput,
		fmt.Sprintf("Output format: table or json (env: %s)", envOutput))
	root.PersistentFlags().BoolVar(&flags.plain, "plain", false,
		"Disable colors and styling for machine-readable output")
	root.PersistentFlags().StringVar(&flags.region, "region", os.Getenv(envRegion),
		fmt.Sprintf("Default AWS region for resource key resolution (env: %s)", envRegion))
	flags.account = os.Getenv(envAccount)
	flags.workspace = os.Getenv(envWorkspace)
	if flags.workspace == "" {
		flags.workspace = loadActiveWorkspace()
	}

	// Register all subcommands.
	root.AddCommand(
		// Verb-first commands (primary CLI grammar)
		newDeployCmd(flags),
		newPlanCmd(flags),
		newGetCmd(flags),
		newDeleteCmd(flags),
		newListCmd(flags),
		newCreateCmd(flags),
		newSetCmd(flags),
		newMoveCmd(flags),
		newImportCmd(flags),
		newReconcileCmd(flags),
		newObserveCmd(flags),
		newAskCmd(flags),
		newApproveCmd(flags),
		newTestCmd(flags),

		// Admin (exception — concierge slack stays nested)
		newConciergeAdminCmd(flags),

		// Utilities
		newFmtCmd(),
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

// useStyles reports whether human-readable output should include color and
// borders. Styling is disabled for --plain, JSON output, NO_COLOR, and when
// stdout is not an interactive terminal.
func (f *rootFlags) useStyles() bool {
	return shouldUseStyles(f.outputFormat(), f.plain, os.Getenv("NO_COLOR") != "", isTerminal(os.Stdout))
}

func (f *rootFlags) renderer() *Renderer {
	return newRenderer(f.useStyles())
}

func (f *rootFlags) activeWorkspace() string {
	return strings.TrimSpace(f.workspace)
}

func shouldUseStyles(format OutputFormat, plain, noColor, stdoutIsTTY bool) bool {
	if plain || format == OutputJSON || noColor {
		return false
	}
	return stdoutIsTTY
}

func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// resolveFilePaths expands a path into a list of individual file paths.
// It supports a single file, a directory (walked recursively), or a glob pattern.
func resolveFilePaths(path string) ([]string, error) {
	// Try glob first — if the pattern contains no glob metacharacters,
	// filepath.Glob simply returns the literal match (same as Stat).
	if strings.ContainsAny(path, "*?[") {
		matches, err := filepath.Glob(path)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no files matched %q", path)
		}
		return matches, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}

	// Walk directory, collecting regular files.
	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found in %q", path)
	}
	return files, nil
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

// canonicalKinds maps lowercased kind names (and their plurals) to the
// canonical form used internally. This lets users type "deployment",
// "Deployment", "deployments", "template", "Templates", etc.
var canonicalKinds = buildCanonicalKinds()

func buildCanonicalKinds() map[string]string {
	// All known kinds expressed in their canonical form.
	known := []string{
		// Meta-resource kinds (lowercase canonical).
		"template", "sink", "workspace", "concierge",
		// Core kinds.
		"Deployment",
		// Cloud resource kinds (added below from kindScopes).
	}
	// Append every kind registered in the scope map.
	for k := range kindScopes {
		known = append(known, k)
	}

	m := make(map[string]string, len(known)*2)
	for _, k := range known {
		low := strings.ToLower(k)
		m[low] = k
		// Also accept a trailing "s" plural.
		m[low+"s"] = k
	}
	return m
}

// normalizeKind returns the canonical spelling for a user-supplied kind,
// performing a case-insensitive lookup and stripping a trailing plural "s".
// If the kind is not recognized, it is returned as-is so that unknown kinds
// still pass through to the backend.
func normalizeKind(kind string) string {
	if canon, ok := canonicalKinds[strings.ToLower(kind)]; ok {
		return canon
	}
	return kind
}

// kindScopes maps known resource kinds to their key scopes. The CLI uses this
// to decide whether user-supplied names need a region prefix. Unknown kinds
// default to scopeRegion (the safe fallback).
var kindScopes = map[string]keyScope{
	// Global-scoped: key is just the resource name, no region prefix.
	"S3Bucket":           scopeGlobal,
	"IAMRole":            scopeGlobal,
	"IAMPolicy":          scopeGlobal,
	"IAMUser":            scopeGlobal,
	"IAMGroup":           scopeGlobal,
	"IAMInstanceProfile": scopeGlobal,
	"Route53HostedZone":  scopeGlobal,
	"Route53HealthCheck": scopeGlobal,

	// Region-scoped: key is region~name.
	"ACMCertificate":       scopeRegion,
	"ALB":                  scopeRegion,
	"AMI":                  scopeRegion,
	"AuroraCluster":        scopeRegion,
	"Dashboard":            scopeRegion,
	"DBParameterGroup":     scopeRegion,
	"DBSubnetGroup":        scopeRegion,
	"EBSVolume":            scopeRegion,
	"EC2Instance":          scopeRegion,
	"ECRRepository":        scopeRegion,
	"ElasticIP":            scopeRegion,
	"EventSourceMapping":   scopeRegion,
	"InternetGateway":      scopeRegion,
	"KeyPair":              scopeRegion,
	"LambdaFunction":       scopeRegion,
	"LambdaLayer":          scopeRegion,
	"LambdaPermission":     scopeRegion,
	"Listener":             scopeRegion,
	"ListenerRule":         scopeRegion,
	"LogGroup":             scopeRegion,
	"MetricAlarm":          scopeRegion,
	"NATGateway":           scopeRegion,
	"NLB":                  scopeRegion,
	"RDSInstance":          scopeRegion,
	"SNSTopic":             scopeRegion,
	"SQSQueue":             scopeRegion,
	"SQSQueuePolicy":       scopeRegion,
	"TargetGroup":          scopeRegion,
	"VPC":                  scopeRegion,
	"VPCPeeringConnection": scopeRegion,

	// Custom-scoped: key is a composite (user supplies the full key).
	"ECRLifecyclePolicy": scopeCustom,
	"NetworkACL":         scopeCustom,
	"Route53Record":      scopeCustom,
	"RouteTable":         scopeCustom,
	"SecurityGroup":      scopeCustom,
	"SNSSubscription":    scopeCustom,
	"Subnet":             scopeCustom,
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

func loadActiveWorkspace() string {
	return strings.TrimSpace(LoadCLIConfig().ActiveWorkspace)
}
