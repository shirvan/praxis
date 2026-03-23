# Route 53 Health Check Driver — Implementation Plan

> **Status: IMPLEMENTED** — Driver is fully implemented with unit tests,
> integration tests, CUE schema, provider adapter, and registry integration.
>
> **Implementation note:** This plan references a `praxis-dns` driver pack.
> The actual implementation places the Health Check driver in **`praxis-network`**
> (`cmd/praxis-network/main.go`).

> Target: A Restate Virtual Object driver that manages Route 53 Health Checks,
> providing full lifecycle management including creation, import, deletion, drift
> detection, and drift correction for endpoint health checks, calculated health
> checks, and CloudWatch alarm-based health checks.
>
> Key scope: `KeyScopeGlobal` — key format is `healthCheckName`, permanent and
> immutable for the lifetime of the Virtual Object. Route 53 is a global AWS service.
> The AWS-assigned health check ID lives only in state/outputs.

---

## Table of Contents

1. [Overview & Scope](#1-overview--scope)
2. [Key Strategy](#2-key-strategy)
3. [File Inventory](#3-file-inventory)
4. [Step 1 — CUE Schema](#step-1--cue-schema)
5. [Step 2 — AWS Client Factory](#step-2--aws-client-factory)
6. [Step 3 — Driver Types](#step-3--driver-types)
7. [Step 4 — AWS API Abstraction Layer](#step-4--aws-api-abstraction-layer)
8. [Step 5 — Drift Detection](#step-5--drift-detection)
9. [Step 6 — Driver Implementation](#step-6--driver-implementation)
10. [Step 7 — Provider Adapter](#step-7--provider-adapter)
11. [Step 8 — Registry Integration](#step-8--registry-integration)
12. [Step 9 — DNS Driver Pack Entry Point](#step-9--dns-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [Health-Check-Specific Design Decisions](#health-check-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The Health Check driver manages the lifecycle of Route 53 **health checks**. It
creates, imports, updates, and deletes health checks that monitor the health of
endpoints, aggregate the results of other health checks, or monitor CloudWatch
alarms.

Health checks are standalone resources — they can exist independently of DNS records.
However, they are most commonly used with DNS failover, weighted, latency,
geolocation, and multivalue answer routing policies. When a record set references a
health check, Route 53 uses the health check's status to determine whether to return
the record in DNS responses.

**Out of scope**:
- **DNS records** — separate driver. This driver manages the health check resource;
  the DNS Record driver references health check IDs.
- **CloudWatch alarms** — the health check can monitor an existing CloudWatch alarm,
  but the driver does not create or manage the alarm itself (future CloudWatch driver).
- **Health check notifications (SNS)** — Route 53 can send health check status
  notifications to SNS topics. Deferred to a future enhancement.

### Resource Scope for This Plan

| In Scope | Out of Scope |
|---|---|
| Endpoint health checks (HTTP, HTTPS, TCP) | CloudWatch alarm creation |
| Calculated health checks (aggregation) | SNS notification configuration |
| CloudWatch alarm-based health checks | Health check status monitoring |
| Health check configuration (IP, port, path, thresholds) | |
| String matching (HTTPS/HTTP) | |
| Health check tags | |
| Import and drift detection | |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a health check |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing health check |
| `Delete` | `ObjectContext` (exclusive) | Delete a health check |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return health check outputs |

### Health Check Types

Route 53 supports three categories of health checks:

| Type | Configuration | Use Case |
|---|---|---|
| **Endpoint** | IP/FQDN + port + protocol (HTTP/HTTPS/TCP) + optional path + optional string match | Monitor a web server, API, or TCP service |
| **Calculated** | List of child health check IDs + threshold | Aggregate multiple health checks (e.g., "healthy if ≥2 of 3 children are healthy") |
| **CloudWatch Alarm** | CloudWatch alarm name + region | Use existing CloudWatch monitoring as a Route 53 health signal |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `type` (HTTP/HTTPS/TCP/CALCULATED/CLOUDWATCH_METRIC) | Immutable | Set at creation; cannot change (requires delete + recreate) |
| `ipAddress` | Mutable | Updated via `UpdateHealthCheck` |
| `port` | Mutable | Updated via `UpdateHealthCheck` |
| `resourcePath` | Mutable | Updated via `UpdateHealthCheck` (HTTP/HTTPS only) |
| `fqdn` | Mutable | Updated via `UpdateHealthCheck` |
| `searchString` | Mutable | Updated via `UpdateHealthCheck` (HTTP/HTTPS string match) |
| `failureThreshold` | Mutable | Updated via `UpdateHealthCheck` (1–10) |
| `requestInterval` | Immutable | 10 or 30 seconds; cannot change after creation |
| `childHealthChecks` | Mutable | Updated via `UpdateHealthCheck` (calculated checks) |
| `healthThreshold` | Mutable | Updated via `UpdateHealthCheck` (calculated checks) |
| `regions` | Mutable | Health checker regions; updated via `UpdateHealthCheck` |
| `disabled` | Mutable | Enable/disable the health check |
| `invertHealthCheck` | Mutable | Invert the health check result |
| `tags` | Mutable | Full replace via `ChangeTagsForResource` |

### Downstream Consumers

```
${resources.my-healthcheck.outputs.healthCheckId}  → Route53Record spec.healthCheckId
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeGlobal`

Route 53 health checks are global resources. Unlike hosted zones (which have domain
names as natural identifiers), health checks have no AWS-enforced unique name — they
are identified only by their health check ID (a UUID). The driver uses a
user-provided logical name (from `metadata.name`) as the Virtual Object key and
enforces uniqueness via `praxis:managed-key` tag-based ownership.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name` from the resource document.
  Returns the health check name directly.

- **`BuildImportKey(region, resourceID)`**: Returns `resourceID` (the health check
  ID, e.g., `12345678-1234-1234-1234-123456789012`). Import creates a VO keyed by
  the health check ID — a **different key** from template management, matching the
  EC2/VPC import pattern.

### Tag-Based Ownership

Health check names are not unique in AWS — multiple health checks can exist with
the same `Name` tag. The driver uses `praxis:managed-key=<key>` tag for ownership
tracking and conflict detection, following the EC2/VPC pattern.

### CallerReference for Idempotent Creation

`CreateHealthCheck` requires a `CallerReference`. The driver uses the Virtual Object
key (the health check name) as the caller reference. This ensures retries after
crash/replay return the existing health check instead of creating duplicates.

---

## 3. File Inventory

```text
✦ schemas/aws/route53/health_check.cue                        — CUE schema for Route53HealthCheck
✦ internal/drivers/route53healthcheck/types.go                 — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/route53healthcheck/aws.go                   — HealthCheckAPI interface + realHealthCheckAPI
✦ internal/drivers/route53healthcheck/drift.go                 — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/route53healthcheck/driver.go                — HealthCheckDriver Virtual Object
✦ internal/drivers/route53healthcheck/driver_test.go           — Unit tests for driver
✦ internal/drivers/route53healthcheck/aws_test.go              — Unit tests for error classification
✦ internal/drivers/route53healthcheck/drift_test.go            — Unit tests for drift detection
✦ internal/core/provider/route53healthcheck_adapter.go         — HealthCheckAdapter implementing provider.Adapter
✦ internal/core/provider/route53healthcheck_adapter_test.go    — Unit tests for adapter
✦ tests/integration/route53_health_check_driver_test.go        — Integration tests
✎ cmd/praxis-dns/main.go                                      — Add HealthCheck driver .Bind()
✎ internal/core/provider/registry.go                           — Add adapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/route53/health_check.cue`

```cue
package route53

#Route53HealthCheck: {
    apiVersion: "praxis.io/v1"
    kind:       "Route53HealthCheck"

    metadata: {
        // name is the logical name for the health check. Used as the
        // Praxis resource identifier and as the praxis:managed-key tag value.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // type is the health check type.
        // Immutable after creation.
        type: "HTTP" | "HTTPS" | "HTTP_STR_MATCH" | "HTTPS_STR_MATCH" | "TCP" | "CALCULATED" | "CLOUDWATCH_METRIC"

        // --- Endpoint Health Check Fields ---
        // Used when type is HTTP, HTTPS, HTTP_STR_MATCH, HTTPS_STR_MATCH, or TCP.

        // ipAddress is the IP address of the endpoint to check.
        // Either ipAddress or fqdn must be specified for endpoint checks.
        ipAddress?: string

        // port is the port on the endpoint. Default: 80 for HTTP, 443 for HTTPS.
        port?: int & >=1 & <=65535

        // resourcePath is the path for HTTP/HTTPS checks (e.g., "/health").
        resourcePath?: string

        // fqdn is the fully-qualified domain name of the endpoint.
        // If specified with ipAddress, Route 53 passes fqdn as the Host header.
        // If specified without ipAddress, Route 53 resolves fqdn to get the IP.
        fqdn?: string

        // searchString is the string to search for in the response body.
        // Required for HTTP_STR_MATCH and HTTPS_STR_MATCH. The response body
        // must contain this string for the health check to succeed.
        // Max 255 characters. Route 53 searches the first 5120 bytes.
        searchString?: string

        // requestInterval is the number of seconds between health check requests.
        // Valid values: 10 or 30. Immutable after creation.
        // 10-second checks cost more but detect failures faster.
        requestInterval: 10 | 30 | *30

        // failureThreshold is the number of consecutive failures before
        // Route 53 considers the endpoint unhealthy (1–10). Default: 3.
        failureThreshold: int & >=1 & <=10 | *3

        // --- Calculated Health Check Fields ---
        // Used when type is CALCULATED.

        // childHealthChecks is the list of child health check IDs.
        // Required for CALCULATED type.
        childHealthChecks?: [...string]

        // healthThreshold is the minimum number of child health checks that
        // must be healthy for this check to be considered healthy.
        // Required for CALCULATED type. Range: 0 to number of children.
        healthThreshold?: int & >=0

        // --- CloudWatch Alarm Health Check Fields ---
        // Used when type is CLOUDWATCH_METRIC.

        // cloudWatchAlarmName is the name of the CloudWatch alarm.
        cloudWatchAlarmName?: string

        // cloudWatchAlarmRegion is the region of the CloudWatch alarm.
        cloudWatchAlarmRegion?: string

        // insufficientDataHealthStatus determines the health check status
        // when CloudWatch has insufficient data.
        insufficientDataHealthStatus?: "Healthy" | "Unhealthy" | "LastKnownStatus"

        // --- Common Fields ---

        // disabled determines whether the health check is disabled.
        // Disabled health checks are always considered healthy.
        disabled: bool | *false

        // invertHealthCheck inverts the health check status.
        // If true, healthy becomes unhealthy and vice versa.
        invertHealthCheck: bool | *false

        // enableSNI determines whether Route 53 sends the host name to the
        // endpoint during TLS negotiation (HTTPS checks only).
        enableSNI?: bool

        // regions is the list of AWS regions from which Route 53 performs
        // health checks. If empty, Route 53 uses its default regions.
        // Must be at least 3 if specified.
        regions?: [...string]

        // tags applied to the health check.
        tags: [string]: string
    }

    outputs?: {
        healthCheckId: string
    }
}
```

### Key Design Decisions

- **`type` as string enum**: All six health check types are supported. The `type`
  field determines which other fields are relevant.

- **Separate string match types**: AWS distinguishes HTTP from HTTP_STR_MATCH (and
  HTTPS from HTTPS_STR_MATCH). The driver preserves this distinction.

- **`requestInterval` immutable**: AWS does not allow changing the request interval
  after creation. The driver reports this as an immutable field during drift detection.
  Changing it requires delete + recreate.

- **`regions` as list**: Route 53 uses health checker nodes in multiple AWS regions.
  The user can specify which regions to use. Minimum 3 regions if specified.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — Uses same Route 53 client as
Hosted Zone and DNS Record drivers. No additional changes needed.

---

## Step 3 — Driver Types

**File**: `internal/drivers/route53healthcheck/types.go`

```go
package route53healthcheck

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "Route53HealthCheck"

// HealthCheckSpec is the desired state for a health check.
type HealthCheckSpec struct {
    Account                      string            `json:"account,omitempty"`
    Type                         string            `json:"type"`
    IPAddress                    string            `json:"ipAddress,omitempty"`
    Port                         *int32            `json:"port,omitempty"`
    ResourcePath                 string            `json:"resourcePath,omitempty"`
    FQDN                         string            `json:"fqdn,omitempty"`
    SearchString                 string            `json:"searchString,omitempty"`
    RequestInterval              int32             `json:"requestInterval"`
    FailureThreshold             int32             `json:"failureThreshold"`
    ChildHealthChecks            []string          `json:"childHealthChecks,omitempty"`
    HealthThreshold              *int32            `json:"healthThreshold,omitempty"`
    CloudWatchAlarmName          string            `json:"cloudWatchAlarmName,omitempty"`
    CloudWatchAlarmRegion        string            `json:"cloudWatchAlarmRegion,omitempty"`
    InsufficientDataHealthStatus string            `json:"insufficientDataHealthStatus,omitempty"`
    Disabled                     bool              `json:"disabled"`
    InvertHealthCheck            bool              `json:"invertHealthCheck"`
    EnableSNI                    *bool             `json:"enableSNI,omitempty"`
    Regions                      []string          `json:"regions,omitempty"`
    Tags                         map[string]string `json:"tags,omitempty"`
    ManagedKey                   string            `json:"managedKey,omitempty"`
}

// HealthCheckOutputs is produced after provisioning.
type HealthCheckOutputs struct {
    HealthCheckId string `json:"healthCheckId"`
}

// ObservedState captures the actual configuration from AWS.
type ObservedState struct {
    HealthCheckId                string            `json:"healthCheckId"`
    CallerReference              string            `json:"callerReference"`
    Type                         string            `json:"type"`
    IPAddress                    string            `json:"ipAddress,omitempty"`
    Port                         *int32            `json:"port,omitempty"`
    ResourcePath                 string            `json:"resourcePath,omitempty"`
    FQDN                         string            `json:"fqdn,omitempty"`
    SearchString                 string            `json:"searchString,omitempty"`
    RequestInterval              int32             `json:"requestInterval"`
    FailureThreshold             int32             `json:"failureThreshold"`
    ChildHealthChecks            []string          `json:"childHealthChecks,omitempty"`
    HealthThreshold              *int32            `json:"healthThreshold,omitempty"`
    CloudWatchAlarmName          string            `json:"cloudWatchAlarmName,omitempty"`
    CloudWatchAlarmRegion        string            `json:"cloudWatchAlarmRegion,omitempty"`
    InsufficientDataHealthStatus string            `json:"insufficientDataHealthStatus,omitempty"`
    Disabled                     bool              `json:"disabled"`
    InvertHealthCheck            bool              `json:"invertHealthCheck"`
    EnableSNI                    *bool             `json:"enableSNI,omitempty"`
    Regions                      []string          `json:"regions,omitempty"`
    Tags                         map[string]string `json:"tags"`
    HealthCheckStatus            string            `json:"healthCheckStatus,omitempty"`
}

// HealthCheckState is the single atomic state object stored under drivers.StateKey.
type HealthCheckState struct {
    Desired            HealthCheckSpec       `json:"desired"`
    Observed           ObservedState         `json:"observed"`
    Outputs            HealthCheckOutputs    `json:"outputs"`
    Status             types.ResourceStatus  `json:"status"`
    Mode               types.Mode            `json:"mode"`
    Error              string                `json:"error,omitempty"`
    Generation         int64                 `json:"generation"`
    LastReconcile      string                `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                  `json:"reconcileScheduled"`
}
```

### Why These Fields

- **`Port` as `*int32`**: Pointer because port may be omitted (Route 53 uses
  defaults: 80 for HTTP, 443 for HTTPS). A zero port is invalid, so nil = default.
- **`HealthThreshold` as `*int32`**: Only relevant for calculated checks. Nil means
  not applicable.
- **`EnableSNI` as `*bool`**: Pointer to distinguish "not set" (use AWS default)
  from explicit true/false.
- **`HealthCheckStatus` in ObservedState**: The current health status
  (Healthy/Unhealthy). Informational only — not used for drift detection.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/route53healthcheck/aws.go`

### HealthCheckAPI Interface

```go
type HealthCheckAPI interface {
    // CreateHealthCheck creates a new health check.
    // Returns the health check ID.
    CreateHealthCheck(ctx context.Context, spec HealthCheckSpec) (string, error)

    // DescribeHealthCheck returns the observed state of a health check.
    DescribeHealthCheck(ctx context.Context, healthCheckId string) (ObservedState, error)

    // UpdateHealthCheck updates mutable attributes of a health check.
    UpdateHealthCheck(ctx context.Context, healthCheckId string, spec HealthCheckSpec) error

    // DeleteHealthCheck deletes a health check.
    DeleteHealthCheck(ctx context.Context, healthCheckId string) error

    // UpdateTags replaces all user-managed tags on the health check.
    UpdateTags(ctx context.Context, healthCheckId string, tags map[string]string) error

    // FindByManagedKey searches for health checks tagged with
    // praxis:managed-key=managedKey.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### realHealthCheckAPI Implementation

```go
type realHealthCheckAPI struct {
    client  *route53.Client
    limiter *ratelimit.Limiter
}

func NewHealthCheckAPI(client *route53.Client) HealthCheckAPI {
    return &realHealthCheckAPI{
        client:  client,
        limiter: ratelimit.New("route53", 5, 3),
    }
}
```

### Key Implementation Details

#### `CreateHealthCheck`

```go
func (r *realHealthCheckAPI) CreateHealthCheck(ctx context.Context, spec HealthCheckSpec) (string, error) {
    input := &route53.CreateHealthCheckInput{
        CallerReference: aws.String(spec.ManagedKey),
        HealthCheckConfig: &route53types.HealthCheckConfig{
            Type: route53types.HealthCheckType(spec.Type),
        },
    }

    cfg := input.HealthCheckConfig

    // Endpoint check fields
    switch spec.Type {
    case "HTTP", "HTTPS", "HTTP_STR_MATCH", "HTTPS_STR_MATCH", "TCP":
        if spec.IPAddress != "" {
            cfg.IPAddress = aws.String(spec.IPAddress)
        }
        if spec.Port != nil {
            cfg.Port = spec.Port
        }
        if spec.ResourcePath != "" {
            cfg.ResourcePath = aws.String(spec.ResourcePath)
        }
        if spec.FQDN != "" {
            cfg.FullyQualifiedDomainName = aws.String(spec.FQDN)
        }
        if spec.SearchString != "" {
            cfg.SearchString = aws.String(spec.SearchString)
        }
        cfg.RequestInterval = aws.Int32(spec.RequestInterval)
        cfg.FailureThreshold = aws.Int32(spec.FailureThreshold)

        if spec.EnableSNI != nil {
            cfg.EnableSNI = spec.EnableSNI
        }
        if len(spec.Regions) > 0 {
            regions := make([]route53types.HealthCheckRegion, 0, len(spec.Regions))
            for _, r := range spec.Regions {
                regions = append(regions, route53types.HealthCheckRegion(r))
            }
            cfg.Regions = regions
        }

    case "CALCULATED":
        if len(spec.ChildHealthChecks) > 0 {
            cfg.ChildHealthChecks = spec.ChildHealthChecks
        }
        if spec.HealthThreshold != nil {
            cfg.HealthThreshold = spec.HealthThreshold
        }

    case "CLOUDWATCH_METRIC":
        if spec.CloudWatchAlarmName != "" || spec.CloudWatchAlarmRegion != "" {
            cfg.AlarmIdentifier = &route53types.AlarmIdentifier{
                Name:   aws.String(spec.CloudWatchAlarmName),
                Region: route53types.CloudWatchRegion(spec.CloudWatchAlarmRegion),
            }
        }
        if spec.InsufficientDataHealthStatus != "" {
            cfg.InsufficientDataHealthStatus = route53types.InsufficientDataHealthStatus(
                spec.InsufficientDataHealthStatus)
        }
    }

    // Common fields
    cfg.Disabled = aws.Bool(spec.Disabled)
    cfg.Inverted = aws.Bool(spec.InvertHealthCheck)

    out, err := r.client.CreateHealthCheck(ctx, input)
    if err != nil {
        return "", err
    }

    return aws.ToString(out.HealthCheck.Id), nil
}
```

#### `DescribeHealthCheck`

```go
func (r *realHealthCheckAPI) DescribeHealthCheck(ctx context.Context, healthCheckId string) (ObservedState, error) {
    // 1. GetHealthCheck — base configuration
    out, err := r.client.GetHealthCheck(ctx, &route53.GetHealthCheckInput{
        HealthCheckId: aws.String(healthCheckId),
    })
    if err != nil {
        return ObservedState{}, err
    }

    hc := out.HealthCheck
    cfg := hc.HealthCheckConfig

    obs := ObservedState{
        HealthCheckId:    aws.ToString(hc.Id),
        CallerReference:  aws.ToString(hc.CallerReference),
        Type:             string(cfg.Type),
        RequestInterval:  aws.ToInt32(cfg.RequestInterval),
        FailureThreshold: aws.ToInt32(cfg.FailureThreshold),
        Disabled:         aws.ToBool(cfg.Disabled),
        InvertHealthCheck: aws.ToBool(cfg.Inverted),
    }

    if cfg.IPAddress != nil {
        obs.IPAddress = aws.ToString(cfg.IPAddress)
    }
    if cfg.Port != nil {
        obs.Port = cfg.Port
    }
    if cfg.ResourcePath != nil {
        obs.ResourcePath = aws.ToString(cfg.ResourcePath)
    }
    if cfg.FullyQualifiedDomainName != nil {
        obs.FQDN = aws.ToString(cfg.FullyQualifiedDomainName)
    }
    if cfg.SearchString != nil {
        obs.SearchString = aws.ToString(cfg.SearchString)
    }
    if cfg.EnableSNI != nil {
        obs.EnableSNI = cfg.EnableSNI
    }
    if len(cfg.Regions) > 0 {
        for _, region := range cfg.Regions {
            obs.Regions = append(obs.Regions, string(region))
        }
    }
    if len(cfg.ChildHealthChecks) > 0 {
        obs.ChildHealthChecks = cfg.ChildHealthChecks
    }
    if cfg.HealthThreshold != nil {
        obs.HealthThreshold = cfg.HealthThreshold
    }
    if cfg.AlarmIdentifier != nil {
        obs.CloudWatchAlarmName = aws.ToString(cfg.AlarmIdentifier.Name)
        obs.CloudWatchAlarmRegion = string(cfg.AlarmIdentifier.Region)
    }
    if cfg.InsufficientDataHealthStatus != "" {
        obs.InsufficientDataHealthStatus = string(cfg.InsufficientDataHealthStatus)
    }

    // 2. ListTagsForResource — tags
    tagOut, err := r.client.ListTagsForResource(ctx, &route53.ListTagsForResourceInput{
        ResourceId:   aws.String(healthCheckId),
        ResourceType: route53types.TagResourceTypeHealthcheck,
    })
    if err != nil {
        return ObservedState{}, fmt.Errorf("list tags for health check %s: %w", healthCheckId, err)
    }

    obs.Tags = make(map[string]string)
    if tagOut.ResourceTagSet != nil {
        for _, tag := range tagOut.ResourceTagSet.Tags {
            obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
        }
    }

    // 3. GetHealthCheckStatus — current health status (informational)
    statusOut, err := r.client.GetHealthCheckStatus(ctx, &route53.GetHealthCheckStatusInput{
        HealthCheckId: aws.String(healthCheckId),
    })
    if err == nil && len(statusOut.HealthCheckObservations) > 0 {
        // Aggregate: if any region reports unhealthy, overall is unhealthy
        healthy := true
        for _, observation := range statusOut.HealthCheckObservations {
            if observation.StatusReport != nil &&
                observation.StatusReport.Status != nil &&
                !strings.Contains(aws.ToString(observation.StatusReport.Status), "Success") {
                healthy = false
                break
            }
        }
        if healthy {
            obs.HealthCheckStatus = "Healthy"
        } else {
            obs.HealthCheckStatus = "Unhealthy"
        }
    }

    return obs, nil
}
```

> **API call count**: `DescribeHealthCheck` makes 3 API calls: `GetHealthCheck` +
> `ListTagsForResource` + `GetHealthCheckStatus`. The status call is best-effort
> (informational only, not used for drift).

#### `UpdateHealthCheck`

```go
func (r *realHealthCheckAPI) UpdateHealthCheck(ctx context.Context, healthCheckId string, spec HealthCheckSpec) error {
    input := &route53.UpdateHealthCheckInput{
        HealthCheckId: aws.String(healthCheckId),
    }

    switch spec.Type {
    case "HTTP", "HTTPS", "HTTP_STR_MATCH", "HTTPS_STR_MATCH", "TCP":
        if spec.IPAddress != "" {
            input.IPAddress = aws.String(spec.IPAddress)
        }
        if spec.Port != nil {
            input.Port = spec.Port
        }
        if spec.ResourcePath != "" {
            input.ResourcePath = aws.String(spec.ResourcePath)
        }
        if spec.FQDN != "" {
            input.FullyQualifiedDomainName = aws.String(spec.FQDN)
        }
        if spec.SearchString != "" {
            input.SearchString = aws.String(spec.SearchString)
        }
        input.FailureThreshold = aws.Int32(spec.FailureThreshold)

        if spec.EnableSNI != nil {
            input.EnableSNI = spec.EnableSNI
        }
        if len(spec.Regions) > 0 {
            regions := make([]route53types.HealthCheckRegion, 0, len(spec.Regions))
            for _, r := range spec.Regions {
                regions = append(regions, route53types.HealthCheckRegion(r))
            }
            input.Regions = regions
        }

    case "CALCULATED":
        if len(spec.ChildHealthChecks) > 0 {
            input.ChildHealthChecks = spec.ChildHealthChecks
        }
        if spec.HealthThreshold != nil {
            input.HealthThreshold = spec.HealthThreshold
        }

    case "CLOUDWATCH_METRIC":
        if spec.CloudWatchAlarmName != "" || spec.CloudWatchAlarmRegion != "" {
            input.AlarmIdentifier = &route53types.AlarmIdentifier{
                Name:   aws.String(spec.CloudWatchAlarmName),
                Region: route53types.CloudWatchRegion(spec.CloudWatchAlarmRegion),
            }
        }
        if spec.InsufficientDataHealthStatus != "" {
            input.InsufficientDataHealthStatus = route53types.InsufficientDataHealthStatus(
                spec.InsufficientDataHealthStatus)
        }
    }

    input.Disabled = aws.Bool(spec.Disabled)
    input.Inverted = aws.Bool(spec.InvertHealthCheck)

    _, err := r.client.UpdateHealthCheck(ctx, input)
    return err
}
```

> **Note**: `requestInterval` is NOT updated here — it is immutable after creation.
> If the desired interval differs from observed, drift detection reports it as
> "(immutable, ignored)".

#### `DeleteHealthCheck`

```go
func (r *realHealthCheckAPI) DeleteHealthCheck(ctx context.Context, healthCheckId string) error {
    _, err := r.client.DeleteHealthCheck(ctx, &route53.DeleteHealthCheckInput{
        HealthCheckId: aws.String(healthCheckId),
    })
    return err
}
```

> **Dependency conflict**: Deleting a health check that is still referenced by DNS
> records will succeed (Route 53 does not enforce referential integrity on delete).
> However, the referencing records will stop receiving health-check-aware routing.
> The DAG scheduler should order health check deletion after record deletion.

#### `UpdateTags`

```go
func (r *realHealthCheckAPI) UpdateTags(ctx context.Context, healthCheckId string, tags map[string]string) error {
    // Same pattern as Hosted Zone — ChangeTagsForResource supports
    // atomic add + remove.
    tagOut, err := r.client.ListTagsForResource(ctx, &route53.ListTagsForResourceInput{
        ResourceId:   aws.String(healthCheckId),
        ResourceType: route53types.TagResourceTypeHealthcheck,
    })
    if err != nil {
        return err
    }

    var removeKeys []string
    if tagOut.ResourceTagSet != nil {
        for _, tag := range tagOut.ResourceTagSet.Tags {
            key := aws.ToString(tag.Key)
            if strings.HasPrefix(key, "praxis:") {
                continue
            }
            if _, ok := tags[key]; !ok {
                removeKeys = append(removeKeys, key)
            }
        }
    }

    var addTags []route53types.Tag
    for k, v := range tags {
        if strings.HasPrefix(k, "praxis:") {
            continue
        }
        addTags = append(addTags, route53types.Tag{
            Key:   aws.String(k),
            Value: aws.String(v),
        })
    }

    if len(addTags) > 0 || len(removeKeys) > 0 {
        input := &route53.ChangeTagsForResourceInput{
            ResourceId:   aws.String(healthCheckId),
            ResourceType: route53types.TagResourceTypeHealthcheck,
            AddTags:      addTags,
        }
        if len(removeKeys) > 0 {
            input.RemoveTagKeys = removeKeys
        }
        _, err = r.client.ChangeTagsForResource(ctx, input)
        return err
    }
    return nil
}
```

#### `FindByManagedKey`

```go
func (r *realHealthCheckAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    // Route 53 does not support tag-based filtering on ListHealthChecks.
    // Must iterate and check tags individually.
    var matches []string

    paginator := route53.NewListHealthChecksPaginator(r.client, &route53.ListHealthChecksInput{})
    for paginator.HasMorePages() {
        page, err := paginator.NextPage(ctx)
        if err != nil {
            return "", err
        }
        for _, hc := range page.HealthChecks {
            hcId := aws.ToString(hc.Id)

            tagOut, err := r.client.ListTagsForResource(ctx, &route53.ListTagsForResourceInput{
                ResourceId:   aws.String(hcId),
                ResourceType: route53types.TagResourceTypeHealthcheck,
            })
            if err != nil {
                continue
            }
            if tagOut.ResourceTagSet != nil {
                for _, tag := range tagOut.ResourceTagSet.Tags {
                    if aws.ToString(tag.Key) == "praxis:managed-key" &&
                        aws.ToString(tag.Value) == managedKey {
                        matches = append(matches, hcId)
                    }
                }
            }
        }
    }

    switch len(matches) {
    case 0:
        return "", nil
    case 1:
        return matches[0], nil
    default:
        return "", fmt.Errorf("ownership corruption: %d health checks tagged with praxis:managed-key=%s",
            len(matches), managedKey)
    }
}
```

### Error Classification Helpers

```go
func IsNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "NoSuchHealthCheck"
    }
    return strings.Contains(err.Error(), "NoSuchHealthCheck")
}

func IsAlreadyExists(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "HealthCheckAlreadyExists"
    }
    return strings.Contains(err.Error(), "HealthCheckAlreadyExists")
}

func IsInUse(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "HealthCheckInUse"
    }
    return strings.Contains(err.Error(), "HealthCheckInUse")
}

func IsInvalidInput(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidInput"
    }
    return strings.Contains(err.Error(), "InvalidInput")
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/route53healthcheck/drift.go`

### Core Functions

**`HasDrift(desired HealthCheckSpec, observed ObservedState) bool`**

```go
func HasDrift(desired HealthCheckSpec, observed ObservedState) bool {
    // Endpoint check fields
    if desired.IPAddress != observed.IPAddress {
        return true
    }
    if !ptrInt32Equal(desired.Port, observed.Port) {
        return true
    }
    if desired.ResourcePath != observed.ResourcePath {
        return true
    }
    if desired.FQDN != observed.FQDN {
        return true
    }
    if desired.SearchString != observed.SearchString {
        return true
    }
    if desired.FailureThreshold != observed.FailureThreshold {
        return true
    }

    // Calculated check fields
    if !stringSlicesEqual(desired.ChildHealthChecks, observed.ChildHealthChecks) {
        return true
    }
    if !ptrInt32Equal(desired.HealthThreshold, observed.HealthThreshold) {
        return true
    }

    // CloudWatch fields
    if desired.CloudWatchAlarmName != observed.CloudWatchAlarmName {
        return true
    }
    if desired.CloudWatchAlarmRegion != observed.CloudWatchAlarmRegion {
        return true
    }
    if desired.InsufficientDataHealthStatus != observed.InsufficientDataHealthStatus {
        return true
    }

    // Common fields
    if desired.Disabled != observed.Disabled {
        return true
    }
    if desired.InvertHealthCheck != observed.InvertHealthCheck {
        return true
    }
    if !ptrBoolEqual(desired.EnableSNI, observed.EnableSNI) {
        return true
    }
    if !stringSlicesEqual(desired.Regions, observed.Regions) {
        return true
    }

    return !tagsMatch(desired.Tags, observed.Tags)
}
```

**`ComputeFieldDiffs(desired HealthCheckSpec, observed ObservedState) []FieldDiffEntry`**

Produces human-readable diffs:

- Immutable fields: `type`, `requestInterval` — reported with "(immutable, ignored)".
- Mutable endpoint fields: IP, port, path, FQDN, search string, failure threshold.
- Calculated fields: child health checks (set diff), health threshold.
- CloudWatch fields: alarm name, region, insufficient data status.
- Common fields: disabled, invert, SNI, regions.
- Tags: per-key diffs.

### Comparison Helpers

```go
func stringSlicesEqual(a, b []string) bool {
    if len(a) != len(b) {
        return false
    }
    aSet := make(map[string]bool, len(a))
    for _, v := range a {
        aSet[v] = true
    }
    for _, v := range b {
        if !aSet[v] {
            return false
        }
    }
    return true
}

func ptrInt32Equal(a, b *int32) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    return *a == *b
}

func ptrBoolEqual(a, b *bool) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    return *a == *b
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/route53healthcheck/driver.go`

### Constructor Pattern

```go
func NewHealthCheckDriver(accounts *auth.Registry) *HealthCheckDriver
func NewHealthCheckDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) HealthCheckAPI) *HealthCheckDriver
```

### Provision Handler

1. **Input validation**: `type` must be non-empty. Endpoint checks require at least
   `ipAddress` or `fqdn`. String match checks require `searchString`. Calculated
   checks require `childHealthChecks` and `healthThreshold`. CloudWatch checks
   require `cloudWatchAlarmName` and `cloudWatchAlarmRegion`. Returns
   `TerminalError(400)` on failure.

2. **Load current state**: Reads `HealthCheckState` from Restate K/V. Sets status to
   `Provisioning`, increments generation.

3. **Re-provision check**: If `state.Outputs.HealthCheckId` is non-empty, describes
   the health check. If deleted externally (404), clears ID and falls through to
   creation.

4. **Conflict check**: On first provision, calls `FindByManagedKey`. Returns
   `TerminalError(409)` if conflict found.

5. **Create health check**: Calls `api.CreateHealthCheck` inside `restate.Run`.
   - `IsAlreadyExists` → caller reference collision → `TerminalError(409)`
   - `IsInvalidInput` → `TerminalError(400)`

6. **Tag the health check**: Applies `praxis:managed-key` tag, `Name` tag, and user
   tags via `UpdateTags`.

7. **Re-provision path — converge mutable attributes**: Calls
   `api.UpdateHealthCheck` with current spec. Route 53's `UpdateHealthCheck`
   replaces all mutable fields atomically.

8. **Tag convergence**: `UpdateTags` if tags drifted.

9. **Describe final state**: Calls `api.DescribeHealthCheck`.

10. **Commit state**: Sets status to `Ready`, saves atomically, schedules reconcile.

### Import Handler

1. Describes the health check by `ref.ResourceID` (the health check ID).
2. Synthesizes a `HealthCheckSpec` from the observed state via `specFromObserved()`.
3. Commits state with `ModeObserved`.
4. Schedules reconciliation.

### Delete Handler

1. Sets status to `Deleting`.
2. **Delete health check**: Calls `api.DeleteHealthCheck`.
   - `IsNotFound` → silent success (already gone).
   - `IsInUse` → `TerminalError(409)` with message about referencing DNS records.
     > Note: Route 53 does not actually enforce this, but the driver checks for
     > referencing records as a safety measure.
3. On success, sets status to `StatusDeleted`.

### Reconcile Handler

Standard 5-minute timer pattern:

1. Describes current health check state (config + tags + status).
2. **Managed + drift**: Updates health check config and tags.
3. **Observed + drift**: Reports only.
4. Re-schedules.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/route53healthcheck_adapter.go`

```go
type Route53HealthCheckAdapter struct {
    accounts *auth.Registry
}

func (a *Route53HealthCheckAdapter) Kind() string       { return "Route53HealthCheck" }
func (a *Route53HealthCheckAdapter) Service() string    { return route53healthcheck.ServiceName }
func (a *Route53HealthCheckAdapter) KeyScope() KeyScope  { return KeyScopeGlobal }

func (a *Route53HealthCheckAdapter) BuildKey(doc types.ResourceDocument) string {
    return doc.Metadata.Name
}

func (a *Route53HealthCheckAdapter) BuildImportKey(region, resourceID string) string {
    return resourceID
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go`

```go
r.Register(NewRoute53HealthCheckAdapterWithRegistry(accounts))
```

---

## Step 9 — DNS Driver Pack Entry Point

See [ROUTE53_DRIVER_PACK_OVERVIEW.md](ROUTE53_DRIVER_PACK_OVERVIEW.md) §3.

---

## Step 10 — Docker Compose & Justfile

See [ROUTE53_DRIVER_PACK_OVERVIEW.md](ROUTE53_DRIVER_PACK_OVERVIEW.md) §7 and §8.

---

## Step 11 — Unit Tests

**File**: `internal/drivers/route53healthcheck/driver_test.go`

### Test Categories

| Category | Tests |
|---|---|
| Provision — HTTP endpoint check | Happy path, verify IP + port + path |
| Provision — HTTPS endpoint check | Verify port defaults, SNI setting |
| Provision — HTTPS string match | Verify search string included |
| Provision — TCP check | Verify IP + port only, no path |
| Provision — calculated check | Verify child health checks + threshold |
| Provision — CloudWatch alarm check | Verify alarm name + region |
| Provision — idempotent retry | Same caller reference returns existing |
| Provision — conflict detection | FindByManagedKey hit → 409 |
| Provision — re-provision (update IP) | Detects drift, calls UpdateHealthCheck |
| Provision — re-provision (update threshold) | Updates failure threshold |
| Provision — re-provision (update tags) | Tag convergence |
| Provision — re-provision (immutable change) | requestInterval change → drift ignored |
| Provision — externally deleted | Detects 404, recreates |
| Provision — invalid input | Missing required fields → 400 |
| Import — by health check ID | Describes, synthesizes spec, sets Observed mode |
| Delete — existing health check | Deletes successfully |
| Delete — already gone | Silent success on 404 |
| Reconcile — no drift | No changes, re-schedule |
| Reconcile — drift detected (managed) | Updates config + tags |
| Reconcile — drift detected (observed) | Reports only |
| GetStatus / GetOutputs | Returns stored state |

**File**: `internal/drivers/route53healthcheck/drift_test.go`

| Test | Behavior |
|---|---|
| No drift — HTTP check | All fields match |
| No drift — calculated check | Children and threshold match |
| IP changed | Reports IP diff |
| Port changed | Reports port diff |
| Failure threshold changed | Reports threshold diff |
| Search string changed | Reports search string diff |
| Child health checks changed | Reports set diff |
| CloudWatch alarm changed | Reports alarm diff |
| Disabled toggled | Reports disabled diff |
| Tags changed | Reports per-key tag diffs |
| requestInterval changed | Reports as "(immutable, ignored)" |
| Type changed | Reports as "(immutable, ignored)" |

---

## Step 12 — Integration Tests

**File**: `tests/integration/route53_health_check_driver_test.go`

Uses Testcontainers + LocalStack. LocalStack supports Route 53 health checks.

### Test Scenarios

| Test | Flow |
|---|---|
| `TestRoute53HealthCheck_HTTP` | Create HTTP health check → verify config → verify tags → delete |
| `TestRoute53HealthCheck_HTTPS` | Create HTTPS check with SNI → verify → delete |
| `TestRoute53HealthCheck_TCP` | Create TCP check → verify IP + port → delete |
| `TestRoute53HealthCheck_StringMatch` | Create HTTPS_STR_MATCH → verify search string → delete |
| `TestRoute53HealthCheck_Calculated` | Create 2 endpoint checks → create calculated → verify children → delete |
| `TestRoute53HealthCheck_Import` | Create check externally → import → verify observed state → delete |
| `TestRoute53HealthCheck_UpdateEndpoint` | Create check → update IP/port/path → verify convergence |
| `TestRoute53HealthCheck_UpdateThreshold` | Create check → update failure threshold → verify |
| `TestRoute53HealthCheck_UpdateTags` | Create check → modify tags → verify drift correction |
| `TestRoute53HealthCheck_Reconcile` | Create check → modify externally → reconcile → verify correction |
| `TestRoute53HealthCheck_WithDNSRecord` | Create zone + health check → create failover record → verify HC association |

---

## Health-Check-Specific Design Decisions

### 1. Separate from DNS Records

Health checks are standalone resources with their own lifecycle. A health check can
exist without being referenced by any DNS record, and a DNS record can exist without
a health check. Separating them into distinct drivers follows the Praxis principle
of one-resource-per-Virtual-Object and enables independent management.

### 2. CallerReference Strategy

The driver uses the managed key (VO key) as the caller reference. This provides
natural idempotency: retries after crashes produce the same caller reference and
return the existing health check. If the caller reference matches an existing health
check with a different configuration, AWS returns the existing check — the driver
detects this and uses `UpdateHealthCheck` to converge the configuration.

### 3. requestInterval Immutability

AWS does not allow changing `requestInterval` after creation. If the desired
interval differs from the existing check, the driver has two options:
1. Report as immutable drift and ignore (current behavior).
2. Delete and recreate the health check with the new interval.

Option 1 is chosen because changing the request interval is rare and the health
check ID would change (breaking DNS record references). The user can manually
delete and recreate if needed.

### 4. Tag-Based Ownership

Unlike hosted zones (which have domain names as natural identifiers) and DNS records
(which have natural compound identifiers), health checks have no AWS-enforced unique
name. The driver uses `praxis:managed-key` tag ownership following the EC2/VPC
pattern. Additionally, the driver sets a `Name` tag on the health check with the
logical name from `metadata.name` for AWS Console visibility.

### 5. Health Status is Informational

The observed state includes `HealthCheckStatus` (Healthy/Unhealthy) from
`GetHealthCheckStatus`. This is informational only — it is NOT used for drift
detection or convergence. The health status reflects the endpoint's current state,
not a configuration drift. Including it provides visibility in `praxis get` output.

### 6. Calculated Check Ordering

`childHealthChecks` is compared as a set (not ordered list) during drift detection.
AWS returns child health check IDs in arbitrary order. The driver sorts before
comparison to prevent false drift.

---

## Design Decisions (Resolved)

| # | Decision | Resolution |
|---|---|---|
| 1 | Key format | `healthCheckName` — `KeyScopeGlobal` |
| 2 | Import key | Health check ID — different VO from template management |
| 3 | Ownership | Tag-based: `praxis:managed-key=<name>` |
| 4 | CallerReference | VO key (managed key) for idempotent creation |
| 5 | requestInterval immutability | Report as "(immutable, ignored)" — no auto-recreate |
| 6 | Health status | Informational in observed state, not used for drift |
| 7 | Child health check ordering | Set comparison, not ordered |
| 8 | Name tag | Driver sets `Name` tag for AWS Console visibility |

---

## Checklist

### Schema
- [x] `schemas/aws/route53/health_check.cue`

### Driver
- [x] `internal/drivers/route53healthcheck/types.go`
- [x] `internal/drivers/route53healthcheck/aws.go`
- [x] `internal/drivers/route53healthcheck/drift.go`
- [x] `internal/drivers/route53healthcheck/driver.go`
- [x] `internal/drivers/route53healthcheck/driver_test.go`
- [x] `internal/drivers/route53healthcheck/aws_test.go`
- [x] `internal/drivers/route53healthcheck/drift_test.go`

### Adapter
- [x] `internal/core/provider/route53healthcheck_adapter.go`
- [x] `internal/core/provider/route53healthcheck_adapter_test.go`

### Registry
- [x] Adapter registered in `NewRegistry()`

### Integration Tests
- [x] `tests/integration/route53_health_check_driver_test.go`

### Infrastructure
- [x] `cmd/praxis-dns/main.go` — `.Bind()` call
