# Debug Deployment

**Description**: Diagnose and fix deployment failures, resource errors, and drift issues.

**When to Use**: A deployment is failing, resources are stuck, or drift is not being corrected.

**Prerequisites**:
- Read [docs/ERRORS.md](../../docs/ERRORS.md) for error classification
- Read [docs/ORCHESTRATOR.md](../../docs/ORCHESTRATOR.md) for deployment lifecycle

---

## Diagnostic Steps

### 1. Check Deployment Status

```bash
praxis get deployment DEPLOYMENT_NAME
praxis get deployment DEPLOYMENT_NAME -o json   # detailed JSON
```

Look for:
- **Deployment status**: Pending, Running, Complete, Failed, Deleting, Deleted, Cancelled
- **Per-resource status**: Ready, Error, Skipped, Provisioning

### 2. Check Events

```bash
praxis observe DEPLOYMENT_NAME                        # real-time stream
praxis list events DEPLOYMENT_NAME                    # event history
praxis list events DEPLOYMENT_NAME --type resource.error  # just errors
```

### 3. Check Logs

```bash
just logs                     # all services
just logs-core                # orchestrator + command service
just logs-storage             # storage driver pack
just logs-network             # network driver pack
```

Look for `slog` structured log entries with error details.

### 4. Check Restate State

```bash
# List all registered services
curl http://localhost:9070/services

# Check specific invocations
curl http://localhost:9070/invocations
```

---

## Common Failure Patterns

### Resource Error: "BucketAlreadyExists" / "ResourceConflict"
**Cause**: Resource name conflict in AWS.
**Fix**: Change `metadata.name` in template or use `--auto-replace`.

### Resource Error: "InvalidParameterValue"
**Cause**: Bad spec field value.
**Fix**: Check CUE schema constraints in `schemas/aws/{resource}/`, fix spec values.

### Resource Skipped: "depends on failed resource"
**Cause**: Upstream dependency failed, dependent resources skipped.
**Fix**: Fix the root cause (the failed resource), then re-deploy.

### Deployment Stuck "Running"
**Cause**: Restate invocation still in progress or retrying.
**Fix**: Check Restate admin for paused invocations. May need to cancel: `praxis delete DEPLOYMENT --force`.

### 409 Conflict on Deploy
**Cause**: Deployment is currently being deleted.
**Fix**: Wait for deletion to complete, then re-deploy.

### Drift Not Correcting
**Cause**: Resource in Observed mode (imported with `--observe`).
**Fix**: Re-import without `--observe` flag to switch to Managed mode.

### "Unknown account" Error
**Cause**: Account not configured.
**Fix**: Set `PRAXIS_ACCOUNT_*` environment variables on the service container.

### Template Validation Error
**Cause**: CUE schema violation or policy constraint.
**Fix**: Run `praxis plan -f template.cue -v ...` for detailed error output with fix suggestions.

---

## Debugging the Pipeline

### Template Compilation Issues
```bash
# Validate template without deploying
praxis plan -f template.cue -v env=dev --dry-run

# Check CUE syntax directly
cue vet template.cue
```

### DAG Issues
Check for:
- Circular dependencies (DAG cycle error)
- Missing dependencies (referencing non-existent resource)
- Expression syntax errors

### Expression Resolution Issues
If expressions aren't resolving:
1. Check that the dependency resource completed successfully
2. Verify the output field name matches the driver's `NormalizeOutputs()`
3. Check adapter's `NormalizeOutputs()` returns the expected fields

### Driver-Level Issues
```bash
# Check driver directly via Restate
curl http://localhost:8080/{DriverServiceName}/{key}/GetStatus
curl http://localhost:8080/{DriverServiceName}/{key}/GetOutputs
```

---

## Key Files for Debugging

| Area | Files to Check |
|------|---------------|
| Deployment lifecycle | `internal/core/orchestrator/workflow.go` |
| Failed resource | `internal/drivers/{resource}/generic.go` (resource operations) and `internal/drivers/kernel/` (lifecycle handler) |
| Template errors | `internal/core/template/engine.go`, `errors.go` |
| DAG issues | `internal/core/dag/graph.go`, `parser.go` |
| Error classification | `internal/drivers/{resource}/aws.go` (classifiers) |
| State issues | `internal/core/orchestrator/deployment_state.go` |

## See Also

- [docs/ERRORS.md](../../docs/ERRORS.md) — Error classification reference
- [docs/ORCHESTRATOR.md](../../docs/ORCHESTRATOR.md) — Deployment execution details
- [docs/OPERATORS.md](../../docs/OPERATORS.md) — Troubleshooting section
