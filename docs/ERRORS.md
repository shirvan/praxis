# Errors

---

## Overview

Every error a user sees is diagnostic (what went wrong), contextual (where it happened), and actionable (how to fix it). This document describes how error handling, classification, and display work across the Praxis stack — CLI, Core (command handlers, pipeline, registries), orchestrator (workflow, runtime, hydrator), DAG, template engine, and all driver packs.

---

## Error Flow

### Backend to User

Errors originate in drivers or command handlers and flow through Restate to the CLI:

```text
Driver / Handler
  │  restate.TerminalError(fmt.Errorf("message"), httpCode)
  │  — OR — bare error (retryable, Restate will retry)
  ▼
Restate Runtime
  │  Wraps TerminalError into HTTP response with [httpCode] prefix
  ▼
Restate Ingress → HTTP response
  ▼
CLI Client (cli/client.go)
  │  Passes error through to Cobra (no redundant wrapping)
  ▼
Cobra RunE → main.go
  │  fmt.Fprintln(os.Stderr, err)
  ▼
User Terminal
```

The CLI does not re-wrap errors from action commands (`apply`, `plan`, `delete`, `import`, `template register`, `template delete`). The operation context is already clear from the command the user ran. State-query methods (`get`, `list`, `observe`) add context since they're called from multiple commands.

#### What the user sees

A simple driver failure surfaces as a single line on stderr:

```text
$ praxis apply -f webapp.cue
Error: bucket "my-logs" exists but is not controllable by Praxis
```

A multi-resource deployment shows the summary returned by the orchestrator:

```text
$ praxis apply -f stack.cue
Error: 2 resource(s) failed:
  1. cache: parameter group "custom-redis" does not exist
  2. web-server: insufficient capacity in us-east-1a
```

### Deployment Lifecycle

During deployment execution, errors flow through the orchestrator and are stored at both the per-resource and deployment levels:

```text
Workflow: Run()
  │
  ├─ Per resource:
  │    HydrateExprs() → TemplateErrors (structured, with Detail)
  │    adapter.Provision() → driver returns outputs or error
  │    On failure: recordApplyFailure()
  │      ├─ markFailed(name, errMsg)           ← per-resource error string
  │      ├─ updateDeploymentResource(Error:)   ← stored in state
  │      ├─ EmitDeploymentCloudEvent(Error:)   ← event log
  │      └─ skipAffectedDependents()           ← cascading skip messages
  │
  └─ Final:
       failureSummary()                        ← numbered list (multi) or flat (single)
       failureMap()                            ← structured map for JSON consumers
       finalizeDeployment(Error: summary)      ← deployment-level error
       ▼
       CLI: printDeploymentDetail()
         ├─ Table: truncated to 60 chars
         └─ "Errors:" section: full per-resource errors
```

If DAG construction fails and the subsequent `finalizeDeployment` call also fails, both errors are surfaced in the returned error message so the user knows the deployment record may be stuck.

#### What `praxis get` shows

The table view truncates errors to 60 characters. Full text appears in the "Errors:" section below:

```text
$ praxis get Deployment/my-stack
Deployment: my-stack
Status:     failed
Error:      2 resource(s) failed:
              1. cache: parameter group "custom-redis" does not exist
              2. web-server: insufficient capacity in us-east-1a
Created:    2026-03-22 14:01:05 UTC
Updated:    2026-03-22 14:01:12 UTC

RESOURCE     KIND              STATUS    ERROR
network      AWS::VPC          created   -
cache        AWS::ElastiCache  error     parameter group "custom-redis" does not exi...
web-server   AWS::EC2          error     insufficient capacity in us-east-1a
api          AWS::Lambda       skipped   skipped because dependency cache failed

Outputs:
  network.vpcId = vpc-0abc1234

Errors:

  cache (AWS::ElastiCache):
    parameter group "custom-redis" does not exist

  web-server (AWS::EC2):
    insufficient capacity in us-east-1a
```

With `--output json`, the same data is machine-readable:

