# EBS Volume Driver — Implementation Plan

> **Status: Not yet implemented.** This document is a plan only.

> Target: A Restate Virtual Object driver that manages EBS volumes, following the
> exact patterns established by the S3 Bucket, Security Group, EC2 Instance, and
> VPC drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~metadata.name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned volume ID
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
12. [Step 9 — Compute Driver Pack Entry Point](#step-9--compute-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [EBS-Specific Design Decisions](#ebs-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The EBS driver manages the lifecycle of EBS **volumes** only. Snapshots, snapshot
lifecycle policies, and volume attachments to EC2 instances are out of scope for
this driver. Volume attachment is an operational concern handled by the EC2 instance
driver or a future compound template that composes EC2 + EBS resources. This plan
focuses exclusively on standalone EBS volume creation, configuration, import,
deletion, and drift reconciliation.

EBS volumes are independent AWS resources with their own lifecycle — they persist
after instance termination (unless `DeleteOnTermination` was set on the attachment),
can be detached and reattached to different instances, and can exist without being
attached to anything. This makes them a natural fit for a standalone driver.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an EBS volume |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing volume |
| `Delete` | `ObjectContext` (exclusive) | Delete a volume (must be detached) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return volume outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `availabilityZone` | Immutable | Cannot move a volume between AZs |
| `volumeType` | Mutable | Via `ModifyVolume` (in-place, no detach needed) |
| `sizeGiB` | Mutable (grow only) | Via `ModifyVolume` — can only increase, never shrink |
| `iops` | Mutable | Via `ModifyVolume` (for io1/io2/gp3) |
| `throughput` | Mutable | Via `ModifyVolume` (for gp3 only) |
| `encrypted` | Immutable | Set at creation time only |
| `kmsKeyId` | Immutable | Set at creation time only |
| `snapshotId` | Immutable | Used at creation time only |
| `tags` | Mutable | Full replace via `CreateTags` / `DeleteTags` |

### Downstream Consumers

```
${resources.my-volume.outputs.volumeId}          → EC2 BlockDeviceMappings, attachment templates
${resources.my-volume.outputs.availabilityZone}   → EC2 instance placement constraint
${resources.my-volume.outputs.arn}                → IAM policies
```

---

## 2. Key Strategy

### Key Format: `region~metadata.name`

The Virtual Object key is always `region~metadata.name`. This follows the EC2 and
VPC drivers — the AWS-assigned volume ID (`vol-0abc123...`) is unavailable at plan
time and lives only in state/outputs.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision** (pipeline → workflow → driver): dispatched to same key.
3. **Delete** (pipeline → workflow → driver): dispatched to same key.
4. **Plan** (adapter → describe by volume ID from state): uses the key to reach
   the Virtual Object, reads the stored volume ID from state, describes by ID.
5. **Import** (handlers_resource.go): `BuildImportKey(region, resourceID)` returns
   `region~resourceID` where `resourceID` is the volume ID — **this targets a
   different Virtual Object** intentionally.

### Constraint: metadata.name Must Be Unique Within a Region

EBS volumes have no AWS-native unique name — the Name tag is mutable and non-unique.
Praxis requires `metadata.name` to be region-unique for managed EBS resources, consistent
with the EC2 and VPC drivers.

### Conflict Enforcement via Ownership Tags

Following the EC2 and VPC driver pattern:

- **Tag written at creation**: every `CreateVolume` call adds the tag
  `praxis:managed-key = <region~metadata.name>` to the volume.

- **Pre-flight conflict check**: when `Provision` runs with no existing VO
  state (first provision), it calls `FindByManagedKey` to search for any
  volume already tagged with `praxis:managed-key = <this key>`. If found,
  `Provision` returns a terminal error (status 409).

- **`FindByManagedKey(ctx, managedKey) (string, error)`** is added to the
  `EBSAPI` interface. Returns `("", nil)` if no match (safe to create),
  `(volumeId, nil)` if exactly one match (conflict), or `("", error)` if
  more than one match (ownership corruption).

### Import Semantics: Separate Lifecycle Track

- `praxis import --kind EBSVolume --region us-east-1 --resource-id vol-0abc123`:
  Creates VO key `us-east-1~vol-0abc123`.

- Template with `metadata.name: data-vol` in `us-east-1`:
  Creates VO key `us-east-1~data-vol`.

Import defaults to `ModeObserved`. EBS volumes may contain critical data;
accidental deletion via an import VO is destructive and unrecoverable (unless
snapshots exist). The operator must explicitly pass `--mode managed` for full
lifecycle control.

### Plan-Time Volume Resolution

State-driven, same as EC2 and VPC:

1. `GetOutputs` has a `volumeId` → describe by ID.
2. No outputs → report `OpCreate`.
3. No `FindVolumeByName` — Name tags are mutable and non-unique.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/ebs/types.go             — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/ebs/aws.go               — EBSAPI interface + realEBSAPI implementation
✦ internal/drivers/ebs/drift.go             — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/ebs/driver.go            — EBSVolumeDriver Virtual Object
✦ internal/drivers/ebs/driver_test.go       — Unit tests for driver (mocked AWS)
✦ internal/drivers/ebs/aws_test.go          — Unit tests for error classification helpers
✦ internal/drivers/ebs/drift_test.go        — Unit tests for drift detection
✦ internal/core/provider/ebs_adapter.go     — EBSAdapter implementing provider.Adapter
✦ internal/core/provider/ebs_adapter_test.go — Unit tests for EBS adapter
✦ schemas/aws/ebs/ebs.cue                   — CUE schema for EBSVolume resource
✦ tests/integration/ebs_driver_test.go      — Integration tests (Testcontainers + LocalStack)
✎ cmd/praxis-storage/main.go               — Add EBS driver `.Bind()` to storage pack
✎ internal/core/provider/registry.go        — Add NewEBSAdapter to NewRegistry()
✎ docker-compose.yaml                       — No change needed (EBS joins existing praxis-storage service)
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ebs/ebs.cue`

```cue
package ebs

#EBSVolume: {
    apiVersion: "praxis.io/v1"
    kind:       "EBSVolume"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region for the volume.
        region: string

        // availabilityZone is the AZ to create the volume in (e.g., "us-east-1a").
        // Must be in the specified region.
        availabilityZone: string

        // volumeType is the EBS volume type.
        volumeType: "gp2" | "gp3" | "io1" | "io2" | "st1" | "sc1" | "standard" | *"gp3"

        // sizeGiB is the volume size in GiB.
        sizeGiB: int & >=1 & <=16384 | *20

        // iops is the provisioned IOPS. Required for io1/io2, optional for gp3.
        // Ignored for gp2, st1, sc1, standard.
        iops?: int & >=100 & <=256000

        // throughput is the provisioned throughput in MiB/s. Only valid for gp3.
        throughput?: int & >=125 & <=1000

        // encrypted enables encryption at rest.
        encrypted: bool | *true

        // kmsKeyId is the KMS key ID or ARN for encryption.
        // If omitted and encrypted=true, uses the default aws/ebs key.
        kmsKeyId?: string

        // snapshotId creates the volume from an existing snapshot.
        snapshotId?: string

        // Tags applied to the volume.
        tags: [string]: string
    }

    outputs?: {
        volumeId:         string
        arn:              string
        availabilityZone: string
        state:            string
        sizeGiB:          int
        volumeType:       string
        encrypted:        bool
    }
}
```

**Key decisions**:

- `availabilityZone` is required — EBS volumes are AZ-scoped, not region-scoped.
- `volumeType` defaults to `gp3` (current AWS best practice for general purpose).
- `sizeGiB` defaults to 20 GiB (matches EC2 root volume default).
- Default encryption enabled — matches AWS best practices and EC2 root volume default.
- `iops` and `throughput` are optional — only meaningful for specific volume types.
- `snapshotId` is optional — used only at creation time to clone from a snapshot.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

The existing `NewEC2Client(cfg)` returns an `*ec2.Client` which serves both EC2
instance and EBS volume APIs. EBS operations (`CreateVolume`, `DescribeVolumes`,
`DeleteVolume`, `ModifyVolume`) are all methods on the EC2 SDK client.

---

## Step 3 — Driver Types

**File**: `internal/drivers/ebs/types.go`

```go
package ebs

