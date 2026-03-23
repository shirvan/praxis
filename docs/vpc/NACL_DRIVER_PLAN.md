# Network ACL Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages AWS Network ACLs, following
> the exact patterns established by the VPC, SG, IGW, and EIP drivers.
>
> Key scope: `KeyScopeCustom` — key format is `vpcId~metadata.name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned Network ACL ID
> lives only in state/outputs.

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
12. [Step 9 — Binary Entry Point & Dockerfile](#step-9--binary-entry-point--dockerfile)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [Network ACL-Specific Design Decisions](#network-acl-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The Network ACL driver manages the lifecycle of AWS **Network ACLs** (NACLs). A
Network ACL is a stateless firewall at the subnet level that controls inbound and
outbound traffic with numbered rules evaluated in order.

### Why Network ACLs

Network ACLs provide defense-in-depth for VPC security:

- **Stateless filtering**: Unlike security groups (stateful), NACLs evaluate each
  packet independently — inbound and outbound rules are separate.
- **Subnet-level control**: NACLs are associated with subnets, providing a broad
  security boundary. Security groups operate at the instance level.
- **Deny rules**: NACLs support explicit deny rules. Security groups only support
  allow rules (implicit deny).
- **Rule ordering**: NACLs evaluate rules in numerical order, stopping at the first
  match. This enables precise traffic control with priority-based rules.

### Resource Scope for This Plan

| In Scope | Out of Scope |
|---|---|
| Network ACL creation, deletion | Subnet management |
| Ingress and egress rules (numbered) | VPC management |
| Subnet associations | Security group management |
| Tags | VPC Flow Logs |
| Default NACL protection | |
| Import and drift detection | |
| Ownership tag enforcement | |

### Network ACL Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create NACL with rules and associations |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing NACL |
| `Delete` | `ObjectContext` (exclusive) | Delete NACL (blocked for Observed/default) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return NACL outputs |

### Downstream Consumers

```text
${resources.my-nacl.outputs.networkAclId}  → Subnet associations (informational)
```

---

## 2. Key Strategy

### Key Format: `vpcId~metadata.name`

Network ACLs are scoped to a VPC. NACL names are unique within a VPC but not
globally. This mirrors the Security Group driver's key strategy.

1. **BuildKey**: returns `vpcId~metadata.name`.
2. **BuildImportKey**: returns `region~networkAclId`.
3. **Import**: `ModeObserved` by default — deleting a NACL reassociates subnets
   to the default NACL, potentially exposing them to different traffic rules.

### Conflict Enforcement via Ownership Tags

Same pattern: `praxis:managed-key = <vpcId~metadata.name>` written at creation.

---

## 3. File Inventory

```text
✦ internal/drivers/nacl/types.go             — Spec, Outputs, ObservedState, State
✦ internal/drivers/nacl/aws.go               — NetworkACLAPI interface + realNetworkACLAPI
✦ internal/drivers/nacl/drift.go             — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/nacl/driver.go            — NetworkACLDriver Virtual Object
✦ internal/drivers/nacl/driver_test.go       — Unit tests for driver
✦ internal/drivers/nacl/aws_test.go          — Unit tests for error classification
✦ internal/drivers/nacl/drift_test.go        — Unit tests for drift detection
✦ internal/core/provider/nacl_adapter.go     — NetworkACLAdapter
✦ internal/core/provider/nacl_adapter_test.go — Unit tests for adapter
✦ schemas/aws/nacl/nacl.cue                  — CUE schema
✦ tests/integration/nacl_driver_test.go       — Integration tests
✎ cmd/praxis-network/main.go                 — Add NetworkACL driver .Bind()
✎ internal/core/provider/registry.go          — Add NewNetworkACLAdapter
✎ justfile                                    — Add nacl test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/nacl/nacl.cue`

```cue
package nacl