```json
{
  "key": "my-stack",
  "status": "failed",
  "templatePath": "stacks/webapp.cue",
  "error": "2 resource(s) failed:\n  1. cache: parameter group \"custom-redis\" does not exist\n  2. web-server: insufficient capacity in us-east-1a",
  "errorCode": "PROVISION_FAILED",
  "resourceErrors": {
    "cache": "parameter group \"custom-redis\" does not exist",
    "web-server": "insufficient capacity in us-east-1a"
  },
  "resources": [
    { "name": "network",    "kind": "AWS::VPC",         "status": "created", "error": "" },
    { "name": "cache",      "kind": "AWS::ElastiCache", "status": "error",   "error": "parameter group \"custom-redis\" does not exist" },
    { "name": "web-server", "kind": "AWS::EC2",         "status": "error",   "error": "insufficient capacity in us-east-1a" },
    { "name": "api",        "kind": "AWS::Lambda",      "status": "skipped", "error": "skipped because dependency cache failed" }
  ],
  "createdAt": "2026-03-22T14:01:05Z",
  "updatedAt": "2026-03-22T14:01:12Z"
}
```

CI pipelines can branch on `errorCode` without string matching. The `resourceErrors` map provides per-resource messages for structured reporting.

---

## Structured Error Types

### TemplateErrors

The template engine defines the most structured error type in the codebase. All template-related failures — CUE parse errors, validation failures, unresolved expressions, policy violations — are represented as `TemplateError` values:

```go
// internal/core/template/errors.go
type TemplateError struct {
    Kind       TemplateErrorKind  // CUELoad, CUEValidation, ExprUnresolved, Resolve, PolicyViolation
    Path       string             // Dot-path to failing field
    Source     string             // File + line
    Message    string             // What went wrong
    Detail     string             // Actionable fix suggestion
    PolicyName string             // Which policy (if applicable)
    Cause      error              // Underlying library error
}

type TemplateErrors []TemplateError  // Error() renders as tree, JSON() renders as array
```

`TemplateErrors` renders as a tree for human output and as a JSON array for machine consumption. The `Detail` field provides actionable fix suggestions — this pattern is the model for error messages throughout the codebase.

#### What the tree looks like

A template with a missing required field and an unknown field renders as:

```text
Template evaluation failed

  storage.bucketName
  |-- stacks/webapp.cue:8:5
  |-- field 'bucketName' is required
  |__ add a bucketName field to the storage resource

  storage.lifecycle[0].rule
  |-- stacks/webapp.cue:14:9
  |-- unknown field 'expiration' (CUEValidation)
  |__ did you mean: expirationDays?

2 error(s) in template evaluation.
```

Each entry shows the dot-path, source location, error message, and an actionable fix suggestion. Policy violations include the policy name:

```text
Template evaluation failed

  compute.instanceType
  |-- stacks/webapp.cue:22:5
  |-- instance type "t2.micro" is not allowed (PolicyViolation)
  |__ policy "require-current-gen" requires instance types from the t3 or m5 family

1 error(s) in template evaluation.
```

### Failure Summary

When a deployment has multiple failed resources, the orchestrator produces a numbered list for readability:

```text
3 resource(s) failed:
  1. cache: parameter group "custom-redis" does not exist
  2. database: subnet-12345 not found
  3. web-server: insufficient capacity in us-east-1a
```

Single-resource failures stay flat (`web-server: insufficient capacity`). The structured form is also available as `ResourceErrors` (a `map[string]string`) in the deployment detail JSON for programmatic consumption.

#### Cascading skips

When a resource fails, its dependents are skipped automatically with a message indicating the root cause:

```text
RESOURCE   STATUS    ERROR
network    error     subnet-12345 not found
db         skipped   skipped because dependency network failed
api        skipped   skipped because dependency network failed
frontend   skipped   skipped because dependency api failed
```

Skipped resources are not retried — fix the root-cause resource and re-apply.

### Lifecycle Policy Errors

When a delete operation encounters a resource with `lifecycle.preventDestroy: true`, the orchestrator returns a terminal error:

```text
$ praxis delete Deployment/my-stack --yes --wait
Error: 1 resource(s) failed:
  1. prod-db: lifecycle.preventDestroy enabled; refusing to delete resource "prod-db" (RDSInstance)
```

The error is terminal — the workflow does not retry. To delete the resource, update the template to remove or set `preventDestroy: false`, re-apply, then retry the delete.

The same check applies when `--replace` would force-recreate a protected resource during apply.

### Error Codes

