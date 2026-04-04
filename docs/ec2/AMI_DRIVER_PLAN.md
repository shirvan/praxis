# AMI Driver — Implementation Specification

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
16. [AMI-Specific Design Decisions](#ami-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The AMI driver manages the lifecycle of **Amazon Machine Images (AMIs)** only. An
AMI is an immutable disk snapshot template used to launch EC2 instances. The driver
supports two creation flows:

1. **Register from snapshot**: Register an AMI from an existing EBS snapshot
   (`RegisterImage`). This is the standard path for building custom AMIs from
   snapshot pipelines.
2. **Copy from source AMI**: Copy an existing AMI, optionally across regions
   (`CopyImage`). Used for AMI distribution across regions.

### Immutability Model

AMIs are fundamentally **immutable resources**. Once registered, the disk contents
and base configuration (architecture, virtualization type, root device) cannot be
changed. The only mutable attributes are:

| Attribute | Mutability | API |
|---|---|---|
| Tags | Mutable | `CreateTags` / `DeleteTags` |
| Description | Mutable | `ModifyImageAttribute` |
| Launch permissions | Mutable | `ModifyImageAttribute` |
| Deprecation time | Mutable | `EnableImageDeprecation` / `DisableImageDeprecation` |
| Block public access | Mutable | `ModifyImageAttribute` |
| Image contents (disks) | Immutable | — |
| Architecture | Immutable | — |
| Root device name | Immutable | — |
| Virtualization type | Immutable | — |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Register/copy AMI or update mutable attrs |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing AMI |
| `Delete` | `ObjectContext` (exclusive) | Deregister AMI |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return AMI outputs |

### What Is NOT In Scope

- **Building AMIs from instances** (CreateImage): This creates a snapshot and
  registers an AMI from a running instance. It requires the instance to exist
  and optionally reboots it. This is an orchestration concern, not a driver
  concern — a workflow would coordinate between the EC2 driver and AMI driver.
- **Snapshot management**: EBS snapshots are managed by the EBS driver or a future
  snapshot driver. The AMI driver references snapshots by ID.
- **AMI sharing to other AWS accounts**: Launch permissions are modeled as a
  mutable attribute in the spec, but cross-account sharing workflows are
  orchestration concerns.

### Downstream Consumers

```text
${resources.my-ami.outputs.imageId}        → EC2 instances, launch templates
${resources.my-ami.outputs.state}          → Readiness checks
${resources.my-ami.outputs.architecture}   → Instance type selection
```

---

## 2. Key Strategy

### Key Format: `region~amiName`

AMI "names" in AWS are metadata (the Name tag) and are **not enforced unique** by
AWS within an account+region. However, Praxis treats `metadata.name` as the logical
AMI name and uses `praxis:managed-key` ownership tags to enforce uniqueness:

- **BuildKey**: returns `region~metadata.name`.
- **`FindByManagedKey`**: searches for AMIs with `praxis:managed-key = region~amiName`
  tag. If found, the existing AMI is adopted (convergent provision).
- **BuildImportKey**: returns `region~resourceID` where `resourceID` is the AMI
  Name (from the Name tag) or AMI ID. Since AMI names are NOT AWS-unique, import
  produces the **same key** only when using the AMI Name that matches `metadata.name`.
  Import by AMI ID produces a different key (following the EC2/VPC pattern).

### Ownership Tags (Required)

Unlike S3/KeyPair where AWS-enforced uniqueness eliminates the need
for ownership tags, AMIs require `praxis:managed-key` because:

1. AWS allows multiple AMIs with the same Name tag in the same account+region.
2. `RegisterImage` does not fail on duplicate names.
3. Without ownership tags, a second VO targeting the same AMI name could create a
   duplicate AMI rather than managing the existing one.

The ownership tag pattern matches EC2/VPC/EBS:

- `praxis:managed-key = region~amiName` written at creation time.
- `FindByManagedKey` used in convergent provision to detect pre-existing managed AMIs.

---

## 3. File Inventory

```text
internal/drivers/ami/types.go            — Spec, Outputs, ObservedState, State structs
internal/drivers/ami/aws.go              — AMIAPI interface + realAMIAPI
internal/drivers/ami/drift.go            — HasDrift(), ComputeFieldDiffs()
internal/drivers/ami/driver.go           — AMIDriver Virtual Object
internal/drivers/ami/driver_test.go      — Unit tests for driver
internal/drivers/ami/aws_test.go         — Unit tests for error classification
internal/drivers/ami/drift_test.go       — Unit tests for drift detection
internal/core/provider/ami_adapter.go    — AMIAdapter implementing provider.Adapter
internal/core/provider/ami_adapter_test.go — Unit tests for adapter
schemas/aws/ec2/ami.cue                  — CUE schema for AMI resource
tests/integration/ami_driver_test.go     — Integration tests
cmd/praxis-compute/main.go              — Bind AMI driver
internal/core/provider/registry.go       — Adapter registered in NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ec2/ami.cue`

```cue
package ec2

#AMI: {
    apiVersion: "praxis.io/v1"
    kind:       "AMI"

    metadata: {
        // name is the logical AMI name, used as the AWS Name tag.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._()/-]{0,127}$"
        labels: [string]: string
    }

    spec: {
        region: string

        // description for the AMI.
        description?: string

        // source defines how the AMI is created. Exactly one must be set.
        source: {
            // fromSnapshot registers an AMI from an EBS snapshot.
            fromSnapshot?: {
                snapshotId:        string
                architecture:      "x86_64" | "arm64" | *"x86_64"
                virtualizationType: "hvm" | *"hvm"
                rootDeviceName:    string | *"/dev/xvda"
                volumeType:        "gp2" | "gp3" | "io1" | "io2" | *"gp3"
                volumeSize?:       int & >0
                enaSupport?:       bool | *true
            }

            // fromAMI copies an existing AMI (same or cross-region).
            fromAMI?: {
                sourceImageId: string
                sourceRegion?: string  // defaults to spec.region if omitted
                encrypted?:    bool
                kmsKeyId?:     string
            }
        }

        // launchPermissions controls who can launch instances from this AMI.
        launchPermissions?: {
            // accountIds that can launch instances from this AMI.
            accountIds?: [...string]
            // public makes the AMI launchable by all AWS accounts.
            public?: bool
        }

        // deprecation schedules the AMI for deprecation.
        deprecation?: {
            // deprecateAt is the RFC 3339 time to deprecate the AMI.
            deprecateAt: string
        }

        // tags applied to the AMI.
        tags: [string]: string
    }

    outputs?: {
        imageId:            string
        name:               string
        state:              string  // "available", "pending", "failed"
        architecture:       string
        virtualizationType: string
        rootDeviceName:     string
        ownerId:            string
        creationDate:       string
    }
}
```

### Schema Design Notes

- **`source`** is a discriminated union: either `fromSnapshot` or `fromAMI`, never
  both. This models the two creation paths cleanly.
- **`launchPermissions`** is optional — omit for private AMIs (account-only).
  Setting `public: true` allows any AWS account to launch instances.
- **`deprecation`** is optional — omit for AMIs that should not be deprecated.
- The Name tag is derived from `metadata.name`, not a separate spec field.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

AMI operations are methods on the EC2 SDK client.

---

## Step 3 — Driver Types

**File**: `internal/drivers/ami/types.go`

```go
package ami

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "AMI"

type AMISpec struct {
    Account            string            `json:"account,omitempty"`
    Region             string            `json:"region"`
    Name               string            `json:"name"`
    Description        string            `json:"description,omitempty"`
    Source             SourceSpec        `json:"source"`
    LaunchPermissions  *LaunchPermsSpec  `json:"launchPermissions,omitempty"`
    Deprecation        *DeprecationSpec  `json:"deprecation,omitempty"`
    Tags               map[string]string `json:"tags,omitempty"`
    ManagedKey         string            `json:"managedKey,omitempty"`
}

type SourceSpec struct {
    FromSnapshot *FromSnapshotSpec `json:"fromSnapshot,omitempty"`
    FromAMI      *FromAMISpec     `json:"fromAMI,omitempty"`
}

type FromSnapshotSpec struct {
    SnapshotId          string `json:"snapshotId"`
    Architecture        string `json:"architecture"`
    VirtualizationType  string `json:"virtualizationType"`
    RootDeviceName      string `json:"rootDeviceName"`
    VolumeType          string `json:"volumeType"`
    VolumeSize          int32  `json:"volumeSize,omitempty"`
    EnaSupport          *bool  `json:"enaSupport,omitempty"`
}

type FromAMISpec struct {
    SourceImageId string `json:"sourceImageId"`
    SourceRegion  string `json:"sourceRegion,omitempty"`
    Encrypted     bool   `json:"encrypted,omitempty"`
    KmsKeyId      string `json:"kmsKeyId,omitempty"`
}

type LaunchPermsSpec struct {
    AccountIds []string `json:"accountIds,omitempty"`
    Public     bool     `json:"public"`
}

type DeprecationSpec struct {
    DeprecateAt string `json:"deprecateAt"`
}

type AMIOutputs struct {
    ImageId            string `json:"imageId"`
    Name               string `json:"name"`
    State              string `json:"state"`
    Architecture       string `json:"architecture"`
    VirtualizationType string `json:"virtualizationType"`
    RootDeviceName     string `json:"rootDeviceName"`
    OwnerId            string `json:"ownerId"`
    CreationDate       string `json:"creationDate"`
}

type ObservedState struct {
    ImageId             string            `json:"imageId"`
    Name                string            `json:"name"`
    Description         string            `json:"description"`
    State               string            `json:"state"`
    Architecture        string            `json:"architecture"`
    VirtualizationType  string            `json:"virtualizationType"`
    RootDeviceName      string            `json:"rootDeviceName"`
    OwnerId             string            `json:"ownerId"`
    CreationDate        string            `json:"creationDate"`
    Tags                map[string]string `json:"tags"`
    LaunchPermPublic    bool              `json:"launchPermPublic"`
    LaunchPermAccounts  []string          `json:"launchPermAccounts,omitempty"`
    DeprecationTime     string            `json:"deprecationTime,omitempty"`
}

type AMIState struct {
    Desired            AMISpec              `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            AMIOutputs           `json:"outputs"`
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

**File**: `internal/drivers/ami/aws.go`

### AMIAPI Interface

```go
type AMIAPI interface {
    RegisterImage(ctx context.Context, spec AMISpec) (string, error)
    CopyImage(ctx context.Context, spec AMISpec) (string, error)
    DescribeImage(ctx context.Context, imageId string) (ObservedState, error)
    DescribeImageByName(ctx context.Context, name string) (ObservedState, error)
    DeregisterImage(ctx context.Context, imageId string) error
    UpdateTags(ctx context.Context, imageId string, tags map[string]string) error
    ModifyDescription(ctx context.Context, imageId, description string) error
    ModifyLaunchPermissions(ctx context.Context, imageId string, perms *LaunchPermsSpec) error
    EnableDeprecation(ctx context.Context, imageId, deprecateAt string) error
    DisableDeprecation(ctx context.Context, imageId string) error
    WaitUntilAvailable(ctx context.Context, imageId string, timeout time.Duration) error
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### Key Implementation Details

#### `RegisterImage`

```go
func (r *realAMIAPI) RegisterImage(ctx context.Context, spec AMISpec) (string, error) {
    snap := spec.Source.FromSnapshot
    if snap == nil {
        return "", fmt.Errorf("RegisterImage requires fromSnapshot source")
    }

    bdm := ec2types.BlockDeviceMapping{
        DeviceName: aws.String(snap.RootDeviceName),
        Ebs: &ec2types.EbsBlockDevice{
            SnapshotId:          aws.String(snap.SnapshotId),
            VolumeType:          ec2types.VolumeType(snap.VolumeType),
            DeleteOnTermination: aws.Bool(true),
        },
    }
    if snap.VolumeSize > 0 {
        bdm.Ebs.VolumeSize = aws.Int32(snap.VolumeSize)
    }

    input := &ec2sdk.RegisterImageInput{
        Name:                aws.String(spec.Name),
        Description:         aws.String(spec.Description),
        Architecture:        ec2types.ArchitectureValues(snap.Architecture),
        VirtualizationType:  aws.String(snap.VirtualizationType),
        RootDeviceName:      aws.String(snap.RootDeviceName),
        BlockDeviceMappings: []ec2types.BlockDeviceMapping{bdm},
    }
    if snap.EnaSupport != nil && *snap.EnaSupport {
        input.EnaSupport = aws.Bool(true)
    }

    out, err := r.client.RegisterImage(ctx, input)
    if err != nil {
        return "", err
    }
    return aws.ToString(out.ImageId), nil
}
```

#### `CopyImage`

```go
func (r *realAMIAPI) CopyImage(ctx context.Context, spec AMISpec) (string, error) {
    cp := spec.Source.FromAMI
    if cp == nil {
        return "", fmt.Errorf("CopyImage requires fromAMI source")
    }

    sourceRegion := cp.SourceRegion
    if sourceRegion == "" {
        sourceRegion = spec.Region
    }

    input := &ec2sdk.CopyImageInput{
        Name:          aws.String(spec.Name),
        Description:   aws.String(spec.Description),
        SourceImageId: aws.String(cp.SourceImageId),
        SourceRegion:  aws.String(sourceRegion),
        Encrypted:     aws.Bool(cp.Encrypted),
    }
    if cp.KmsKeyId != "" {
        input.KmsKeyId = aws.String(cp.KmsKeyId)
    }

    out, err := r.client.CopyImage(ctx, input)
    if err != nil {
        return "", err
    }
    return aws.ToString(out.ImageId), nil
}
```

#### `DescribeImage`

Combines `DescribeImages` (main metadata) with `DescribeImageAttribute` (launch
permissions) into a single `ObservedState`:

```go
func (r *realAMIAPI) DescribeImage(ctx context.Context, imageId string) (ObservedState, error) {
    descOut, err := r.client.DescribeImages(ctx, &ec2sdk.DescribeImagesInput{
        ImageIds: []string{imageId},
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(descOut.Images) == 0 {
        return ObservedState{}, fmt.Errorf("AMI %q not found", imageId)
    }
    img := descOut.Images[0]

    obs := ObservedState{
        ImageId:            aws.ToString(img.ImageId),
        Name:               aws.ToString(img.Name),
        Description:        aws.ToString(img.Description),
        State:              string(img.State),
        Architecture:       string(img.Architecture),
        VirtualizationType: string(img.VirtualizationType),
        RootDeviceName:     aws.ToString(img.RootDeviceName),
        OwnerId:            aws.ToString(img.OwnerId),
        CreationDate:       aws.ToString(img.CreationDate),
        Tags:               extractTags(img.Tags),
    }

    // Fetch launch permissions
    permOut, err := r.client.DescribeImageAttribute(ctx, &ec2sdk.DescribeImageAttributeInput{
        ImageId:   aws.String(imageId),
        Attribute: ec2types.ImageAttributeNameLaunchPermission,
    })
    if err == nil {
        for _, perm := range permOut.LaunchPermissions {
            if perm.Group == ec2types.PermissionGroupAll {
                obs.LaunchPermPublic = true
            }
            if perm.UserId != nil {
                obs.LaunchPermAccounts = append(obs.LaunchPermAccounts, aws.ToString(perm.UserId))
            }
        }
    }

    // Fetch deprecation time
    if img.DeprecationTime != nil {
        obs.DeprecationTime = aws.ToString(img.DeprecationTime)
    }

    return obs, nil
}
```

#### `FindByManagedKey`

```go
func (r *realAMIAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    out, err := r.client.DescribeImages(ctx, &ec2sdk.DescribeImagesInput{
        Owners: []string{"self"},
        Filters: []ec2types.Filter{
            {
                Name:   aws.String("tag:praxis:managed-key"),
                Values: []string{managedKey},
            },
        },
    })
    if err != nil {
        return "", err
    }
    if len(out.Images) == 0 {
        return "", nil // not found
    }
    return aws.ToString(out.Images[0].ImageId), nil
}
```

### Error Classification

```go
func IsNotFound(err error) bool {
    // InvalidAMIID.NotFound or InvalidAMIID.Unavailable
}

func IsInvalidParam(err error) bool {
    // InvalidParameterValue, InvalidParameter, MissingParameter
}

func IsSnapshotNotFound(err error) bool {
    // InvalidSnapshot.NotFound — referenced snapshot doesn't exist
}

func IsAMIQuotaExceeded(err error) bool {
    // AMIQuotaExceeded — too many AMIs in the account
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/ami/drift.go`

```go
func HasDrift(desired AMISpec, observed ObservedState) bool {
    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }
    if desired.Description != "" && desired.Description != observed.Description {
        return true
    }
    if hasLaunchPermDrift(desired.LaunchPermissions, observed) {
        return true
    }
    if hasDeprecationDrift(desired.Deprecation, observed.DeprecationTime) {
        return true
    }
    return false
}
```

### Drift Categories

| Field | Drift Detection | Correction |
|---|---|---|
| Tags | `tagsMatch()` | `UpdateTags` |
| Description | String comparison | `ModifyDescription` |
| Launch permissions | Account list + public flag | `ModifyLaunchPermissions` |
| Deprecation time | Time comparison | `EnableDeprecation` / `DisableDeprecation` |
| Image contents | N/A (immutable) | Report as "(immutable, ignored)" |

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/ami/driver.go`

### Provision Handler

```go
func (d *AMIDriver) Provision(ctx restate.ObjectContext, spec AMISpec) (AMIOutputs, error) {
    // Validate: exactly one source type specified
    if spec.Source.FromSnapshot == nil && spec.Source.FromAMI == nil {
        return AMIOutputs{}, restate.TerminalError(
            fmt.Errorf("exactly one of source.fromSnapshot or source.fromAMI must be specified"), 400)
    }
    if spec.Source.FromSnapshot != nil && spec.Source.FromAMI != nil {
        return AMIOutputs{}, restate.TerminalError(
            fmt.Errorf("cannot specify both source.fromSnapshot and source.fromAMI"), 400)
    }

    // Load state, update desired/status/generation
    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Mode = types.ModeManaged

    existingImageId := state.Outputs.ImageId

    if existingImageId == "" {
        // Check for existing managed AMI (convergent provision)
        managedKey := fmt.Sprintf("%s~%s", spec.Region, spec.Name)
        foundId, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.FindByManagedKey(rc, managedKey)
        })
        if err != nil { /* handle */ }
        if foundId != "" {
            existingImageId = foundId
        }
    }

    if existingImageId == "" {
        // Create AMI
        var imageId string
        if spec.Source.FromSnapshot != nil {
            imageId, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
                id, err := api.RegisterImage(rc, spec)
                if err != nil {
                    if IsSnapshotNotFound(err) {
                        return "", restate.TerminalError(err, 400)
                    }
                    if IsAMIQuotaExceeded(err) {
                        return "", restate.TerminalError(err, 503)
                    }
                    return "", err
                }
                return id, nil
            })
        } else {
            imageId, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
                id, err := api.CopyImage(rc, spec)
                if err != nil { return "", err }
                return id, nil
            })
        }

        // Tag with managed-key and user tags
        allTags := mergeTags(spec.Tags, map[string]string{
            "praxis:managed-key": managedKey,
            "Name":               spec.Name,
        })
        _, _ = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.UpdateTags(rc, imageId, allTags)
        })

        existingImageId = imageId
    } else {
        // Re-provision: update mutable attributes
        // Tags, description, launch permissions, deprecation
    }

    // Wait for AMI to become available (RegisterImage/CopyImage are async)
    _, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.WaitUntilAvailable(rc, existingImageId, 10*time.Minute)
    })

    // Apply mutable attributes (launch permissions, deprecation)
    // Describe to refresh observed state
    // Save state, schedule reconcile
    return outputs, nil
}
```

### AMI Availability Wait

Both `RegisterImage` and `CopyImage` return immediately with an image ID, but the
AMI enters a `pending` state. The driver waits for the AMI to transition to
`available` before completing Provision. This wait is wrapped in `restate.Run` to
journal the result.

For `CopyImage`, the wait can be substantial (minutes for large AMIs, especially
cross-region copies). The timeout is generous (10 minutes) to accommodate this.

### Delete Handler

```go
func (d *AMIDriver) Delete(ctx restate.ObjectContext) error {
    // Mode guard: reject ModeObserved (409)
    // DeregisterImage
    // Not found → already gone, idempotent
    // Note: DeregisterImage does NOT delete the underlying EBS snapshots.
    // Snapshot cleanup is the responsibility of the EBS/snapshot driver.
}
```

**Important**: `DeregisterImage` only deregisters the AMI — it does NOT delete the
underlying EBS snapshots. This is by AWS design. Snapshot cleanup is out of scope
for this driver and belongs to a snapshot management flow or the EBS driver.

### Import Handler

Defaults to `ModeObserved` — AMIs may be critical golden images used by many
services. Deregistering a production AMI prevents new instances from launching
and could break auto-scaling.

Import flow:

1. Accept image ID or image name.
2. Describe the AMI.
3. Build spec from observed state.
4. Store state with `ModeObserved`.
5. Write `praxis:managed-key` tag if not already present.

### Reconcile Handler

1. Describe the AMI.
2. Compare mutable attributes (tags, description, launch permissions, deprecation).
3. Correct drift in Managed mode.
4. Report-only in Observed mode.
5. If AMI is in `failed` or `deregistered` state → set status to Error.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/ami_adapter.go`

```go
func (a *AMIAdapter) Kind() string       { return ami.ServiceName }
func (a *AMIAdapter) ServiceName() string { return ami.ServiceName }
func (a *AMIAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *AMIAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    // Extract region and metadata.name from CUE-validated document.
    // Return region~metadata.name
}

func (a *AMIAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // If resourceID looks like an AMI ID (ami-xxx): describe it to get the Name.
    // If resourceID is a name: use it directly.
    // Return region~name.
    // For AMI ID import: return region~name where name comes from DescribeImages.
}
```

### Plan Logic

Plan uses the state-driven approach (EC2/VPC pattern, not S3/SG pattern):

1. Call `GetOutputs` on the VO.
2. If outputs contain an image ID → describe by ID to check existence.
3. If AMI exists → compare mutable attributes for drift → report as `OpUpdate`
   or `OpNoop`.
4. If AMI is gone or no outputs → `OpCreate`.

---

## Step 8 — Registry Integration

Add `NewAMIAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go`

Add `.Bind(restate.Reflect(amiDriver))` alongside EC2 and KeyPair
driver bindings. AMIs are compute image resources.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes — AMIs join `praxis-compute`. Add `test-ami` and `ls-ami`
to the justfile.

---

## Step 11 — Unit Tests

### `internal/drivers/ami/drift_test.go`

1. `TestHasDrift_NoDrift` — identical mutable attributes.
2. `TestHasDrift_TagsChanged` — tag drift.
3. `TestHasDrift_DescriptionChanged` — description drift.
4. `TestHasDrift_LaunchPermAccountAdded` — launch permission drift.
5. `TestHasDrift_PublicChanged` — public launch permission drift.
6. `TestHasDrift_DeprecationChanged` — deprecation time drift.
7. `TestComputeFieldDiffs_AllMutableFields` — correct diff entries.

### `internal/drivers/ami/aws_test.go`

1. `TestIsNotFound_NotFound` — InvalidAMIID.NotFound.
2. `TestIsNotFound_Unavailable` — InvalidAMIID.Unavailable.
3. `TestIsSnapshotNotFound` — InvalidSnapshot.NotFound.
4. `TestIsAMIQuotaExceeded` — AMIQuotaExceeded.
5. `TestFindByManagedKey_Found` — returns image ID.
6. `TestFindByManagedKey_NotFound` — returns empty string.

### `internal/drivers/ami/driver_test.go`

1. `TestServiceName` — returns "AMI".
2. `TestProvision_RequiresExactlyOneSource` — rejects no source / both sources.
3. `TestProvision_ConvergentWithManagedKey` — finds existing AMI by tag.
4. `TestOutputsFromObserved` — correct output mapping.
5. `TestDelete_DeregistersOnly` — does not delete snapshots.

### `internal/core/provider/ami_adapter_test.go`

1. `TestAMIAdapter_BuildKey` — returns `region~amiName`.
2. `TestAMIAdapter_Kind` — returns "AMI".
3. `TestAMIAdapter_Scope` — returns `KeyScopeRegion`.

---

## Step 12 — Integration Tests

**File**: `tests/integration/ami_driver_test.go`

Integration tests require Moto with EC2/AMI support. Note: Moto's AMI
support may be limited — some tests may need to be conditional.

1. **TestAMIProvision_RegisterFromSnapshot** — Creates a snapshot, registers an AMI,
   verifies `available` state.
2. **TestAMIProvision_Idempotent** — Two identical provisions, second is no-op.
3. **TestAMIProvision_UpdateDescription** — Re-provision with new description.
4. **TestAMIImport_ExistingAMI** — Registers via SDK, imports via driver.
5. **TestAMIDelete_Deregisters** — Provisions, deletes, verifies deregistered.
6. **TestAMIReconcile_DetectsTagDrift** — External tag change detected.
7. **TestAMIProvision_ManagedKeyConvergence** — Creates AMI externally with
   managed-key tag, provisions with same name, driver adopts existing AMI.

---

## AMI-Specific Design Decisions

### 1. Two Creation Paths: RegisterImage vs CopyImage

The driver supports both `RegisterImage` (from snapshot) and `CopyImage` (from
existing AMI). The choice is made via the `source` discriminated union in the spec:

- `source.fromSnapshot`: uses `RegisterImage`, requires an EBS snapshot ID.
- `source.fromAMI`: uses `CopyImage`, requires a source AMI ID and optionally a
  source region for cross-region copies.

Once created, the source type is irrelevant — the AMI is a standalone resource
regardless of how it was created.

### 2. Ownership Tags Required

AMI names are NOT unique in AWS. Multiple AMIs can have the same Name tag. The
`praxis:managed-key` tag enforces uniqueness for Praxis-managed AMIs and enables
convergent provision (the `FindByManagedKey` pattern).

### 3. Async Creation with WaitUntilAvailable

Both `RegisterImage` and `CopyImage` return immediately, but the AMI is in `pending`
state. The driver waits for `available` state inside a `restate.Run` block. The wait
timeout is 10 minutes to accommodate cross-region copies of large AMIs.

If the wait times out, the error is retryable — Restate will retry the handler and
the driver will find the existing AMI via `FindByManagedKey` (convergent provision).

### 4. DeregisterImage Does NOT Delete Snapshots

This is AWS's design: deregistering an AMI removes the AMI metadata but leaves the
underlying EBS snapshots intact. The driver does NOT attempt to clean up snapshots.
Snapshot lifecycle is managed by the EBS driver or a dedicated snapshot management
workflow.

### 5. Import Defaults to ModeObserved

AMIs are often critical shared resources (golden images, base images). Deregistering
a production AMI prevents new instances from launching. The conservative default
protects running infrastructure.

### 6. Launch Permissions as Mutable Spec

Launch permissions (account sharing + public access) are modeled as a mutable spec
field. Changes are applied via `ModifyImageAttribute` without creating a new AMI.
This is the correct AWS model — launch permissions are metadata on the AMI, not
part of the immutable image contents.

### 7. Deprecation Support

AMI deprecation is a lifecycle management feature — it marks an AMI as deprecated
at a scheduled time. Deprecated AMIs still exist and can launch instances, but they
show a "deprecated" status. The driver supports setting and clearing deprecation
schedules as a mutable attribute.

### 8. Driver Pack: praxis-compute

AMIs are compute image resources. They define what runs on EC2 instances and are
referenced by launch templates and direct instance launches. They belong in the
compute driver pack alongside EC2, Key Pair, and Launch Template drivers.

---

## Checklist

- [ ] **Schema**: `schemas/aws/ec2/ami.cue` created
- [ ] **Types**: `internal/drivers/ami/types.go` created
- [ ] **AWS API**: `internal/drivers/ami/aws.go` created
- [ ] **Drift**: `internal/drivers/ami/drift.go` created
- [ ] **Driver**: `internal/drivers/ami/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/ami_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: AMI driver bound in `cmd/praxis-compute/main.go`
- [ ] **Justfile**: Updated with ami targets
- [ ] **Unit tests (drift)**: `internal/drivers/ami/drift_test.go`
- [ ] **Unit tests (aws helpers)**: `internal/drivers/ami/aws_test.go`
- [ ] **Unit tests (driver)**: `internal/drivers/ami/driver_test.go`
- [ ] **Unit tests (adapter)**: `internal/core/provider/ami_adapter_test.go`
- [ ] **Integration tests**: `tests/integration/ami_driver_test.go`
- [ ] **Ownership tags**: `praxis:managed-key` written at creation, `FindByManagedKey` for convergence
- [ ] **Source validation**: Exactly one of `fromSnapshot` / `fromAMI` enforced
- [ ] **WaitUntilAvailable**: AMI creation waits for `available` state
- [ ] **Import default mode**: `ModeObserved` (critical shared resource)
- [ ] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [ ] **Deregister only**: Delete does NOT delete underlying snapshots
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/ami/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestAMI -tags=integration`