#NetworkACL: {
    apiVersion: "praxis.io/v1"
    kind:       "NetworkACL"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region.
        region: string

        // vpcId is the VPC to create the NACL in.
        // Immutable after creation.
        vpcId: string

        // ingressRules are the inbound rules evaluated in ruleNumber order.
        // Rule numbers range from 1-32766. Lower numbers are evaluated first.
        // The implicit deny-all rule (32767, *) is always present and cannot
        // be managed.
        ingressRules: [...#NetworkACLRule]

        // egressRules are the outbound rules evaluated in ruleNumber order.
        // Same numbering rules as ingressRules.
        egressRules: [...#NetworkACLRule]

        // subnetAssociations is the list of subnet IDs to associate with this NACL.
        // A subnet can only be associated with one NACL at a time.
        // Associating a subnet here implicitly disassociates it from its previous NACL.
        subnetAssociations: [...string]

        // tags applied to the Network ACL resource.
        tags: [string]: string
    }

    outputs?: {
        networkAclId: string
        vpcId:        string
        isDefault:    bool
        ingressRules: [...#NetworkACLRuleOutput]
        egressRules:  [...#NetworkACLRuleOutput]
        associations: [...#NetworkACLAssociationOutput]
    }
}

#NetworkACLRule: {
    // ruleNumber determines evaluation order (1-32766, lower first).
    ruleNumber: int & >=1 & <=32766

    // protocol is the IP protocol number.
    // -1 = all protocols, 6 = TCP, 17 = UDP, 1 = ICMP.
    protocol: string

    // ruleAction is "allow" or "deny".
    ruleAction: "allow" | "deny"

    // cidrBlock is the IPv4 CIDR range for the rule.
    cidrBlock: string

    // fromPort is the start of the port range (inclusive).
    // For ICMP: the ICMP type (-1 for all).
    // For all protocols (-1): must be 0.
    fromPort?: int

    // toPort is the end of the port range (inclusive).
    // For ICMP: the ICMP code (-1 for all).
    // For all protocols (-1): must be 0.
    toPort?: int
}

#NetworkACLRuleOutput: {
    ruleNumber: int
    protocol:   string
    ruleAction: string
    cidrBlock:  string
    fromPort:   int
    toPort:     int
}

#NetworkACLAssociationOutput: {
    associationId: string
    subnetId:      string
}
```

**Key decisions**:

- Rules are ordered by `ruleNumber`. The CUE schema validates 1-32766.
- Protocol is a string (not int) to allow "-1" for all protocols and named
  protocols. The driver normalizes to protocol numbers internally.
- IPv6 rules (`ipv6CidrBlock`) are omitted from v1 for simplicity.
- ICMP type/code use the same `fromPort`/`toPort` fields (AWS API convention).
- Subnet associations are an explicit list — the driver manages the association
  lifecycle.

---

## Step 2 — AWS Client Factory

**NO CHANGES NEEDED** — uses `NewEC2Client`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/nacl/types.go`

```go
package nacl

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "NetworkACL"

type NetworkACLSpec struct {
    Account            string            `json:"account,omitempty"`
    Region             string            `json:"region"`
    VpcId              string            `json:"vpcId"`
    IngressRules       []NetworkACLRule  `json:"ingressRules,omitempty"`
    EgressRules        []NetworkACLRule  `json:"egressRules,omitempty"`
    SubnetAssociations []string          `json:"subnetAssociations,omitempty"`
    Tags               map[string]string `json:"tags,omitempty"`
    ManagedKey         string            `json:"managedKey,omitempty"`
}

type NetworkACLRule struct {
    RuleNumber int    `json:"ruleNumber"`
    Protocol   string `json:"protocol"`
    RuleAction string `json:"ruleAction"`
    CidrBlock  string `json:"cidrBlock"`
    FromPort   int    `json:"fromPort,omitempty"`
    ToPort     int    `json:"toPort,omitempty"`
}

type NetworkACLOutputs struct {
    NetworkAclId string                  `json:"networkAclId"`
    VpcId        string                  `json:"vpcId"`
    IsDefault    bool                    `json:"isDefault"`
    IngressRules []NetworkACLRule        `json:"ingressRules"`
    EgressRules  []NetworkACLRule        `json:"egressRules"`
    Associations []NetworkACLAssociation `json:"associations"`
}

type NetworkACLAssociation struct {
    AssociationId string `json:"associationId"`
    SubnetId      string `json:"subnetId"`
}

type ObservedState struct {
    NetworkAclId string                  `json:"networkAclId"`
    VpcId        string                  `json:"vpcId"`
    IsDefault    bool                    `json:"isDefault"`
    IngressRules []NetworkACLRule        `json:"ingressRules"`
    EgressRules  []NetworkACLRule        `json:"egressRules"`
    Associations []NetworkACLAssociation `json:"associations"`
    Tags         map[string]string       `json:"tags"`
}

type NetworkACLState struct {
    Desired            NetworkACLSpec       `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            NetworkACLOutputs    `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/nacl/aws.go`

