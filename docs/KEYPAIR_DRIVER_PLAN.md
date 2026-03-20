# Key Pair Driver — Implementation Plan

> **Status: Not yet implemented.** This document is a plan only.

> Target: A Restate Virtual Object driver that manages EC2 key pairs, following the
> exact patterns established by the S3, Security Group, EC2, VPC, EBS, and Elastic
> IP drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~keyName`, permanent and
> immutable for the lifetime of the Virtual Object. Unlike other region-scoped
> drivers, the key uses the AWS key pair name directly (not `metadata.name`) because
> key pair names are already unique within a region.

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
12. [Step 9 — Compute Driver Pack Entry Point](#step-9--compute-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [KeyPair-Specific Design Decisions](#keypair-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The Key Pair driver manages the lifecycle of EC2 **key pairs** only. The driver
handles key pair creation (AWS-generated or user-imported public key material),
tagging, import of existing key pairs, deletion, and drift reconciliation.

Key pairs are a foundational compute resource — they are referenced by EC2 instances
at launch time for SSH access. The key pair must exist before the instance is
launched. In compound templates, the key pair resource is a dependency of the EC2
instance resource, and the DAG ensures creation ordering.

### Important Security Constraint

When AWS generates a key pair (`CreateKeyPair`), the private key material is returned
**exactly once** in the API response. There is no way to retrieve it again. The
driver stores the private key fingerprint in outputs but does **NOT** store the
private key material in Restate state. The private key is returned to the caller
from the Provision handler response and must be captured at that moment.

For the "import public key" flow (`ImportKeyPair`), no private key is involved —
the user already has their key and provides only the public half.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or import a key pair |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing key pair |
| `Delete` | `ObjectContext` (exclusive) | Delete a key pair |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return key pair outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `keyName` | Immutable | The key pair's unique name within a region |
| `keyType` | Immutable | `rsa` or `ed25519`, set at creation |
| `publicKeyMaterial` | Immutable | Set at creation (import flow only) |
| `keyFingerprint` | Immutable | Computed by AWS at creation |
| `tags` | Mutable | Full replace via `CreateTags` / `DeleteTags` |

Like Elastic IPs, key pairs have minimal mutable state — only tags can drift.

### Downstream Consumers

```
${resources.my-keypair.outputs.keyName}        → EC2 instance spec.keyName
${resources.my-keypair.outputs.keyPairId}      → IAM policies, audit references
${resources.my-keypair.outputs.keyFingerprint} → SSH verification
```

---

## 2. Key Strategy

### Key Format: `region~keyName`

Key pair names are unique within a region in AWS. Unlike EC2 instances or VPCs,
where there is no stable user-visible AWS identifier, key pairs *do* have a stable
user-assigned name. However, we still use `region~keyName` (not just `keyName`)
because key pairs are region-scoped, not global.

The CUE schema maps `metadata.name` to `keyName` in the spec. The adapter's
`BuildKey` extracts both and produces the key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name` where
   `metadata.name` = the desired key pair name.
2. **Provision / Delete**: dispatched to same key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs exist, describes the key
   pair by name (unlike EC2/VPC which describe by ID, key pairs *can* be described
   by name because key names are stable and unique).
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the key pair name. Since key names are unique within a
   region, import and template-based management produce the **same key** for the
   same key pair — matching the S3 pattern, not the EC2/VPC pattern.

### BuildImportKey Produces the Same Key as BuildKey

This is the same pattern as S3: the AWS resource identifier (key pair name) is the
same as the Praxis logical name. Import and template management converge on the same
Virtual Object. This is correct because:

- Key pair names are unique within a region (AWS-enforced).
- There is no separate AWS-assigned ID that differs from the name (there is a
  `keyPairId`, but the name is the primary identifier for all API operations).
- Importing a key pair by name should produce the same VO as managing it via a
  template with the same name — preventing dual-control issues.

### No Ownership Tags

Key pairs do not need `praxis:managed-key` ownership tags because:

1. Key pair names are AWS-unique within a region (creating a duplicate name fails
   with `InvalidKeyPair.Duplicate`).
2. `BuildImportKey` and `BuildKey` produce the same key, so there's no risk of
   two VOs targeting the same AWS resource.
