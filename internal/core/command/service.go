// Package command implements the Praxis command surface as a Restate Basic
// Service (stateless, non-keyed). It is the primary entry point for every CLI
// and API operation: apply, plan, deploy, delete, import, and the template /
// policy registry mutations.
//
// # Architecture role
//
// The command service sits between the external API layer (CLI → HTTP gateway)
// and the durable orchestration layer (deployment workflows, virtual objects).
// It performs all synchronous preparation work:
//
//   - Resolve workspace defaults and merge variables
//   - Evaluate CUE templates with policy enforcement
//   - Resolve data sources via provider Lookup calls
//   - Substitute SSM parameter references
//   - Build the resource dependency DAG
//   - Compute a dry-run plan diff
//
// Once preparation succeeds, the service hands off to durable Restate
// components:
//
//   - DeploymentStateObj (Virtual Object) — owns the deployment lifecycle state
//   - DeploymentWorkflow / DeleteWorkflow — execute the actual create/update/delete
//   - TemplateRegistryObj / PolicyRegistryObj — own template and policy storage
//
// # Error model
//
// All validation failures and user-input errors are returned as
// [restate.TerminalError] with an appropriate HTTP status code (400, 404, 409).
// Terminal errors stop Restate retries immediately. Infrastructure errors
// (network timeouts, SDK failures) bubble up as plain errors, letting Restate
// retry the handler invocation automatically.
//
// # Restate execution model
//
// Because PraxisCommandService is a Restate Basic Service (not a Virtual
// Object), every handler invocation is independent — there is no per-key lock
// and no durable state on this service itself.  All durable state lives in the
// downstream Virtual Objects the handlers call into via restate.Object[T].
package command

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/resolver"
	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/internal/core/workspace"
)

// PraxisCommandService is the high-level command entry point for Praxis Core.
//
// It intentionally remains thin and mostly stateless:
//   - template preparation happens synchronously inside the request
//   - durable deployment lifecycle state lives in DeploymentStateObj
//   - asynchronous apply/delete execution lives in workflows
//   - provider-specific type branching stays behind the adapter registry
//
// Every public method on this struct is a Restate handler registered via
// restate.Reflect. The method signature (ctx, request) → (response, error)
// is enforced by the SDK's reflection registration.
type PraxisCommandService struct {
	// cfg holds static configuration (schema directory paths, feature flags).
	cfg config.Config
	// auth provides cross-account AWS credential resolution, used to obtain
	// per-account SDK configs for provider adapters and SSM lookups.
	auth authservice.AuthClient
	// engine is the CUE template evaluation engine that compiles templates,
	// validates schemas, and enforces policies.
	engine *template.Engine
	// providers maps resource kind strings (e.g., "AWS::S3::Bucket") to the
	// typed adapter that can plan, create, update, delete, and import
	// resources of that kind.
	providers *provider.Registry
}

// NewPraxisCommandService constructs the command surface with the
// concrete dependencies it needs.
func NewPraxisCommandService(cfg config.Config, auth authservice.AuthClient, providers *provider.Registry) *PraxisCommandService {
	if providers == nil {
		providers = provider.NewRegistry(auth)
	}

	return &PraxisCommandService{
		cfg:       cfg,
		auth:      auth,
		engine:    template.NewEngine(cfg.SchemaDir),
		providers: providers,
	}
}

// ServiceName pins the stable Restate service name for the command surface.
func (*PraxisCommandService) ServiceName() string {
	return "PraxisCommandService"
}

// trimTemplate normalises whitespace around user-supplied template bodies.
// Templates arriving from CLI or API may have leading/trailing newlines from
// heredoc encoding or editor artifacts.
func trimTemplate(templateBody string) string {
	return strings.TrimSpace(templateBody)
}

// marshalPrettyJSON serializes a value as human-readable JSON for inclusion
// in plan output and rendered template displays shown back to the user.
func marshalPrettyJSON(value any) (string, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal pretty JSON: %w", err)
	}
	return string(encoded), nil
}

// resolveRequestAccount determines the AWS account alias for this request.
// The account can be specified at two levels:
//  1. Directly in the request's "account" field (requestAccount parameter).
//  2. Inside the variables map as variables["account"].
//
// The variables-level value takes precedence because it can be set inside the
// CUE template itself, allowing templates to be self-contained.
func (s *PraxisCommandService) resolveRequestAccount(requestAccount string, variables map[string]any) (string, error) {
	accountName := strings.TrimSpace(requestAccount)
	// The variables["account"] override lets templates embed their target account.
	if raw, ok := variables["account"]; ok {
		accountValue, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("variables.account must be a string")
		}
		accountName = strings.TrimSpace(accountValue)
		if accountName == "" {
			return "", fmt.Errorf("variables.account must not be empty")
		}
	}
	return accountName, nil
}

// resolveWorkspaceDefaults loads workspace defaults and merges them with
// explicit request values. Explicit values always take precedence.
func (s *PraxisCommandService) resolveWorkspaceDefaults(
	ctx restate.Context,
	requestAccount string,
	requestWorkspace string,
	variables map[string]any,
) (account string, mergedVars map[string]any, err error) {
	account, err = s.resolveRequestAccount(requestAccount, variables)
	if err != nil {
		return "", nil, err
	}
	mergedVars = variables

	// If a workspace is specified, load its defaults from the durable
	// WorkspaceObj virtual object. This is a Restate request-response call
	// that will be journaled – on replay the result is restored from the
	// journal rather than re-fetching.
	if requestWorkspace != "" {
		wsInfo, wsErr := restate.Object[workspace.WorkspaceInfo](
			ctx, workspace.WorkspaceServiceName, requestWorkspace, "Get",
		).Request(restate.Void{})
		if wsErr != nil {
			return "", nil, fmt.Errorf("workspace %q: %w", requestWorkspace, wsErr)
		}

		// Workspace defaults apply only when the request doesn't override.
		if account == "" {
			account = wsInfo.Account
		}

		// Merge workspace variables (lower priority) with request variables
		// (higher priority). This means CLI flags and template inline values
		// always beat workspace defaults.
		if len(wsInfo.Variables) > 0 {
			merged := make(map[string]any, len(wsInfo.Variables)+len(variables))
			for k, v := range wsInfo.Variables {
				merged[k] = v
			}
			maps.Copy(merged, variables)
			mergedVars = merged
		}
	}

	// Fallback chain: PRAXIS_ACCOUNT env var → "default".
	// This ensures every command always resolves to a concrete account name.
	if account == "" {
		account = os.Getenv("PRAXIS_ACCOUNT")
	}
	if account == "" {
		account = "default"
	}

	return account, mergedVars, nil
}

// newSSMResolver creates an SSM parameter resolver for the given account.
// The resolver is wrapped in a Restate-aware layer (RestateSSMResolver) that
// performs SSM GetParameter calls inside restate.Run blocks, ensuring that
// resolved secret values are journaled exactly once and replayed without
// re-fetching on retries.
func (s *PraxisCommandService) newSSMResolver(ctx restate.Context, accountName string) (*resolver.RestateSSMResolver, error) {
	awsCfg, err := s.auth.GetCredentials(ctx, accountName)
	if err != nil {
		return nil, fmt.Errorf("resolve AWS config for account %q: %w", accountName, err)
	}
	return resolver.NewRestateSSMResolver(resolver.NewSSMResolver(ssm.NewFromConfig(awsCfg))), nil
}