### NetworkACLAPI Interface

```go
type NetworkACLAPI interface {
    // CreateNetworkACL creates a new Network ACL in the specified VPC.
    // Returns the Network ACL ID.
    CreateNetworkACL(ctx context.Context, spec NetworkACLSpec) (string, error)

    // DescribeNetworkACL returns the full observed state including rules
    // and associations.
    DescribeNetworkACL(ctx context.Context, networkAclId string) (ObservedState, error)

    // DeleteNetworkACL deletes a Network ACL.
    // Fails if subnets are still associated (must disassociate first).
    // Fails if this is the default NACL.
    DeleteNetworkACL(ctx context.Context, networkAclId string) error

    // CreateEntry adds a rule to the NACL.
    CreateEntry(ctx context.Context, networkAclId string, rule NetworkACLRule, egress bool) error

    // DeleteEntry removes a rule from the NACL.
    DeleteEntry(ctx context.Context, networkAclId string, ruleNumber int, egress bool) error

    // ReplaceEntry replaces a rule in the NACL (same rule number, different params).
    ReplaceEntry(ctx context.Context, networkAclId string, rule NetworkACLRule, egress bool) error

    // ReplaceNetworkACLAssociation changes the NACL associated with a subnet.
    // Returns the new association ID.
    // The subnet must already be associated with a NACL (always true — all
    // subnets are associated with the default NACL if not explicitly associated).
    ReplaceNetworkACLAssociation(ctx context.Context, associationId string, networkAclId string) (string, error)

    // UpdateTags replaces user-managed tags.
    UpdateTags(ctx context.Context, networkAclId string, tags map[string]string) error

    // FindByManagedKey searches for NACLs tagged with praxis:managed-key.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)

    // FindAssociationIdForSubnet returns the current NACL association ID
    // for a given subnet. Needed to call ReplaceNetworkACLAssociation.
    FindAssociationIdForSubnet(ctx context.Context, subnetId string) (string, error)
}
```

### Implementation Notes

- `CreateNetworkACL`: Uses `CreateNetworkAcl` API with `TagSpecifications`. A
  newly created NACL has only the implicit deny-all rules (rule 32767, * DENY).
  Rules are added separately via `CreateEntry`.
- `DescribeNetworkACL`: Uses `DescribeNetworkAcls`. Returns all rules, filtering
  out the implicit deny-all rule (ruleNumber 32767) from the observed rules since
  it cannot be managed. Also returns all subnet associations.
- `DeleteNetworkACL`: Uses `DeleteNetworkAcl`. Prerequisites: all subnets must be
  disassociated first (reassociated to the default NACL).
- `CreateEntry`: Uses `CreateNetworkAclEntry`. Adds an individual rule.
- `DeleteEntry`: Uses `DeleteNetworkAclEntry`. Removes by rule number + direction.
- `ReplaceEntry`: Uses `ReplaceNetworkAclEntry`. Updates rule at same number.
- `ReplaceNetworkACLAssociation`: Uses `ReplaceNetworkAclAssociation`. Swaps which
  NACL a subnet belongs to. AWS requires the current association ID, not the subnet
  ID, so the driver must look up the association first.
