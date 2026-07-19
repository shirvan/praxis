# Review Code

**Description**: Code review guidelines for Praxis contributions—what to check, conventions to enforce, and common issues.

**When to Use**: Reviewing a PR, validating your own code before committing, or auditing existing code.

**Prerequisites**:
- Read [docs/PRAXIS_ARCHITECTURE.md](../../docs/PRAXIS_ARCHITECTURE.md) for design patterns
- Read [docs/DRIVERS.md](../../docs/DRIVERS.md) for driver conventions

---

## Review Dimensions

### 1. Correctness

- [ ] Does the code do what it claims?
- [ ] Are edge cases handled (nil, empty, not-found, already-exists)?
- [ ] Are AWS errors classified correctly (terminal vs retryable)?
- [ ] Is Restate context used properly (no goroutines, deterministic operations)?

### 2. Idempotency

Every Praxis handler can be re-invoked by Restate. Check:

- [ ] `Provision` re-creates or updates without error if already exists
- [ ] `Delete` returns nil if already deleted
- [ ] No side effects in `GetStatus` / `GetOutputs`
- [ ] State is set before returning (crash between API call and state set = safe retry)

### 3. Error Handling

```go
// GOOD: Use error classifiers
return classifyError(err, "CreateBucket", spec.BucketName)

// BAD: Return raw AWS errors
return fmt.Errorf("failed: %w", err)
```

- [ ] All AWS API errors pass through a classifier
- [ ] Terminal errors use `restate.TerminalError()` inside the `drivers.RunAWS()` callback
- [ ] Retryable errors propagate unwrapped (Restate auto-retries)
- [ ] Error messages include the operation name and resource identifier

### 4. State Management

- [ ] State keys use constants, not string literals
- [ ] `restate.Set(ctx, ...)` called for all outputs after API success
- [ ] `restate.Clear(ctx, ...)` called during Delete
- [ ] No redundant state reads (prefer local variables within a handler)

### 5. Driver Convention Compliance

| Convention | Check |
|-----------|-------|
| File layout | `generic.go`, `aws.go`, `drift.go`, `types.go`, plus focused tests |
| Adapter file | `{resource}_adapter.go` under `internal/core/provider/` |
| Key scope | `{deployment}/{resource}` |
| Handler contract | Exactly 8 required handlers; no extra public lifecycle hooks |
| Lifecycle implementation | Shared `kernel.Driver`; resource packages supply only typed operations and capabilities |
| Production binding | `genericbinding.Reflect` only |
| Core adapter | Embedded `GenericAdapter`; no copied dispatch or planning lifecycle |
| Package naming | Singular resource name (`bucket`, `instance`, not `buckets`) |

### 6. CUE Schema

- [ ] Schema uses `#ResourceName` definition
- [ ] Required fields have no `?` marker
- [ ] Optional fields use `?`
- [ ] Enum values use `"a" \| "b"` disjunctions
- [ ] `apiVersion` and `kind` are literal constants

### 7. Test Coverage

- [ ] Driver unit tests: provision, delete, get-status, reconcile flows
- [ ] AWS error classification tests: covers terminal + retryable cases
- [ ] Drift detection tests: no-drift + drift-detected each field
- [ ] Adapter unit tests: BuildKey, DecodeSpec, NormalizeOutputs

### 8. Code Style

- Named return values only when useful for documentation
- No `else` after `return` (early-return style)
- Table-driven tests with `t.Run`
- Structured logging via `slog`
- No `context.TODO()` or `context.Background()` in handlers (use Restate context)
- Imports: stdlib → external → internal (grouped with blank lines)

---

## Common Issues

### Drift Detection

```go
// GOOD: Compare specific fields
if aws.ToString(remote.Name) != state.Name {
    diffs = append(diffs, types.DriftField{...})
}

// BAD: reflect.DeepEqual on entire structs (brittle, false positives)
if !reflect.DeepEqual(remote, state) { ... }
```

### AWS Client Initialization

```go
// GOOD: Accepted pattern
cfg, err := config.LoadDefaultConfig(ctx.Ctx())
client := s3.NewFromConfig(cfg)

// BAD: Global clients (not safe with multiple accounts)
var globalClient *s3.Client
```

### Restate Anti-Patterns

```go
// BAD: Goroutines break Restate's deterministic replay
go func() { doSomething(ctx) }()

// BAD: time.Now() is non-deterministic
createdAt := time.Now()

// GOOD: Use Restate context methods for side effects
result, err := restate.Run(ctx, func(runCtx restate.RunContext) (T, error) {
    return callExternalAPI()
})
```

### Tag Handling

```go
// GOOD: Include Praxis metadata tags + user tags
tags := mergeTags(spec.Tags, map[string]string{
    "praxis:deployment": deploymentName,
    "praxis:resource":   resourceName,
})

// BAD: Only user tags (breaks drift tracking)
tags := spec.Tags
```

---

## PR Checklist Template

```markdown
## Checklist
- [ ] All 8 required driver handlers implemented
- [ ] Error classifiers cover known AWS errors
- [ ] Drift detection covers all mutable spec fields
- [ ] Unit tests pass (`just test-unit`)
- [ ] Integration tests pass (`just test-integration`)
- [ ] CUE schema validates example templates
- [ ] No goroutines or non-deterministic calls in Restate handlers
- [ ] Adapter registered in driver pack binary
```

## See Also

- [docs/DEVELOPERS.md](../../docs/DEVELOPERS.md) — Build and test instructions
- [docs/ERRORS.md](../../docs/ERRORS.md) — Error classification reference
- [docs/DRIVERS.md](../../docs/DRIVERS.md) — Driver contract
