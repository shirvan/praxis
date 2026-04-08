// SecurityGroup provider adapter.
//
// This file implements the provider.Adapter interface for Amazon EC2 (Security Groups)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the SecurityGroup Restate Virtual Object driver.
//
// Key scope: custom (VPC-scoped).
// Key parts: VPC ID + group name.
// Security groups are scoped to a VPC, so the key combines VPC ID and group name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// Scope returns the key-scope strategy for SecurityGroup resources,
// which controls how BuildKey assembles the canonical object key.
func (a *SecurityGroupAdapter) Scope() KeyScope {
	return KeyScopeCustom
}

// SecurityGroupAdapter implements provider.Adapter for SecurityGroup (Amazon EC2 (Security Groups)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type SecurityGroupAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI sg.SGAPI
	apiFactory        func(aws.Config) sg.SGAPI
}

// NewSecurityGroupAdapterWithAuth creates a production SecurityGroup adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewSecurityGroupAdapterWithAuth(auth authservice.AuthClient) *SecurityGroupAdapter {
	return &SecurityGroupAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) sg.SGAPI {
			return sg.NewSGAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

// NewSecurityGroupAdapterWithAPI injects a fixed EC2 planning API. This is primarily
// useful in tests that do not need per-account planning behavior.
func NewSecurityGroupAdapterWithAPI(api sg.SGAPI) *SecurityGroupAdapter {
	return &SecurityGroupAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "SecurityGroup" that maps template
// resource documents to this adapter in the provider registry.
func (a *SecurityGroupAdapter) Kind() string {
	return sg.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// SecurityGroup driver. The orchestrator uses this to dispatch durable RPCs.
func (a *SecurityGroupAdapter) ServiceName() string {
	return sg.ServiceName
}

// BuildKey derives the canonical Restate object key for a SecurityGroup resource
// from the raw JSON resource document. The key is composed of VPC ID + group name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *SecurityGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("VPC ID", spec.VpcId); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("group name", spec.GroupName); err != nil {
		return "", err
	}
	return JoinKey(spec.VpcId, spec.GroupName), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete SecurityGroup spec struct expected by the driver.
func (a *SecurityGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the SecurityGroup Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *SecurityGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[sg.SecurityGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		restate.Object[sg.SecurityGroupOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[sg.SecurityGroupOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

// Delete sends a durable Delete request to the SecurityGroup Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *SecurityGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{
		id:  fut.GetInvocationId(),
		raw: fut,
	}, nil
}

// NormalizeOutputs converts the typed SecurityGroup driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *SecurityGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[sg.SecurityGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"groupId":  out.GroupId,
		"groupArn": out.GroupArn,
		"vpcId":    out.VpcId,
	}, nil
}

// Plan compares the desired SecurityGroup spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *SecurityGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[sg.SecurityGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	// describePlanResult packages the describe response so that "not found" is
	// a successful journal entry rather than a retried error.
	type describePlanResult struct {
		State sg.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		out, descErr := planningAPI.FindSecurityGroup(runCtx, desired.GroupName, desired.VpcId)
		if descErr != nil {
			if sg.IsNotFound(descErr) {
				return describePlanResult{Found: false}, nil
			}
			return describePlanResult{}, restate.TerminalError(descErr, 500)
		}
		return describePlanResult{State: out, Found: true}, nil
	})
	if err != nil {
		return "", nil, err
	}
	if !result.Found {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}
	observed := result.State

	rawDiffs := sg.ComputeFieldDiffs(desired, observed)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{
			Path:     diff.Path,
			OldValue: diff.OldValue,
			NewValue: diff.NewValue,
		})
	}
	return types.OpUpdate, fields, nil
}

// BuildImportKey derives the canonical Restate object key for importing
// an existing SecurityGroup resource by its region and provider-native ID.
func (a *SecurityGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

// Import adopts an existing SecurityGroup resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *SecurityGroupAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, sg.SecurityGroupOutputs](
		restate.Object[sg.SecurityGroupOutputs](ctx, a.ServiceName(), key, "Import"),
	).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

// Lookup performs a read-only data-source query for an existing SecurityGroup
// resource, matching by ID, name, or tags. This is used by template data
// source blocks to resolve references to pre-existing infrastructure.
func (a *SecurityGroupAdapter) Lookup(ctx restate.Context, account string, filter LookupFilter) (map[string]any, error) {
	api, err := a.lookupAPI(ctx, account, filter.Region)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	observed, err := restate.Run(ctx, func(runCtx restate.RunContext) (sg.ObservedState, error) {
		obs, runErr := lookupSecurityGroup(runCtx, api, filter)
		if runErr != nil {
			return obs, classifyLookupError(runErr, sg.IsNotFound)
		}
		return obs, nil
	})
	if err != nil {
		return nil, err
	}
	if !matchesSecurityGroupFilter(observed, filter) {
		return nil, restate.TerminalError(fmt.Errorf("data source lookup: no SecurityGroup found matching filter"), 404)
	}
	outputs, err := a.NormalizeOutputs(sg.SecurityGroupOutputs{
		GroupId:  observed.GroupId,
		GroupArn: securityGroupARN(filter.Region, observed.OwnerId, observed.GroupId),
		VpcId:    observed.VpcId,
	})
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	return outputs, nil
}

