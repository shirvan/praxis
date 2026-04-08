// pipeline.go contains the shared template evaluation pipeline and the
// deployment submission logic used by all command handlers (Apply, Plan,
// Deploy, PlanDeploy, ValidateTemplate).
//
// # Pipeline overview
//
// The pipeline transforms a user-supplied CUE template into a deployment-ready
// plan through the following stages:
//
//  1. Resolve template source — inline body or registry lookup.
//  2. Load applicable policies — global + template-scoped.
//  3. CUE evaluation — compile, unify variables, enforce policies.
//  4. Data source resolution — call provider Lookup for each `data` block.
//  5. Data expression substitution — replace ${data.x.outputs.y} references.
//  6. SSM parameter resolution — fetch AWS SSM parameters, track sensitive paths.
//  7. Build resource nodes — parse kind, key, dependencies, lifecycle.
//  8. DAG construction — topological sort with optional target filtering.
//  9. Render display template — pretty-print with sensitive values masked.
//
// The pipeline output (compiledTemplate) is consumed differently by each handler:
//   - Plan/PlanDeploy: compute per-resource diffs, return to caller.
//   - Apply/Deploy: submit an async DeploymentWorkflow via submitDeployment.
//   - ValidateTemplate (full mode): discard result, success = valid.
//
// # Error model
//
// Pipeline stages that detect user errors (bad CUE, unknown kind, cycle in
// DAG) return TerminalError(err, 400). Infrastructure failures (SSM API down,
// auth failure) return TerminalError(err, 500) to prevent retries on issues
// that cannot self-heal.
package command

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"
	"unicode"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/internal/core/jsonpath"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/registry"
	"github.com/shirvan/praxis/internal/core/resolver"
	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

// renderedResourceDocument is a minimal JSON projection used to extract the
// "kind" and "metadata.name" fields from a rendered resource spec without
// fully decoding the provider-specific schema.
type renderedResourceDocument struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

// compiledTemplate holds the complete output of the template evaluation
// pipeline. It is the intermediate product between "user intent" and
// "durable deployment state". Handlers consume it differently:
//   - Plan handlers read PlanResources and Rendered for display.
//   - Apply/Deploy handlers pass it to submitDeployment for workflow dispatch.
type compiledTemplate struct {
	// TemplatePath records the origin of the template source for audit
	// purposes: "inline://template.cue" or "registry://<name>".
	TemplatePath string
	// TemplateHash is the SHA-256 digest of the template source used to build
	// this compiled plan.
	TemplateHash string
	// Specs maps logical resource name → fully-resolved JSON spec after
	// CUE evaluation, data source substitution, and SSM resolution.
	Specs map[string]json.RawMessage
	// DataSources maps data source name → resolved outputs from provider
	// Lookup calls. Included in plan responses for transparency.
	DataSources map[string]types.DataSourceResult
	// Nodes are the parsed ResourceNode values extracted from Specs,
	// including dependency and lifecycle metadata.
	Nodes []*types.ResourceNode
	// Graph is the directed acyclic graph of resource dependencies,
	// used for topological ordering during deployment.
	Graph *dag.Graph
	// PlanResources are the resources in topologically-sorted order,
	// ready for submission to the DeploymentWorkflow.
	PlanResources []orchestrator.PlanResource
	// Rendered is the human-readable, pretty-printed JSON representation
	// of the resolved template with sensitive values masked.
	Rendered string
	// Sensitive tracks which JSON paths contain SSM-resolved secrets so
	// they can be masked in display output.
	Sensitive *resolver.SensitiveParams
}

