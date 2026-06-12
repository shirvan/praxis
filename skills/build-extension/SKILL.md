# Build Extension

**Description**: Create a custom Praxis extension—a standalone service implementing the driver contract in any language.

**When to Use**: You need to manage a resource type not covered by built-in drivers (e.g., Cloudflare DNS, Datadog monitors, PagerDuty services).

**Prerequisites**:
- Read [docs/EXTENDING.md](../../docs/EXTENDING.md) for the extension model
- Read [docs/DRIVERS.md](../../docs/DRIVERS.md) for the 6-handler contract

---

## Steps

### 1. Decide Scope

- **Resource type** (e.g., `cloudflare:dns_record`, `datadog:monitor`)
- **Provider** (the cloud/SaaS platform)
- **Operations**: Provision? Delete? Drift detection? Plan?

### 2. Implement the 6-Handler Contract

Your service must expose these Restate service handlers:

```
{ServiceName}/Provision   — Create or update the resource
{ServiceName}/Delete      — Remove the resource
{ServiceName}/GetStatus   — Return current status and outputs
{ServiceName}/GetOutputs  — Return resolved output fields
{ServiceName}/Reconcile   — Check for drift, return diff
{ServiceName}/Plan        — Dry-run: return planned changes
```

#### Handler Signatures (conceptual)

```
Provision(ctx, key string, spec map[string]any) → (outputs map[string]any, error)
Delete(ctx, key string) → error
GetStatus(ctx, key string) → Status
GetOutputs(ctx, key string) → map[string]any
Reconcile(ctx, key string) → DriftResult
Plan(ctx, key string, spec map[string]any) → PlanResult
```

### 3. Key Scope

Your service stores state per resource keyed by:
```
{deployment_name}/{resource_name}
```

This key arrives as the Restate service key. Use it to isolate state per resource instance.

### 4. Choose a Language

Any language with a Restate SDK:
- **Go** — `github.com/restatedev/sdk-go` (same as Praxis)
- **TypeScript** — `@restatedev/restate-sdk`
- **Python** — `restate` PyPI package
- **Java/Kotlin** — `dev.restate:sdk-api`
- **Rust** — `restate-sdk` crate

### 5. Create the CUE Schema

Define the resource spec schema in CUE for template validation:

```cue
package myextension

#DNSRecord: {
    apiVersion: "cloudflare/v1"
    kind:       "DNSRecord"
    metadata:   #Metadata
    spec: {
        zone_id:  string
        name:     string
        type:     "A" | "AAAA" | "CNAME" | "MX" | "TXT"
        content:  string
        ttl?:     int & >=60
        proxied?: bool
    }
}
```

### 6. Register with Restate

When deploying your extension, register the service endpoint with Restate so the orchestrator can invoke it:

```bash
curl -X POST http://localhost:9070/deployments \
  -H "Content-Type: application/json" \
  -d '{"uri": "http://my-extension:9080"}'
```

### 7. Wire into Templates

Once registered, templates can reference your resource kind:

```cue
resources: dns: {
    apiVersion: "cloudflare/v1"
    kind:       "DNSRecord"
    spec: {
        zone_id: "abc123"
        name:    "api.example.com"
        type:    "A"
        content: "1.2.3.4"
    }
}
```

### 8. Add Docker Compose Entry (for local dev)

```yaml
praxis-myextension:
  build: .
  ports:
    - "9085:9085"
  environment:
    CLOUDFLARE_API_TOKEN: ${CLOUDFLARE_API_TOKEN}
```

Register in Restate (one call to the admin API, same as `just register`):
```bash
curl -X POST http://localhost:9070/deployments \
  -H 'content-type: application/json' \
  -d '{"uri": "http://praxis-myextension:9085"}' | jq .
```

---

## Verification Checklist

- [ ] All 6 handlers respond correctly
- [ ] Provision is idempotent (re-provision same key = no error)
- [ ] Delete handles "already deleted" gracefully
- [ ] Reconcile returns meaningful diffs
- [ ] GetOutputs returns fields that other resources can reference
- [ ] CUE schema validates correct specs and rejects invalid ones
- [ ] Service registers with Restate and appears in `GET /services`

## Example: Minimal Go Extension

```go
package main

import (
    "context"
    restate "github.com/restatedev/sdk-go"
    "github.com/restatedev/sdk-go/server"
)

type DNSRecordDriver struct{}

func (d *DNSRecordDriver) Provision(ctx restate.ObjectContext, spec map[string]any) (map[string]any, error) {
    key := restate.Key(ctx)
    // Call Cloudflare API to create/update record
    // Store state via restate.Set(ctx, "status", "ready")
    return map[string]any{"record_id": "cf-123"}, nil
}

// ... implement other 5 handlers ...

func main() {
    srv := server.NewRestate().
        Bind(restate.NewObject("CloudflareDNSRecord").
            Handler("Provision", restate.NewObjectHandler(/* ... */)).
            // ... other handlers
        )
    srv.Start(context.Background(), ":9085")
}
```

## See Also

- [docs/EXTENDING.md](../../docs/EXTENDING.md) — Full extension reference
- [docs/DRIVERS.md](../../docs/DRIVERS.md) — Driver contract details
- [skills/implement-driver/SKILL.md](../implement-driver/SKILL.md) — Go driver implementation (for built-in drivers)
