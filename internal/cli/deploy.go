package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/pkg/types"
)

type deployOpts struct {
	vars          []string
	varsFile      string
	deploymentKey string
	wait          bool
	dryRun        bool
	showRendered  bool
	pollInterval  time.Duration
	timeout       time.Duration
	account       string
	yes           bool
	targets       []string
	replace       []string
	allowReplace  bool
	parallelism   int
	maxRetries    int
	orphanRemoved bool
	planFile      string
	maxPlanAge    time.Duration
}

// newDeployCmd builds the `praxis deploy` subcommand.
func newDeployCmd(flags *rootFlags) *cobra.Command {
	opts := deployOpts{account: flags.account}

	cmd := &cobra.Command{
		Use:   "deploy [<file.cue | template-name>]",
		Short: "Provision infrastructure from a template source",
		Long: `Deploy provisions infrastructure from either a local CUE file or a
registered template name.

If the argument ends in .cue or points to an existing file on disk, Praxis
treats it as an inline template source. Otherwise it is treated as a
registered template name.

Before provisioning, a plan diff is displayed showing what would change.
You must confirm before changes are applied. Use --yes to skip the prompt.

Examples:

    praxis deploy webapp.cue --var env=prod
    praxis deploy ./templates/webapp.cue -f variables.json
    praxis deploy stack1 --var name=orders-api --key orders-prod
	    praxis deploy stack1 --dry-run --show-rendered
    praxis deploy --plan=plan.json`,
		Args: func(cmd *cobra.Command, args []string) error {
			if opts.planFile != "" {
				if len(args) != 0 {
					return fmt.Errorf("cannot specify a template argument with --plan")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.orphanRemoved && len(opts.targets) > 0 {
				return fmt.Errorf("cannot combine --orphan-removed with --target")
			}
			if opts.planFile != "" {
				return deployFromSavedPlan(flags, opts)
			}
			source := args[0]
			if isFilePath(source) {
				return deployFromFile(flags, source, opts)
			}
			return deployFromTemplate(flags, source, opts)
		},
	}

	cmd.Flags().StringArrayVar(&opts.vars, "var", nil, "Template variable in key=value format (repeatable)")
	cmd.Flags().StringVarP(&opts.varsFile, "file", "f", "", "JSON file containing template variables")
	cmd.Flags().StringVar(&opts.deploymentKey, "key", "", "Pin a stable deployment key for idempotent re-deploy")
	cmd.Flags().StringVar(&opts.account, "account", opts.account, "AWS account name to use (env: PRAXIS_ACCOUNT)")
	cmd.Flags().BoolVar(&opts.wait, "wait", false, "Poll until deployment completes or fails")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Preview changes without provisioning")
	cmd.Flags().BoolVar(&opts.showRendered, "show-rendered", false, "Also display the fully-evaluated template JSON (with --dry-run)")
	cmd.Flags().DurationVar(&opts.pollInterval, "poll-interval", 2*time.Second, "Polling interval when --wait is set")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 5*time.Minute, "Maximum time to wait for completion (0 for no limit)")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip the confirmation prompt")
	cmd.Flags().StringArrayVar(&opts.targets, "target", nil, "Limit to named resource and its dependencies (repeatable)")
	cmd.Flags().StringArrayVar(&opts.replace, "replace", nil, "Force delete and re-provision of named resource (repeatable)")
	cmd.Flags().BoolVar(&opts.allowReplace, "allow-replace", false, "Automatically replace resources that fail due to immutable field changes (WARNING: destroys and recreates affected resources)")
	cmd.Flags().IntVar(&opts.parallelism, "parallelism", 0, "Maximum number of concurrent resource operations (0 = unlimited)")
	cmd.Flags().IntVar(&opts.maxRetries, "max-retries", -1, "Maximum number of resource retries for transient failures (-1 = default)")
	cmd.Flags().BoolVar(&opts.orphanRemoved, "orphan-removed", false, "Keep resources removed from the template running and only detach them from Praxis management")
	cmd.Flags().StringVar(&opts.planFile, "plan", "", "Apply a previously saved plan file")
	cmd.Flags().DurationVar(&opts.maxPlanAge, "max-plan-age", time.Hour, "Maximum allowed age for a saved plan (0 = no limit)")

	return cmd
}

func deployFromFile(flags *rootFlags, templatePath string, opts deployOpts) error {
	renderer := flags.renderer()
	workspace := flags.activeWorkspace()

	content, err := os.ReadFile(templatePath) //nolint:gosec // G304: path is user-supplied CLI argument
	if err != nil {
		return fmt.Errorf("read template %q: %w", templatePath, err)
	}

	variables, err := mergeVariables(opts.vars, opts.varsFile)
	if err != nil {
		return err
	}

	client := flags.newClient()
	ctx := context.Background()

	planResp, err := client.Plan(ctx, types.PlanRequest{
		Template:      string(content),
		Variables:     variables,
		Account:       opts.account,
		Workspace:     workspace,
		Targets:       opts.targets,
		TemplatePath:  templatePath,
		DeploymentKey: opts.deploymentKey,
	})
	if err != nil {
		return err
	}

	if opts.dryRun {
		if flags.outputFormat() == OutputJSON {
			return printJSON(planResp)
		}
		printWarnings(renderer, planResp.Warnings)
		printPlan(renderer, planResp.Plan)
		if opts.showRendered && planResp.Rendered != "" {
			_, _ = fmt.Fprintln(renderer.out)
			_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Rendered template:"))
			_, _ = fmt.Fprintln(renderer.out, planResp.Rendered)
		}
		return nil
	}

	if flags.outputFormat() != OutputJSON {
		printWarnings(renderer, planResp.Warnings)
		printPlan(renderer, planResp.Plan)
	}

	if planResp.Plan == nil || !planResp.Plan.Summary.HasChanges() {
		if flags.outputFormat() == OutputJSON {
			return printJSON(planResp)
		}
		return nil
	}

	if !opts.yes && !confirmDeploy(renderer) {
		return nil
	}

	resp, err := client.Apply(ctx, types.ApplyRequest{
		Template:       string(content),
		Variables:      variables,
		DeploymentKey:  opts.deploymentKey,
		Account:        opts.account,
		Workspace:      workspace,
		Targets:        opts.targets,
		OrphanRemoved:  opts.orphanRemoved,
		Replace:        opts.replace,
		AllowReplace:   opts.allowReplace,
		TemplatePath:   templatePath,
		MaxParallelism: opts.parallelism,
		MaxRetries:     optionalInt(opts.maxRetries),
	})
	if err != nil {
		return err
	}

	return finishDeployment(flags, ctx, client, renderer, resp.DeploymentKey, resp.Status, opts.wait, opts.pollInterval, opts.timeout)
}

func deployFromTemplate(flags *rootFlags, templateName string, opts deployOpts) error {
	renderer := flags.renderer()
	workspace := flags.activeWorkspace()

	variables, err := mergeVariables(opts.vars, opts.varsFile)
	if err != nil {
		return err
	}

	client := flags.newClient()
	ctx := context.Background()

	planResp, err := client.PlanDeploy(ctx, types.PlanDeployRequest{
		Template:      templateName,
		Variables:     variables,
		Account:       opts.account,
		Workspace:     workspace,
		Targets:       opts.targets,
		DeploymentKey: opts.deploymentKey,
	})
	if err != nil {
		return err
	}

	if opts.dryRun {
		if flags.outputFormat() == OutputJSON {
			return printJSON(planResp)
		}
		printWarnings(renderer, planResp.Warnings)
		printDataSources(renderer, planResp.DataSources)
		printPlan(renderer, planResp.Plan)
		if opts.showRendered && planResp.Rendered != "" {
			_, _ = fmt.Fprintln(renderer.out)
			_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Rendered template:"))
			_, _ = fmt.Fprintln(renderer.out, planResp.Rendered)
		}
		return nil
	}

	if flags.outputFormat() != OutputJSON {
		printWarnings(renderer, planResp.Warnings)
		printDataSources(renderer, planResp.DataSources)
		printPlan(renderer, planResp.Plan)
	}

	if planResp.Plan == nil || !planResp.Plan.Summary.HasChanges() {
		if flags.outputFormat() == OutputJSON {
			return printJSON(planResp)
		}
		return nil
	}

	if !opts.yes && !confirmDeploy(renderer) {
		return nil
	}

	resp, err := client.Deploy(ctx, types.DeployRequest{
		Template:       templateName,
		Variables:      variables,
		DeploymentKey:  opts.deploymentKey,
		Account:        opts.account,
		Workspace:      workspace,
		Targets:        opts.targets,
		OrphanRemoved:  opts.orphanRemoved,
		Replace:        opts.replace,
		AllowReplace:   opts.allowReplace,
		MaxParallelism: opts.parallelism,
		MaxRetries:     optionalInt(opts.maxRetries),
	})
	if err != nil {
		return err
	}

	return finishDeployment(flags, ctx, client, renderer, resp.DeploymentKey, resp.Status, opts.wait, opts.pollInterval, opts.timeout)
}

func deployFromSavedPlan(flags *rootFlags, opts deployOpts) error {
	if len(opts.vars) > 0 || opts.varsFile != "" {
		return fmt.Errorf("cannot use --var or --file with --plan")
	}
	if opts.dryRun {
		return fmt.Errorf("cannot use --dry-run with --plan")
	}
	if opts.showRendered {
		return fmt.Errorf("cannot use --show-rendered with --plan")
	}
	if opts.deploymentKey != "" {
		return fmt.Errorf("cannot override --key when using --plan")
	}
	if len(opts.targets) > 0 {
		return fmt.Errorf("cannot use --target with --plan")
	}
	if len(opts.replace) > 0 || opts.allowReplace {
		return fmt.Errorf("cannot use replacement flags with --plan")
	}

	renderer := flags.renderer()
	client := flags.newClient()
	ctx := context.Background()

	saved, err := command.ReadSavedPlanFile(opts.planFile)
	if err != nil {
		return err
	}
	if saved.Plan.Account != "" && opts.account != "" && saved.Plan.Account != opts.account {
		return fmt.Errorf("saved plan was created for account %q but current account is %q", saved.Plan.Account, opts.account)
	}
	var signingKey []byte
	if raw := os.Getenv("PRAXIS_PLAN_SIGNING_KEY"); raw != "" {
		signingKey = []byte(raw)
	}
	if err := command.VerifySavedPlan(saved, signingKey); err != nil {
		return err
	}
	warnings := make([]string, 0, 2)
	if saved.Signature == "" {
		warnings = append(warnings, "Saved plan is unsigned; set PRAXIS_PLAN_SIGNING_KEY to require HMAC verification")
	}
	if opts.maxPlanAge > 0 && time.Since(saved.CreatedAt) > opts.maxPlanAge {
		return fmt.Errorf("saved plan is too old: created %s ago (max %s)", time.Since(saved.CreatedAt).Round(time.Second), opts.maxPlanAge)
	}
	if saved.TemplateHash != "" {
		currentHash, err := currentTemplateHash(ctx, client, saved.Plan.TemplatePath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Could not verify current template hash: %v", err))
		} else if currentHash != "" && currentHash != saved.TemplateHash {
			warnings = append(warnings, "Template source has changed since this plan was created; regenerate the plan if you need strict source parity")
		}
	}
	if flags.outputFormat() != OutputJSON {
		for _, warning := range warnings {
			renderer.writeLabelValue("Warning", 11, warning)
		}
	}

	if flags.outputFormat() != OutputJSON && saved.Diff != nil {
		printPlan(renderer, saved.Diff)
	}
	if saved.Diff == nil || !saved.Diff.Summary.HasChanges() {
		if flags.outputFormat() == OutputJSON {
			return printJSON(saved)
		}
		return nil
	}
	if !opts.yes && !confirmDeploy(renderer) {
		return nil
	}

	resp, err := client.ApplySavedPlan(ctx, types.ApplySavedPlanRequest{
		Plan:           saved.Plan,
		OrphanRemoved:  opts.orphanRemoved,
		MaxParallelism: opts.parallelism,
		MaxRetries:     optionalInt(opts.maxRetries),
	})
	if err != nil {
		return err
	}
	return finishDeployment(flags, ctx, client, renderer, resp.DeploymentKey, resp.Status, opts.wait, opts.pollInterval, opts.timeout)
}

func currentTemplateHash(ctx context.Context, client *Client, templatePath string) (string, error) {
	if templatePath == "" || strings.HasPrefix(templatePath, "inline://") {
		return "", nil
	}
	if strings.HasPrefix(templatePath, "registry://") {
		source, err := client.GetTemplateSource(ctx, strings.TrimPrefix(templatePath, "registry://"))
		if err != nil {
			return "", err
		}
		return command.TemplateSourceHash(source), nil
	}
	content, err := os.ReadFile(templatePath) //nolint:gosec // G304: path is user-supplied plan content
	if err != nil {
		return "", err
	}
	return command.TemplateSourceHash(string(content)), nil
}

func confirmDeploy(renderer *Renderer) bool {
	_, _ = fmt.Fprint(renderer.out, "\n"+renderer.renderPrompt("Do you want to apply these changes? (yes/no): "))
	var confirm string
	if _, err := fmt.Scanln(&confirm); err != nil {
		_, _ = fmt.Fprintln(renderer.out, renderer.renderMuted("Apply cancelled."))
		return false
	}
	confirm = strings.ToLower(strings.TrimSpace(confirm))
	if confirm != "yes" && confirm != "y" {
		_, _ = fmt.Fprintln(renderer.out, renderer.renderMuted("Apply cancelled."))
		return false
	}
	return true
}

func finishDeployment(flags *rootFlags, ctx context.Context, client *Client, renderer *Renderer, deploymentKey string, status types.DeploymentStatus, wait bool, pollInterval, timeout time.Duration) error {
	if flags.outputFormat() == OutputJSON {
		return printJSON(types.DeployResponse{DeploymentKey: deploymentKey, Status: status})
	}

	renderer.writeLabelValue("Deployment", 11, deploymentKey)
	renderer.writeLabelStyledValue("Status", 11, renderer.renderStatus(string(status)))

	if !wait {
		_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderMuted("Use 'praxis get Deployment/"+deploymentKey+"' to check progress."))
		return nil
	}

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}

	err := pollDeployment(ctx, client, deploymentKey, pollInterval, flags.outputFormat(), renderer)
	if cancel != nil {
		cancel()
	}
	if isTimeoutError(ctx, err) {
		printTimeoutError(renderer, timeout, deploymentKey)
		os.Exit(2)
	}
	return err
}