Deployment details include a machine-readable `ErrorCode` field (`pkg/types/errorcode.go`) for JSON consumers. This allows CI pipelines and bots to branch on error type without string matching:

```go
type ErrorCode string

const (
    ErrCodeValidation       ErrorCode = "VALIDATION_ERROR"
    ErrCodeNotFound         ErrorCode = "NOT_FOUND"
    ErrCodeConflict         ErrorCode = "CONFLICT"
    ErrCodeCapacityExceeded ErrorCode = "CAPACITY_EXCEEDED"
    ErrCodeTemplateInvalid  ErrorCode = "TEMPLATE_INVALID"
    ErrCodeGraphInvalid     ErrorCode = "GRAPH_INVALID"
    ErrCodeProvisionFailed  ErrorCode = "PROVISION_FAILED"
    ErrCodeDeleteFailed     ErrorCode = "DELETE_FAILED"
    ErrCodeInternal         ErrorCode = "INTERNAL_ERROR"
)
```

Error codes use `SCREAMING_SNAKE_CASE` with domain prefixes for grouping. The `ErrorCode` field is `omitempty` — it does not appear for successful deployments. The `ResourceErrors` map provides per-resource error strings alongside the human-readable `Error` summary.

---

## AWS Error Classification

### Shared Classifier (`internal/drivers/awserr/`)

All drivers classify AWS SDK errors using the shared `awserr` package. This package extracts smithy error codes from the SDK error chain and provides generic matching helpers:

```go
// Extract the AWS error code string from any error
code := awserr.ErrorCode(err)  // e.g., "InvalidInstanceID.NotFound"

// Match against exact codes
awserr.HasCode(err, "InvalidInstanceID.NotFound", "InvalidInstanceID.Malformed")

// Match against code prefixes (for code families)
awserr.HasCodePrefix(err, "InvalidParameter")  // matches InvalidParameterValue, InvalidParameterCombination, etc.
```

The package also provides cross-cutting classifiers for error categories that are common across all AWS services:

| Function | Matches | Use |
|----------|---------|-----|
| `IsThrottled(err)` | `Throttling`, `ThrottlingException`, `RequestLimitExceeded`, `TooManyRequestsException` | Always retryable — never wrap in `TerminalError` |
| `IsAccessDenied(err)` | `AccessDenied`, `AccessDeniedException`, `UnauthorizedAccess`, `AuthorizationError`, `AuthFailure`, `Forbidden`, `InvalidClientTokenId`, `SignatureDoesNotMatch` | IAM/credential issues |
| `IsExpiredToken(err)` | `ExpiredToken`, `ExpiredTokenException`, `RequestExpired`, `TokenRefreshRequired` | Session/token expiry |

### Per-Driver Classifiers

Each driver defines its own classifier functions in `aws.go` for service-specific error codes, delegating to `awserr.HasCode`:

```go
// internal/drivers/ec2/aws.go
func IsNotFound(err error) bool {
    return awserr.HasCode(err, "InvalidInstanceID.NotFound", "InvalidInstanceID.Malformed")
}

func IsInvalidParam(err error) bool {
    return awserr.HasCode(err, "InvalidParameterValue", "InvalidAMIID.Malformed",
        "InvalidAMIID.NotFound", "InvalidSubnetID.NotFound", "InvalidGroup.NotFound")
}

func IsInsufficientCapacity(err error) bool {
    return awserr.HasCode(err, "InsufficientInstanceCapacity", "InstanceLimitExceeded")
}
```

These per-driver classifiers handle the AWS service-specific error codes while the generic smithy extraction lives in `awserr`.

#### What classified errors look like in practice

A throttled error passes through as retryable — the user never sees it:

```text
# Restate retries automatically; no user-visible output.
# Internally: awserr.IsThrottled(err) == true → bare error → retry with backoff
```

An access-denied error becomes terminal:

```text
$ praxis apply -f bucket.cue
Error: AccessDenied: User: arn:aws:iam::123456789012:user/deploy is not authorized to perform: s3:CreateBucket
```

A not-found during import:

```text
$ praxis import EC2/my-server --instance-id i-9999999999
Error: InvalidInstanceID.NotFound: The instance ID 'i-9999999999' does not exist
```

---

## Terminal vs Retryable Errors