- `FindAssociationIdForSubnet`: Describes ALL NACLs in the VPC and finds the one
  associated with the target subnet. Required for `ReplaceNetworkACLAssociation`.

### Error Classification

```go
func IsNotFound(err error) bool        // "InvalidNetworkAclID.NotFound"
func IsInUse(err error) bool           // Subnets still associated
func IsDefaultACL(err error) bool      // Cannot delete default NACL
func IsDuplicateRule(err error) bool   // Rule number already exists
func IsRuleNotFound(err error) bool    // Rule number doesn't exist for delete
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/nacl/drift.go`

### Drift Rules

| Field | Mutable? | Drift Checked? | How Corrected |
|---|---|---|---|
| `tags` | Yes | **Yes** | CreateTags / DeleteTags |
| `ingressRules` | Yes | **Yes** | Create/Delete/ReplaceEntry |
| `egressRules` | Yes | **Yes** | Create/Delete/ReplaceEntry |
| `subnetAssociations` | Yes | **Yes** | ReplaceNetworkACLAssociation |
| `vpcId` | **No** | **No** | Requires replacement |

### Rule Drift Detection

Rules are keyed by `ruleNumber` + direction (ingress/egress). This is the same
set-based approach used by the Route Table driver (keyed on `destinationCidrBlock`)
and the SG driver (keyed on rule identity).

For each direction (ingress and egress):

1. Build a map of desired rules keyed by `ruleNumber`.
2. Build a map of observed rules keyed by `ruleNumber`.
3. **Added**: rules in desired but not observed → `CreateEntry`.
4. **Removed**: rules in observed but not desired → `DeleteEntry`.
5. **Changed**: rules in both but with different parameters → `ReplaceEntry`.

### Implicit Deny-All Rule (32767)

The implicit deny-all rule (ruleNumber 32767, protocol -1, deny 0.0.0.0/0) is
ALWAYS present and CANNOT be created, modified, or deleted. It is:

- **Excluded from observed state**: `DescribeNetworkACL` filters it out.
- **Excluded from drift detection**: The drift engine never sees it.
- **Excluded from CUE schema validation**: ruleNumber max is 32766.

### Association Drift Detection

Subnet associations are compared as sets:

1. **Desired associations**: subnets listed in `spec.subnetAssociations`.
2. **Observed associations**: subnets from `DescribeNetworkACL`.
3. **Added**: subnets in desired but not observed → `ReplaceNetworkACLAssociation`.
4. **Removed**: subnets in observed but not desired → Reassociate to default NACL.

```go
func HasDrift(desired NetworkACLSpec, observed ObservedState) bool {
    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }
    if !rulesMatch(desired.IngressRules, observed.IngressRules) {
        return true
    }
    if !rulesMatch(desired.EgressRules, observed.EgressRules) {
        return true
    }
    if !associationsMatch(desired.SubnetAssociations, observed.Associations) {
        return true
    }
    return false
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/nacl/driver.go`

### Provision

1. Input validation: `region`, `vpcId` required. Rule numbers in range 1-32766.
   No duplicate rule numbers within the same direction. Validate `ruleAction`
   is "allow" or "deny". Validate `protocol` is valid.
2. Load current state. If NACL exists, verify via Describe.
3. Pre-flight ownership check via `FindByManagedKey`.
4. Create NACL if new: `CreateNetworkACL`.
5. Add rules: For each ingress and egress rule, call `CreateEntry` in a
   separate `restate.Run()`. Rules are added in order of `ruleNumber`.
6. Associate subnets: For each subnet, call `ReplaceNetworkACLAssociation`.
7. Re-provision path: converge rules (add/remove/replace), associations, and tags.
8. Final describe → build outputs → commit state.
9. Schedule reconcile.

> **Rule convergence order**: When updating rules, the driver follows
> add-before-remove ordering (same as SG and Route Table). This prevents a
> transient window where traffic is unexpectedly denied. New allow rules are
> added first, then old rules are removed, then modified rules are replaced.

### Delete