import "github.com/praxiscloud/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for EBS volumes.
const ServiceName = "EBSVolume"

// EBSVolumeSpec is the desired state for an EBS volume.
type EBSVolumeSpec struct {
    Account          string            `json:"account,omitempty"`
    Region           string            `json:"region"`
    AvailabilityZone string            `json:"availabilityZone"`
    VolumeType       string            `json:"volumeType"`
    SizeGiB          int32             `json:"sizeGiB"`
    Iops             int32             `json:"iops,omitempty"`
    Throughput       int32             `json:"throughput,omitempty"`
    Encrypted        bool              `json:"encrypted"`
    KmsKeyId         string            `json:"kmsKeyId,omitempty"`
    SnapshotId       string            `json:"snapshotId,omitempty"`
    Tags             map[string]string `json:"tags,omitempty"`
    ManagedKey       string            `json:"managedKey,omitempty"`
}

// EBSVolumeOutputs is produced after provisioning and stored in Restate K/V.
type EBSVolumeOutputs struct {
    VolumeId         string `json:"volumeId"`
    ARN              string `json:"arn"`
    AvailabilityZone string `json:"availabilityZone"`
    State            string `json:"state"`
    SizeGiB          int32  `json:"sizeGiB"`
    VolumeType       string `json:"volumeType"`
    Encrypted        bool   `json:"encrypted"`
}