// DefaultTimeouts provides per-kind default timeouts for Security Groups.
func (a *SecurityGroupAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "5m", Update: "5m", Delete: "5m"}
}

// Observe performs a lightweight live check to determine whether the Security
// Group exists and matches the desired spec. Implements the Observer interface.
func (a *SecurityGroupAdapter) Observe(ctx restate.Context, key string, account string, spec any) (ObserveResult, error) {
	desired, err := castSpec[sg.SecurityGroupSpec](spec)
	if err != nil {
		return ObserveResult{}, err
	}
	outputs, getErr := restate.Object[sg.SecurityGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").
		Request(restate.Void{})
	if getErr != nil || outputs.GroupId == "" {
		return ObserveResult{Exists: false}, nil
	}
	api, err := a.planningAPI(ctx, account)
	if err != nil {
		return ObserveResult{}, err
	}
	type describeResult struct {
		State sg.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(rc restate.RunContext) (describeResult, error) {
		obs, descErr := api.DescribeSecurityGroup(rc, outputs.GroupId)
		if descErr != nil {
			if sg.IsNotFound(descErr) {
				return describeResult{Found: false}, nil
			}
			return describeResult{}, descErr
		}
		return describeResult{State: obs, Found: true}, nil
	})
	if err != nil {
		return ObserveResult{}, err
	}
	if !result.Found {
		return ObserveResult{Exists: false}, nil
	}
	upToDate := !sg.HasDrift(desired, result.State)
	normalizedOutputs, _ := a.NormalizeOutputs(outputs)
	return ObserveResult{Exists: true, UpToDate: upToDate, Outputs: normalizedOutputs}, nil
}

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed SecurityGroup spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *SecurityGroupAdapter) decodeSpec(doc resourceDocument) (sg.SecurityGroupSpec, error) {
	var spec sg.SecurityGroupSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return sg.SecurityGroupSpec{}, fmt.Errorf("decode SecurityGroup spec: %w", err)
	}
	if strings.TrimSpace(spec.GroupName) == "" {
		return sg.SecurityGroupSpec{}, fmt.Errorf("SecurityGroup spec.groupName is required")
	}
	spec.Account = ""
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *SecurityGroupAdapter) planningAPI(ctx restate.Context, account string) (sg.SGAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("SecurityGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SecurityGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

// lookupAPI returns the AWS API client used for Lookup (data-source) queries.
// Like planningAPI, it resolves credentials per-account, but also overrides
// the region when the lookup filter specifies one.
func (a *SecurityGroupAdapter) lookupAPI(ctx restate.Context, account string, region string) (sg.SGAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("SecurityGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SecurityGroup planning account %q: %w", account, err)
	}
	if strings.TrimSpace(region) != "" {
		awsCfg.Region = strings.TrimSpace(region)
	}
	return a.apiFactory(awsCfg), nil
}

func lookupSecurityGroup(ctx restate.RunContext, api sg.SGAPI, filter LookupFilter) (sg.ObservedState, error) {
	if strings.TrimSpace(filter.ID) != "" {
		return api.DescribeSecurityGroup(ctx, strings.TrimSpace(filter.ID))
	}
	tags := lookupTags(filter)
	if len(tags) == 0 {
		return sg.ObservedState{}, fmt.Errorf("SecurityGroup lookup requires at least one of: id, name, tag")
	}
	id, err := api.FindByTags(ctx, tags)
	if err != nil {
		return sg.ObservedState{}, err
	}
	if strings.TrimSpace(id) == "" {
		return sg.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeSecurityGroup(ctx, id)
}

func matchesSecurityGroupFilter(observed sg.ObservedState, filter LookupFilter) bool {
	if strings.TrimSpace(filter.ID) != "" && observed.GroupId != strings.TrimSpace(filter.ID) {
		return false
	}
	if strings.TrimSpace(filter.Name) != "" && observed.Tags["Name"] != strings.TrimSpace(filter.Name) {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}

func securityGroupARN(region, ownerID, groupID string) string {
	if strings.TrimSpace(region) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(groupID) == "" {
		return ""
	}
	return fmt.Sprintf("arn:aws:ec2:%s:%s:security-group/%s", region, ownerID, groupID)
}