3. `CreateKeyPair` returns a duplicate error if the name exists — this is a natural
   conflict signal.

This follows the S3 pattern (bucket names are globally unique, no ownership tags
needed) rather than the EC2/VPC pattern.

---

## 3. File Inventory

```text
✦ internal/drivers/keypair/types.go            — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/keypair/aws.go              — KeyPairAPI interface + realKeyPairAPI
✦ internal/drivers/keypair/drift.go            — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/keypair/driver.go           — KeyPairDriver Virtual Object
✦ internal/drivers/keypair/driver_test.go      — Unit tests for driver (mocked AWS)
✦ internal/drivers/keypair/aws_test.go         — Unit tests for error classification
✦ internal/drivers/keypair/drift_test.go       — Unit tests for drift detection
✦ internal/core/provider/keypair_adapter.go    — KeyPairAdapter implementing provider.Adapter
✦ internal/core/provider/keypair_adapter_test.go — Unit tests for adapter
✦ schemas/aws/ec2/keypair.cue                  — CUE schema for KeyPair resource
✦ tests/integration/keypair_driver_test.go     — Integration tests
✎ cmd/praxis-compute/main.go                  — Add KeyPair driver `.Bind()`
✎ internal/core/provider/registry.go           — Add NewKeyPairAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ec2/keypair.cue`

```cue
package ec2

#KeyPair: {
    apiVersion: "praxis.io/v1"
    kind:       "KeyPair"

    metadata: {
        // name is used as the key pair name in AWS.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the key pair in.
        region: string

        // keyType is the cryptographic algorithm for the key pair.
        keyType: "rsa" | "ed25519" | *"ed25519"

        // publicKeyMaterial is the public key to import (SSH public key format).
        // If omitted, AWS generates the key pair and returns the private key
        // in the Provision response (one-time retrieval).
        publicKeyMaterial?: string

        // tags applied to the key pair.
        tags: [string]: string
    }

    outputs?: {
        keyName:        string
        keyPairId:      string
        keyFingerprint: string
        keyType:        string
        // privateKeyMaterial is ONLY populated on first Provision when AWS
        // generates the key pair. It is NOT stored in Restate state.
        // The caller must capture it from the Provision response.
        privateKeyMaterial?: string
    }
}
```

**Key decisions**:

- `keyType` defaults to `ed25519` — modern, faster, shorter keys, recommended
  over RSA for SSH.
- `publicKeyMaterial` is optional — omit for AWS-generated keys, provide for
  user-imported keys.
- `privateKeyMaterial` in outputs is populated only once, on the initial Provision
  response. It is never stored in Virtual Object state.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

Key pair operations (`CreateKeyPair`, `ImportKeyPair`, `DescribeKeyPairs`,
`DeleteKeyPair`) are methods on the EC2 SDK client.

---

## Step 3 — Driver Types

**File**: `internal/drivers/keypair/types.go`

```go
package keypair

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "KeyPair"

type KeyPairSpec struct {
    Account            string            `json:"account,omitempty"`
    Region             string            `json:"region"`
    KeyName            string            `json:"keyName"`
    KeyType            string            `json:"keyType"`
    PublicKeyMaterial   string            `json:"publicKeyMaterial,omitempty"`
    Tags               map[string]string `json:"tags,omitempty"`
}

type KeyPairOutputs struct {
    KeyName            string `json:"keyName"`
    KeyPairId          string `json:"keyPairId"`
    KeyFingerprint     string `json:"keyFingerprint"`
    KeyType            string `json:"keyType"`
    // PrivateKeyMaterial is populated ONLY on first Provision (AWS-generated keys).
    // NOT stored in state — returned to the caller once, then zeroed.
    PrivateKeyMaterial string `json:"privateKeyMaterial,omitempty"`
}

type ObservedState struct {
    KeyName        string            `json:"keyName"`
    KeyPairId      string            `json:"keyPairId"`
    KeyFingerprint string            `json:"keyFingerprint"`
    KeyType        string            `json:"keyType"`
    Tags           map[string]string `json:"tags"`
}

type KeyPairState struct {
    Desired            KeyPairSpec          `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            KeyPairOutputs       `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

**Note**: `KeyPairOutputs.PrivateKeyMaterial` is populated only in the return value
from the Provision handler. When saving state, the driver zeroes this field:

```go
outputs.PrivateKeyMaterial = "" // never persist private key material
state.Outputs = outputs
restate.Set(ctx, drivers.StateKey, state)
return outputsWithPrivateKey, nil // return WITH the private key
```

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/keypair/aws.go`