1. Block `ModeObserved` (409).
2. Block default NACL deletion (409 with descriptive error).
3. Disassociate all subnets first: For each associated subnet, reassociate it
   to the VPC's default NACL via `ReplaceNetworkACLAssociation`.
4. Delete NACL: `DeleteNetworkACL`.
5. Set tombstone state.

> **Subnet reassociation on delete**: When a NACL is deleted, its associated
> subnets must be moved somewhere. AWS requires reassociation to another NACL —
> the driver reassociates to the VPC's default NACL. This is safe because the
> default NACL always exists and has permissive default rules.

### Import

1. Describe NACL by ID.
2. Synthesize spec from observed state.
3. Handle default NACL: If the NACL is the VPC's default, mark in outputs
   (`isDefault: true`). Default NACLs cannot be deleted.
4. Return outputs, set `ModeObserved`.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/nacl_adapter.go`

- **Key Scope**: `KeyScopeCustom`
- **BuildKey**: `JoinKey(spec.VpcId, metadata.name)`
- **BuildImportKey**: `JoinKey(region, networkAclId)`

---

## Step 8 — Registry Integration

Add `NewNetworkACLAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Binary Entry Point & Dockerfile

Add `.Bind(restate.Reflect(nacl.NewNetworkACLDriver(cfg.Auth())))` to
`cmd/praxis-network/main.go`.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes. Justfile:

```makefile
test-nacl:
    go test ./internal/drivers/nacl/... -v -count=1 -race
```

---

## Step 11 — Unit Tests

### `internal/drivers/nacl/driver_test.go`

1. `TestProvision_CreatesNACL` — happy path with rules and associations.
2. `TestProvision_EmptyRules` — NACL with no custom rules (only implicit deny).
3. `TestProvision_MissingVpcIdFails` — terminal error 400.
4. `TestProvision_DuplicateRuleNumberFails` — terminal error 400.
5. `TestProvision_InvalidRuleNumberFails` — 0 or 32767+ → terminal error 400.
6. `TestProvision_IdempotentReprovision` — no duplicate NACL.
7. `TestProvision_RuleAdded` — new rule added on reprovision.
8. `TestProvision_RuleRemoved` — stale rule removed on reprovision.
9. `TestProvision_RuleReplaced` — same rule number, different params.
10. `TestProvision_AssociationAdded` — new subnet associated.
11. `TestProvision_AssociationRemoved` — subnet reassociated to default.
12. `TestProvision_TagUpdate` — tags converged.
13. `TestProvision_ConflictFails` — FindByManagedKey → 409.
14. `TestImport_ExistingNACL` — describes, synthesizes spec, returns outputs.
15. `TestImport_DefaultNACL` — isDefault: true in outputs.
16. `TestImport_NotFoundFails` — terminal error 404.
17. `TestImport_DefaultsToObservedMode`.
18. `TestDelete_DisassociatesAndDeletes`.
19. `TestDelete_AlreadyDeleted` — IsNotFound → success.
20. `TestDelete_DefaultNACLBlocked` — terminal error 409.
21. `TestDelete_ObservedModeBlocked` — terminal error 409.
22. `TestReconcile_NoDrift` — no changes.
23. `TestReconcile_DetectsRuleDrift` — rules corrected.
24. `TestReconcile_DetectsAssociationDrift` — associations corrected.
25. `TestReconcile_DetectsTagDrift` — tags corrected.
26. `TestReconcile_ObservedModeReportsOnly`.
27. `TestGetStatus_ReturnsCurrentState`.
28. `TestGetOutputs_ReturnsOutputs`.

### `internal/drivers/nacl/drift_test.go`