// compileTemplate runs the shared command pipeline up to the point where a
// handler can either submit a workflow or build a dry-run plan.
//
// This is the core of the command service. Every handler that touches
// templates calls this function. The pipeline stages are:
//
//  1. resolveTemplateSource — choose inline body or registry lookup.
//  2. loadAllPolicies — gather global + template-scoped CUE policies.
//  3. engine.EvaluateBytesWithPolicies — CUE compilation with policy gates.
//  4. validateDataSources + resolveDataSources — if data blocks exist,
//     validate them and call provider Lookup to fetch live outputs.
//  5. substituteDataExprs — replace ${data.x.outputs.y} string expressions
//     with the actual resolved values.
//  6. newSSMResolver.Resolve — fetch AWS SSM parameters and track sensitive paths.
//  7. buildResourceNodes — parse kind, canonical key, dependencies, lifecycle.
//  8. dag.NewGraph — construct the dependency DAG and verify it's acyclic.
//  9. (optional) graph.Subgraph — if --target flags were passed, reduce to
//     the named resources and their transitive dependencies.
//
// 10. renderResolvedTemplate — pretty-print with sensitive values masked.
//
// All user-input errors are returned as TerminalError(err, 400).
// Infrastructure errors (e.g., SSM API failure) → TerminalError(err, 500).
func (s *PraxisCommandService) compileTemplate(
	ctx restate.Context,
	templateBody string,
	ref *types.TemplateRef,
	variables map[string]any,
	accountName string,
	targets []string,
	templatePathHint string,
) (*compiledTemplate, error) {
	source, templatePath, err := s.resolveTemplateSource(ctx, templateBody, ref, templatePathHint)
	if err != nil {
		return nil, err
	}

	policies, err := s.loadAllPolicies(ctx, ref)
	if err != nil {
		return nil, err
	}

	evalResult, err := s.engine.EvaluateBytesWithPolicies([]byte(source), policies, variables)
	if err != nil {
		return nil, restate.TerminalError(err, 400)
	}
	rawSpecs := evalResult.Resources
	dataSources := map[string]types.DataSourceResult(nil)

	// --- Data source resolution (optional) ---
	// If the template declares `data` blocks, resolve them now. Data sources
	// let templates reference existing cloud resources (e.g., look up a VPC
	// ID by tag and inject it into a subnet spec).
	if len(evalResult.DataSources) > 0 {
		// Build a set of resource names so we can detect name collisions
		// between data sources and resources (they share the same namespace
		// in expression references like ${data.x.outputs.y}).
		resourceNames := make(map[string]bool, len(rawSpecs))
		for name := range rawSpecs {
			resourceNames[name] = true
		}

		if err := s.validateDataSources(evalResult.DataSources, resourceNames); err != nil {
			return nil, restate.TerminalError(err, 400)
		}

		dataSources, err = s.resolveDataSources(ctx, evalResult.DataSources, accountName)
		if err != nil {
			return nil, restate.TerminalError(err, 500)
		}

		// After successful data source resolution, substitute all
		// ${data.x.outputs.y} expressions in resource specs with the
		// actual resolved values.
		rawSpecs, err = substituteDataExprs(rawSpecs, dataSources)
		if err != nil {
			return nil, restate.TerminalError(err, 400)
		}
	}

	// --- SSM parameter resolution ---
	// Resolve any ssm:// references in spec values. The resolver fetches
	// the actual secret values from AWS SSM Parameter Store and tracks
	// which JSON paths are sensitive (for masking in rendered output).
	ssmResolver, err := s.newSSMResolver(ctx, accountName)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}

	ssmResolvedSpecs, sensitive, err := ssmResolver.Resolve(ctx, rawSpecs)
	if err != nil {
		return nil, restate.TerminalError(err, 400)
	}

	// --- Resource node construction ---
	// Parse each resolved spec into a ResourceNode, extracting:
	//   - kind: the provider resource type (e.g., "AWS::S3::Bucket")
	//   - key: the canonical resource identifier built by the adapter
	//   - dependencies: inter-resource references (${resource.x.outputs.y})
	//   - expressions: output reference expressions for runtime substitution
	//   - lifecycle: optional ignore_changes rules
	nodes, err := s.buildResourceNodes(ssmResolvedSpecs)
	if err != nil {
		return nil, restate.TerminalError(err, 400)
	}

	// --- DAG construction ---
	// Build a directed acyclic graph from the resource dependencies.
	// This also validates that there are no cycles.
	graph, err := dag.NewGraph(nodes)
	if err != nil {
		return nil, restate.TerminalError(fmt.Errorf("invalid deployment graph: %w", err), 400)
	}

	// --- Optional target filtering ---
	// If targets are specified (via --target CLI flags), reduce the graph
	// to only the named resources and their transitive dependencies.
	// This enables incremental deployments of specific resources within
	// a larger template.
	if len(targets) > 0 {
		graph, err = graph.Subgraph(targets)
		if err != nil {
			return nil, restate.TerminalError(err, 400)
		}
		// Rebuild the node list to match the filtered graph.
		filtered := make(map[string]bool, len(targets))
		for _, name := range graph.TopologicalOrder() {
			filtered[name] = true
		}
		filteredNodes := make([]*types.ResourceNode, 0, len(filtered))
		for _, node := range nodes {
			if filtered[node.Name] {
				filteredNodes = append(filteredNodes, node)
			}
		}
		nodes = filteredNodes
	}

	// --- Rendered output ---
	// Build the human-readable JSON representation with sensitive values
	// replaced by "***". This is returned in Plan responses and displayed
	// by the CLI.
	rendered, err := renderResolvedTemplate(dataSources, ssmResolvedSpecs, sensitive)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}

	return &compiledTemplate{
		TemplatePath:  templatePath,
		TemplateHash:  TemplateSourceHash(source),
		Specs:         ssmResolvedSpecs,
		DataSources:   dataSources,
		Nodes:         nodes,
		Graph:         graph,
		PlanResources: planResourcesFromGraph(nodes, graph),
		Rendered:      rendered,
		Sensitive:     sensitive,
	}, nil
}