### KeyPairAPI Interface

```go
type KeyPairAPI interface {
    // CreateKeyPair creates a new AWS-generated key pair.
    // Returns the key pair ID, fingerprint, and the private key material (PEM).
    CreateKeyPair(ctx context.Context, name, keyType string, tags map[string]string) (keyPairId, fingerprint, privateKey string, err error)

    // ImportKeyPair imports a user-provided public key.
    // Returns the key pair ID and fingerprint.
    ImportKeyPair(ctx context.Context, name, publicKeyMaterial string, tags map[string]string) (keyPairId, fingerprint string, err error)

    // DescribeKeyPair returns the observed state of a key pair by name.
    DescribeKeyPair(ctx context.Context, keyName string) (ObservedState, error)

    // DeleteKeyPair deletes a key pair by name.
    DeleteKeyPair(ctx context.Context, keyName string) error

    // UpdateTags replaces all user tags on the key pair.
    UpdateTags(ctx context.Context, keyPairId string, tags map[string]string) error
}
```

### realKeyPairAPI Implementation

```go
type realKeyPairAPI struct {
    client  *ec2sdk.Client
    limiter *ratelimit.Limiter
}

func NewKeyPairAPI(client *ec2sdk.Client) KeyPairAPI {
    return &realKeyPairAPI{
        client:  client,
        limiter: ratelimit.New("key-pair", 20, 10),
    }
}
```

### Key Implementation Details

#### `CreateKeyPair`

```go
func (r *realKeyPairAPI) CreateKeyPair(ctx context.Context, name, keyType string, tags map[string]string) (string, string, string, error) {
    ec2Tags := make([]ec2types.Tag, 0, len(tags))
    for k, v := range tags {
        ec2Tags = append(ec2Tags, ec2types.Tag{
            Key: aws.String(k), Value: aws.String(v),
        })
    }

    input := &ec2sdk.CreateKeyPairInput{
        KeyName: aws.String(name),
        KeyType: ec2types.KeyType(keyType),
    }
    if len(ec2Tags) > 0 {
        input.TagSpecifications = []ec2types.TagSpecification{{
            ResourceType: ec2types.ResourceTypeKeyPair,
            Tags:         ec2Tags,
        }}
    }

    out, err := r.client.CreateKeyPair(ctx, input)
    if err != nil {
        return "", "", "", err
    }
    return aws.ToString(out.KeyPairId),
           aws.ToString(out.KeyFingerprint),
           aws.ToString(out.KeyMaterial), // Private key PEM — returned ONCE
           nil
}
```

#### `ImportKeyPair`

```go
func (r *realKeyPairAPI) ImportKeyPair(ctx context.Context, name, publicKeyMaterial string, tags map[string]string) (string, string, error) {
    ec2Tags := make([]ec2types.Tag, 0, len(tags))
    for k, v := range tags {
        ec2Tags = append(ec2Tags, ec2types.Tag{
            Key: aws.String(k), Value: aws.String(v),
        })
    }

    input := &ec2sdk.ImportKeyPairInput{
        KeyName:           aws.String(name),
        PublicKeyMaterial:  []byte(publicKeyMaterial),
    }
    if len(ec2Tags) > 0 {
        input.TagSpecifications = []ec2types.TagSpecification{{
            ResourceType: ec2types.ResourceTypeKeyPair,
            Tags:         ec2Tags,
        }}
    }

    out, err := r.client.ImportKeyPair(ctx, input)
    if err != nil {
        return "", "", err
    }
    return aws.ToString(out.KeyPairId),
           aws.ToString(out.KeyFingerprint),
           nil
}
```

#### `DescribeKeyPair`