1. `TestHasDrift_NoDrift` — identical → false.
2. `TestHasDrift_IngressRuleAdded` → true.
3. `TestHasDrift_IngressRuleRemoved` → true.
4. `TestHasDrift_IngressRuleChanged` → true.
5. `TestHasDrift_EgressRuleDrift` → true.
6. `TestHasDrift_TagChanged` → true.
7. `TestHasDrift_AssociationChanged` → true.
8. `TestHasDrift_ImplicitDenyExcluded` — rule 32767 never triggers drift.
9. `TestRulesMatch_OrderIndependent` — rules match regardless of array order.
10. `TestAssociationsMatch_SetBased` — order independent.
11. `TestComputeFieldDiffs_IngressRules` — detailed per-rule diffs.
12. `TestComputeFieldDiffs_EgressRules`.
13. `TestComputeFieldDiffs_Associations`.
14. `TestComputeFieldDiffs_Tags`.
15. `TestTagsMatch_IgnoresPraxisTags`.

---

## Step 12 — Integration Tests

**File**: `tests/integration/nacl_driver_test.go`

1. **TestNACLProvision_CreatesNACL** — Creates VPC, provisions NACL with
   ingress/egress rules, verifies via DescribeNetworkAcls.
2. **TestNACLProvision_WithAssociation** — Creates VPC + subnet, provisions NACL,
   associates subnet, verifies association.
3. **TestNACLProvision_Idempotent** — Two provisions, same outputs.
4. **TestNACLProvision_RuleConvergence** — Adds, removes, replaces rules.
5. **TestNACLImport_Existing** — Creates NACL via SDK, imports.
6. **TestNACLImport_DefaultNACL** — Imports the VPC's default NACL.
7. **TestNACLDelete_DisassociatesAndDeletes** — Provisions with associations,
   deletes, verifies subnets returned to default NACL.
8. **TestNACLDelete_DefaultBlocked** — Import default NACL, attempt delete → 409.
9. **TestNACLReconcile_RuleDrift** — Changes rules, reconcile corrects.
10. **TestNACLReconcile_AssociationDrift** — Manually reassociates subnet,
    reconcile corrects.
11. **TestNACLGetStatus_ReturnsReady**.

---

## Network ACL-Specific Design Decisions

### 1. Stateless Firewall vs. Security Groups

NACLs and Security Groups complement each other but serve different purposes:

| Feature | Security Group | Network ACL |
|---|---|---|
| Level | Instance (ENI) | Subnet |
| Statefulness | Stateful (return traffic auto-allowed) | Stateless (explicit rules both ways) |
| Deny rules | No (implicit deny only) | Yes (explicit deny) |
| Rule ordering | All rules evaluated | Numbered, first-match wins |
| Default | Allow all outbound, deny all inbound | Allow all (default NACL) |

The NACL driver follows the same sub-resource pattern as the SG driver (rules
are managed as part of the NACL) and the Route Table driver (routes + associations).

### 2. Rule Numbering and Ordering

NACL rules are evaluated in numerical order (lowest first). The first rule that
matches a packet is applied, and subsequent rules are skipped. This makes rule
numbering critical:

```text
Rule 100: ALLOW TCP 443 from 0.0.0.0/0     ← evaluated first
Rule 200: DENY  TCP 443 from 10.0.0.0/8    ← never reached for 10.x traffic
          (because rule 100 already allowed it)
*:        DENY  all from 0.0.0.0/0          ← implicit catch-all (rule 32767)
```

The driver preserves rule numbers exactly as specified. It does NOT auto-assign
rule numbers or reorder rules. Users must carefully plan their rule numbering.

**Best practice**: Use increments of 100 (100, 200, 300...) to leave room for
inserting rules later.

### 3. Default NACL Protection

Every VPC has a default NACL that cannot be deleted. When a subnet is created
without an explicit NACL association, it's automatically associated with the
default NACL.

The driver:

- **Import**: Supports importing the default NACL. Sets `isDefault: true` in
  outputs. Import mode is `ModeObserved`.
- **Delete**: Blocks deletion of the default NACL with a terminal 409 error.
- **Provision**: Cannot create a new default NACL — only one exists per VPC.
  Provisioning always creates a custom NACL.

Default NACL rules can be MODIFIED via import + reconcile (in Managed mode).
This is a common use case: hardening the default NACL by replacing permissive
rules with restrictive ones.

### 4. Subnet Association Model