// submitDeployment handles the common post-compilation steps shared by Apply
// and Deploy: check for active deletion, initialize deployment state, update
// the global index, append an event, and send the async workflow.
//
// This is the "point of no return" — once submitDeployment succeeds, the
// deployment is durable and the workflow will execute to completion even if
// the command handler crashes. All steps are journaled by Restate.
//
// Steps:
//  1. Guard: reject if the deployment is currently deleting (409 conflict).
//  2. Capture current time via restate.Run (journaled for determinism).
//  3. Validate --replace resource names against the plan.
//  4. Initialize DeploymentStateObj with the plan (returns generation number).
//  5. Upsert the global DeploymentIndex for listing/search.
//  6. Emit audit CloudEvents (command + deployment-submitted).
//  7. Send the DeploymentWorkflow one-way message to start async execution.
func (s *PraxisCommandService) submitDeployment(
	ctx restate.Context,
	deploymentKey string,
	account string,
	workspace string,
	variables map[string]any,
	compiled *compiledTemplate,
	removeMissing bool,
	orphanRemoved bool,
	forceReplace []string,
	allowReplace bool,
	maxParallelism int,
	maxRetries *int,
) (string, types.DeploymentStatus, error) {
	return s.submitPlanResources(
		ctx,
		deploymentKey,
		account,
		workspace,
		variables,
		compiled.PlanResources,
		compiled.TemplatePath,
		removeMissing,
		orphanRemoved,
		forceReplace,
		allowReplace,
		maxParallelism,
		maxRetries,
	)
}

