package command

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/dag"
	"github.com/praxiscloud/praxis/internal/core/jsonpath"
	"github.com/praxiscloud/praxis/internal/core/orchestrator"
	"github.com/praxiscloud/praxis/internal/core/registry"
	"github.com/praxiscloud/praxis/internal/core/resolver"
	"github.com/praxiscloud/praxis/internal/core/template"
	"github.com/praxiscloud/praxis/pkg/types"
)

type renderedResourceDocument struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

type compiledTemplate struct {
	TemplatePath  string
	Specs         map[string]json.RawMessage
	Nodes         []*types.ResourceNode
	Graph         *dag.Graph
	PlanResources []orchestrator.PlanResource
	Rendered      string
	Sensitive     *resolver.SensitiveParams
}

// compileTemplate runs the shared command pipeline up to the point where a
// handler can either submit a workflow or build a dry-run plan.
func (s *PraxisCommandService) compileTemplate(
	ctx restate.Context,
	templateBody string,
	ref *types.TemplateRef,
	variables map[string]any,
	accountName string,
) (*compiledTemplate, error) {
	source, templatePath, err := s.resolveTemplateSource(ctx, templateBody, ref)
	if err != nil {
		return nil, err
	}

	policies, err := s.loadAllPolicies(ctx, ref)
	if err != nil {
		return nil, err
	}

	rawSpecs, err := s.engine.EvaluateBytesWithPolicies([]byte(source), policies, variables)
	if err != nil {
		return nil, restate.TerminalError(err, 400)
	}

	ssmResolver, err := s.newSSMResolver(accountName)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}

	ssmResolvedSpecs, sensitive, err := ssmResolver.Resolve(ctx, rawSpecs)
	if err != nil {
		return nil, restate.TerminalError(err, 400)
	}

	nodes, err := s.buildResourceNodes(ssmResolvedSpecs)
	if err != nil {
		return nil, restate.TerminalError(err, 400)
	}

	graph, err := dag.NewGraph(nodes)
	if err != nil {
		return nil, restate.TerminalError(fmt.Errorf("invalid deployment graph: %w", err), 400)
	}

	rendered, err := renderResolvedTemplate(ssmResolvedSpecs, sensitive)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}

	return &compiledTemplate{
		TemplatePath:  templatePath,
		Specs:         ssmResolvedSpecs,
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
func (s *PraxisCommandService) submitDeployment(
	ctx restate.Context,
	deploymentKey string,
	account string,
	variables map[string]any,
	compiled *compiledTemplate,
) (string, types.DeploymentStatus, error) {
	existingState, err := restate.Object[*orchestrator.DeploymentState](
		ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "GetState",
	).Request(restate.Void{})
	if err != nil {
		return "", "", err
	}
	if existingState != nil && existingState.Status == types.DeploymentDeleting {
		return "", "", restate.TerminalError(
			fmt.Errorf("deployment %q is currently deleting", deploymentKey), 409)
	}

	createdAt, err := restate.Run(ctx, func(runCtx restate.RunContext) (time.Time, error) {
		return time.Now().UTC(), nil
	})
	if err != nil {
		return "", "", err
	}

	plan := orchestrator.DeploymentPlan{
		Key:          deploymentKey,
		Account:      account,
		Resources:    compiled.PlanResources,
		Variables:    variables,
		CreatedAt:    createdAt,
		TemplatePath: compiled.TemplatePath,
	}

	generation, err := restate.WithRequestType[orchestrator.DeploymentPlan, int64](
		restate.Object[int64](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "InitDeployment"),
	).Request(plan)
	if err != nil {
		return "", "", err
	}

	_, err = restate.WithRequestType[types.DeploymentSummary, restate.Void](
		restate.Object[restate.Void](ctx, orchestrator.DeploymentIndexServiceName, orchestrator.DeploymentIndexGlobalKey, "Upsert"),
	).Request(types.DeploymentSummary{
		Key:       deploymentKey,
		Status:    types.DeploymentPending,
		Resources: len(plan.Resources),
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})
	if err != nil {
		return "", "", err
	}

	_, err = restate.WithRequestType[orchestrator.DeploymentEvent, restate.Void](
		restate.Object[restate.Void](ctx, orchestrator.DeploymentEventsServiceName, deploymentKey, "Append"),
	).Request(orchestrator.DeploymentEvent{
		DeploymentKey: deploymentKey,
		Status:        types.DeploymentPending,
		Message:       "apply request accepted",
		CreatedAt:     createdAt,
	})
	if err != nil {
		return "", "", err
	}

	workflowID := fmt.Sprintf("%s-gen-%d", deploymentKey, generation)
	restate.WorkflowSend(ctx, orchestrator.DeploymentWorkflowServiceName, workflowID, "Run").Send(
		plan,
		restate.WithIdempotencyKey(workflowID),
	)

	return deploymentKey, types.DeploymentPending, nil
}

func (s *PraxisCommandService) resolveTemplateSource(ctx restate.Context, templateBody string, ref *types.TemplateRef) (string, string, error) {
	source := trimTemplate(templateBody)
	templatePath := "inline://template.cue"
	if ref != nil && source != "" {
		return "", "", restate.TerminalError(fmt.Errorf("provide either template or templateRef, not both"), 400)
	}
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

func (s *PraxisCommandService) loadAllPolicies(ctx restate.Context, ref *types.TemplateRef) ([]template.PolicySource, error) {
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

func (s *PraxisCommandService) buildResourceNodes(specs map[string]json.RawMessage) ([]*types.ResourceNode, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("template rendered no resources")
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

		nodes = append(nodes, &types.ResourceNode{
			Name:         name,
			Kind:         kind,
			Key:          key,
			Spec:         raw,
			Dependencies: deps,
			Expressions:  exprs,
		})
	}

	return nodes, nil
}

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
		})
	}

	return resources
}

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

func renderResolvedTemplate(specs map[string]json.RawMessage, sensitive *resolver.SensitiveParams) (string, error) {
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

	return marshalPrettyJSON(map[string]any{"resources": renderedResources})
}

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

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

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

func filterEmpty(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			filtered = append(filtered, value)
		}
	}
	return filtered
}