// ObservedState captures the actual configuration of a volume from AWS Describe calls.
type ObservedState struct {
    VolumeId         string            `json:"volumeId"`
    AvailabilityZone string            `json:"availabilityZone"`
    VolumeType       string            `json:"volumeType"`
    SizeGiB          int32             `json:"sizeGiB"`
    Iops             int32             `json:"iops"`
    Throughput       int32             `json:"throughput"`
    Encrypted        bool              `json:"encrypted"`
    KmsKeyId         string            `json:"kmsKeyId"`
    State            string            `json:"state"` // "creating", "available", "in-use", "deleting", "deleted"
    SnapshotId       string            `json:"snapshotId"`
    Tags             map[string]string `json:"tags"`
}

// EBSVolumeState is the single atomic state object stored under drivers.StateKey.
type EBSVolumeState struct {
    Desired            EBSVolumeSpec        `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            EBSVolumeOutputs     `json:"outputs"`
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

**File**: `internal/drivers/ebs/aws.go`

### EBSAPI Interface

```go
package ebs

import "context"

// EBSAPI abstracts the AWS EC2 SDK operations for EBS volume management.
// All methods receive a plain context.Context — the caller wraps in restate.Run().
type EBSAPI interface {
    // CreateVolume creates a new EBS volume with the given spec.
    // Returns the volume ID assigned by AWS.
    CreateVolume(ctx context.Context, spec EBSVolumeSpec) (string, error)

    // DescribeVolume returns the full observed state of a volume.
    DescribeVolume(ctx context.Context, volumeId string) (ObservedState, error)

    // DeleteVolume deletes a volume. The volume must be in "available" state
    // (i.e., detached from all instances).
    DeleteVolume(ctx context.Context, volumeId string) error

    // ModifyVolume modifies the volume type, size, IOPS, and/or throughput.
    // This is an in-place operation — no detach required.
    // AWS enforces a 6-hour cooldown between modifications.
    ModifyVolume(ctx context.Context, volumeId string, spec EBSVolumeSpec) error

    // WaitUntilAvailable blocks until the volume reaches "available" state.
    WaitUntilAvailable(ctx context.Context, volumeId string) error

    // UpdateTags replaces all user tags on the volume.
    // Preserves praxis:* system tags.
    UpdateTags(ctx context.Context, volumeId string, tags map[string]string) error

    // FindByManagedKey searches for volumes tagged with praxis:managed-key=managedKey
    // that are not in "deleted" state.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### realEBSAPI Implementation

```go
type realEBSAPI struct {
    client  *ec2sdk.Client
    limiter *ratelimit.Limiter
}

func NewEBSAPI(client *ec2sdk.Client) EBSAPI {
    return &realEBSAPI{
        client:  client,
        limiter: ratelimit.New("ebs-volume", 20, 10),
    }
}
```

### Key Implementation Details

#### `CreateVolume`

```go
func (r *realEBSAPI) CreateVolume(ctx context.Context, spec EBSVolumeSpec) (string, error) {
    input := &ec2sdk.CreateVolumeInput{
        AvailabilityZone: aws.String(spec.AvailabilityZone),
        VolumeType:       ec2types.VolumeType(spec.VolumeType),
        Size:             aws.Int32(spec.SizeGiB),
        Encrypted:        aws.Bool(spec.Encrypted),
    }

    if spec.Iops > 0 {
        input.Iops = aws.Int32(spec.Iops)
    }
    if spec.Throughput > 0 {
        input.Throughput = aws.Int32(spec.Throughput)
    }
    if spec.KmsKeyId != "" {
        input.KmsKeyId = aws.String(spec.KmsKeyId)
    }
    if spec.SnapshotId != "" {
        input.SnapshotId = aws.String(spec.SnapshotId)
    }

    // Apply tags at creation — always include the praxis:managed-key ownership tag.
    ec2Tags := []ec2types.Tag{{
        Key:   aws.String("praxis:managed-key"),
        Value: aws.String(spec.ManagedKey),
    }}
    for k, v := range spec.Tags {
        ec2Tags = append(ec2Tags, ec2types.Tag{
            Key: aws.String(k), Value: aws.String(v),
        })
    }
    input.TagSpecifications = []ec2types.TagSpecification{{
        ResourceType: ec2types.ResourceTypeVolume,
        Tags:         ec2Tags,
    }}

    out, err := r.client.CreateVolume(ctx, input)
    if err != nil {
        return "", err
    }
    return aws.ToString(out.VolumeId), nil
}
```

#### `DescribeVolume`

```go
func (r *realEBSAPI) DescribeVolume(ctx context.Context, volumeId string) (ObservedState, error) {
    out, err := r.client.DescribeVolumes(ctx, &ec2sdk.DescribeVolumesInput{
        VolumeIds: []string{volumeId},
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.Volumes) == 0 {
        return ObservedState{}, fmt.Errorf("volume %s not found", volumeId)
    }
    vol := out.Volumes[0]

    obs := ObservedState{
        VolumeId:         aws.ToString(vol.VolumeId),
        AvailabilityZone: aws.ToString(vol.AvailabilityZone),
        VolumeType:       string(vol.VolumeType),
        SizeGiB:          aws.ToInt32(vol.Size),
        Encrypted:        aws.ToBool(vol.Encrypted),
        KmsKeyId:         aws.ToString(vol.KmsKeyId),
        State:            string(vol.State),
        SnapshotId:       aws.ToString(vol.SnapshotId),
        Tags:             make(map[string]string, len(vol.Tags)),
    }
    if vol.Iops != nil {
        obs.Iops = aws.ToInt32(vol.Iops)
    }
    if vol.Throughput != nil {
        obs.Throughput = aws.ToInt32(vol.Throughput)
    }
    for _, tag := range vol.Tags {
        obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
    }
    return obs, nil
}
```

#### `ModifyVolume`

```go
func (r *realEBSAPI) ModifyVolume(ctx context.Context, volumeId string, spec EBSVolumeSpec) error {
    input := &ec2sdk.ModifyVolumeInput{
        VolumeId:   aws.String(volumeId),
        VolumeType: ec2types.VolumeType(spec.VolumeType),
        Size:       aws.Int32(spec.SizeGiB),
    }
    if spec.Iops > 0 {
        input.Iops = aws.Int32(spec.Iops)
    }
    if spec.Throughput > 0 {
        input.Throughput = aws.Int32(spec.Throughput)
    }

    _, err := r.client.ModifyVolume(ctx, input)
    return err
}
```

#### `DeleteVolume`

```go
func (r *realEBSAPI) DeleteVolume(ctx context.Context, volumeId string) error {
    _, err := r.client.DeleteVolume(ctx, &ec2sdk.DeleteVolumeInput{
        VolumeId: aws.String(volumeId),
    })
    return err
}
```

#### `FindByManagedKey`

```go
func (r *realEBSAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    out, err := r.client.DescribeVolumes(ctx, &ec2sdk.DescribeVolumesInput{
        Filters: []ec2types.Filter{
            {Name: aws.String("tag:praxis:managed-key"), Values: []string{managedKey}},
            {Name: aws.String("status"), Values: []string{"creating", "available", "in-use"}},
        },
    })
    if err != nil {
        return "", err
    }

    var matches []string
    for _, vol := range out.Volumes {
        if id := aws.ToString(vol.VolumeId); id != "" {
            matches = append(matches, id)
        }
    }

    switch len(matches) {
    case 0:
        return "", nil
    case 1:
        return matches[0], nil
    default:
        return "", fmt.Errorf(
            "ownership corruption: %d volumes claim managed-key %q: %v; "+
                "manual intervention required",
            len(matches), managedKey, matches,
        )
    }
}
```

#### `UpdateTags`

Follows the EC2 instance pattern: delete old non-praxis tags, create new tags,
preserve `praxis:*` system tags.

### Error Classification Helpers

```go
func IsNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "InvalidVolume.NotFound" ||
               code == "InvalidVolumeID.Malformed"
    }
    errText := err.Error()
    return strings.Contains(errText, "InvalidVolume.NotFound")
}

func IsVolumeInUse(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "VolumeInUse"
    }
    errText := err.Error()
    return strings.Contains(errText, "VolumeInUse")
}

func IsModificationCooldown(err error) bool {
    if err == nil {
        return false
    }
    errText := err.Error()
    return strings.Contains(errText, "currently being modified") ||
           strings.Contains(errText, "modification cooldown")
}

func IsInvalidParam(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "InvalidParameterValue" ||
               code == "InvalidParameter"
    }
    return false
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/ebs/drift.go`

### HasDrift Function

```go
package ebs

// HasDrift returns true if the desired spec and observed state differ on mutable fields.
//
// EBS-specific drift rules:
// - availabilityZone is NOT checked — immutable, cannot move volumes between AZs.
// - encrypted is NOT checked — immutable, set at creation time.
// - kmsKeyId is NOT checked — immutable, set at creation time.
// - snapshotId is NOT checked — used at creation time only.
//
// Fields that ARE checked (and can be corrected via ModifyVolume):
// - volumeType
// - sizeGiB (grow only — shrink drift is reported but not corrected)
// - iops (for io1/io2/gp3)
// - throughput (for gp3)
// - tags
func HasDrift(desired EBSVolumeSpec, observed ObservedState) bool {
    if desired.VolumeType != observed.VolumeType {
        return true
    }
    if desired.SizeGiB != observed.SizeGiB {
        return true
    }
    if desired.Iops > 0 && desired.Iops != observed.Iops {
        return true
    }
    if desired.Throughput > 0 && desired.Throughput != observed.Throughput {
        return true
    }
    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }
    return false
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired EBSVolumeSpec, observed ObservedState) []FieldDiffEntry {
    var diffs []FieldDiffEntry

    // Mutable fields
    if desired.VolumeType != observed.VolumeType {
        diffs = append(diffs, FieldDiffEntry{
            Path: "spec.volumeType", Old: observed.VolumeType, New: desired.VolumeType,
        })
    }
    if desired.SizeGiB != observed.SizeGiB {
        path := "spec.sizeGiB"
        if desired.SizeGiB < observed.SizeGiB {
            path = "spec.sizeGiB (shrink not supported, ignored)"
        }
        diffs = append(diffs, FieldDiffEntry{
            Path: path,
            Old:  fmt.Sprintf("%d", observed.SizeGiB),
            New:  fmt.Sprintf("%d", desired.SizeGiB),
        })
    }
    if desired.Iops > 0 && desired.Iops != observed.Iops {
        diffs = append(diffs, FieldDiffEntry{
            Path: "spec.iops",
            Old:  fmt.Sprintf("%d", observed.Iops),
            New:  fmt.Sprintf("%d", desired.Iops),
        })
    }
    if desired.Throughput > 0 && desired.Throughput != observed.Throughput {
        diffs = append(diffs, FieldDiffEntry{
            Path: "spec.throughput",
            Old:  fmt.Sprintf("%d", observed.Throughput),
            New:  fmt.Sprintf("%d", desired.Throughput),
        })
    }

    // Immutable fields — report but do not correct
    if desired.AvailabilityZone != observed.AvailabilityZone && observed.AvailabilityZone != "" {
        diffs = append(diffs, FieldDiffEntry{
            Path: "spec.availabilityZone (immutable, ignored)",
            Old:  observed.AvailabilityZone,
            New:  desired.AvailabilityZone,
        })
    }

    // Tags
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

**File**: `internal/drivers/ebs/driver.go`

### Struct & Constructor

```go
package ebs

type EBSVolumeDriver struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) EBSAPI
}