func (s *PraxisCommandService) submitPlanResources(
	ctx restate.Context,
	deploymentKey string,
	account string,
	workspace string,
	variables map[string]any,
	planResources []orchestrator.PlanResource,
	templatePath string,
	removeMissing bool,
	orphanRemoved bool,
	forceReplace []string,
	allowReplace bool,
	maxParallelism int,
	maxRetries *int,
) (string, types.DeploymentStatus, error) {
	// Guard: prevent submitting a new deployment while a delete is in progress.
	// The user must wait for deletion to complete first.
	existingState, err := restate.Object[*orchestrator.DeploymentState](
		ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "GetState",
	).Request(restate.Void{})
	if err != nil {
		return "", "", err
	}
	if existingState != nil {
		if err := checkSubmitGuard(deploymentKey, existingState.Status); err != nil {
			return "", "", err
		}
		if removeMissing {
			removed := missingDeploymentResources(existingState, planResources)
			if len(removed) > 0 {
				if err := s.cleanupMissingResources(ctx, deploymentKey, removed, orphanRemoved); err != nil {
					return "", "", err
				}
			}
		}
	}

	// Capture the current timestamp inside restate.Run so it is journaled.
	// Using time.Now() directly would be non-deterministic during replay.
	createdAt, err := restate.Run(ctx, func(runCtx restate.RunContext) (time.Time, error) {
		return time.Now().UTC(), nil
	})
	if err != nil {
		return "", "", err
	}

	// Validate --replace resource names exist in the plan.
	if len(forceReplace) > 0 {
		planNames := make(map[string]bool, len(planResources))
		for i := range planResources {
			planNames[planResources[i].Name] = true
		}
		for _, name := range forceReplace {
			if !planNames[name] {
				return "", "", restate.TerminalError(
					fmt.Errorf("--replace resource %q does not exist in the deployment", name), 400)
			}
		}
	}

	retryConfig := (*orchestrator.RetryConfig)(nil)
	if maxRetries != nil {
		retryConfig = &orchestrator.RetryConfig{
			MaxRetries: *maxRetries,
			BaseDelay:  5 * time.Second,
			MaxDelay:   2 * time.Minute,
		}
	}

	plan := orchestrator.DeploymentPlan{
		Key:            deploymentKey,
		Account:        account,
		Workspace:      workspace,
		Resources:      planResources,
		Variables:      variables,
		CreatedAt:      createdAt,
		TemplatePath:   templatePath,
		ForceReplace:   forceReplace,
		AllowReplace:   allowReplace,
		MaxParallelism: maxParallelism,
		RetryConfig:    retryConfig,
	}

	// Initialize the durable DeploymentStateObj. This is the central
	// virtual object that tracks the deployment's lifecycle. InitDeployment
	// stores the plan and returns the new generation number (monotonically
	// increasing integer used to version deployment state).
	generation, err := restate.WithRequestType[orchestrator.DeploymentPlan, int64](
		restate.Object[int64](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "InitDeployment"),
	).Request(plan)
	if err != nil {
		return "", "", err
	}

	// Upsert the global deployment index. This index powers `praxis list`
	// and allows the CLI/API to enumerate all deployments without scanning
	// every DeploymentStateObj.
	_, err = restate.WithRequestType[types.DeploymentSummary, restate.Void](
		restate.Object[restate.Void](ctx, orchestrator.DeploymentIndexServiceName, orchestrator.DeploymentIndexGlobalKey, "Upsert"),
	).Request(types.DeploymentSummary{
		Key:       deploymentKey,
		Status:    types.DeploymentPending,
		Resources: len(plan.Resources),
		Workspace: workspace,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})
	if err != nil {
		return "", "", err
	}

	// Seed the global resource index with Pending entries for all resources
	// in this deployment. This enables `praxis list <Kind>` to show resources
	// immediately after submission, before the workflow transitions them.
	for i := range plan.Resources {
		r := &plan.Resources[i]
		_, err = restate.WithRequestType[orchestrator.ResourceIndexEntry, restate.Void](
			restate.Object[restate.Void](ctx, orchestrator.ResourceIndexServiceName, orchestrator.ResourceIndexGlobalKey, "Upsert"),
		).Request(orchestrator.ResourceIndexEntry{
			Kind:          r.Kind,
			Key:           r.Key,
			DeploymentKey: deploymentKey,
			ResourceName:  r.Name,
			Workspace:     workspace,
			Status:        string(types.DeploymentResourcePending),
			CreatedAt:     createdAt,
		})
		if err != nil {
			return "", "", err
		}
	}

	// Emit structured CloudEvents for audit trail. Two events:
	// 1. command.apply — records the user action with metadata.
	// 2. deployment.submitted — records the workflow submission.
	commandEvent, err := orchestrator.NewCommandApplyEvent(deploymentKey, workspace, account, generation, createdAt)
	if err != nil {
		return "", "", err
	}
	if err := orchestrator.EmitCloudEvent(ctx, commandEvent); err != nil {
		return "", "", err
	}

	event, err := orchestrator.NewDeploymentSubmittedEvent(deploymentKey, workspace, generation, createdAt)
	if err != nil {
		return "", "", err
	}
	err = orchestrator.EmitDeploymentCloudEvent(ctx, event)
	if err != nil {
		return "", "", err
	}

	// Dispatch the deployment workflow asynchronously via WorkflowSend.
	// The workflow ID incorporates the generation to ensure idempotency —
	// Restate deduplicates by (workflow service, workflow ID) pair.
	workflowID := fmt.Sprintf("%s-gen-%d", deploymentKey, generation)
	restate.WorkflowSend(ctx, orchestrator.DeploymentWorkflowServiceName, workflowID, "Run").Send(
		plan,
		restate.WithIdempotencyKey(workflowID),
	)

	return deploymentKey, types.DeploymentPending, nil
}

