# Write Tests

**Description**: Write unit, integration, and E2E tests following Praxis patterns.

**When to Use**: Testing new drivers, adapters, templates, or any code changes.

**Prerequisites**:
- Read [docs/DEVELOPERS.md](../../docs/DEVELOPERS.md) for testing strategy
- Understand the test layer you're targeting (unit, integration, E2E)

---

## Test Layers

| Layer | Docker? | What | Where |
|-------|---------|------|-------|
| Unit | No | Pure logic, mocks | `internal/drivers/{resource}/*_test.go` |
| Integration | Yes | Real Restate + mock AWS | `tests/integration/{resource}_driver_test.go` |
| E2E | Yes | Full stack lifecycle | `tests/integration/core_test.go` |

---

## Unit Tests

### Shared Lifecycle Conformance

Every new built-in driver should adopt both reusable black-box contracts from
`internal/drivers/drivertest` using a stateful provider double and a real
Restate test environment:

- `RunCoreLifecycle` — create, identical re-provision, GetStatus/GetInputs/GetOutputs,
  Delete, double Delete, retained tombstone, and post-delete Reconcile.
- `RunObservedImportLifecycle` — read-only import baseline, no phantom drift,
  Observed Delete rejection, and read-only Reconcile.

Set `AllowNoopUpdates` only when the provider API genuinely requires a
convergent write on every identical Provision. Keep field mutation, readiness,
error, and fault-window cases in resource-specific tests.

### Driver Test (`driver_test.go`)

Test handler logic with mock API:

```go
package {resource}

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// Mock API implementing {Resource}API interface
type mock{Resource}API struct {
    createFn   func(ctx context.Context, spec {Resource}Spec) (string, error)
    describeFn func(ctx context.Context, id string) (ObservedState, error)
    deleteFn   func(ctx context.Context, id string) error
}

func (m *mock{Resource}API) Create{Resource}(ctx context.Context, spec {Resource}Spec) (string, error) {
    return m.createFn(ctx, spec)
}
// ... implement other interface methods similarly

func TestProvision_CreatesNewResource(t *testing.T) {
    // Setup mock API
    // Create driver with mock
    // Call Provision with test spec
    // Assert outputs and state
}

func TestProvision_ConvergesExistingResource(t *testing.T) {
    // Setup mock API returning existing resource
    // Call Provision
    // Assert convergence behavior
}

func TestProvision_IdempotentWhenNoChange(t *testing.T) {
    // Setup mock API returning resource matching spec
    // Call Provision
    // Assert no update calls made
}
```

### AWS Error Test (`aws_test.go`)

Test error classifiers:

```go
func TestIsNotFound(t *testing.T) {
    tests := []struct {
        name     string
        err      error
        expected bool
    }{
        {"typed not found", &types.NotFoundException{}, true},
        {"string not found", errors.New("NotFoundException: resource not found"), true},
        {"nil error", nil, false},
        {"other error", errors.New("something else"), false},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            assert.Equal(t, tt.expected, IsNotFound(tt.err))
        })
    }
}
```

### Drift Test (`drift_test.go`)

Test drift detection and field diffs:

```go
func TestHasDrift_NoChanges(t *testing.T) {
    desired := {Resource}Spec{Region: "us-east-1", Name: "test"}
    observed := ObservedState{Name: "test"}
    assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_MutableFieldChanged(t *testing.T) {
    desired := {Resource}Spec{Tags: map[string]string{"env": "prod"}}
    observed := ObservedState{Tags: map[string]string{"env": "dev"}}
    assert.True(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_TagChange(t *testing.T) {
    desired := {Resource}Spec{Tags: map[string]string{"env": "prod"}}
    observed := ObservedState{Tags: map[string]string{"env": "dev"}}
    diffs := ComputeFieldDiffs(desired, observed)
    require.Len(t, diffs, 1)
    assert.Equal(t, "tags.env", diffs[0].Path)
}
```

### Adapter Test (`{resource}_adapter_test.go`)

Test spec decoding and key building:

```go
func TestBuildKey(t *testing.T) {
    adapter := New{Resource}Adapter(nil)
    doc := json.RawMessage(`{
        "metadata": {"name": "test-resource"},
        "spec": {"region": "us-east-1"}
    }`)
    key, err := adapter.BuildKey(doc)
    require.NoError(t, err)
    assert.Equal(t, "us-east-1~test-resource", key)
}

func TestDecodeSpec(t *testing.T) {
    adapter := New{Resource}Adapter(nil)
    doc := json.RawMessage(`{
        "metadata": {"name": "test"},
        "spec": {"region": "us-east-1", "field": "value"}
    }`)
    spec, err := adapter.DecodeSpec(doc)
    require.NoError(t, err)
    typed := spec.({resource}.{Resource}Spec)
    assert.Equal(t, "us-east-1", typed.Region)
}
```

---

## Integration Tests

File: `tests/integration/{resource}_driver_test.go`

```go
//go:build integration

package integration

import (
    "testing"
    "github.com/restatedev/sdk-go/test"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func setup{Resource}Driver(t *testing.T) (*ingress.Client, *awssdk.Client) {
    configureLocalAccount(t)  // Set PRAXIS_ACCOUNT_* env vars
    awsCfg := motoAWSConfig(t)
    awsClient := awsclient.New{Service}Client(awsCfg)
    driver := {resource}.New{Resource}Driver(nil)
    env := restatetest.Start(t, restate.Reflect(driver))
    return env.Ingress(), awsClient
}

func Test{Resource}Provision_CreatesResource(t *testing.T) {
    client, awsClient := setup{Resource}Driver(t)
    
    outputs, err := ingress.Object[{resource}.{Resource}Spec, {resource}.{Resource}Outputs](
        client, "{Resource}", "us-east-1~test-resource", "Provision",
    ).Request(t.Context(), {resource}.{Resource}Spec{
        Region: "us-east-1",
        // ... spec fields
    })
    require.NoError(t, err)
    assert.NotEmpty(t, outputs.ARN)
    
    // Verify directly in mock AWS
    desc, err := awsClient.Describe{Resource}(t.Context(), &awssdk.Describe{Resource}Input{...})
    require.NoError(t, err)
    assert.NotNil(t, desc)
}

func Test{Resource}Provision_Idempotent(t *testing.T) {
    // Provision twice with same spec → same outputs
}

func Test{Resource}Delete_RemovesResource(t *testing.T) {
    // Provision, then Delete, then verify gone in AWS
}

func Test{Resource}Import_ExistingResource(t *testing.T) {
    // Create directly in AWS, import via driver
}

func Test{Resource}Reconcile_DetectsAndFixesDrift(t *testing.T) {
    // Provision, modify directly in AWS, reconcile, verify corrected
}
```

---

## Running Tests

```bash
# Unit tests (no Docker)
just test                                              # All
go test ./internal/drivers/{resource}/... -v -count=1 -race  # Specific driver

# Integration tests (requires Docker)
just test-integration                                   # All
go test ./tests/integration/{resource}_driver_test.go -v -tags=integration -timeout=5m

# E2E
just test-e2e

# Full CI
just ci
```

---

## Verification

1. All tests pass: `go test ./... -count=1`
2. No race conditions: `go test ./... -race`
3. Coverage is reasonable: `go test ./... -coverprofile=coverage.out`

## Common Pitfalls

1. **Forgetting `//go:build integration` tag**: Integration tests will run in unit test mode and fail
2. **Hardcoded resource names in integration tests**: Use unique names per test to avoid collisions
3. **Not using `t.Context()`**: Tests should use the test context for proper cleanup
4. **Missing string fallback in error classifier tests**: Test both typed and string-wrapped errors
5. **Testing mutable drift with immutable fields**: Drift detection should only compare mutable fields

## Reference Tests

- **Simple driver tests**: `internal/drivers/s3/driver_test.go`
- **Complex drift tests**: `internal/drivers/sg/drift_test.go`
- **Adapter tests**: `internal/core/provider/vpc_adapter_test.go`
- **Integration tests**: `tests/integration/vpc_driver_test.go`
