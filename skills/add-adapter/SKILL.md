# Add Adapter

**Description**: Create a provider adapter that bridges the orchestrator to a driver.

**When to Use**: After implementing a new driver, to enable orchestrator dispatch, plan diffs, and data source lookups.

**Prerequisites**:
- Driver implemented and compiling (see [implement-driver](../implement-driver/SKILL.md))
- Know the resource's key scope (Global, Region, Custom)

---

## Steps

### 1. Create Adapter File

File: `internal/core/provider/{resource}_adapter.go`

```go
package provider

import (
    "encoding/json"
    "fmt"
    "github.com/aws/aws-sdk-go-v2/aws"
    restate "github.com/restatedev/sdk-go"
    "github.com/shirvan/praxis/internal/core/auth"
    "{resource}" // your driver package
    "github.com/shirvan/praxis/pkg/types"
)

type {Resource}Adapter struct {
    auth              *auth.Registry
    staticPlanningAPI {resource}.{Resource}API
    apiFactory        func(aws.Config) {resource}.{Resource}API
}

func New{Resource}Adapter(auth *auth.Registry) *{Resource}Adapter {
    return &{Resource}Adapter{
        auth:       auth,
        apiFactory: func(cfg aws.Config) {resource}.{Resource}API {
            return {resource}.New{Resource}API(/* client from cfg */)
        },
    }
}

func New{Resource}AdapterWithRegistry(auth *auth.Registry) *{Resource}Adapter {
    return New{Resource}Adapter(auth)
}

func (a *{Resource}Adapter) Kind() string        { return {resource}.ServiceName }
func (a *{Resource}Adapter) ServiceName() string  { return {resource}.ServiceName }
func (a *{Resource}Adapter) Scope() KeyScope      { return KeyScopeRegion } // adjust

func (a *{Resource}Adapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    doc, err := decodeResourceDocument(resourceDoc)
    if err != nil { return "", err }
    spec, err := a.decodeSpec(doc)
    if err != nil { return "", err }
    if err := ValidateKeyPart("region", spec.Region); err != nil { return "", err }
    if err := ValidateKeyPart("name", doc.Metadata.Name); err != nil { return "", err }
    return JoinKey(spec.Region, doc.Metadata.Name), nil
}

func (a *{Resource}Adapter) BuildImportKey(region, resourceID string) (string, error) {
    if err := ValidateKeyPart("region", region); err != nil { return "", err }
    if err := ValidateKeyPart("resource ID", resourceID); err != nil { return "", err }
    return JoinKey(region, resourceID), nil
}

func (a *{Resource}Adapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
    doc, err := decodeResourceDocument(resourceDoc)
    if err != nil { return nil, err }
    return a.decodeSpec(doc)
}

func (a *{Resource}Adapter) decodeSpec(doc resourceDocument) ({resource}.{Resource}Spec, error) {
    var spec {resource}.{Resource}Spec
    if err := json.Unmarshal(doc.Spec, &spec); err != nil {
        return spec, fmt.Errorf("decode spec: %w", err)
    }
    if spec.Region == "" {
        return spec, fmt.Errorf("region is required")
    }
    return spec, nil
}

func (a *{Resource}Adapter) Provision(ctx restate.Context, key, account string, spec any) (ProvisionInvocation, error) {
    typedSpec, err := castSpec[{resource}.{Resource}Spec](spec)
    if err != nil { return nil, err }
    typedSpec.Account = account
    
    fut := restate.WithRequestType[{resource}.{Resource}Spec, {resource}.{Resource}Outputs](
        restate.Object[{resource}.{Resource}Outputs](ctx, a.ServiceName(), key, "Provision"),
    ).RequestFuture(typedSpec)
    
    return &provisionHandle[{resource}.{Resource}Outputs]{
        id:        fut.GetInvocationId(),
        raw:       fut,
        normalize: a.NormalizeOutputs,
    }, nil
}

func (a *{Resource}Adapter) Delete(ctx restate.Context, key, account string) (DeleteInvocation, error) {
    fut := restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete").RequestFuture(restate.Void{})
    return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *{Resource}Adapter) NormalizeOutputs(raw any) (map[string]any, error) {
    out, err := castOutput[{resource}.{Resource}Outputs](raw)
    if err != nil { return nil, err }
    return map[string]any{
        "arn":        out.ARN,
        "resourceId": out.ResourceId,
        // Add all output fields that templates can reference
    }, nil
}

func (a *{Resource}Adapter) Plan(ctx restate.Context, key, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
    desired, err := castSpec[{resource}.{Resource}Spec](desiredSpec)
    if err != nil { return types.OpCreate, nil, err }
    
    api, err := a.planningAPI(account)
    if err != nil { return types.OpCreate, nil, err }
    
    // Describe existing resource
    result, err := restate.Run(ctx, func(rc restate.RunContext) (describePlanResult, error) {
        obs, err := api.Describe{Resource}(rc, /* id from key */)
        if err != nil {
            if {resource}.IsNotFound(err) {
                return describePlanResult{Found: false}, nil
            }
            return describePlanResult{}, restate.TerminalError(err, 500)
        }
        return describePlanResult{State: obs, Found: true}, nil
    })
    if err != nil { return types.OpCreate, nil, err }
    
    if !result.Found {
        diffs, _ := createFieldDiffsFromSpec(desiredSpec)
        return types.OpCreate, diffs, nil
    }
    
    rawDiffs := {resource}.ComputeFieldDiffs(desired, result.State)
    if len(rawDiffs) == 0 {
        return types.OpNoOp, nil, nil
    }
    
    fieldDiffs := make([]types.FieldDiff, len(rawDiffs))
    for i, d := range rawDiffs {
        fieldDiffs[i] = types.FieldDiff{Path: d.Path, OldValue: d.OldValue, NewValue: d.NewValue}
    }
    return types.OpUpdate, fieldDiffs, nil
}
```

### 2. Register in Registry

File: `internal/core/provider/registry.go`

Add to `NewRegistry()`:
```go
New{Resource}AdapterWithRegistry(accounts),
```

### 3. Create Adapter Tests

File: `internal/core/provider/{resource}_adapter_test.go`

```go
func TestBuildKey_{Resource}(t *testing.T) { /* test key construction */ }
func TestDecodeSpec_{Resource}(t *testing.T) { /* test spec decoding */ }
func TestBuildKey_{Resource}_MissingRegion(t *testing.T) { /* error case */ }
```

---

## Key Scope Selection

| Scope | When | Key Format | Example |
|-------|------|------------|---------|
| `KeyScopeGlobal` | Globally unique name (S3) | `name` | `my-bucket` |
| `KeyScopeRegion` | Region + name unique | `region~name` | `us-east-1~my-vpc` |
| `KeyScopeCustom` | Custom compound key | varies | `vpc-123~my-sg` |

## Verification

1. `go test ./internal/core/provider/... -run {Resource} -v`
2. `go build ./cmd/praxis-core/...` — compiles with new adapter
3. Plan works: `praxis plan -f template.cue` shows your resource

## Common Pitfalls

1. **Forgetting to register in `NewRegistry()`**: Adapter won't be found
2. **Wrong NormalizeOutputs fields**: Templates can only reference fields returned here
3. **Missing Account injection**: `typedSpec.Account = account` before dispatch
4. **Key mismatch between driver and adapter**: Must produce identical keys