A subnet is ALWAYS associated with exactly one NACL. Changing a subnet's NACL
is done via `ReplaceNetworkAclAssociation`, which atomically swaps the association.

The driver manages subnet associations as a set:

- **Association**: Call `ReplaceNetworkAclAssociation` to associate the subnet
  with this NACL (moving it from its current NACL).
- **Disassociation**: Call `ReplaceNetworkAclAssociation` to move the subnet
  to the VPC's default NACL.

> **Cross-NACL conflict**: A subnet can only be in one NACL. If NACL-A and NACL-B
> both claim the same subnet, the last provision wins. The driver does NOT detect
> this conflict — it's the user's responsibility to ensure each subnet appears in
> at most one NACL's `subnetAssociations`.

### 5. Delete Prerequisites

Deleting a NACL requires all subnet associations to be removed first. The driver
handles this automatically:

1. Describe the NACL to get current associations.
2. For each associated subnet, reassociate it to the VPC's default NACL.
3. Delete the NACL.

Each reassociation is a separate `restate.Run()` call for durability.

### 6. Rule Convergence Strategy

Same add-before-remove strategy as the SG and Route Table drivers:

1. **Identify diffs**: Compare desired rules with observed rules by `ruleNumber`.
2. **Add new rules**: `CreateEntry` for rules in desired but not observed.
3. **Replace changed rules**: `ReplaceEntry` for rules with same number but
   different parameters.
4. **Remove stale rules**: `DeleteEntry` for rules in observed but not desired.

This ordering ensures:

- New allow rules are active before old ones are removed.
- There's no transient deny-all window during rule updates.

Each rule operation is a separate `restate.Run()` call.

### 7. ICMP Rules

NACL rules support ICMP with special semantics:

- `protocol: "1"` (ICMP)
- `fromPort`: ICMP type (-1 for all types)
- `toPort`: ICMP code (-1 for all codes)

Common ICMP rules:

```text
Rule 110: ALLOW ICMP type -1 code -1 from 0.0.0.0/0   (all ICMP)
Rule 120: ALLOW ICMP type 8  code -1 from 0.0.0.0/0   (echo request / ping)
```

The driver passes ICMP type/code through `fromPort`/`toPort` without special
handling — this matches the AWS API convention.

---

## Design Decisions (Resolved)

1. **Should the driver manage the implicit deny-all rule?**
   No. Rule 32767 cannot be created, modified, or deleted. It is always present.
   The driver filters it from observed state and never includes it in drift
   detection. Users should not reference it in their specs.

2. **Should rules be a separate resource type?**
   No. NACL rules don't have independent identity beyond their NACL + direction +
   rule number. They're managed as sub-resources of the NACL (same pattern as SG
   rules and route table routes).

3. **Should the driver support IPv6 rules?**
   Not in v1. IPv6 NACL rules use separate `ipv6CidrBlock` field. Deferring to
   v2 to keep the initial implementation focused.

4. **Should the driver validate rule number gaps?**
   No. The driver accepts any valid rule numbers (1-32766). Suggesting increments
   of 100 is a documentation best practice, not an enforcement.

5. **Should the driver auto-reassociate subnets on delete?**
   Yes. Moving subnets to the default NACL on delete is the only safe option.
   Without this, `DeleteNetworkAcl` would fail. The alternative — forcing users
   to manually disassociate — increases the risk of failed deletes and orphaned
   resources.

6. **Should the driver detect cross-NACL subnet conflicts?**
   No. This requires querying all NACLs in the VPC and comparing with all other
   driver states. The complexity is not justified. The last provision wins, and
   reconcile will correct drift on the other NACL.

7. **Should the driver sort rules in outputs?**
   Yes. Output rules are sorted by `ruleNumber` ascending for consistent output
   regardless of AWS API ordering.

---

## Example Template