func NewEBSVolumeDriver(accounts *auth.Registry) *EBSVolumeDriver {
    return NewEBSVolumeDriverWithFactory(accounts, func(cfg aws.Config) EBSAPI {
        return NewEBSAPI(awsclient.NewEC2Client(cfg))
    })
}

func NewEBSVolumeDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) EBSAPI) *EBSVolumeDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) EBSAPI {
            return NewEBSAPI(awsclient.NewEC2Client(cfg))
        }
    }
    return &EBSVolumeDriver{auth: accounts, apiFactory: factory}
}

func (d *EBSVolumeDriver) ServiceName() string {
    return ServiceName
}
```

### Provision Handler

```go
func (d *EBSVolumeDriver) Provision(ctx restate.ObjectContext, spec EBSVolumeSpec) (EBSVolumeOutputs, error) {
    api, _, err := d.apiForAccount(spec.Account)
    if err != nil {
        return EBSVolumeOutputs{}, restate.TerminalError(err, 400)
    }

    // Input validation
    if spec.Region == "" {
        return EBSVolumeOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
    }
    if spec.AvailabilityZone == "" {
        return EBSVolumeOutputs{}, restate.TerminalError(fmt.Errorf("availabilityZone is required"), 400)
    }

    // Load current state
    state, err := restate.Get[EBSVolumeState](ctx, drivers.StateKey)
    if err != nil {
        return EBSVolumeOutputs{}, err
    }

    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Mode = types.ModeManaged
    state.Error = ""
    state.Generation++

    volumeId := state.Outputs.VolumeId

    // Check if volume already exists (re-provision path)
    if volumeId != "" {
        descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
            obs, err := api.DescribeVolume(rc, volumeId)
            if err != nil {
                if IsNotFound(err) {
                    return ObservedState{}, restate.TerminalError(err, 404)
                }
                return ObservedState{}, err
            }
            return obs, nil
        })
        if descErr != nil || descResult.State == "deleted" || descResult.State == "deleting" {
            volumeId = "" // Volume gone, recreate
        }
    }

    // Pre-flight ownership conflict check (first provision only)
    if volumeId == "" && spec.ManagedKey != "" {
        conflictId, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.FindByManagedKey(rc, spec.ManagedKey)
        })
        if conflictErr != nil {
            return EBSVolumeOutputs{}, conflictErr
        }
        if conflictId != "" {
            return EBSVolumeOutputs{}, restate.TerminalError(
                fmt.Errorf("volume name %q in this region is already managed by Praxis (volumeId: %s); "+
                    "remove the existing resource or use a different metadata.name", spec.ManagedKey, conflictId),
                409,
            )
        }
    }

    // Create volume if it doesn't exist
    if volumeId == "" {
        newVolumeId, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            id, err := api.CreateVolume(rc, spec)
            if err != nil {
                if IsInvalidParam(err) {
                    return "", restate.TerminalError(err, 400)
                }
                return "", err
            }
            return id, nil
        })
        if err != nil {
            state.Status = types.StatusError
            state.Error = err.Error()
            restate.Set(ctx, drivers.StateKey, state)
            return EBSVolumeOutputs{}, err
        }
        volumeId = newVolumeId

        // Wait for volume to become available
        _, waitErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.WaitUntilAvailable(rc, volumeId)
        })
        if waitErr != nil {
            state.Status = types.StatusError
            state.Error = fmt.Sprintf("volume %s created but failed to become available: %v", volumeId, waitErr)
            state.Outputs = EBSVolumeOutputs{VolumeId: volumeId}
            restate.Set(ctx, drivers.StateKey, state)
            return EBSVolumeOutputs{}, waitErr
        }
    } else {
        // Re-provision path: converge mutable attributes via ModifyVolume
        if volumeNeedsModification(spec, state.Observed) {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                err := api.ModifyVolume(rc, volumeId, spec)
                if err != nil {
                    if IsModificationCooldown(err) {
                        return restate.Void{}, restate.TerminalError(
                            fmt.Errorf("volume %s is in modification cooldown (6h between changes): %w", volumeId, err), 429)
                    }
                    return restate.Void{}, err
                }
                return restate.Void{}, nil
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return EBSVolumeOutputs{}, err
            }
        }

        // Tags
        if !tagsMatch(spec.Tags, state.Observed.Tags) {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateTags(rc, volumeId, spec.Tags)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return EBSVolumeOutputs{}, err
            }
        }
    }

    // Describe final state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeVolume(rc, volumeId)
    })
    if err != nil {
        state.Status = types.StatusError
        state.Error = err.Error()
        restate.Set(ctx, drivers.StateKey, state)
        return EBSVolumeOutputs{}, err
    }

    outputs := outputsFromObserved(observed)
    state.Observed = observed
    state.Outputs = outputs
    state.Status = types.StatusReady
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx, &state)
    return outputs, nil
}
```

### Delete Handler

```go
func (d *EBSVolumeDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[EBSVolumeState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state.Status == types.StatusDeleted {
        return nil // already deleted
    }

    // Mode guard: Observed-mode resources cannot be deleted
    if state.Mode == types.ModeObserved {
        return restate.TerminalError(
            fmt.Errorf("cannot delete EBS volume in Observed mode; change to Managed mode first"), 409)
    }

    volumeId := state.Outputs.VolumeId
    if volumeId == "" {
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
        err := api.DeleteVolume(rc, volumeId)
        if err != nil {
            if IsNotFound(err) {
                return restate.Void{}, nil // already gone
            }
            if IsVolumeInUse(err) {
                return restate.Void{}, restate.TerminalError(
                    fmt.Errorf("volume %s is still attached to an instance; detach before deleting", volumeId), 409)
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

Follow the established pattern from EC2/VPC drivers. Import defaults to `ModeObserved`.
Reconcile detects drift on mutable fields, corrects via `ModifyVolume` + `UpdateTags`
for Managed mode, reports only for Observed mode.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/ebs_adapter.go`

```go
func (a *EBSAdapter) Kind() string        { return ebs.ServiceName }
func (a *EBSAdapter) ServiceName() string  { return ebs.ServiceName }
func (a *EBSAdapter) Scope() KeyScope      { return KeyScopeRegion }

func (a *EBSAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    doc, err := decodeResourceDocument(resourceDoc)
    if err != nil { return "", err }
    spec, err := a.decodeSpec(doc)
    if err != nil { return "", err }
    if err := ValidateKeyPart("region", spec.Region); err != nil { return "", err }
    name := strings.TrimSpace(doc.Metadata.Name)
    if err := ValidateKeyPart("volume name", name); err != nil { return "", err }
    return JoinKey(spec.Region, name), nil
}

func (a *EBSAdapter) BuildImportKey(region, resourceID string) (string, error) {
    if err := ValidateKeyPart("region", region); err != nil { return "", err }
    if err := ValidateKeyPart("volume ID", resourceID); err != nil { return "", err }
    return JoinKey(region, resourceID), nil
}
```

Plan follows the state-driven pattern (GetOutputs → DescribeVolume by ID).

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go`

Add `NewEBSAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-storage/main.go`

Add `.Bind(restate.Reflect(ebsDriver))` alongside the existing S3 driver binding.
EBS is a storage resource, so it belongs in the storage driver pack.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes needed — EBS joins the existing `praxis-storage` service.
Add `test-ebs` and `ls-ebs` targets to the justfile.

---

## Step 11 — Unit Tests

### `internal/drivers/ebs/drift_test.go`

1. `TestHasDrift_NoDrift` — identical spec and observed.
2. `TestHasDrift_VolumeTypeChanged` — gp3 → io2.
3. `TestHasDrift_SizeIncreased` — desired larger than observed.
4. `TestHasDrift_IopsChanged` — IOPS drift for io1.
5. `TestHasDrift_ThroughputChanged` — throughput drift for gp3.
6. `TestHasDrift_TagsChanged` — tag addition/removal.
7. `TestHasDrift_ImmutableFieldIgnored` — AZ change not flagged as drift.
8. `TestComputeFieldDiffs_ShrinkNotSupported` — reports shrink with "(shrink not supported, ignored)".
9. `TestComputeFieldDiffs_ImmutableAZ` — reports AZ change with "(immutable, ignored)".

### `internal/drivers/ebs/aws_test.go`

1. `TestIsNotFound_True` — InvalidVolume.NotFound.
2. `TestIsNotFound_False` — other errors.
3. `TestIsVolumeInUse_True` — VolumeInUse error.
4. `TestIsModificationCooldown_True` — cooldown string match.
5. `TestFindByManagedKey_Found` — single match.
6. `TestFindByManagedKey_NotFound` — no match.
7. `TestFindByManagedKey_MultipleMatchesError` — ownership corruption.

### `internal/drivers/ebs/driver_test.go`

1. `TestSpecFromObserved_RoundTrip` — import creates matching spec.
2. `TestServiceName` — returns "EBSVolume".
3. `TestVolumeNeedsModification_TypeChange` — detects type change.
4. `TestVolumeNeedsModification_SizeIncrease` — detects size increase.
5. `TestVolumeNeedsModification_NoChange` — returns false.
6. `TestOutputsFromObserved` — correct output mapping.

### `internal/core/provider/ebs_adapter_test.go`

1. `TestEBSAdapter_DecodeSpecAndBuildKey` — parses JSON doc, returns `region~name` key.
2. `TestEBSAdapter_BuildImportKey` — returns `region~volumeId` key.
3. `TestEBSAdapter_Kind` — returns "EBSVolume".
4. `TestEBSAdapter_Scope` — returns `KeyScopeRegion`.
5. `TestEBSAdapter_NormalizeOutputs` — converts struct to map.

---

## Step 12 — Integration Tests

**File**: `tests/integration/ebs_driver_test.go`

1. **TestEBSProvision_CreatesRealVolume** — Creates a volume, verifies it in DescribeVolumes.
2. **TestEBSProvision_Idempotent** — Two provisions with same spec produce same outputs.
3. **TestEBSImport_ExistingVolume** — Creates volume via SDK, imports via driver.
4. **TestEBSDelete_RemovesVolume** — Provisions, deletes, verifies volume gone.
5. **TestEBSDelete_AttachedVolumeFails** — Attached volume returns 409.
6. **TestEBSReconcile_DetectsAndFixesDrift** — Tag drift correction.
7. **TestEBSGetStatus_ReturnsReady** — Provisions, checks Ready + ModeManaged.

---

## EBS-Specific Design Decisions

### 1. Key Strategy: region~metadata.name

Same rationale as EC2 and VPC. Volume IDs are AWS-assigned and unavailable at plan
time. `metadata.name` must be region-unique for managed EBS resources.

### 2. Volume Modification Cooldown

AWS enforces a 6-hour cooldown between `ModifyVolume` calls on the same volume.
If Provision or Reconcile attempts a modification during the cooldown period, the
driver returns a terminal error with status 429 (rate limited). The operator must
wait and re-apply.

The driver does NOT implement automatic retry-after-cooldown because:
- A 6-hour `restate.Sleep()` would tie up the Virtual Object key for 6 hours.
- The cooldown is a rare operational event, not a normal flow.
- Operators should know about the cooldown and plan accordingly.

### 3. Volume Size: Grow Only

EBS volumes can only be increased in size, never decreased. The drift engine reports
a size decrease as a diff with the "(shrink not supported, ignored)" annotation in
the field path, but does not attempt to correct it. `ModifyVolume` skips the size
parameter if `desired.SizeGiB < observed.SizeGiB`.

### 4. Volume Must Be Detached to Delete

`DeleteVolume` fails if the volume is attached to an instance (`VolumeInUse` error).
The driver does NOT auto-detach — this would be destructive to the attached instance.
The terminal error (409) tells the operator to detach before deleting.

For templates that compose EC2 + EBS, the DAG ensures the EC2 instance is deleted
(or the volume is detached) before the EBS volume deletion runs, via resource
dependency ordering.

### 5. Snapshot-Based Creation

The `snapshotId` field is used only at creation time. Once a volume is created from
a snapshot, the snapshot reference is informational — it cannot be changed. Drift
detection does not check `snapshotId`.

### 6. Driver Pack Placement: praxis-storage

EBS volumes are storage resources. They belong in the `praxis-storage` driver pack
alongside S3, not in `praxis-compute` with EC2 instances. The EC2 SDK client is
reused (EBS APIs are part of the EC2 service), so `NewEC2Client` is already
available.

### 7. Import Defaults to ModeObserved

EBS volumes may contain critical data. Accidental deletion via an import VO is
destructive and unrecoverable (unless snapshots exist). The Delete mode guard
(409 for Observed mode) prevents this.

---

## Design Decisions (Resolved)

1. **Should ModifyVolume be called with all fields or only changed fields?**
   All fields. `ModifyVolume` is convergent — AWS ignores unchanged fields. Sending
   all fields avoids tracking which individual fields changed and matches the
   convergent Provision pattern of the other drivers.

2. **Should the driver wait for ModifyVolume to complete?**
   No. `ModifyVolume` returns immediately and the change applies asynchronously.
   The driver does NOT call a waiter after `ModifyVolume` — the volume remains
   in "optimizing" state during the modification. The next Reconcile cycle will
   describe the volume and see the updated values once optimization completes.
   Waiting for the modification (up to several hours for large volumes) would
   block the Virtual Object key for too long.

3. **LocalStack EBS compatibility scope:**
   Integration tests cover: create, describe, delete, tag updates. `ModifyVolume`
   may not be fully supported by LocalStack — volume type/size/IOPS changes are
   covered in unit tests with a mocked EBSAPI only.

4. **Should Observed mode block both Reconcile corrections and Delete?**
   Yes. Same as EC2 — Observed mode is fully read-only.

---

## Checklist

- [ ] **Schema**: `schemas/aws/ebs/ebs.cue` created
- [ ] **Types**: `internal/drivers/ebs/types.go` created
- [ ] **AWS API**: `internal/drivers/ebs/aws.go` created
- [ ] **Drift**: `internal/drivers/ebs/drift.go` created
- [ ] **Driver**: `internal/drivers/ebs/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/ebs_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: EBS driver bound in `cmd/praxis-storage/main.go`
- [ ] **Justfile**: Updated with ebs targets
- [ ] **Unit tests (drift)**: `internal/drivers/ebs/drift_test.go`
- [ ] **Unit tests (aws helpers)**: `internal/drivers/ebs/aws_test.go`
- [ ] **Unit tests (driver)**: `internal/drivers/ebs/driver_test.go`
- [ ] **Unit tests (adapter)**: `internal/core/provider/ebs_adapter_test.go`
- [ ] **Integration tests**: `tests/integration/ebs_driver_test.go`
- [ ] **Conflict check**: `FindByManagedKey` in EBSAPI interface
- [ ] **Ownership tag**: `praxis:managed-key` written by `CreateVolume`
- [ ] **Import default mode**: `ModeObserved` when unspecified
- [ ] **Delete mode guard**: Delete handler blocks deletion for ModeObserved (409)
- [ ] **Delete volume-in-use guard**: Delete handler returns 409 for attached volumes
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/ebs/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestEBS -tags=integration`