```go
func (r *realKeyPairAPI) DescribeKeyPair(ctx context.Context, keyName string) (ObservedState, error) {
    out, err := r.client.DescribeKeyPairs(ctx, &ec2sdk.DescribeKeyPairsInput{
        KeyNames: []string{keyName},
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.KeyPairs) == 0 {
        return ObservedState{}, fmt.Errorf("key pair %q not found", keyName)
    }
    kp := out.KeyPairs[0]

    obs := ObservedState{
        KeyName:        aws.ToString(kp.KeyName),
        KeyPairId:      aws.ToString(kp.KeyPairId),
        KeyFingerprint: aws.ToString(kp.KeyFingerprint),
        KeyType:        string(kp.KeyType),
        Tags:           make(map[string]string, len(kp.Tags)),
    }
    for _, tag := range kp.Tags {
        obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
    }
    return obs, nil
}
```

#### `DeleteKeyPair`

```go
func (r *realKeyPairAPI) DeleteKeyPair(ctx context.Context, keyName string) error {
    _, err := r.client.DeleteKeyPair(ctx, &ec2sdk.DeleteKeyPairInput{
        KeyName: aws.String(keyName),
    })
    return err
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
        return apiErr.ErrorCode() == "InvalidKeyPair.NotFound"
    }
    errText := err.Error()
    return strings.Contains(errText, "InvalidKeyPair.NotFound")
}

func IsDuplicate(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidKeyPair.Duplicate"
    }
    errText := err.Error()
    return strings.Contains(errText, "InvalidKeyPair.Duplicate")
}

func IsInvalidKeyFormat(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidKey.Format"
    }
    return false
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/keypair/drift.go`

Key pairs have minimal mutable state — only tags can drift. `keyType`,
`keyFingerprint`, and `keyName` are all immutable.

```go
package keypair

func HasDrift(desired KeyPairSpec, observed ObservedState) bool {
    return !tagsMatch(desired.Tags, observed.Tags)
}

func ComputeFieldDiffs(desired KeyPairSpec, observed ObservedState) []FieldDiffEntry {
    var diffs []FieldDiffEntry

    // Immutable field changes — report but do not correct
    if desired.KeyType != observed.KeyType && observed.KeyType != "" {
        diffs = append(diffs, FieldDiffEntry{
            Path: "spec.keyType (immutable, ignored)",
            Old:  observed.KeyType,
            New:  desired.KeyType,
        })
    }

    diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
    return diffs
}

type FieldDiffEntry struct {
    Path string
    Old  string
    New  string
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/keypair/driver.go`

### Struct & Constructor

```go
type KeyPairDriver struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) KeyPairAPI
}

func NewKeyPairDriver(accounts *auth.Registry) *KeyPairDriver {
    return NewKeyPairDriverWithFactory(accounts, func(cfg aws.Config) KeyPairAPI {
        return NewKeyPairAPI(awsclient.NewEC2Client(cfg))
    })
}

func NewKeyPairDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) KeyPairAPI) *KeyPairDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) KeyPairAPI {
            return NewKeyPairAPI(awsclient.NewEC2Client(cfg))
        }
    }
    return &KeyPairDriver{auth: accounts, apiFactory: factory}
}

func (d *KeyPairDriver) ServiceName() string {
    return ServiceName
}
```

### Provision Handler