// resolveTemplateSource determines the CUE template source from either an
// inline body or a registry reference. Exactly one must be provided.
// Returns the raw CUE source string and a path string for audit purposes.
func (s *PraxisCommandService) resolveTemplateSource(ctx restate.Context, templateBody string, ref *types.TemplateRef, templatePathHint string) (string, string, error) {
	source := trimTemplate(templateBody)
	templatePath := "inline://template.cue"
	if templatePathHint != "" {
		templatePath = templatePathHint
	}
	// Mutual exclusion: cannot provide both inline source and a registry ref.
	if ref != nil && source != "" {
		return "", "", restate.TerminalError(fmt.Errorf("provide either template or templateRef, not both"), 400)
	}
	// If a registry reference is provided, fetch the stored source from the
	// TemplateRegistryObj's GetSource shared handler.
	if ref != nil {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			return "", "", restate.TerminalError(fmt.Errorf("templateRef.name is required"), 400)
		}
		registrySource, err := restate.Object[string](ctx, registry.TemplateRegistryServiceName, name, "GetSource").Request(restate.Void{})
		if err != nil {
			return "", "", err
		}
		source = registrySource
		templatePath = fmt.Sprintf("registry://%s", name)
	}
	if source == "" {
		return "", "", restate.TerminalError(fmt.Errorf("template content is required"), 400)
	}
	return source, templatePath, nil
}

// loadAllPolicies gathers CUE policies from both global scope and
// (if a template reference is provided) template scope. Both sets are
// merged and passed to the CUE engine for enforcement during evaluation.
//
// Policy loading uses Restate request-response calls to the PolicyRegistryObj,
// so results are journaled and deterministic on replay.
func (s *PraxisCommandService) loadAllPolicies(ctx restate.Context, ref *types.TemplateRef) ([]template.PolicySource, error) {
	// Always load global policies.
	globalRecord, err := restate.Object[types.PolicyRecord](
		ctx,
		registry.PolicyRegistryServiceName,
		registry.PolicyScopeKey(types.PolicyScopeGlobal, ""),
		"GetPolicies",
	).Request(restate.Void{})
	if err != nil {
		return nil, err
	}

	policies := policySources(globalRecord)
	if ref == nil {
		return policies, nil
	}

	// If a template reference is present, also load template-scoped policies.
	// These are appended after global policies so they evaluate in order.
	templateRecord, err := restate.Object[types.PolicyRecord](
		ctx,
		registry.PolicyRegistryServiceName,
		registry.PolicyScopeKey(types.PolicyScopeTemplate, ref.Name),
		"GetPolicies",
	).Request(restate.Void{})
	if err != nil {
		return nil, err
	}

	return append(policies, policySources(templateRecord)...), nil
}

// policySources converts a PolicyRecord (durable storage format) into the
// []PolicySource format expected by the CUE template engine.
func policySources(record types.PolicyRecord) []template.PolicySource {
	if len(record.Policies) == 0 {
		return nil
	}
	out := make([]template.PolicySource, 0, len(record.Policies))
	for _, policy := range record.Policies {
		out = append(out, template.PolicySource{
			Name:   policy.Name,
			Source: []byte(policy.Source),
		})
	}
	return out
}