```cue
import (
    "praxis.io/schemas/aws/vpc"
    "praxis.io/schemas/aws/subnet"
    "praxis.io/schemas/aws/nacl"
)

resources: {
    "my-vpc": vpc.#VPC & {
        apiVersion: "praxis.io/v1"
        kind:       "VPC"
        metadata: name: "app-vpc"
        spec: {
            region:    "\(variables.region)"
            cidrBlock: "10.0.0.0/16"
            tags: { Name: "app-vpc" }
        }
    }

    "public-subnet": subnet.#Subnet & {
        apiVersion: "praxis.io/v1"
        kind:       "Subnet"
        metadata: name: "public-a"
        spec: {
            region:           "\(variables.region)"
            vpcId:            "${resources.my-vpc.outputs.vpcId}"
            cidrBlock:        "10.0.1.0/24"
            availabilityZone: "\(variables.region)a"
            tags: { Name: "public-a" }
        }
    }

    "public-nacl": nacl.#NetworkACL & {
        apiVersion: "praxis.io/v1"
        kind:       "NetworkACL"
        metadata: name: "public-nacl"
        spec: {
            region: "\(variables.region)"
            vpcId:  "${resources.my-vpc.outputs.vpcId}"

            ingressRules: [
                {
                    ruleNumber: 100
                    protocol:   "6"         // TCP
                    ruleAction: "allow"
                    cidrBlock:  "0.0.0.0/0"
                    fromPort:   443
                    toPort:     443
                },
                {
                    ruleNumber: 200
                    protocol:   "6"         // TCP
                    ruleAction: "allow"
                    cidrBlock:  "0.0.0.0/0"
                    fromPort:   80
                    toPort:     80
                },
                {
                    ruleNumber: 300
                    protocol:   "6"         // TCP
                    ruleAction: "allow"
                    cidrBlock:  "0.0.0.0/0"
                    fromPort:   1024
                    toPort:     65535       // ephemeral ports for return traffic
                },
                {
                    ruleNumber: 900
                    protocol:   "-1"        // all protocols
                    ruleAction: "deny"
                    cidrBlock:  "10.0.0.0/8"
                },
            ]

            egressRules: [
                {
                    ruleNumber: 100
                    protocol:   "-1"        // all protocols
                    ruleAction: "allow"
                    cidrBlock:  "0.0.0.0/0"
                },
            ]

            subnetAssociations: [
                "${resources.public-subnet.outputs.subnetId}",
            ]

            tags: {
                Name:        "public-nacl"
                Environment: "production"
            }
        }
    }
}

variables: {
    region: string | *"us-east-1"
}
```

---

## Checklist

- [x] **Schema**: `schemas/aws/nacl/nacl.cue` created
- [x] **Types**: `internal/drivers/nacl/types.go` created
- [x] **AWS API**: `internal/drivers/nacl/aws.go` created
- [x] **Drift**: `internal/drivers/nacl/drift.go` created
- [x] **Driver**: `internal/drivers/nacl/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/nacl_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: `.Bind()` added to `cmd/praxis-network/main.go`
- [x] **Justfile**: Updated with nacl test targets
- [x] **Unit tests**: driver (16 tests), drift (6 tests), aws helpers (7 tests) created
- [x] **Unit tests (adapter)**: `internal/core/provider/nacl_adapter_test.go` created
- [x] **Integration tests**: `tests/integration/nacl_driver_test.go` created (11 tests)
- [x] **Conflict check**: `FindByManagedKey` filters out deleted NACLs
- [x] **Ownership tag**: `praxis:managed-key` written at creation
- [x] **Import default mode**: ModeObserved
- [x] **Delete mode guard**: Blocks deletion for ModeObserved (409)
- [x] **Default NACL guard**: Blocks deletion of default NACL (409)
- [x] **Delete reassociates**: Subnets moved to default NACL before delete
- [x] **Rule convergence**: Add-before-remove ordering
- [x] **Implicit deny excluded**: Rule 32767 filtered from observed state
- [x] **Association management**: ReplaceNetworkAclAssociation for associations
- [x] **Build passes**: `go build ./...`
- [x] **Unit tests pass**: `go test ./internal/drivers/nacl/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestNACL -tags=integration`