```go
func (d *KeyPairDriver) Provision(ctx restate.ObjectContext, spec KeyPairSpec) (KeyPairOutputs, error) {
    api, _, err := d.apiForAccount(spec.Account)
    if err != nil {
        return KeyPairOutputs{}, restate.TerminalError(err, 400)
    }

    if spec.Region == "" {
        return KeyPairOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
    }
    if spec.KeyName == "" {
        return KeyPairOutputs{}, restate.TerminalError(fmt.Errorf("keyName is required"), 400)
    }

    state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
    if err != nil {
        return KeyPairOutputs{}, err
    }

    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Mode = types.ModeManaged
    state.Error = ""
    state.Generation++

    // Check if key pair already exists (re-provision path)
    existingKeyPairId := state.Outputs.KeyPairId
    if existingKeyPairId != "" {
        _, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
            obs, err := api.DescribeKeyPair(rc, spec.KeyName)
            if err != nil {
                if IsNotFound(err) {
                    return ObservedState{}, restate.TerminalError(err, 404)
                }
                return ObservedState{}, err
            }
            return obs, nil
        })
        if descErr != nil {
            existingKeyPairId = "" // Key pair gone, recreate
        }
    }

    var outputs KeyPairOutputs

    if existingKeyPairId == "" {
        // Create or import key pair
        if spec.PublicKeyMaterial != "" {
            // Import user-provided public key
            result, err := restate.Run(ctx, func(rc restate.RunContext) (importResult, error) {
                kpId, fp, err := api.ImportKeyPair(rc, spec.KeyName, spec.PublicKeyMaterial, spec.Tags)
                if err != nil {
                    if IsDuplicate(err) {
                        return importResult{}, restate.TerminalError(err, 409)
                    }
                    if IsInvalidKeyFormat(err) {
                        return importResult{}, restate.TerminalError(err, 400)
                    }
                    return importResult{}, err
                }
                return importResult{keyPairId: kpId, fingerprint: fp}, nil
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return KeyPairOutputs{}, err
            }
            outputs = KeyPairOutputs{
                KeyName:        spec.KeyName,
                KeyPairId:      result.keyPairId,
                KeyFingerprint: result.fingerprint,
                KeyType:        spec.KeyType,
            }
        } else {
            // AWS-generated key pair
            result, err := restate.Run(ctx, func(rc restate.RunContext) (createResult, error) {
                kpId, fp, pk, err := api.CreateKeyPair(rc, spec.KeyName, spec.KeyType, spec.Tags)
                if err != nil {
                    if IsDuplicate(err) {
                        return createResult{}, restate.TerminalError(err, 409)
                    }
                    return createResult{}, err
                }
                return createResult{keyPairId: kpId, fingerprint: fp, privateKey: pk}, nil
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return KeyPairOutputs{}, err
            }
            outputs = KeyPairOutputs{
                KeyName:            spec.KeyName,
                KeyPairId:          result.keyPairId,
                KeyFingerprint:     result.fingerprint,
                KeyType:            spec.KeyType,
                PrivateKeyMaterial: result.privateKey, // returned to caller ONCE
            }
        }
    } else {
        // Re-provision: only tags can change
        if !tagsMatch(spec.Tags, state.Observed.Tags) {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateTags(rc, existingKeyPairId, spec.Tags)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return KeyPairOutputs{}, err
            }
        }
        outputs = state.Outputs // retain existing outputs (no private key on re-provision)
    }

    // Describe to update observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeKeyPair(rc, spec.KeyName)
    })
    if err != nil {
        state.Status = types.StatusError
        state.Error = err.Error()
        restate.Set(ctx, drivers.StateKey, state)
        return KeyPairOutputs{}, err
    }

    // Update outputs from observed (but preserve private key for the return value)
    privateKeyForReturn := outputs.PrivateKeyMaterial
    outputs = outputsFromObserved(observed)

    // Store state WITHOUT private key material
    state.Observed = observed
    state.Outputs = outputs // no private key in stored state
    state.Status = types.StatusReady
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx, &state)

    // Return WITH private key (first provision only)
    outputs.PrivateKeyMaterial = privateKeyForReturn
    return outputs, nil
}
```

### Delete Handler

```go
func (d *KeyPairDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state.Status == types.StatusDeleted {
        return nil
    }
    if state.Mode == types.ModeObserved {
        return restate.TerminalError(
            fmt.Errorf("cannot delete key pair in Observed mode; change to Managed mode first"), 409)
    }

    keyName := state.Desired.KeyName
    if keyName == "" {
        state.Status = types.StatusDeleted
        restate.Set(ctx, drivers.StateKey, state)
        return nil
    }

    state.Status = types.StatusDeleting
    restate.Set(ctx, drivers.StateKey, state)

    api, _, err := d.apiForAccount(state.Desired.Account)
    if err != nil {
        return restate.TerminalError(err, 400)
    }

    _, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        err := api.DeleteKeyPair(rc, keyName)
        if err != nil {
            if IsNotFound(err) {
                return restate.Void{}, nil // already gone
            }
            return restate.Void{}, err
        }
        return restate.Void{}, nil
    })
    if err != nil {
        state.Status = types.StatusError
        state.Error = err.Error()
        restate.Set(ctx, drivers.StateKey, state)
        return err
    }

    state.Status = types.StatusDeleted
    restate.Set(ctx, drivers.StateKey, state)
    return nil
}
```

### Import, Reconcile, GetStatus, GetOutputs