// buildResourceNodes parses each rendered resource spec into a ResourceNode.
// For each resource, it:
//  1. Extracts the "kind" field to identify the provider adapter.
//  2. Calls adapter.BuildKey to construct the canonical resource key.
//  3. Parses inter-resource dependency expressions (${resource.x.outputs.y}).
//  4. Extracts optional lifecycle rules (ignore_changes).
//
// Resources are processed in sorted name order for deterministic output.
// Returns TerminalError if any resource has an unknown kind, malformed spec,
// or circular expression references.
func (s *PraxisCommandService) buildResourceNodes(specs map[string]json.RawMessage) ([]*types.ResourceNode, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("template rendered no resources; ensure the template defines a top-level 'resources' block with at least one resource")
	}

	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)

	nodes := make([]*types.ResourceNode, 0, len(names))
	for _, name := range names {
		raw := specs[name]
		kind, err := resourceKind(raw)
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", name, err)
		}

		adapter, err := s.providers.Get(kind)
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", name, err)
		}

		key, err := adapter.BuildKey(raw)
		if err != nil {
			return nil, fmt.Errorf("resource %q: build canonical key: %w", name, err)
		}

		deps, exprs, err := dag.ParseDependencies(name, raw)
		if err != nil {
			return nil, fmt.Errorf("resource %q: parse dependencies: %w", name, err)
		}

		lifecycle, err := parseLifecycle(raw)
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", name, err)
		}

		nodes = append(nodes, &types.ResourceNode{
			Name:         name,
			Kind:         kind,
			Key:          key,
			Spec:         raw,
			Dependencies: deps,
			Expressions:  exprs,
			Lifecycle:    lifecycle,
		})
	}

	return nodes, nil
}

// planResourcesFromGraph converts ResourceNodes into PlanResource values
// ordered by the DAG's topological sort. The topological order ensures that
// when the DeploymentWorkflow processes resources sequentially, each resource
// is created after its dependencies are ready.
func planResourcesFromGraph(nodes []*types.ResourceNode, graph *dag.Graph) []orchestrator.PlanResource {
	if len(nodes) == 0 {
		return nil
	}

	byName := make(map[string]*types.ResourceNode, len(nodes))
	for _, node := range nodes {
		byName[node.Name] = node
	}

	resources := make([]orchestrator.PlanResource, 0, len(nodes))
	for _, name := range graph.TopologicalOrder() {
		node := byName[name]
		resources = append(resources, orchestrator.PlanResource{
			Name:          node.Name,
			Kind:          node.Kind,
			DriverService: node.Kind,
			Key:           node.Key,
			Spec:          node.Spec,
			Dependencies:  append([]string(nil), node.Dependencies...),
			Expressions:   cloneStringMap(node.Expressions),
			Lifecycle:     node.Lifecycle,
		})
	}

	return resources
}

// deriveDeploymentKey generates a deployment key from the first resource's
// kind and metadata.name when the user doesn't provide an explicit key.
// For multi-resource templates, "-stack" is appended to indicate the
// deployment manages multiple resources.
//
// Examples:
//   - Single resource: "aws-s3-bucket-my-bucket"
//   - Multi-resource:  "aws-s3-bucket-my-bucket-stack"
func deriveDeploymentKey(specs map[string]json.RawMessage) (string, error) {
	if len(specs) == 0 {
		return "", fmt.Errorf("cannot derive deployment key from an empty template")
	}

	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)

	var doc renderedResourceDocument
	if err := json.Unmarshal(specs[names[0]], &doc); err != nil {
		return "", fmt.Errorf("decode deployment identity from %q: %w", names[0], err)
	}

	baseName := strings.TrimSpace(doc.Metadata.Name)
	if baseName == "" {
		baseName = names[0]
	}

	parts := []string{normalizeIdentifier(doc.Kind), normalizeIdentifier(baseName)}
	if len(specs) > 1 {
		parts = append(parts, "stack")
	}

	key := strings.Join(filterEmpty(parts), "-")
	key = strings.Trim(key, "-")
	if key == "" {
		return "", fmt.Errorf("failed to derive a usable deployment key from rendered template metadata")
	}
	return key, nil
}