Drivers classify errors into two categories. See [Drivers — Error Classification](DRIVERS.md#error-classification) for the full decision flowchart and rules.

**Terminal errors** stop the retry loop immediately:

```go
return restate.TerminalError(fmt.Errorf("bucket is not empty"), 409)
```

**Retryable errors** are retried automatically by Restate with backoff:

```go
return fmt.Errorf("AWS API timeout: %w", err)
```

### HTTP Status Code Convention

All drivers use consistent HTTP status codes with `restate.TerminalError`:

| Condition | HTTP Code | When to Use |
|-----------|-----------|-------------|
| Invalid input, missing fields, bad spec | **400** | User can fix by changing template |
| Resource not found | **404** | Resource deleted outside Praxis or never existed |
| Conflict, duplicate, wrong state | **409** | Resource already exists, currently deleting, etc. |
| Internal/unexpected errors | **500** | Should be rare — indicates a bug |
| Capacity, quota, hard limit | **503** | AWS account limits reached; user must request quota increase |

Throttling and transient rate-limit errors are **not** terminal — they are returned as bare errors so Restate retries them. Only hard quota limits that won't resolve on retry (e.g., `AddressLimitExceeded`, `TooManyBuckets`, `NetworkAclLimitExceeded`) use 503.

#### Examples by status code

**400 — Invalid input:**

```text
Error: bucketName is required
Error: imageId is required
Error: invalid deployment graph: resource "app" depends on unknown resource "db"
```

**404 — Not found:**

```text
Error: InvalidInstanceID.NotFound: The instance ID 'i-0abc123' does not exist
Error: deployment "staging-v2" not found
```

**409 — Conflict:**

```text
Error: bucket "my-logs" exists but is not controllable by Praxis
Error: instance name "web-1" in this region is already managed by Praxis (instanceId: i-0abc123); remove the existing resource or use a different metadata.name
Error: deployment "prod" is already deleted
```

**503 — Capacity / quota:**

```text
Error: TooManyBuckets: You have attempted to create more buckets than allowed
Error: InstanceLimitExceeded: You have reached the limit on the number of instances you can launch
```

---

## Actionable Error Messages

Error messages include fix suggestions where the user might not know the next step. This follows the `TemplateError.Detail` pattern — tell the user what went wrong and how to fix it:

| Error | Suggestion |
|-------|------------|
| Invalid validation mode `"foo"` | Lists supported modes: `"static"`, `"full"` |
| Template rendered no resources | Check that the template has a top-level `resources` block |
| Dependency cycle detected: `a -> b -> a` | Review dependency expressions in these resources to break the cycle |
| Target resource `"foo"` does not exist | Lists available resources in the graph |
| Invalid policy scope `"foo"` | Lists supported scopes: `"global"`, `"template"` |
| Deployment is currently deleting | Suggests `praxis observe Deployment/<key>` to watch progress |

### What actionable errors look like

Messages pair the problem with a concrete next step:

```text
Error: invalid validation mode "foo"; supported modes: "static", "full"
```

```text
Error: dependency cycle detected: app -> db -> app — review the dependency expressions in these resources to break the cycle
```

```text
Error: target resource "cache" does not exist in deployment; available resources: network, database, web-server
```

```text
Error: deployment "prod" is currently deleting; run `praxis observe Deployment/prod` to watch progress
```

---

## Key Files

| File | Purpose |
|------|---------|
| `internal/drivers/awserr/classify.go` | Shared AWS error code extraction and cross-cutting classifiers |
| `internal/drivers/awserr/classify_test.go` | Tests for shared classifiers |
| `internal/drivers/*/aws.go` | Per-driver AWS wrapper with service-specific classifiers |
| `internal/core/template/errors.go` | `TemplateError` / `TemplateErrors` structured error types |
| `internal/core/orchestrator/runtime.go` | `failureSummary()` and `failureMap()` for deployment-level errors |
| `internal/core/orchestrator/workflow.go` | Finalization error handling in apply workflow |
| `internal/cli/client.go` | CLI-to-backend error passthrough |
| `pkg/types/errorcode.go` | Machine-readable `ErrorCode` constants |
| `pkg/types/deployment.go` | `DeploymentDetail` with `ErrorCode` and `ResourceErrors` fields |