func optionalInt(value int) *int {
	if value < 0 {
		return nil
	}
	return &value
}

func isFilePath(arg string) bool {
	if strings.HasSuffix(arg, ".cue") {
		return true
	}
	info, err := os.Stat(arg)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// mergeVariables combines --var flags and -f JSON file. Flag values override file.
func mergeVariables(vars []string, varsFile string) (map[string]any, error) {
	result := make(map[string]any)

	if varsFile != "" {
		content, err := os.ReadFile(varsFile) //nolint:gosec // G304: path is user-supplied CLI argument
		if err != nil {
			return nil, fmt.Errorf("read variables file %q: %w", varsFile, err)
		}
		if err := json.Unmarshal(content, &result); err != nil {
			return nil, fmt.Errorf("parse variables file %q: %w", varsFile, err)
		}
	}

	flagVars, err := parseVariables(vars)
	if err != nil {
		return nil, err
	}
	maps.Copy(result, flagVars)

	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// parseVariables converts a slice of "key=value" strings into a map. Returns
// an error if any entry is malformed.
func parseVariables(vars []string) (map[string]any, error) {
	if len(vars) == 0 {
		return nil, nil
	}
	result := make(map[string]any, len(vars))
	for _, v := range vars {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("invalid variable %q: expected key=value", v)
		}
		result[strings.TrimSpace(parts[0])] = parts[1]
	}
	return result, nil
}

// pollDeployment queries the deployment state at regular intervals until it
// reaches a terminal status. It prints incremental status updates for the user.
func pollDeployment(ctx context.Context, client *Client, key string, interval time.Duration, format OutputFormat, renderer *Renderer) error {
	_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderSection("Waiting for deployment to complete..."))

	var lastStatus types.DeploymentStatus
	for {
		detail, err := client.GetDeployment(ctx, key)
		if err != nil {
			return fmt.Errorf("poll deployment: %w", err)
		}
		if detail == nil {
			return fmt.Errorf("deployment %q not found during polling", key)
		}

		if detail.Status != lastStatus {
			renderer.writeLabelStyledValue("Status", 9, renderer.renderStatus(string(detail.Status)))
			lastStatus = detail.Status
		}

		if isTerminalStatus(detail.Status) {
			_, _ = fmt.Fprintln(renderer.out)
			if format == OutputJSON {
				return printJSON(detail)
			}
			printDeploymentDetail(renderer, detail, deploymentSections{
				Deps:    true,
				Inputs:  true,
				Outputs: true,
				Errors:  true,
			})
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func isTerminalStatus(s types.DeploymentStatus) bool {
	switch s {
	case types.DeploymentComplete, types.DeploymentFailed,
		types.DeploymentDeleted, types.DeploymentCancelled:
		return true
	default:
		return false
	}
}