// renderResolvedTemplate builds a human-readable JSON document showing the
// fully-resolved template. Sensitive values (from SSM parameters) are masked
// with "***". Data source outputs are included under a "data" key.
// This is the "Rendered" field returned in Plan/PlanDeploy responses and
// displayed by the CLI.
func renderResolvedTemplate(dataSources map[string]types.DataSourceResult, specs map[string]json.RawMessage, sensitive *resolver.SensitiveParams) (string, error) {
	resourceNames := make([]string, 0, len(specs))
	for name := range specs {
		resourceNames = append(resourceNames, name)
	}
	sort.Strings(resourceNames)

	renderedResources := make(map[string]any, len(specs))
	for _, name := range resourceNames {
		var decoded any
		if err := json.Unmarshal(specs[name], &decoded); err != nil {
			return "", fmt.Errorf("decode rendered resource %q: %w", name, err)
		}
		masked, err := maskSensitiveValues(name, decoded, sensitive)
		if err != nil {
			return "", fmt.Errorf("mask sensitive values for %q: %w", name, err)
		}
		renderedResources[name] = masked
	}

	rendered := map[string]any{"resources": renderedResources}
	if len(dataSources) > 0 {
		names := make([]string, 0, len(dataSources))
		for name := range dataSources {
			names = append(names, name)
		}
		sort.Strings(names)

		renderedDataSources := make(map[string]any, len(dataSources))
		for _, name := range names {
			result := dataSources[name]
			renderedDataSources[name] = map[string]any{
				"kind":    result.Kind,
				"outputs": result.Outputs,
			}
		}
		rendered["data"] = renderedDataSources
	}

	return marshalPrettyJSON(rendered)
}

// maskSensitiveValues replaces SSM-resolved secret values with "***" in the
// rendered output. It uses the SensitiveParams paths (recorded during SSM
// resolution) to locate and mask the exact JSON paths containing secrets.
func maskSensitiveValues(resourceName string, value any, sensitive *resolver.SensitiveParams) (any, error) {
	if sensitive == nil || len(sensitive.Paths) == 0 {
		return value, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal resource for masking: %w", err)
	}

	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decode resource for masking: %w", err)
	}

	for fullPath := range sensitive.Paths {
		prefix := resourceName + "."
		if !strings.HasPrefix(fullPath, prefix) {
			continue
		}
		resourcePath := strings.TrimPrefix(fullPath, prefix)
		updated, err := jsonpath.Set(decoded, resourcePath, "***")
		if err != nil {
			return nil, err
		}
		decoded = updated
	}

	return decoded, nil
}

// resourceKind extracts the "kind" field from a raw JSON resource spec.
// This is used during node construction to look up the correct provider adapter.
func resourceKind(raw json.RawMessage) (string, error) {
	var doc renderedResourceDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("decode rendered resource: %w", err)
	}
	if strings.TrimSpace(doc.Kind) == "" {
		return "", fmt.Errorf("resource kind is required")
	}
	return doc.Kind, nil
}

// parseLifecycle extracts the optional lifecycle block from a rendered resource
// document. Returns nil if the resource does not declare lifecycle rules.
func parseLifecycle(raw json.RawMessage) (*types.LifecyclePolicy, error) {
	var doc struct {
		Lifecycle *types.LifecyclePolicy `json:"lifecycle"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode lifecycle: %w", err)
	}
	return doc.Lifecycle, nil
}

// cloneStringMap creates a shallow copy of a string-to-string map.
// Returns nil for empty inputs to avoid allocating empty maps.
func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	maps.Copy(cloned, input)
	return cloned
}

// normalizeIdentifier converts an arbitrary string into a URL-safe,
// lowercase, dash-separated identifier. Non-alphanumeric characters become
// dashes, and consecutive dashes are collapsed. Used by deriveDeploymentKey
// to transform kind + name into a valid deployment key.
func normalizeIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

// filterEmpty removes blank strings from a slice. Used during deployment
// key construction to avoid double-dashes from empty segments.
func filterEmpty(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

// checkSubmitGuard validates that a deployment is not in a state that prevents
// submitting a new apply. Returns a TerminalError if the deployment is
// currently Deleting, Running, or Pending.
func checkSubmitGuard(deploymentKey string, status types.DeploymentStatus) error {
	switch status {
	case types.DeploymentDeleting:
		return restate.TerminalError(
			fmt.Errorf("deployment %q is currently deleting; wait for deletion to complete or run 'praxis observe Deployment/%s'", deploymentKey, deploymentKey), 409)
	case types.DeploymentRunning, types.DeploymentPending:
		return restate.TerminalError(
			fmt.Errorf("deployment %q is currently %s; wait for completion, cancel, or delete before re-applying", deploymentKey, status), 409)
	default:
		return nil
	}
}