Follow the established pattern. Import defaults to `ModeManaged` (unlike EC2/VPC/EBS
which default to Observed). Key pairs are lightweight metadata resources — deleting
them does not destroy running infrastructure. Instances that reference a deleted key
pair continue to function; they just can't be launched with that key again.

Reconcile detects tag drift only and corrects in Managed mode.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/keypair_adapter.go`

```go
func (a *KeyPairAdapter) Kind() string        { return keypair.ServiceName }
func (a *KeyPairAdapter) ServiceName() string  { return keypair.ServiceName }
func (a *KeyPairAdapter) Scope() KeyScope      { return KeyScopeRegion }

func (a *KeyPairAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    // region~metadata.name (where metadata.name = keyName)
}

func (a *KeyPairAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // region~resourceID (where resourceID = keyName)
    // Same key as BuildKey for the same key pair — matches S3 pattern.
}
```

Plan can describe the key pair by name directly (unlike EC2/VPC which must use
stored IDs). Key names are stable and unique within a region.

---

## Step 8 — Registry Integration

Add `NewKeyPairAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go`

Add `.Bind(restate.Reflect(keypairDriver))` alongside the existing EC2 driver
binding. Key pairs are compute-adjacent resources — they are created for EC2
instance SSH access.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes needed — key pairs join the existing `praxis-compute`
service. Add `test-keypair` and `ls-keypair` targets to the justfile.

---

## Step 11 — Unit Tests

### `internal/drivers/keypair/drift_test.go`

1. `TestHasDrift_NoDrift` — identical tags.
2. `TestHasDrift_TagAdded` — tag drift detected.
3. `TestHasDrift_TagRemoved` — tag drift detected.
4. `TestComputeFieldDiffs_ImmutableKeyType` — reports keyType change as "(immutable, ignored)".
5. `TestComputeFieldDiffs_Tags` — correct tag diff entries.

### `internal/drivers/keypair/aws_test.go`

1. `TestIsNotFound_True` — InvalidKeyPair.NotFound.
2. `TestIsNotFound_False` — other errors.
3. `TestIsDuplicate_True` — InvalidKeyPair.Duplicate.
4. `TestIsInvalidKeyFormat_True` — InvalidKey.Format.

### `internal/drivers/keypair/driver_test.go`

1. `TestSpecFromObserved_RoundTrip` — import creates matching spec.
2. `TestServiceName` — returns "KeyPair".
3. `TestOutputsFromObserved` — correct output mapping.
4. `TestPrivateKeyNotStoredInState` — verify private key is zeroed in state.

### `internal/core/provider/keypair_adapter_test.go`

1. `TestKeyPairAdapter_DecodeSpecAndBuildKey` — returns `region~keyName` key.
2. `TestKeyPairAdapter_BuildImportKey` — returns `region~keyName` (same pattern).
3. `TestKeyPairAdapter_Kind` — returns "KeyPair".
4. `TestKeyPairAdapter_Scope` — returns `KeyScopeRegion`.
5. `TestKeyPairAdapter_NormalizeOutputs` — converts struct to map.

---

## Step 12 — Integration Tests

**File**: `tests/integration/keypair_driver_test.go`

1. **TestKeyPairProvision_CreatesKeyPair** — Creates a key pair, verifies in DescribeKeyPairs,
   verifies private key is returned in response.
2. **TestKeyPairProvision_Idempotent** — Two provisions, second does not return private key.
3. **TestKeyPairProvision_ImportPublicKey** — Imports a user-provided RSA public key.
4. **TestKeyPairImport_ExistingKeyPair** — Creates via SDK, imports via driver.
5. **TestKeyPairDelete_RemovesKeyPair** — Provisions, deletes, verifies gone.
6. **TestKeyPairReconcile_DetectsTagDrift** — Tag drift correction.

---

## KeyPair-Specific Design Decisions

### 1. Private Key Handling: Return Once, Never Store

The private key material from AWS-generated key pairs is:
- Returned in the Provision handler response (so the caller can capture it).
- NOT stored in Restate Virtual Object state.
- NOT returned on subsequent Provision calls (re-provision returns empty private key).

This is a deliberate security choice. Storing the private key in Restate state would
mean it persists in the Restate journal and any state snapshots indefinitely. The
one-time return pattern matches AWS's own behaviour (the Console shows the private
key once and never again).

**Consequence**: if the caller fails to capture the private key from the first
Provision response, the key is lost. The only recovery is to delete the key pair and
create a new one. This is the same user experience as the AWS Console.

### 2. No Ownership Tags

Key pairs use AWS-enforced unique names within a region. `CreateKeyPair` returns
`InvalidKeyPair.Duplicate` if the name already exists. This natural conflict signal
eliminates the need for `praxis:managed-key` ownership tags and `FindByManagedKey`.
The duplicate error maps to a terminal 409 in the Provision handler.

### 3. Import Produces Same Key as BuildKey

Unlike EC2/VPC/EBS (where import uses the AWS-assigned ID, producing a different VO),
key pair import uses the key pair name, producing the same VO key as template
management. This follows the S3 pattern and is correct because:
- Key pair names are unique within a region (unlike EC2 Name tags).
- The name is the primary API identifier for all operations.
- Two VOs for the same key pair would be confusing and error-prone.

### 4. Import Defaults to ModeManaged

Unlike EC2/VPC/EBS which default to ModeObserved, key pairs default to ModeManaged
on import. Key pairs are lightweight metadata — deleting a key pair does not affect
running instances, does not destroy data, and does not cause service disruption. The
risk profile for key pair deletion is much lower than instance termination or volume
deletion.

### 5. Driver Pack Placement: praxis-compute

Key pairs are EC2 SSH access resources. They belong in the `praxis-compute` driver
pack alongside EC2 instances. When an EC2 template references `spec.keyName`, the
key pair must be available in the same driver ecosystem.

### 6. Delete Does Not Check for In-Use Instances

AWS allows deleting a key pair even if instances are using it — the delete only
removes the key pair metadata from AWS. Running instances retain their authorized
keys and continue to accept SSH connections with the original key. New instances
cannot be launched with a deleted key pair.

The driver does NOT check for in-use instances before deleting.

---

## Design Decisions (Resolved)

1. **Should the driver support key rotation (delete + recreate)?**
   No. Key rotation is a destructive operation that produces a new fingerprint and
   (for AWS-generated keys) a new private key. This is a deliberate user action, not
   something drift correction should automate. If a user wants to rotate, they should
   delete the key pair resource and create a new one.

2. **Should the driver store the public key material for imported keys?**
   No. The public key material is only used at import time. `DescribeKeyPairs` returns
   the public key in the response, so it can be observed, but it's not stored in the
   spec or used for drift comparison (you can't change an imported key's public key).

3. **Should re-provision with different `publicKeyMaterial` replace the key?**
   No. The key material is immutable. If the desired `publicKeyMaterial` differs from
   what was originally imported, `ComputeFieldDiffs` reports it as an immutable change
   but the driver takes no action. The user must delete and recreate.

4. **Should Observed mode block Delete?**
   Yes. Same contract as all other drivers.

---

## Checklist

- [ ] **Schema**: `schemas/aws/ec2/keypair.cue` created
- [ ] **Types**: `internal/drivers/keypair/types.go` created
- [ ] **AWS API**: `internal/drivers/keypair/aws.go` created
- [ ] **Drift**: `internal/drivers/keypair/drift.go` created
- [ ] **Driver**: `internal/drivers/keypair/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/keypair_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: KeyPair driver bound in `cmd/praxis-compute/main.go`
- [ ] **Justfile**: Updated with keypair targets
- [ ] **Unit tests (drift)**: `internal/drivers/keypair/drift_test.go`
- [ ] **Unit tests (aws helpers)**: `internal/drivers/keypair/aws_test.go`
- [ ] **Unit tests (driver)**: `internal/drivers/keypair/driver_test.go`
- [ ] **Unit tests (adapter)**: `internal/core/provider/keypair_adapter_test.go`
- [ ] **Integration tests**: `tests/integration/keypair_driver_test.go`
- [ ] **Private key not persisted**: Zeroed before `restate.Set()`, returned in response only
- [ ] **Duplicate detection**: `IsDuplicate` error → terminal 409
- [ ] **Import default mode**: `ModeManaged` (lightweight metadata resource)
- [ ] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/keypair/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestKeyPair -tags=integration`
