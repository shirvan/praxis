# EC2 Instance Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages EC2 instances, following the
> exact patterns established by the S3 Bucket and Security Group drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~metadata.name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned instance ID
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
12. [Step 9 — Compute Driver Pack Entry Point & Dockerfile](#step-9--compute-driver-pack-entry-point--dockerfile)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [EC2-Specific Design Decisions](#ec2-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The EC2 driver manages the lifecycle of EC2 **instances** only. AMIs, EBS volumes,
key pairs, launch templates, and Elastic IPs are separate drivers (or handled
within compound templates that compose multiple resource types). This document
focuses exclusively on EC2 instances.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an EC2 instance |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing instance |
| `Delete` | `ObjectContext` (exclusive) | Terminate an instance (blocked for Observed mode) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return instance outputs |

---

## 2. Key Strategy

### Why `region~metadata.name`, not `region~instanceId`

A Restate Virtual Object key is immutable once created, and there is no rename path.
Using `region~instanceId` would be impossible because the instance ID is assigned by
AWS at launch time — it is unavailable when `BuildKey` runs at plan/dispatch time.
The pipeline in `pipeline.go:272` calls `adapter.BuildKey()` before dispatch and
passes the returned key to `workflow.go:147` for `Provision`, and later to
`handlers_resource.go:62` for `Delete` and `Import`. The key must be the **same** at
every stage.

The Virtual Object key is always `region~metadata.name`. This matches how the
existing wiring works:

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision** (pipeline → workflow → driver): dispatched to same key.
3. **Delete** (pipeline → workflow → driver): dispatched to same key.
4. **Plan** (adapter → describe by instance ID from state): uses the key to reach
   the Virtual Object, reads the stored instance ID from state, describes by ID.
5. **Import** (handlers_resource.go): `BuildImportKey(region, resourceID)` returns
   `region~resourceID` where `resourceID` is the instance ID — **this targets a
   different Virtual Object** intentionally (see Import Semantics below).

The AWS instance ID is stored only in `EC2InstanceState.Outputs.InstanceId` and
`EC2InstanceState.Observed.InstanceId`. It is the AWS API handle, not the Praxis
identity handle.

### Constraint: metadata.name must be unique within a region

Instance names in AWS are only unique within region+subnet, but the key drops
subnet. This is intentional — `metadata.name` must be
region-unique for managed EC2 resources. Justification:

- S3 requires globally unique bucket names. SG requires VPC-unique group names.
  EC2 requiring region-unique template names is consistent and simpler.
- Subnet is a deployment-time concern (which AZ, which VPC), not a naming concern.
  If a user wants two instances named "web" in different subnets, they should use
  different template resource names ("web-a", "web-b").
- CUE schema validation can enforce the name pattern. The adapter validates at
  `BuildKey` time that `metadata.name` and `spec.region` are non-empty.

#### Conflict enforcement via ownership tags

Schema validation and `BuildKey` can validate *shape*, not cross-deployment
uniqueness. Two templates using the same `metadata.name` in the same region
produce the same Virtual Object key and therefore converge on the same EC2
lifecycle — potentially overwriting each other's desired state without any
error signal.

To make key collisions visible, the driver enforces ownership at provisioning
time using an AWS resource tag:

- **Tag written at launch**: every `RunInstance` call adds the tag
  `praxis:managed-key = <region~metadata.name>` to the instance in addition
  to any user-declared tags. This tag is immutable from Praxis's perspective
  (never removed, never overwritten by drift correction).

- **Pre-flight conflict check**: when `Provision` runs with no existing VO
  state (first provision), it calls `FindByManagedKey` to search for any
  live instance already tagged with `praxis:managed-key = <this key>`. If
  found, `Provision` returns a terminal error (status 409):
  `"instance name 'X' in region Y is already managed by Praxis (i-0abc123)"`.
  This gives operators a clear conflict signal instead of silently launching
  a second instance.

- **`FindByManagedKey(ctx, managedKey) (string, error)`** is added to the
  `EC2API` interface (see Step 4). It queries by tag filter and returns:
  - `("", nil)` if no live instances match (safe to create),
  - `(instanceId, nil)` if exactly one matches (conflict/recovery target),
  - `("", error)` if more than one matches (ownership corruption — terminal error).

This is the *minimum viable conflict policy*. Cross-deployment ownership
(e.g., two separate Praxis installations managing the same region) is not
protected; that requires a centralised lock, which is out of scope.

### Import semantics: separate lifecycle track

Import and template-based management produce **separate Virtual Objects** for the
same AWS instance when the instance was not originally provisioned by Praxis. This
is the same as how the S3 and SG drivers behave:

- `praxis import --kind EC2Instance --region us-east-1 --resource-id i-0abc123`:
  Creates VO key `us-east-1~i-0abc123`. The import handler describes the instance,
  synthesizes a spec, and manages it under that key going forward.

- Template with `metadata.name: web-server` in `us-east-1`:
  Creates VO key `us-east-1~web-server`. A brand-new lifecycle.

The two VOs will target the same AWS resource only if the user explicitly
provisions a template that references the imported instance. This is consistent
with the existing architecture where S3 `BuildImportKey` returns the bucket name
(same as `BuildKey`) and SG `BuildImportKey` returns the group ID (different from
`BuildKey` which is `vpcId~groupName`).

### Plan-time instance resolution

The driver does not search by Name tag — Name tags are mutable and not unique.
Plan-time resolution works as follows:

1. **Preferred path**: The adapter's `Plan()` method reads the Virtual Object's
   stored state via `GetOutputs` (a shared handler, non-blocking). If the VO has
   outputs with an `instanceId`, describe that specific instance by ID.

2. **Fallback for new resources**: If `GetOutputs` returns empty (no instance
   provisioned yet), the plan reports `OpCreate`. No AWS describe needed.

3. **No `FindInstanceByName`**: The EC2API interface does not include a
   Name-tag search method. Instance identity after provisioning comes from
   the stored instance ID.

> **Product decision — state-driven plan contract**: Plan returns `OpCreate`
> whenever there is no Praxis-owned Virtual Object state for this key, regardless
> of whether an unmanaged instance with the same name already exists in AWS. Two
> consequences operators must understand:
>
> - **Unmanaged existing instance**: if a human-created EC2 instance already
>   exists in the target region with the same `metadata.name`, `praxis plan`
>   will show `OpCreate` and `praxis apply` will launch a *second* instance.
>   The pre-flight ownership check in `Provision` (see Design Decisions §10)
>   catches this **only if the existing instance already carries the
>   `praxis:managed-key` tag** — i.e., it was previously managed by Praxis.
>   If the existing instance was created manually or by another tool and has
>   no ownership tag, `Provision` will silently launch a second instance.
>   Operators are responsible for not reusing names from pre-existing
>   unmanaged resources.
> - **Wiped Praxis state**: if the Restate journal is lost after an instance was
>   provisioned, the plan degrades to `OpCreate`. `Provision` will re-create
>   the resource unless the ownership tag is still present on the existing AWS
>   instance, in which case the conflict check fires.
>
> This departs from the S3 and SG adapters, which do a live describe during every
> Plan call because bucket names and security group names are stable, unique,
> user-visible AWS identifiers. EC2 has no such guarantee: Name tags are mutable
> and non-unique. State-driven discovery is the only durable option.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/ec2/types.go           — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/ec2/aws.go             — EC2API interface + realEC2API implementation
✦ internal/drivers/ec2/drift.go           — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/ec2/driver.go          — EC2InstanceDriver Virtual Object
✦ internal/drivers/ec2/driver_test.go     — Unit tests for driver (mocked AWS)
✦ internal/drivers/ec2/aws_test.go        — Unit tests for error classification helpers
✦ internal/drivers/ec2/drift_test.go      — Unit tests for drift detection
✦ internal/core/provider/ec2_adapter.go   — EC2Adapter implementing provider.Adapter
✦ internal/core/provider/ec2_adapter_test.go — Unit tests for EC2 adapter
✦ schemas/aws/ec2/ec2.cue                 — CUE schema for EC2Instance resource
✦ cmd/praxis-compute/main.go              — Compute driver pack entry point (EC2 bound here)
✦ cmd/praxis-compute/Dockerfile           — Multi-stage Docker build
✦ tests/integration/ec2_driver_test.go    — Integration tests (Testcontainers + LocalStack)
✎ internal/core/provider/registry.go      — Add NewEC2Adapter to NewRegistry()
✎ internal/infra/awsclient/client.go      — Already has NewEC2Client() — NO changes needed
✎ docker-compose.yaml                     — Add praxis-compute service
✎ justfile                                — Add ec2 build/test/register targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ec2/ec2.cue`

This defines the shape of an `EC2Instance` resource document. The template engine
validates user templates against this schema before dispatch.

```cue
package ec2

#EC2Instance: {
    apiVersion: "praxis.io/v1"
    kind:       "EC2Instance"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to launch the instance in.
        region: string

        // imageId is the AMI ID (e.g. "ami-0abcdef1234567890").
        imageId: string & =~"^ami-[a-f0-9]{8,17}$"

        // instanceType is the EC2 instance type (e.g. "t3.micro", "m5.large").
        instanceType: string

        // keyName is the name of an existing EC2 key pair for SSH access.
        // Optional — omit for instances that don't need SSH (e.g. containers, SSM-only).
        keyName?: string

        // subnetId is the VPC subnet to launch into.
        // Required for VPC instances (which is all modern instances).
        subnetId: string

        // securityGroupIds is a list of security group IDs to attach.
        securityGroupIds: [...string] | *[]

        // userData is base64-encoded user data script.
        // Optional — the driver will base64-encode it if provided as plain text.
        userData?: string

        // iamInstanceProfile is the name or ARN of an IAM instance profile.
        // Optional — omit for instances that don't need IAM role access.
        iamInstanceProfile?: string

        // rootVolume configures the root EBS volume.
        rootVolume?: {
            sizeGiB:    int & >=1 & <=16384 | *20
            volumeType: "gp2" | "gp3" | "io1" | "io2" | "st1" | "sc1" | *"gp3"
            encrypted:  bool | *true
        }

        // monitoring enables detailed CloudWatch monitoring (1-minute intervals).
        monitoring: bool | *false

        // Tags applied to the instance only. Root volume tagging is not supported (see Design Decisions §3).
        tags: [string]: string
    }

    outputs?: {
        instanceId:       string
        privateIpAddress: string
        publicIpAddress?: string
        privateDnsName:   string
        arn:              string
        state:            string
        subnetId:         string
        vpcId:            string
    }
}
```

**Key decisions**:

- `imageId` uses a regex to validate AMI ID format — this catches typos before hitting AWS.
- `subnetId` is required (not optional) — all modern instances launch in a VPC subnet.
- `rootVolume` is optional — AWS uses defaults if not specified.
- `securityGroupIds` takes IDs (not names) — names are ambiguous across VPCs.
- `userData` is a plain string — the driver handles base64 encoding.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

The existing `NewEC2Client(cfg aws.Config) *ec2.Client` function already exists and
is used by the Security Group driver. The EC2 instance driver will reuse it.

---

## Step 3 — Driver Types

**File**: `internal/drivers/ec2/types.go`

Define all the data structures the driver uses. Follow the S3/SG pattern exactly:
one package-level constant for `ServiceName`, typed spec/outputs/observed/state structs.

```go
package ec2

import "github.com/praxiscloud/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for EC2 instances.
// This becomes the URL path component (e.g., /EC2Instance/<key>/Provision).
const ServiceName = "EC2Instance"

// EC2InstanceSpec is the desired state for an EC2 instance.
// Fields map to the #EC2Instance CUE schema in schemas/aws/ec2/ec2.cue.
type EC2InstanceSpec struct {
    Account            string            `json:"account,omitempty"`
    Region             string            `json:"region"`
    ImageId            string            `json:"imageId"`
    InstanceType       string            `json:"instanceType"`
    KeyName            string            `json:"keyName,omitempty"`
    SubnetId           string            `json:"subnetId"`
    SecurityGroupIds   []string          `json:"securityGroupIds,omitempty"`
    UserData           string            `json:"userData,omitempty"`
    IamInstanceProfile string            `json:"iamInstanceProfile,omitempty"`
    RootVolume         *RootVolumeSpec   `json:"rootVolume,omitempty"`
    Monitoring         bool              `json:"monitoring"`
    Tags               map[string]string `json:"tags,omitempty"`
    // ManagedKey is the Restate Virtual Object key (region~metadata.name).
    // Set by the adapter before dispatch; written as praxis:managed-key tag at launch.
    // Never stored in user-facing YAML, never validated by CUE.
    ManagedKey         string            `json:"managedKey,omitempty"`
}

// RootVolumeSpec configures the root EBS volume.
type RootVolumeSpec struct {
    SizeGiB    int32  `json:"sizeGiB"`
    VolumeType string `json:"volumeType"`
    Encrypted  bool   `json:"encrypted"`
}

// EC2InstanceOutputs is produced after provisioning and stored in Restate K/V.
// Dependent resources reference these via output expressions (e.g., "${ resources.web.outputs.instanceId }").
type EC2InstanceOutputs struct {
    InstanceId       string `json:"instanceId"`
    PrivateIpAddress string `json:"privateIpAddress"`
    PublicIpAddress  string `json:"publicIpAddress,omitempty"`
    PrivateDnsName   string `json:"privateDnsName"`
    ARN              string `json:"arn"`
    State            string `json:"state"`
    SubnetId         string `json:"subnetId"`
    VpcId            string `json:"vpcId"`
}

// ObservedState captures the actual configuration of an instance from AWS Describe calls.
type ObservedState struct {
    InstanceId         string            `json:"instanceId"`
    ImageId            string            `json:"imageId"`
    InstanceType       string            `json:"instanceType"`
    KeyName            string            `json:"keyName"`
    SubnetId           string            `json:"subnetId"`
    VpcId              string            `json:"vpcId"`
    SecurityGroupIds   []string          `json:"securityGroupIds"`
    IamInstanceProfile string            `json:"iamInstanceProfile"`
    Monitoring         bool              `json:"monitoring"`
    State              string            `json:"state"` // "running", "stopped", etc.
    PrivateIpAddress   string            `json:"privateIpAddress"`
    PublicIpAddress    string            `json:"publicIpAddress"`
    PrivateDnsName     string            `json:"privateDnsName"`
    RootVolumeType     string            `json:"rootVolumeType"`
    RootVolumeSizeGiB  int32             `json:"rootVolumeSizeGiB"`
    RootVolumeEncrypted bool            `json:"rootVolumeEncrypted"`
    Tags               map[string]string `json:"tags"`
}

// EC2InstanceState is the single atomic state object stored under drivers.StateKey.
// All fields written together in one restate.Set() call.
type EC2InstanceState struct {
    Desired            EC2InstanceSpec      `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            EC2InstanceOutputs   `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

**Why these fields**:

- `Account` is passed through from the adapter for credential resolution (same as S3/SG).
- `RootVolume` is a pointer so it can be nil (optional), unlike the SG rules which are slices.
- `ObservedState.State` tracks the EC2 instance state machine (`running`, `stopped`, `terminated`).
- `SecurityGroupIds` is a slice of strings (not a struct) — simplifies drift comparison.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/ec2/aws.go`

This file defines the `EC2API` interface and its real implementation (`realEC2API`).
The interface is what gets mocked in unit tests. All methods take `context.Context`
(not `restate.RunContext`) — the driver wraps calls in `restate.Run()`.

### EC2API Interface

```go
package ec2

import (
    "context"
    // ... imports
)

// EC2API abstracts the AWS EC2 SDK operations that the driver uses.
// All methods receive a plain context.Context, NOT a restate.RunContext.
// The caller in driver.go wraps these calls inside restate.Run().
type EC2API interface {
    // RunInstance launches a new EC2 instance with the given spec.
    // Returns the instance ID assigned by AWS.
    RunInstance(ctx context.Context, spec EC2InstanceSpec) (string, error)

    // DescribeInstance returns the full observed state of an instance.
    DescribeInstance(ctx context.Context, instanceId string) (ObservedState, error)

    // TerminateInstance terminates an instance.
    TerminateInstance(ctx context.Context, instanceId string) error

    // WaitUntilRunning blocks until the instance reaches "running" state.
    WaitUntilRunning(ctx context.Context, instanceId string) error

    // ModifyInstanceType stops the instance, changes the type, and restarts.
    // This causes downtime.
    ModifyInstanceType(ctx context.Context, instanceId, newType string) error

    // ModifySecurityGroups changes the security groups attached to an instance (live).
    ModifySecurityGroups(ctx context.Context, instanceId string, sgIds []string) error

    // UpdateMonitoring enables or disables detailed monitoring.
    UpdateMonitoring(ctx context.Context, instanceId string, enabled bool) error

    // UpdateTags replaces all tags on the instance.
    // Does NOT tag root volumes — see Design Decisions §3.
    UpdateTags(ctx context.Context, instanceId string, tags map[string]string) error

    // FindByManagedKey searches for live (non-terminated) instances tagged with
    // praxis:managed-key=managedKey.
    //
    // Return semantics:
    //   - ("", nil):       no match — safe to create.
    //   - (instanceId, nil): exactly one match — conflict or recovery target.
    //   - ("", error):      more than one match → terminal ownership-corruption
    //                        error, or an AWS API failure.
    //
    // The "more than one match" case should never happen under normal operation.
    // It indicates a bug or manual tag tampering. The caller (Provision) treats
    // it as a terminal error (status 500) so the operator must investigate.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

The interface does not include a `FindInstanceByName` method — Name-tag searches are
mutable and non-unique. Plan-time resolution reads the stored instance ID from the
Virtual Object via `GetOutputs` (see Key Strategy §2).

### realEC2API Implementation

```go
type realEC2API struct {
    client  *ec2sdk.Client
    limiter *ratelimit.Limiter
}

func NewEC2API(client *ec2sdk.Client) EC2API {
    return &realEC2API{
        client:  client,
        limiter: ratelimit.New("ec2-instance", 20, 10),
    }
}
```

### Key Implementation Details for Each Method

#### `RunInstance`

```go
func (r *realEC2API) RunInstance(ctx context.Context, spec EC2InstanceSpec) (string, error) {
    // Build RunInstancesInput from spec
    input := &ec2sdk.RunInstancesInput{
        ImageId:      aws.String(spec.ImageId),
        InstanceType: ec2types.InstanceType(spec.InstanceType),
        MinCount:     aws.Int32(1),
        MaxCount:     aws.Int32(1),
        SubnetId:     aws.String(spec.SubnetId),
    }

    // Optional fields
    if spec.KeyName != "" {
        input.KeyName = aws.String(spec.KeyName)
    }
    if len(spec.SecurityGroupIds) > 0 {
        input.SecurityGroupIds = spec.SecurityGroupIds
    }
    if spec.UserData != "" {
        // Always base64-encode — see base64Encode() doc for why we don't try to detect.
        input.UserData = aws.String(base64Encode(spec.UserData))
    }
    if spec.IamInstanceProfile != "" {
        input.IamInstanceProfile = &ec2types.IamInstanceProfileSpecification{
            Name: aws.String(spec.IamInstanceProfile),
            // Support ARN too: detect by prefix "arn:"
        }
    }
    if spec.RootVolume != nil {
        // Root device name is "/dev/xvda" (Amazon Linux default).
        // See Design Decisions §11 for AMI-family considerations.
        input.BlockDeviceMappings = []ec2types.BlockDeviceMapping{{
            DeviceName: aws.String("/dev/xvda"),
            Ebs: &ec2types.EbsBlockDevice{
                VolumeSize: aws.Int32(spec.RootVolume.SizeGiB),
                VolumeType: ec2types.VolumeType(spec.RootVolume.VolumeType),
                Encrypted:  aws.Bool(spec.RootVolume.Encrypted),
            },
        }}
    }
    if spec.Monitoring {
        input.Monitoring = &ec2types.RunInstancesMonitoringEnabled{
            Enabled: aws.Bool(true),
        }
    }

    // Apply tags at launch to the instance only.
    // Tags apply to the instance only — see Design Decisions §3.
    // Always include the praxis:managed-key ownership tag (see Design Decisions §10).
    ec2Tags := []ec2types.Tag{{
        Key:   aws.String("praxis:managed-key"),
        Value: aws.String(spec.ManagedKey), // set by adapter before dispatch
    }}
    for k, v := range spec.Tags {
        ec2Tags = append(ec2Tags, ec2types.Tag{
            Key: aws.String(k), Value: aws.String(v),
        })
    }
    input.TagSpecifications = []ec2types.TagSpecification{{
        ResourceType: ec2types.ResourceTypeInstance,
        Tags:         ec2Tags,
    }}

    out, err := r.client.RunInstances(ctx, input)
    if err != nil {
        return "", err
    }
    if len(out.Instances) == 0 {
        return "", fmt.Errorf("RunInstances returned no instances")
    }
    return aws.ToString(out.Instances[0].InstanceId), nil
}
```

#### `DescribeInstance`

```go
func (r *realEC2API) DescribeInstance(ctx context.Context, instanceId string) (ObservedState, error) {
    out, err := r.client.DescribeInstances(ctx, &ec2sdk.DescribeInstancesInput{
        InstanceIds: []string{instanceId},
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
        return ObservedState{}, fmt.Errorf("instance %s not found", instanceId)
    }
    inst := out.Reservations[0].Instances[0]

    obs := ObservedState{
        InstanceId:       aws.ToString(inst.InstanceId),
        ImageId:          aws.ToString(inst.ImageId),
        InstanceType:     string(inst.InstanceType),
        KeyName:          aws.ToString(inst.KeyName),
        SubnetId:         aws.ToString(inst.SubnetId),
        VpcId:            aws.ToString(inst.VpcId),
        State:            string(inst.State.Name),
        PrivateIpAddress: aws.ToString(inst.PrivateIpAddress),
        PublicIpAddress:  aws.ToString(inst.PublicIpAddress),
        PrivateDnsName:   aws.ToString(inst.PrivateDnsName),
        Tags:             make(map[string]string, len(inst.Tags)),
    }
    if inst.Monitoring != nil {
        obs.Monitoring = inst.Monitoring.State == ec2types.MonitoringStateEnabled
    }
    if inst.IamInstanceProfile != nil {
        // Extract the profile name from the ARN
        obs.IamInstanceProfile = extractProfileName(aws.ToString(inst.IamInstanceProfile.Arn))
    }
    for _, sg := range inst.SecurityGroups {
        obs.SecurityGroupIds = append(obs.SecurityGroupIds, aws.ToString(sg.GroupId))
    }
    for _, tag := range inst.Tags {
        obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
    }

    // Describe root volume (need a separate API call)
    if inst.RootDeviceName != nil {
        for _, bdm := range inst.BlockDeviceMappings {
            if aws.ToString(bdm.DeviceName) == aws.ToString(inst.RootDeviceName) && bdm.Ebs != nil {
                volId := aws.ToString(bdm.Ebs.VolumeId)
                volOut, volErr := r.client.DescribeVolumes(ctx, &ec2sdk.DescribeVolumesInput{
                    VolumeIds: []string{volId},
                })
                if volErr == nil && len(volOut.Volumes) > 0 {
                    vol := volOut.Volumes[0]
                    obs.RootVolumeType = string(vol.VolumeType)
                    obs.RootVolumeSizeGiB = aws.ToInt32(vol.Size)
                    obs.RootVolumeEncrypted = aws.ToBool(vol.Encrypted)
                }
                break
            }
        }
    }

    // Sort security group IDs for deterministic comparison
    sort.Strings(obs.SecurityGroupIds)

    return obs, nil
}
```

#### `TerminateInstance`

```go
func (r *realEC2API) TerminateInstance(ctx context.Context, instanceId string) error {
    _, err := r.client.TerminateInstances(ctx, &ec2sdk.TerminateInstancesInput{
        InstanceIds: []string{instanceId},
    })
    return err
}
```

#### `WaitUntilRunning`

> **Crash-recovery note**: `WaitUntilRunning` blocks for up to 5 minutes inside a
> single `restate.Run()` call. If the service crashes mid-wait, Restate replays the
> entire wait from scratch (the waiter result was never journaled). The waiter is
> idempotent and the 5-minute budget is generous, so replay overhead is negligible.
> An alternative design would replace the SDK waiter with a polling loop using
> `restate.Sleep()` between describe calls, journaling each describe result
> individually so replays skip already-completed polls.

```go
func (r *realEC2API) WaitUntilRunning(ctx context.Context, instanceId string) error {
    waiter := ec2sdk.NewInstanceRunningWaiter(r.client)
    return waiter.Wait(ctx, &ec2sdk.DescribeInstancesInput{
        InstanceIds: []string{instanceId},
    }, 5*time.Minute)
}
```

#### `ModifyInstanceType`

> **Crash-recovery note**: if the driver crashes after stopping the instance but
> before starting it, the next Restate replay will re-enter this function. The
> `StopInstances` call on an already-stopped instance returns an
> `IncorrectInstanceState` error. The real implementation should check the current
> instance state before calling `StopInstances` and skip the stop step if the
> instance is already stopped. This is left as a note here — the snippet below
> shows the happy-path sequence; the implementation must add the state guard.

```go
func (r *realEC2API) ModifyInstanceType(ctx context.Context, instanceId, newType string) error {
    // 1. Stop the instance
    _, err := r.client.StopInstances(ctx, &ec2sdk.StopInstancesInput{
        InstanceIds: []string{instanceId},
    })
    if err != nil {
        return fmt.Errorf("stop instance for type change: %w", err)
    }

    // 2. Wait until stopped
    stoppedWaiter := ec2sdk.NewInstanceStoppedWaiter(r.client)
    if err := stoppedWaiter.Wait(ctx, &ec2sdk.DescribeInstancesInput{
        InstanceIds: []string{instanceId},
    }, 5*time.Minute); err != nil {
        return fmt.Errorf("wait for instance stop: %w", err)
    }

    // 3. Modify instance type
    _, err = r.client.ModifyInstanceAttribute(ctx, &ec2sdk.ModifyInstanceAttributeInput{
        InstanceId: aws.String(instanceId),
        InstanceType: &ec2types.AttributeValue{
            Value: aws.String(newType),
        },
    })
    if err != nil {
        return fmt.Errorf("modify instance type: %w", err)
    }

    // 4. Start the instance
    _, err = r.client.StartInstances(ctx, &ec2sdk.StartInstancesInput{
        InstanceIds: []string{instanceId},
    })
    if err != nil {
        return fmt.Errorf("start instance after type change: %w", err)
    }

    return nil
}
```

#### `ModifySecurityGroups`

```go
func (r *realEC2API) ModifySecurityGroups(ctx context.Context, instanceId string, sgIds []string) error {
    _, err := r.client.ModifyInstanceAttribute(ctx, &ec2sdk.ModifyInstanceAttributeInput{
        InstanceId: aws.String(instanceId),
        Groups:     sgIds,
    })
    return err
}
```

#### `UpdateMonitoring`

```go
func (r *realEC2API) UpdateMonitoring(ctx context.Context, instanceId string, enabled bool) error {
    if enabled {
        _, err := r.client.MonitorInstances(ctx, &ec2sdk.MonitorInstancesInput{
            InstanceIds: []string{instanceId},
        })
        return err
    }
    _, err := r.client.UnmonitorInstances(ctx, &ec2sdk.UnmonitorInstancesInput{
        InstanceIds: []string{instanceId},
    })
    return err
}
```

#### `UpdateTags`

Follow the same pattern as the SG driver: delete all existing tags, then create new ones.
Tags are applied to the **instance only** — root volume tagging is not supported (see Design Decisions §3).

```go
func (r *realEC2API) UpdateTags(ctx context.Context, instanceId string, tags map[string]string) error {
    // Get current tags to delete old ones — but preserve praxis:* system tags.
    // Design Decision §10: UpdateTags must never remove the praxis:managed-key
    // ownership tag (or any future praxis: namespace tags).
    out, err := r.client.DescribeInstances(ctx, &ec2sdk.DescribeInstancesInput{
        InstanceIds: []string{instanceId},
    })
    if err != nil {
        return err
    }
    if len(out.Reservations) > 0 && len(out.Reservations[0].Instances) > 0 {
        inst := out.Reservations[0].Instances[0]
        if len(inst.Tags) > 0 {
            var oldTags []ec2types.Tag
            for _, t := range inst.Tags {
                key := aws.ToString(t.Key)
                // Skip praxis:* system tags — these are driver-managed and must
                // survive tag updates. See Design Decision §10.
                if strings.HasPrefix(key, "praxis:") {
                    continue
                }
                oldTags = append(oldTags, ec2types.Tag{Key: t.Key})
            }
            if len(oldTags) > 0 {
                _, _ = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
                    Resources: []string{instanceId},
                    Tags:      oldTags,
                })
            }
        }
    }

    // Set new tags — skip any praxis:* keys the caller may have passed.
    // The praxis:managed-key tag is written once at RunInstance time and never
    // overwritten. If the caller's tags map contains praxis:* entries, they are
    // silently dropped to prevent accidental ownership tag corruption.
    if len(tags) > 0 {
        var ec2Tags []ec2types.Tag
        for k, v := range tags {
            if strings.HasPrefix(k, "praxis:") {
                continue
            }
            ec2Tags = append(ec2Tags, ec2types.Tag{
                Key: aws.String(k), Value: aws.String(v),
            })
        }
        if len(ec2Tags) > 0 {
            _, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{
                Resources: []string{instanceId},
                Tags:      ec2Tags,
            })
            return err
        }
    }
    return nil
}
```

### Error Classification Helpers

Add these at the bottom of `aws.go`. Follow the exact pattern from the S3/SG drivers.

**Error code mapping:**

| Classification | HTTP Status | Meaning |
|---|---|---|
| `IsNotFound` | 404 | Instance does not exist |
| `IsInvalidParam` | 400 | User input error (bad AMI, bad subnet, etc.) |
| `IsInsufficientCapacity` | **503** | AWS supply constraint — not a user error |
| `IsTerminated` | (used internally) | Instance in terminated state — currently unused in handler code (Reconcile checks `observed.State` directly); retained for driver extensions that operate on error objects rather than state structs |

```go
// IsNotFound returns true if the AWS error indicates the instance does not exist.
func IsNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "InvalidInstanceID.NotFound" ||
               code == "InvalidInstanceID.Malformed"
    }
    errText := err.Error()
    return strings.Contains(errText, "InvalidInstanceID.NotFound")
}

// IsTerminated returns true if the instance is in a terminated state.
func IsTerminated(err error) bool {
    if err == nil {
        return false
    }
    errText := err.Error()
    return strings.Contains(errText, "terminated") ||
           strings.Contains(errText, "InvalidInstanceID.NotFound")
}

// IsInvalidParam returns true if the error indicates an invalid parameter.
// These are surfaced as terminal errors with status 400 (bad request).
func IsInvalidParam(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "InvalidParameterValue" ||
               code == "InvalidAMIID.Malformed" ||
               code == "InvalidAMIID.NotFound" ||
               code == "InvalidSubnetID.NotFound" ||
               code == "InvalidGroup.NotFound"
    }
    return false
}

// IsInsufficientCapacity returns true when AWS can't fulfill the instance request.
// These are terminal errors but NOT user input errors — surfaced as status 503
// (service unavailable) to distinguish from bad input (400).
func IsInsufficientCapacity(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "InsufficientInstanceCapacity" ||
               code == "InstanceLimitExceeded" ||
               code == "Unsupported"
    }
    return false
}
```

#### `FindByManagedKey`

```go
func (r *realEC2API) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    out, err := r.client.DescribeInstances(ctx, &ec2sdk.DescribeInstancesInput{
        Filters: []ec2types.Filter{
            {Name: aws.String("tag:praxis:managed-key"), Values: []string{managedKey}},
            {Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
        },
    })
    if err != nil {
        return "", err
    }

    // Collect all matching instance IDs.
    var matches []string
    for _, r := range out.Reservations {
        for _, inst := range r.Instances {
            if id := aws.ToString(inst.InstanceId); id != "" {
                matches = append(matches, id)
            }
        }
    }

    switch len(matches) {
    case 0:
        return "", nil // no conflict — safe to create
    case 1:
        return matches[0], nil // exactly one match — conflict or recovery target
    default:
        // Multiple live instances claim the same managed-key tag.
        // This should never happen under normal operation and indicates
        // either a bug or manual tag tampering. Surface as a terminal error
        // so the operator must investigate and resolve the corruption.
        return "", fmt.Errorf(
            "ownership corruption: %d live instances claim managed-key %q: %v; "+
                "manual intervention required",
            len(matches), managedKey, matches,
        )
    }
}
```

### Helper: base64 Encoding

```go
// base64Encode unconditionally base64-encodes the input string.
//
// Why not "encode if needed"? The Go base64 decoder is lenient: short
// alphanumeric strings like "hello" decode without error because they happen
// to be valid (if non-canonical) base64. A detect-then-skip approach would
// treat such strings as already-encoded and pass them through unchanged,
// producing corrupt user data on the instance.
//
// Callers MUST always pass plain-text user data. If the user data is already
// base64-encoded (e.g. from a CI pipeline), the caller should document that
// the value will be double-encoded and the instance's cloud-init must account
// for it, or the template schema should use a separate field/flag.
func base64Encode(s string) string {
    return base64.StdEncoding.EncodeToString([]byte(s))
}
```

### Helper: Extract IAM Profile Name from ARN

```go
// extractProfileName extracts the instance profile name from its ARN.
// ARN format: arn:aws:iam::123456789012:instance-profile/MyProfile
func extractProfileName(arn string) string {
    parts := strings.Split(arn, "/")
    if len(parts) > 1 {
        return parts[len(parts)-1]
    }
    return arn
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/ec2/drift.go`

### HasDrift Function

```go
package ec2

import (
    "sort"
    "strings"
)

// HasDrift returns true if the desired spec and observed state differ.
//
// EC2-specific drift rules:
// - imageId is NOT checked — you can't change an AMI on a running instance.
//   Detecting AMI drift would be misleading since correction requires termination.
// - rootVolume is NOT checked for type/size changes — requires stop+modify+start
//   for type, and creating a new volume for size. Only encryption is checked.
// - userData is NOT checked — AWS doesn't return user data in DescribeInstances.
// - subnetId is NOT checked — can't move an instance to a different subnet.
//
// Fields that ARE checked (and can be corrected):
// - instanceType (requires stop → modify → start)
// - securityGroupIds (can be changed while running)
// - monitoring (can be toggled while running)
// - tags (can be changed while running)
func HasDrift(desired EC2InstanceSpec, observed ObservedState) bool {
    // Skip drift check if instance is not running or stopped
    if observed.State != "running" && observed.State != "stopped" {
        return false
    }

    if desired.InstanceType != observed.InstanceType {
        return true
    }

    if !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) {
        return true
    }

    if desired.Monitoring != observed.Monitoring {
        return true
    }

    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }

    return false
}
```

### ComputeFieldDiffs Function

```go
// ComputeFieldDiffs returns a list of field-level differences for plan output.
// Includes both mutable fields (which the driver will correct) and immutable fields
// (which are reported with a "(immutable, ignored)" suffix so the user sees the
// discrepancy without the driver attempting to fix it).
func ComputeFieldDiffs(desired EC2InstanceSpec, observed ObservedState) []FieldDiffEntry {
    var diffs []FieldDiffEntry

    // --- Mutable fields (driver will correct these) ---

    if desired.InstanceType != observed.InstanceType {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.instanceType",
            OldValue: observed.InstanceType,
            NewValue: desired.InstanceType,
        })
    }

    if !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.securityGroupIds",
            OldValue: observed.SecurityGroupIds,
            NewValue: desired.SecurityGroupIds,
        })
    }

    if desired.Monitoring != observed.Monitoring {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.monitoring",
            OldValue: observed.Monitoring,
            NewValue: desired.Monitoring,
        })
    }

    // Tags: added, changed, removed.
    // Filter out praxis:* system tags from both sides to avoid false diffs
    // (observed always has praxis:managed-key, desired never does).
    desiredFiltered := filterPraxisTags(desired.Tags)
    observedFiltered := filterPraxisTags(observed.Tags)
    for k, v := range desiredFiltered {
        if ov, ok := observedFiltered[k]; !ok {
            diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: nil, NewValue: v})
        } else if ov != v {
            diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: ov, NewValue: v})
        }
    }
    for k, v := range observedFiltered {
        if _, ok := desiredFiltered[k]; !ok {
            diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: v, NewValue: nil})
        }
    }

    // --- Immutable fields (reported for visibility, not corrected) ---
    // These appear in plan output so the user knows their change won't take effect.

    if desired.ImageId != observed.ImageId && observed.ImageId != "" {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.imageId (immutable, ignored)",
            OldValue: observed.ImageId,
            NewValue: desired.ImageId,
        })
    }

    if desired.SubnetId != observed.SubnetId && observed.SubnetId != "" {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.subnetId (immutable, ignored)",
            OldValue: observed.SubnetId,
            NewValue: desired.SubnetId,
        })
    }

    if desired.KeyName != observed.KeyName && observed.KeyName != "" {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.keyName (immutable, ignored)",
            OldValue: observed.KeyName,
            NewValue: desired.KeyName,
        })
    }

    return diffs
}

// FieldDiffEntry is the driver-specific diff unit.
type FieldDiffEntry struct {
    Path     string
    OldValue any
    NewValue any
}
```

### Helper Functions

```go
// securityGroupsMatch compares two sorted slices of security group IDs.
func securityGroupsMatch(desired, observed []string) bool {
    a := sortedCopy(desired)
    b := sortedCopy(observed)
    if len(a) != len(b) {
        return false
    }
    for i := range a {
        if a[i] != b[i] {
            return false
        }
    }
    return true
}

func sortedCopy(s []string) []string {
    c := make([]string, len(s))
    copy(c, s)
    sort.Strings(c)
    return c
}

// tagsMatch returns true when two tag maps are semantically equal,
// ignoring praxis:* system tags in both maps. The observed tags always
// contain praxis:managed-key (written at launch), but desired tags never
// do — comparing them directly would cause perpetual drift detection.
func tagsMatch(a, b map[string]string) bool {
    fa := filterPraxisTags(a)
    fb := filterPraxisTags(b)
    if len(fa) != len(fb) {
        return false
    }
    for k, v := range fa {
        if bv, ok := fb[k]; !ok || bv != v {
            return false
        }
    }
    return true
}

// filterPraxisTags returns a copy of the map with all praxis:* keys removed.
func filterPraxisTags(m map[string]string) map[string]string {
    out := make(map[string]string, len(m))
    for k, v := range m {
        if !strings.HasPrefix(k, "praxis:") {
            out[k] = v
        }
    }
    return out
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/ec2/driver.go`

This is the heart of the driver. Follow the S3 and SG patterns exactly.

> **⚠️ Critical Restate footgun — `restate.Run()` panics on non-terminal errors.**
> When the callback passed to `restate.Run()` returns a non-terminal (retryable)
> error, Restate aborts the invocation via panic — the error is never returned to
> the caller. This means error classification (terminal vs. retryable) **must happen
> inside the callback**, not after it. Every AWS API call wrapped in `restate.Run()`
> must check for terminal conditions (invalid input, not-found, insufficient
> capacity) inside the callback and return `restate.TerminalError(err, code)` for
> those. Only truly transient errors (network timeouts, throttling) should be
> returned as plain errors, which Restate will retry automatically.
>
> The S3 and SG drivers follow this pattern. All code snippets in this plan do as
> well — but the implementer must be vigilant about any new `restate.Run()` calls
> added during development.

### Struct & Constructor

```go
package ec2

import (
    "fmt"
    "time"

    "github.com/aws/aws-sdk-go-v2/aws"
    restate "github.com/restatedev/sdk-go"

    "github.com/praxiscloud/praxis/internal/core/auth"
    "github.com/praxiscloud/praxis/internal/drivers"
    "github.com/praxiscloud/praxis/internal/infra/awsclient"
    "github.com/praxiscloud/praxis/pkg/types"
)

type EC2InstanceDriver struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) EC2API
}

func NewEC2InstanceDriver(accounts *auth.Registry) *EC2InstanceDriver {
    return NewEC2InstanceDriverWithFactory(accounts, func(cfg aws.Config) EC2API {
        return NewEC2API(awsclient.NewEC2Client(cfg))
    })
}

func NewEC2InstanceDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) EC2API) *EC2InstanceDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) EC2API {
            return NewEC2API(awsclient.NewEC2Client(cfg))
        }
    }
    return &EC2InstanceDriver{auth: accounts, apiFactory: factory}
}

func (d *EC2InstanceDriver) ServiceName() string {
    return ServiceName
}
```

### Provision Handler

```go
func (d *EC2InstanceDriver) Provision(ctx restate.ObjectContext, spec EC2InstanceSpec) (EC2InstanceOutputs, error) {
    ctx.Log().Info("provisioning EC2 instance", "name", spec.Tags["Name"], "key", restate.Key(ctx))
    api, region, err := d.apiForAccount(spec.Account)
    if err != nil {
        return EC2InstanceOutputs{}, restate.TerminalError(err, 400)
    }

    // --- Input validation ---
    if spec.ImageId == "" {
        return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("imageId is required"), 400)
    }
    if spec.InstanceType == "" {
        return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("instanceType is required"), 400)
    }
    if spec.SubnetId == "" {
        return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("subnetId is required"), 400)
    }
    if spec.Region == "" {
        return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
    }

    // --- Load current state ---
    state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
    if err != nil {
        return EC2InstanceOutputs{}, err
    }

    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Mode = types.ModeManaged
    state.Error = ""
    state.Generation++

    // --- Check if instance already exists (re-provision path) ---
    instanceId := state.Outputs.InstanceId
    if instanceId != "" {
        // Verify it still exists and is not terminated
        descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
            obs, err := api.DescribeInstance(rc, instanceId)
            if err != nil {
                if IsNotFound(err) {
                    return ObservedState{}, restate.TerminalError(err, 404)
                }
                return ObservedState{}, err
            }
            return obs, nil
        })
        if descErr != nil || descResult.State == "terminated" || descResult.State == "shutting-down" {
            instanceId = "" // Instance gone or terminating, recreate
        }
    }

    // --- Pre-flight ownership conflict check (first provision only) ---
    // Before creating a new instance, verify no live instance already claims
    // this managed-key tag. This surfaces accidental duplicate templates as a
    // terminal conflict rather than silently launching a second machine.
    if instanceId == "" && spec.ManagedKey != "" {
        conflictId, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.FindByManagedKey(rc, spec.ManagedKey)
        })
        if conflictErr != nil {
            // Non-terminal: describe failure is transient, let Restate retry.
            return EC2InstanceOutputs{}, conflictErr
        }
        if conflictId != "" {
            // Another instance already owns this key — operator must resolve manually.
            return EC2InstanceOutputs{}, restate.TerminalError(
                fmt.Errorf("instance name %q in this region is already managed by Praxis (instanceId: %s); "+
                    "remove the existing resource or use a different metadata.name", spec.ManagedKey, conflictId),
                409,
            )
        }
    }

    // --- Create instance if it doesn't exist ---
    if instanceId == "" {
        newInstanceId, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            id, err := api.RunInstance(rc, spec)
            if err != nil {
                if IsInvalidParam(err) {
                    return "", restate.TerminalError(err, 400) // bad input
                }
                if IsInsufficientCapacity(err) {
                    return "", restate.TerminalError(err, 503) // AWS supply constraint
                }
                return "", err // transient → Restate retries
            }
            return id, nil
        })
        if err != nil {
            state.Status = types.StatusError
            state.Error = err.Error()
            restate.Set(ctx, drivers.StateKey, state)
            return EC2InstanceOutputs{}, err
        }
        instanceId = newInstanceId

        // Wait for the instance to reach "running" state
        _, waitErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            if err := api.WaitUntilRunning(rc, instanceId); err != nil {
                return restate.Void{}, err // transient — will retry
            }
            return restate.Void{}, nil
        })
        if waitErr != nil {
            // Instance created but not yet running — set error but don't fail terminally.
            // Reconcile will pick up the state later.
            state.Status = types.StatusError
            state.Error = fmt.Sprintf("instance %s created but failed to reach running state: %v", instanceId, waitErr)
            // Still save the instance ID so we don't orphan it
            state.Outputs = EC2InstanceOutputs{InstanceId: instanceId}
            restate.Set(ctx, drivers.StateKey, state)
            return EC2InstanceOutputs{}, waitErr
        }
    } else {
        // --- Re-provision path: converge mutable attributes ---
        // Instance type change requires stop → modify → start
        if spec.InstanceType != state.Observed.InstanceType && state.Observed.InstanceType != "" {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.ModifyInstanceType(rc, instanceId, spec.InstanceType)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = fmt.Sprintf("failed to change instance type: %v", err)
                restate.Set(ctx, drivers.StateKey, state)
                return EC2InstanceOutputs{}, restate.TerminalError(err, 500)
            }
        }

        // Security groups can be changed while running
        if !securityGroupsMatch(spec.SecurityGroupIds, state.Observed.SecurityGroupIds) && len(spec.SecurityGroupIds) > 0 {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.ModifySecurityGroups(rc, instanceId, spec.SecurityGroupIds)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return EC2InstanceOutputs{}, restate.TerminalError(err, 500)
            }
        }

        // Monitoring can be toggled while running
        if spec.Monitoring != state.Observed.Monitoring {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateMonitoring(rc, instanceId, spec.Monitoring)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return EC2InstanceOutputs{}, restate.TerminalError(err, 500)
            }
        }

        // Tags can be changed while running
        if !tagsMatch(spec.Tags, state.Observed.Tags) {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateTags(rc, instanceId, spec.Tags)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return EC2InstanceOutputs{}, restate.TerminalError(err, 500)
            }
        }
    }

    // --- Describe final state to populate outputs ---
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeInstance(rc, instanceId)
    })
    if err != nil {
        state.Status = types.StatusError
        state.Error = err.Error()
        state.Outputs = EC2InstanceOutputs{InstanceId: instanceId}
        restate.Set(ctx, drivers.StateKey, state)
        return EC2InstanceOutputs{}, err
    }

    // --- Build outputs ---
    outputs := EC2InstanceOutputs{
        InstanceId:       instanceId,
        PrivateIpAddress: observed.PrivateIpAddress,
        PublicIpAddress:  observed.PublicIpAddress,
        PrivateDnsName:   observed.PrivateDnsName,
        // ARN is not populated: constructing a correct ARN requires the AWS
        // account ID (via STS GetCallerIdentity), which the auth.Registry does
        // not currently resolve. The adapter's NormalizeOutputs omits it when
        // empty. See FUTURE.md for account-ID resolution.
        ARN:              "",
        State:            observed.State,
        SubnetId:         observed.SubnetId,
        VpcId:            observed.VpcId,
    }

    // --- Commit state atomically ---
    state.Observed = observed
    state.Outputs = outputs
    state.Status = types.StatusReady
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    d.scheduleReconcile(ctx, &state)
    return outputs, nil
}
```

### Import Handler

```go
func (d *EC2InstanceDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (EC2InstanceOutputs, error) {
    ctx.Log().Info("importing EC2 instance", "resourceId", ref.ResourceID, "mode", ref.Mode)
    api, region, err := d.apiForAccount(ref.Account)
    if err != nil {
        return EC2InstanceOutputs{}, restate.TerminalError(err, 400)
    }

    // EC2 import defaults to ModeObserved, unlike S3/SG which default to ModeManaged.
    // Rationale: EC2 delete is destructive (instance termination). Two VOs that target
    // the same instance — the import VO (region~instanceId) and a potential template VO
    // (region~metadata.name) — can both issue Delete, terminating the same machine.
    // Defaulting to Observed prevents the import VO from participating in destructive
    // lifecycle actions unless the operator explicitly opts into managed mode.
    // The user must pass --mode managed to grant full lifecycle control to the import VO.
    mode := defaultEC2ImportMode(ref.Mode)

    state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
    if err != nil {
        return EC2InstanceOutputs{}, err
    }
    state.Generation++

    // Describe the existing instance
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeInstance(rc, ref.ResourceID)
    })
    if err != nil {
        if IsNotFound(err) {
            return EC2InstanceOutputs{}, restate.TerminalError(
                fmt.Errorf("import failed: instance %s does not exist", ref.ResourceID), 404,
            )
        }
        return EC2InstanceOutputs{}, err
    }

    if observed.State == "terminated" || observed.State == "shutting-down" {
        return EC2InstanceOutputs{}, restate.TerminalError(
            fmt.Errorf("import failed: instance %s is %s", ref.ResourceID, observed.State), 400,
        )
    }

    // Synthesize spec from observed
    spec := specFromObserved(observed)
    spec.Account = ref.Account
    spec.Region = region // Set region from AWS config since it's not in DescribeInstances response

    outputs := EC2InstanceOutputs{
        InstanceId:       observed.InstanceId,
        PrivateIpAddress: observed.PrivateIpAddress,
        PublicIpAddress:  observed.PublicIpAddress,
        PrivateDnsName:   observed.PrivateDnsName,
        // ARN not populated — see Provision handler comment.
        ARN:              "",
        State:            observed.State,
        SubnetId:         observed.SubnetId,
        VpcId:            observed.VpcId,
    }

    state.Desired = spec
    state.Observed = observed
    state.Outputs = outputs
    state.Status = types.StatusReady
    state.Mode = mode
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    d.scheduleReconcile(ctx, &state)
    return outputs, nil
}

// specFromObserved creates an EC2InstanceSpec from observed state
// so the first reconciliation after import sees no drift.
//
// RootVolume is intentionally omitted: drift detection skips root volume
// attributes (type, size, encryption require stop+modify or volume replacement),
// so synthesizing a RootVolume spec would create a field that is never compared
// and never corrected. Omitting it keeps the synthesized spec honest — it only
// contains fields the driver actively manages.
func specFromObserved(obs ObservedState) EC2InstanceSpec {
    return EC2InstanceSpec{
        ImageId:            obs.ImageId,
        InstanceType:       obs.InstanceType,
        KeyName:            obs.KeyName,
        SubnetId:           obs.SubnetId,
        SecurityGroupIds:   obs.SecurityGroupIds,
        IamInstanceProfile: obs.IamInstanceProfile,
        Monitoring:         obs.Monitoring,
        Tags:               obs.Tags,
    }
}

// defaultEC2ImportMode returns ModeObserved when no explicit mode is requested.
// EC2 import defaults to Observed (not Managed) because delete is destructive:
// an imported instance under ManagedMode can be terminated by the driver.
// Operators must pass --mode managed to opt into full lifecycle control.
func defaultEC2ImportMode(m types.Mode) types.Mode {
    if m == "" {
        return types.ModeObserved
    }
    return m
}
```

### Delete Handler

The Delete handler **blocks deletion for Observed-mode resources**. This is the
critical safety mechanism that makes the import-default-to-Observed rationale work:
an imported instance in Observed mode cannot be terminated by the driver. The
operator must explicitly import with `--mode managed` to allow deletion.

This is a deliberate departure from the S3 and SG drivers, which do not check
`state.Mode` in their Delete handlers. For S3 and SG, deletion is less destructive
(buckets can be protected by versioning; removing a security group doesn't destroy
instances). EC2 termination is permanent and irreversible, so the guard is warranted.

```go
func (d *EC2InstanceDriver) Delete(ctx restate.ObjectContext) error {
    ctx.Log().Info("deleting EC2 instance", "key", restate.Key(ctx))

    state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }

    // --- Observed-mode guard ---
    // Observed-mode resources are read-only: the driver reports drift but does
    // not modify or delete the underlying AWS resource. Attempting to delete an
    // Observed-mode resource is a terminal error (not retried) because the
    // caller's intent conflicts with the resource's declared mode.
    if state.Mode == types.ModeObserved {
        return restate.TerminalError(
            fmt.Errorf("cannot delete instance %s: resource is in Observed mode; "+
                "re-import with --mode managed to allow deletion", state.Outputs.InstanceId),
            409,
        )
    }

    api, _, err := d.apiForAccount(state.Desired.Account)
    if err != nil {
        return restate.TerminalError(err, 400)
    }

    instanceId := state.Outputs.InstanceId
    if instanceId == "" {
        // No instance was ever provisioned — set tombstone
        restate.Set(ctx, drivers.StateKey, EC2InstanceState{Status: types.StatusDeleted})
        return nil
    }

    state.Status = types.StatusDeleting
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    _, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        if err := api.TerminateInstance(rc, instanceId); err != nil {
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

    restate.Set(ctx, drivers.StateKey, EC2InstanceState{
        Status: types.StatusDeleted,
    })
    return nil
}
```

### Reconcile Handler

```go
func (d *EC2InstanceDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    api, _, err := d.apiForAccount(state.Desired.Account)
    if err != nil {
        return types.ReconcileResult{}, restate.TerminalError(err, 400)
    }

    state.ReconcileScheduled = false

    if state.Status != types.StatusReady && state.Status != types.StatusError {
        restate.Set(ctx, drivers.StateKey, state)
        return types.ReconcileResult{}, nil
    }

    instanceId := state.Outputs.InstanceId
    if instanceId == "" {
        restate.Set(ctx, drivers.StateKey, state)
        return types.ReconcileResult{}, nil
    }

    // Capture a replay-stable timestamp for this reconcile cycle.
    // IMPORTANT: time.Now() is non-deterministic and must NEVER be called directly
    // in a Restate handler — during replay it returns a different value, corrupting
    // the journal. Wrapping it in restate.Run() journals the result so replays
    // produce the same timestamp.
    now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
        return time.Now().UTC().Format(time.RFC3339), nil
    })
    if err != nil {
        return types.ReconcileResult{}, err
    }

    // Describe current state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        obs, err := api.DescribeInstance(rc, instanceId)
        if err != nil {
            if IsNotFound(err) {
                return ObservedState{}, restate.TerminalError(err, 404)
            }
            return ObservedState{}, err
        }
        return obs, nil
    })
    if err != nil {
        if IsNotFound(err) {
            state.Status = types.StatusError
            state.Error = fmt.Sprintf("instance %s was terminated externally", instanceId)
            state.LastReconcile = now
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx, &state)
            return types.ReconcileResult{Error: state.Error}, nil
        }
        state.LastReconcile = now
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Error: err.Error()}, nil
    }

    // Check if instance is terminated
    if observed.State == "terminated" || observed.State == "shutting-down" {
        state.Status = types.StatusError
        state.Error = fmt.Sprintf("instance %s is %s", instanceId, observed.State)
        state.Observed = observed
        state.LastReconcile = now
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Error: state.Error}, nil
    }

    state.Observed = observed
    state.LastReconcile = now

    drift := HasDrift(state.Desired, observed)

    // Error status: read-only, no correction
    if state.Status == types.StatusError {
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Drift: drift, Correcting: false}, nil
    }

    // Ready + Managed + drift: correct
    if drift && state.Mode == types.ModeManaged {
        ctx.Log().Info("drift detected, correcting", "instanceId", instanceId)
        correctionErr := d.correctDrift(ctx, api, instanceId, state.Desired, observed)
        if correctionErr != nil {
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx, &state)
            return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
        }
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Drift: true, Correcting: true}, nil
    }

    // Ready + Observed + drift: report only
    if drift && state.Mode == types.ModeObserved {
        ctx.Log().Info("drift detected (observed mode, not correcting)", "instanceId", instanceId)
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Drift: true, Correcting: false}, nil
    }

    // No drift
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx, &state)
    return types.ReconcileResult{}, nil
}

// correctDrift applies corrections for mutable attributes.
func (d *EC2InstanceDriver) correctDrift(ctx restate.ObjectContext, api EC2API, instanceId string, desired EC2InstanceSpec, observed ObservedState) error {
    // Instance type change — requires stop → modify → start
    if desired.InstanceType != observed.InstanceType {
        _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.ModifyInstanceType(rc, instanceId, desired.InstanceType)
        })
        if err != nil {
            return fmt.Errorf("modify instance type: %w", err)
        }
    }

    // Security groups — can change while running
    if !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) && len(desired.SecurityGroupIds) > 0 {
        _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.ModifySecurityGroups(rc, instanceId, desired.SecurityGroupIds)
        })
        if err != nil {
            return fmt.Errorf("modify security groups: %w", err)
        }
    }

    // Monitoring
    if desired.Monitoring != observed.Monitoring {
        _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.UpdateMonitoring(rc, instanceId, desired.Monitoring)
        })
        if err != nil {
            return fmt.Errorf("update monitoring: %w", err)
        }
    }

    // Tags
    if !tagsMatch(desired.Tags, observed.Tags) {
        _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.UpdateTags(rc, instanceId, desired.Tags)
        })
        if err != nil {
            return fmt.Errorf("update tags: %w", err)
        }
    }

    return nil
}
```

### Shared Handlers

```go
func (d *EC2InstanceDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
    state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
    if err != nil {
        return types.StatusResponse{}, err
    }
    return types.StatusResponse{
        Status:     state.Status,
        Mode:       state.Mode,
        Generation: state.Generation,
        Error:      state.Error,
    }, nil
}

func (d *EC2InstanceDriver) GetOutputs(ctx restate.ObjectSharedContext) (EC2InstanceOutputs, error) {
    state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
    if err != nil {
        return EC2InstanceOutputs{}, err
    }
    return state.Outputs, nil
}
```

### Private Helpers

```go
func (d *EC2InstanceDriver) scheduleReconcile(ctx restate.ObjectContext, state *EC2InstanceState) {
    if state.ReconcileScheduled {
        return
    }
    state.ReconcileScheduled = true
    restate.Set(ctx, drivers.StateKey, *state)
    restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
        Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

// apiForAccount follows the SG driver's (EC2API, string, error) signature (not S3's
// (S3API, error)) because EC2 needs the resolved region for the Import handler's
// specFromObserved and for ARN construction when account-ID resolution is available.
func (d *EC2InstanceDriver) apiForAccount(account string) (EC2API, string, error) {
    if d == nil || d.auth == nil || d.apiFactory == nil {
        return nil, "", fmt.Errorf("EC2InstanceDriver is not configured with an auth registry")
    }
    awsCfg, err := d.auth.Resolve(account)
    if err != nil {
        return nil, "", fmt.Errorf("resolve EC2 account %q: %w", account, err)
    }
    return d.apiFactory(awsCfg), awsCfg.Region, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/ec2_adapter.go`

This is the type bridge between generic JSON resource documents and the typed
EC2 driver. Follows the exact pattern of `s3_adapter.go` and `sg_adapter.go`.

### Key Scope

EC2 instances use `KeyScopeRegion`. The key format is `region~metadata.name`.
Both `BuildKey` (template path) and `BuildImportKey` (import path) produce keys
in this format. See [Key Strategy §2](#2-key-strategy) for the full rationale.

- `BuildKey`: `region~metadata.name` — template declares both values.
- `BuildImportKey`: `region~resourceID` — import uses instance ID as the name
  component, producing a separate Virtual Object from any template-managed one.

### Plan: VO-to-VO call pattern (departure from S3/SG)

The EC2 adapter’s `Plan()` method diverges from the S3 and SG adapters in how it
discovers existing state. S3 and SG call their respective AWS APIs inside
`restate.Run()` to describe the resource by its stable AWS identifier (bucket name,
group name+VPC). EC2 has no such stable identifier, so `Plan()` reads the Virtual
Object’s stored outputs via a VO-to-VO call (`GetOutputs`) on the Restate context.

> **Critical implementation rule**: VO-to-VO calls (like `GetOutputs`) **must be
> invoked directly on the Restate context**, NOT inside a `restate.Run()` callback.
> `restate.Run()` is for non-Restate side effects (AWS API calls, HTTP requests).
> Wrapping a VO-to-VO call in `restate.Run()` leaks the outer context into a
> `RunContext`, and Restate won’t journal the inner invocation correctly. The code
> in the `Plan()` method below shows the correct pattern.

### Complete Adapter

```go
package provider

import (
    "encoding/json"
    "fmt"
    "strings"

    "github.com/aws/aws-sdk-go-v2/aws"
    restate "github.com/restatedev/sdk-go"

    "github.com/praxiscloud/praxis/internal/core/auth"
    "github.com/praxiscloud/praxis/internal/drivers/ec2"
    "github.com/praxiscloud/praxis/internal/infra/awsclient"
    "github.com/praxiscloud/praxis/pkg/types"
)

// EC2Adapter adapts generic resource documents to the strongly typed EC2 instance driver.
type EC2Adapter struct {
    auth              *auth.Registry
    staticPlanningAPI ec2.EC2API
    apiFactory        func(aws.Config) ec2.EC2API
}

func NewEC2Adapter() *EC2Adapter {
    return NewEC2AdapterWithRegistry(auth.LoadFromEnv())
}

func NewEC2AdapterWithRegistry(accounts *auth.Registry) *EC2Adapter {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    return &EC2Adapter{
        auth: accounts,
        apiFactory: func(cfg aws.Config) ec2.EC2API {
            return ec2.NewEC2API(awsclient.NewEC2Client(cfg))
        },
    }
}

func NewEC2AdapterWithAPI(api ec2.EC2API) *EC2Adapter {
    return &EC2Adapter{staticPlanningAPI: api}
}

func (a *EC2Adapter) Kind() string {
    return ec2.ServiceName
}

func (a *EC2Adapter) ServiceName() string {
    return ec2.ServiceName
}

func (a *EC2Adapter) Scope() KeyScope {
    return KeyScopeRegion
}

func (a *EC2Adapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    doc, err := decodeResourceDocument(resourceDoc)
    if err != nil {
        return "", err
    }
    spec, err := a.decodeSpec(doc)
    if err != nil {
        return "", err
    }
    if err := ValidateKeyPart("region", spec.Region); err != nil {
        return "", err
    }
    name := strings.TrimSpace(doc.Metadata.Name)
    if err := ValidateKeyPart("instance name", name); err != nil {
        return "", err
    }
    return JoinKey(spec.Region, name), nil
}

func (a *EC2Adapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
    doc, err := decodeResourceDocument(resourceDoc)
    if err != nil {
        return nil, err
    }
    return a.decodeSpec(doc)
}

func (a *EC2Adapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
    typedSpec, err := castSpec[ec2.EC2InstanceSpec](spec)
    if err != nil {
        return nil, err
    }
    typedSpec.Account = account
    typedSpec.ManagedKey = key // used by driver for praxis:managed-key tag + conflict check

    fut := restate.WithRequestType[ec2.EC2InstanceSpec, ec2.EC2InstanceOutputs](
        restate.Object[ec2.EC2InstanceOutputs](ctx, a.ServiceName(), key, "Provision"),
    ).RequestFuture(typedSpec)

    return &provisionHandle[ec2.EC2InstanceOutputs]{
        id:        fut.GetInvocationId(),
        raw:       fut,
        normalize: a.NormalizeOutputs,
    }, nil
}

func (a *EC2Adapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
    fut := restate.WithRequestType[restate.Void, restate.Void](
        restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
    ).RequestFuture(restate.Void{})

    return &deleteHandle{
        id:  fut.GetInvocationId(),
        raw: fut,
    }, nil
}

func (a *EC2Adapter) NormalizeOutputs(raw any) (map[string]any, error) {
    out, err := castOutput[ec2.EC2InstanceOutputs](raw)
    if err != nil {
        return nil, err
    }
    result := map[string]any{
        "instanceId":       out.InstanceId,
        "privateIpAddress": out.PrivateIpAddress,
        "privateDnsName":   out.PrivateDnsName,
        "state":            out.State,
        "subnetId":         out.SubnetId,
        "vpcId":            out.VpcId,
    }
    if out.ARN != "" {
        result["arn"] = out.ARN
    }
    if out.PublicIpAddress != "" {
        result["publicIpAddress"] = out.PublicIpAddress
    }
    return result, nil
}

func (a *EC2Adapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
    desired, err := castSpec[ec2.EC2InstanceSpec](desiredSpec)
    if err != nil {
        return "", nil, err
    }

    // Plan-time resolution: read the stored instance ID from the Virtual Object's
    // outputs via the shared GetOutputs handler (non-blocking, concurrent-safe).
    // If the VO has no outputs, the instance hasn't been provisioned yet → OpCreate.
    // IMPORTANT: GetOutputs is a Restate Virtual Object call — it MUST be invoked
    // directly on the Restate context (ctx), NOT inside a restate.Run() callback.
    // restate.Run() is for non-Restate side effects (AWS API calls, HTTP, etc.).
    // Wrapping a VO-to-VO call in restate.Run() leaks the outer ctx into a
    // RunContext, and Restate won't journal the inner invocation correctly.
    type outputsResult struct {
        Outputs ec2.EC2InstanceOutputs
        Found   bool
    }
    outputs, getErr := restate.Object[ec2.EC2InstanceOutputs](ctx, a.ServiceName(), key, "GetOutputs").
        Request(restate.Void{})
    var existingOutputs outputsResult
    if getErr != nil {
        // Only treat "no state" (zero-value response) as "not provisioned yet".
        // Real failures (handler bugs, serialization errors, Restate service unavailability)
        // must be surfaced — masking them as OpCreate is actively misleading and can
        // cause accidental resource duplication. This follows the S3 and SG adapter
        // pattern, which propagate planning errors rather than swallowing them.
        return "", nil, fmt.Errorf("EC2 Plan: failed to read outputs for key %q: %w", key, getErr)
    } else if outputs.InstanceId == "" {
        existingOutputs = outputsResult{Found: false}
    } else {
        existingOutputs = outputsResult{Outputs: outputs, Found: true}
    }

    if !existingOutputs.Found {
        fields, fieldErr := createFieldDiffsFromSpec(desired)
        if fieldErr != nil {
            return "", nil, fieldErr
        }
        return types.OpCreate, fields, nil
    }

    // Instance exists — describe it by stored ID for drift comparison.
    planningAPI, err := a.planningAPI(account)
    if err != nil {
        return "", nil, err
    }

    type describePlanResult struct {
        State ec2.ObservedState
        Found bool
    }
    result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
        obs, descErr := planningAPI.DescribeInstance(runCtx, existingOutputs.Outputs.InstanceId)
        if descErr != nil {
            if ec2.IsNotFound(descErr) {
                return describePlanResult{Found: false}, nil
            }
            return describePlanResult{}, restate.TerminalError(descErr, 500)
        }
        // Treat terminated instances as "not found" for plan purposes.
        if obs.State == "terminated" || obs.State == "shutting-down" {
            return describePlanResult{Found: false}, nil
        }
        return describePlanResult{State: obs, Found: true}, nil
    })
    if err != nil {
        return "", nil, err
    }

    if !result.Found {
        // Instance was removed externally — plan shows re-create.
        fields, fieldErr := createFieldDiffsFromSpec(desired)
        if fieldErr != nil {
            return "", nil, fieldErr
        }
        return types.OpCreate, fields, nil
    }

    rawDiffs := ec2.ComputeFieldDiffs(desired, result.State)
    if len(rawDiffs) == 0 {
        return types.OpNoOp, nil, nil
    }

    fields := make([]types.FieldDiff, 0, len(rawDiffs))
    for _, diff := range rawDiffs {
        fields = append(fields, types.FieldDiff{
            Path:     diff.Path,
            OldValue: diff.OldValue,
            NewValue: diff.NewValue,
        })
    }
    return types.OpUpdate, fields, nil
}

func (a *EC2Adapter) BuildImportKey(region, resourceID string) (string, error) {
    if err := ValidateKeyPart("region", region); err != nil {
        return "", err
    }
    if err := ValidateKeyPart("resource ID", resourceID); err != nil {
        return "", err
    }
    return JoinKey(region, resourceID), nil
}

func (a *EC2Adapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
    ref.Account = account
    output, err := restate.WithRequestType[types.ImportRef, ec2.EC2InstanceOutputs](
        restate.Object[ec2.EC2InstanceOutputs](ctx, a.ServiceName(), key, "Import"),
    ).Request(ref)
    if err != nil {
        return "", nil, err
    }
    outputs, err := a.NormalizeOutputs(output)
    if err != nil {
        return "", nil, err
    }
    return types.StatusReady, outputs, nil
}

func (a *EC2Adapter) decodeSpec(doc resourceDocument) (ec2.EC2InstanceSpec, error) {
    var spec ec2.EC2InstanceSpec
    if err := json.Unmarshal(doc.Spec, &spec); err != nil {
        return ec2.EC2InstanceSpec{}, fmt.Errorf("decode EC2Instance spec: %w", err)
    }
    name := strings.TrimSpace(doc.Metadata.Name)
    if name == "" {
        return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance metadata.name is required")
    }
    if strings.TrimSpace(spec.Region) == "" {
        return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.region is required")
    }
    if strings.TrimSpace(spec.ImageId) == "" {
        return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.imageId is required")
    }
    if strings.TrimSpace(spec.InstanceType) == "" {
        return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.instanceType is required")
    }
    if strings.TrimSpace(spec.SubnetId) == "" {
        return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.subnetId is required")
    }
    // Set Name tag from metadata.name if not already set
    if spec.Tags == nil {
        spec.Tags = make(map[string]string)
    }
    if spec.Tags["Name"] == "" {
        spec.Tags["Name"] = name
    }
    spec.Account = ""
    return spec, nil
}

func (a *EC2Adapter) planningAPI(account string) (ec2.EC2API, error) {
    if a.staticPlanningAPI != nil {
        return a.staticPlanningAPI, nil
    }
    if a.auth == nil || a.apiFactory == nil {
        return nil, fmt.Errorf("EC2 adapter planning API is not configured")
    }
    awsCfg, err := a.auth.Resolve(account)
    if err != nil {
        return nil, fmt.Errorf("resolve EC2 planning account %q: %w", account, err)
    }
    return a.apiFactory(awsCfg), nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — **MODIFY**

Add `NewEC2Adapter` to the hardcoded adapter set in `NewRegistry()`.

### Change

In the `NewRegistry()` function, add one line:

```go
// Before:
func NewRegistry() *Registry {
    accounts := auth.LoadFromEnv()
    return NewRegistryWithAdapters(
        NewS3AdapterWithRegistry(accounts),
        NewSecurityGroupAdapterWithRegistry(accounts),
    )
}

// After:
func NewRegistry() *Registry {
    accounts := auth.LoadFromEnv()
    return NewRegistryWithAdapters(
        NewS3AdapterWithRegistry(accounts),
        NewSecurityGroupAdapterWithRegistry(accounts),
        NewEC2AdapterWithRegistry(accounts),
    )
}
```

The `ec2` package is not imported directly in `registry.go` — it is only used via
the adapter. The adapter file (`ec2_adapter.go`) handles the import.

---

## Step 9 — Compute Driver Pack Entry Point & Dockerfile

### Entry Point

**File**: `cmd/praxis-compute/main.go`

The EC2 driver is added to the **compute** driver pack. The Restate SDK supports binding multiple Virtual Objects to one server via chained `.Bind()` calls, so the compute pack hosts all compute-related drivers (EC2, and in the future Auto Scaling, Lambda, ECS, EKS).

```go
package main

import (
    "context"
    "log/slog"
    "os"

    restate "github.com/restatedev/sdk-go"
    "github.com/restatedev/sdk-go/server"

    "github.com/praxiscloud/praxis/internal/core/config"
    "github.com/praxiscloud/praxis/internal/drivers/ec2"
)

func main() {
    cfg := config.Load()

    srv := server.NewRestate().
        Bind(restate.Reflect(ec2.NewEC2InstanceDriver(cfg.Auth())))

    slog.Info("starting compute driver pack", "addr", cfg.ListenAddr)
    if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
        slog.Error("compute driver pack exited", "err", err.Error())
        os.Exit(1)
    }
}
```

### Dockerfile

**File**: `cmd/praxis-compute/Dockerfile`

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-compute ./cmd/praxis-compute

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-compute /praxis-compute
ENTRYPOINT ["/praxis-compute"]
```

---

## Step 10 — Docker Compose & Justfile

### docker-compose.yaml — **MODIFY**

Add a `praxis-compute` service block (the compute driver pack). This hosts the EC2 driver and will host future compute drivers (Auto Scaling, Lambda, etc.).

```yaml
  praxis-compute:
    build:
      context: .
      dockerfile: cmd/praxis-compute/Dockerfile
    container_name: praxis-compute
    env_file:
      - .env
    depends_on:
      restate:
        condition: service_healthy
      localstack:
        condition: service_healthy
    ports:
      - "9084:9080"
    environment:
      - PRAXIS_LISTEN_ADDR=0.0.0.0:9080
```

**Note**: Storage pack uses host port 9081, Network pack uses 9082, Core uses 9083. Compute pack gets 9084.

### justfile — **MODIFY**

Add these entries:

1. **In `register` recipe** — add compute pack registration:

```bash
    @echo "Registering compute driver pack with Restate..."
    @curl -s -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-compute:9080"}' | jq .
    @echo "✓ Compute driver pack registered"
```

2. **In `build` recipe** — add compute binary:

```bash
    go build -o bin/praxis-compute ./cmd/praxis-compute
```

3. **In `restart` recipe** — add praxis-compute:

```bash
    docker compose up -d --build praxis-core praxis-storage praxis-network praxis-compute
```

4. **Add driver test/log targets**:

```makefile
# Run EC2 driver unit tests only
test-ec2:
    go test ./internal/drivers/ec2/... -v -count=1 -race

# Follow logs for the compute driver pack.
logs-compute:
    docker compose logs -f praxis-compute

# Follow logs for all driver packs together.
logs-drivers:
    docker compose logs -f praxis-storage praxis-network praxis-compute
```

---

## Step 11 — Unit Tests

### `internal/drivers/ec2/driver_test.go`

Create a mock `EC2API` using testify/mock. Test each handler with mocked AWS responses.

**Test cases to implement:**

#### Provision Tests

1. `TestProvision_CreatesNewInstance` — happy path: RunInstance succeeds, WaitUntilRunning succeeds, DescribeInstance returns outputs.
2. `TestProvision_MissingImageIdFails` — returns terminal error 400.
3. `TestProvision_MissingInstanceTypeFails` — returns terminal error 400.
4. `TestProvision_MissingSubnetIdFails` — returns terminal error 400.
5. `TestProvision_InvalidAMIFails` — `IsInvalidParam` triggers terminal error 400.
6. `TestProvision_InsufficientCapacityFails` — `IsInsufficientCapacity` triggers terminal error **503** (not 400).
7. `TestProvision_IdempotentReprovision` — second call with same spec converges, doesn't create new instance.
8. `TestProvision_TypeChangeStopsAndRestarts` — instance type change calls ModifyInstanceType.
9. `TestProvision_SecurityGroupUpdate` — SG change calls ModifySecurityGroups.
10. `TestProvision_ConflictTaggedInstanceFails` — `FindByManagedKey` returns a non-empty ID → terminal error 409.
11. `TestProvision_NoConflictWhenTagNotPresent` — `FindByManagedKey` returns `""` → proceeds to RunInstance.
12. `TestProvision_MultipleConflictsFails` — `FindByManagedKey` returns ownership corruption error → terminal error 500.

#### Import Tests

13. `TestImport_ExistingInstance` — describes instance, synthesizes spec, returns outputs.
14. `TestImport_TerminatedInstanceFails` — returns terminal error for terminated instance.
15. `TestImport_NotFoundFails` — returns terminal error 404.
16. `TestImport_DefaultsToObservedMode` — import with no explicit mode sets ModeObserved.
17. `TestImport_ExplicitManagedMode` — import with `--mode managed` sets ModeManaged.

#### Delete Tests

18. `TestDelete_TerminatesInstance` — calls TerminateInstance, sets tombstone state (ModeManaged).
19. `TestDelete_AlreadyGone` — IsNotFound returns success (idempotent).
20. `TestDelete_NoInstanceProvisioned` — sets tombstone without API call.
21. `TestDelete_ObservedModeBlocked` — Delete returns terminal error 409 for ModeObserved resources.

#### Reconcile Tests

22. `TestReconcile_NoDrift` — no changes when spec matches observed.
23. `TestReconcile_DetectsInstanceTypeDrift` — drift=true, correcting=true in managed mode.
24. `TestReconcile_DetectsTagDrift` — drift=true, tag correction applied.
25. `TestReconcile_ObservedModeReportsOnly` — drift=true, correcting=false.
26. `TestReconcile_ExternalTermination` — transitions to Error status.
27. `TestReconcile_SkipsNonReadyStatus` — no-op for Pending/Provisioning/Deleting.

#### Shared Handler Tests

28. `TestGetStatus_ReturnsCurrentState` — reads state from K/V.
29. `TestGetOutputs_ReturnsOutputs` — reads outputs from K/V.

### `internal/drivers/ec2/drift_test.go`

Test cases:

1. `TestHasDrift_NoDrift` — identical desired and observed returns false.
2. `TestHasDrift_InstanceTypeChanged` — returns true.
3. `TestHasDrift_SecurityGroupsChanged` — returns true.
4. `TestHasDrift_SecurityGroupsReordered` — returns false (order-independent).
5. `TestHasDrift_MonitoringChanged` — returns true.
6. `TestHasDrift_TagAdded` — returns true.
7. `TestHasDrift_TagRemoved` — returns true.
8. `TestHasDrift_TagValueChanged` — returns true.
9. `TestHasDrift_TerminatedInstanceNoDrift` — returns false (skip terminated).
10. `TestComputeFieldDiffs_MultipleDiffs` — returns list of specific diffs.
11. `TestTagsMatch_NilAndEmpty` — treats nil and empty as equivalent.
12. `TestTagsMatch_IgnoresPraxisTags` — praxis:managed-key in observed does not cause mismatch.
13. `TestComputeFieldDiffs_IgnoresPraxisTags` — praxis:* entries excluded from tag diffs.
14. `TestComputeFieldDiffs_ImmutableImageId` — reports diff with "(immutable, ignored)" suffix.
15. `TestComputeFieldDiffs_ImmutableSubnetId` — reports diff with "(immutable, ignored)" suffix.
16. `TestComputeFieldDiffs_ImmutableKeyName` — reports diff with "(immutable, ignored)" suffix.

### `internal/drivers/ec2/aws_test.go`

Test error classification helpers:

1. `TestIsNotFound_True` — various NotFound error shapes.
2. `TestIsNotFound_False` — other error types.
3. `TestIsInvalidParam_AmiNotFound` — InvalidAMIID.NotFound.
4. `TestIsInsufficientCapacity_True` — InsufficientInstanceCapacity.
5. `TestBase64Encode_PlainText` — always encodes.
6. `TestBase64Encode_AlreadyEncodedInput` — double-encodes (by design, see base64Encode doc).
7. `TestFindByManagedKey_Found` — returns instance ID when exactly one tag filter match.
8. `TestFindByManagedKey_NotFound` — returns `""` when no match.
9. `TestFindByManagedKey_ExcludesTerminated` — state filter excludes terminated instances.
10. `TestFindByManagedKey_MultipleMatchesReturnsError` — returns ownership corruption error when >1 live instances match.

### `internal/core/provider/ec2_adapter_test.go`

Test cases (follow `registry_test.go` patterns):

1. `TestEC2Adapter_DecodeSpecAndBuildKey` — parses JSON doc, returns `region~name` key.
2. `TestEC2Adapter_BuildImportKey` — returns `region~instanceId` key.
3. `TestEC2Adapter_Kind` — returns `EC2Instance`.
4. `TestEC2Adapter_Scope` — returns `KeyScopeRegion`.
5. `TestEC2Adapter_NormalizeOutputs` — converts struct to map.
6. `TestEC2Adapter_DecodeSpec_MissingRegion` — returns error.
7. `TestEC2Adapter_DecodeSpec_MissingImageId` — returns error.
8. `TestEC2Adapter_DecodeSpec_SetsNameTag` — auto-sets Name tag from metadata.name.

---

## Step 12 — Integration Tests

**File**: `tests/integration/ec2_driver_test.go`

These tests use Testcontainers (Restate) + LocalStack (EC2 emulation) — same pattern
as the S3 and SG integration tests.

### Setup Helper

```go
func setupEC2Driver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
    t.Helper()
    configureLocalAccount(t)

    awsCfg := localstackAWSConfig(t)
    ec2Client := awsclient.NewEC2Client(awsCfg)
    driver := ec2.NewEC2InstanceDriver(nil)

    env := restatetest.Start(t, restate.Reflect(driver))
    return env.Ingress(), ec2Client
}

// getDefaultSubnetId returns a subnet in the default VPC.
func getDefaultSubnetId(t *testing.T, ec2Client *ec2sdk.Client) (string, string) {
    t.Helper()
    vpcId := getDefaultVpcId(t, ec2Client)

    out, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{
        Filters: []ec2types.Filter{
            {Name: aws.String("vpc-id"), Values: []string{vpcId}},
        },
    })
    require.NoError(t, err)
    require.NotEmpty(t, out.Subnets)
    return aws.ToString(out.Subnets[0].SubnetId), vpcId
}

// uniqueInstanceName generates a unique instance name for each test.
func uniqueInstanceName(t *testing.T) string {
    t.Helper()
    name := strings.ReplaceAll(t.Name(), "/", "-")
    name = strings.ReplaceAll(name, "_", "-")
    if len(name) > 50 {
        name = name[:50]
    }
    return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}
```

### Test Cases

1. **TestEC2Provision_CreatesRealInstance** — Provisions an instance, verifies it appears in
   DescribeInstances. Uses a LocalStack-compatible AMI ID (LocalStack typically accepts any AMI
   format — use `ami-12345678` for tests).

2. **TestEC2Provision_Idempotent** — Two provisions with same spec, same outputs returned.

3. **TestEC2Import_ExistingInstance** — Creates instance directly via EC2 SDK, imports via driver.

4. **TestEC2Delete_TerminatesInstance** — Provisions, deletes, verifies instance is terminated.

5. **TestEC2Reconcile_DetectsDrift** — Provisions, manually changes tags via EC2 API, triggers
   reconcile, verifies tags are corrected.

6. **TestEC2GetStatus_ReturnsReady** — Provisions, calls GetStatus, verifies Ready + ModeManaged.

### LocalStack EC2 Compatibility Note

LocalStack's EC2 emulation is reasonably complete for basic instance operations:
RunInstances, DescribeInstances, TerminateInstances, CreateTags, DescribeVolumes.
Some advanced features (instance type modification, monitoring toggle) may behave
differently. Integration tests should focus on the core CRUD lifecycle and tag drift.

---

## EC2-Specific Design Decisions

### 1. Key Strategy: `region~metadata.name`

See [Key Strategy §2](#2-key-strategy) for the full analysis, including the
rationale, import semantics, and plan-time resolution. Summary:

- **The Virtual Object key is `region~metadata.name`**, always and permanently.
- Template authors **must** give each instance a unique name within its region.
- The `Name` tag is automatically set from `metadata.name` by the adapter.
- After provisioning, the AWS instance ID is stored in `state.Outputs.InstanceId`.
- **Import uses a separate key**: `region~instanceId`, creating a separate VO.

**Why not `region~instanceId`**: Restate VOs are immutable once keyed. The instance
ID is assigned by AWS at launch time and is unavailable when `BuildKey` runs at
plan/dispatch time. The pipeline calls `BuildKey` → `Provision` → `Delete` with the
same key. There is no rename path for a VO.

### 2. Immutable vs. Mutable Attributes

| Attribute | Mutable? | How? | Drift Checked? |
|---|---|---|---|
| `instanceType` | Yes | Stop → ModifyInstanceAttribute → Start | **Yes** |
| `securityGroupIds` | Yes | ModifyInstanceAttribute (live) | **Yes** |
| `monitoring` | Yes | MonitorInstances / UnmonitorInstances (live) | **Yes** |
| `tags` | Yes | CreateTags / DeleteTags (live) | **Yes** |
| `imageId` | **No** | Requires new instance (replacement) | **No** |
| `subnetId` | **No** | Requires new instance (replacement) | **No** |
| `keyName` | **No** | Requires new instance (replacement) | **No** |
| `userData` | Partial | Only on stopped instances | **No** |
| `rootVolume.sizeGiB` | **No** | Requires volume modification (out of scope) | **No** |
| `iamInstanceProfile` | Partial | Can associate/disassociate | **No** |

The drift engine only checks **mutable attributes** to avoid false positives.

**Immutable field changes in plan output**: When an immutable field changes between
the desired spec and observed state, `ComputeFieldDiffs` reports it as a diff entry
with the path suffixed with `(immutable, ignored)`. This makes the change visible
in `praxis plan` output without triggering correction. The plan operation remains
`OpUpdate` (not a new `OpReplace`) — replacement semantics are not implemented.
The user sees what changed, but the driver does not act on immutable field changes.

### 3. Tagging: Instance Only

Tags apply to the **instance only**:

- `RunInstance`: `TagSpecifications` with `ResourceType: instance` only.
- `UpdateTags`: operates on the instance resource only.
- CUE schema comment updated to say "tags applied to the instance only."
- Root volume tagging is not supported (tracked in FUTURE.md).

Rationale: Volume tagging at create time requires a second `TagSpecification` entry
(ResourceType: volume), which is straightforward. But volume retagging requires
discovering the root volume ID via DescribeInstances → BlockDeviceMappings → EBS
VolumeId, then calling CreateTags on that volume. This adds complexity and extra API
calls to every tag update.

### 4. Capacity Failures: 503, Not 400

Capacity failures and input errors use separate status codes:

- `IsInvalidParam` → `restate.TerminalError(err, 400)` — bad AMI, bad subnet, etc.
- `IsInsufficientCapacity` → `restate.TerminalError(err, 503)` — AWS can't fulfill
  the request. This is a provider-side issue, not a user input issue.

Both are terminal (no retry), but the status code tells the caller what went wrong.

### 5. Instance Type Changes Are Disruptive

Changing `instanceType` requires stopping the instance. The driver:

1. Stops the instance
2. Waits for stopped state
3. Modifies the instance type
4. Starts the instance

This causes **downtime**. The current `FieldDiff` shape (path, old value, new value)
does not carry a "disruptive" flag. The plan output includes the diff as a normal
`OpUpdate` field diff. The driver logs a warning before stopping.

Extending `types.FieldDiff` with an optional `Disruptive bool` field to surface
disruptive changes in CLI output (e.g., "~ instanceType: t3.micro => t3.large ⚠ disruptive")
requires changes to the shared diff model in `pkg/types/diff.go` and CLI
formatting in `internal/cli/plan.go`.

### 6. No Auto-Termination Protection

The Delete handler directly terminates instances. It does NOT check for
termination protection. If termination protection is enabled, AWS returns an
`OperationNotPermitted` error, which the driver surfaces as a terminal error.

### 7. UserData Limitations

AWS does not return user data in DescribeInstances (it requires a separate
GetInstanceAttribute call and the data is base64-encoded). User data drift
detection is intentionally skipped. User data can only be modified on
stopped instances, making it impractical for automated drift correction.

### 8. Import and Template: Separate Lifecycle Tracks

See [Import semantics](#import-semantics-separate-lifecycle-track) in §2 for the
complete analysis. Summary:

Import and template-based management are intentionally separate lifecycle tracks.
`praxis import --kind EC2Instance --region us-east-1 --resource-id i-0abc123`
creates VO key `us-east-1~i-0abc123`. A template with `metadata.name: web-server`
creates VO key `us-east-1~web-server`. These are different Restate Virtual Objects
even if they happen to target the same AWS instance.

This is consistent with S3 (import key = bucket name = BuildKey) and SG (import
key = group ID ≠ BuildKey which is vpcId~groupName). The SG import already creates
a separate lifecycle track; EC2 follows the same pattern.

If a user wants to bring an imported instance under template management, they
should delete the import VO and apply a template whose metadata.name and spec
match what they want. This is a manual migration, not an automatic convergence.

#### Dual-control risk: EC2 delete is destructive

Unlike S3 (bucket deletion is recoverable from versioning / lifecycle policies) and
SG (removing a security group leaves instances running), **EC2 instance deletion is
permanent** — `TerminateInstances` destroys the machine and all ephemeral storage.

When a region+instanceId VO and a region+metadata.name VO both target the same AWS
instance, two independent control planes can each issue `Delete`, or one can issue
`Delete` while the other is mid-`Reconcile`. This is a real risk with the separate
lifecycle tracks.

**Mitigation — import defaults to ModeObserved**: the `Import` handler uses
`defaultEC2ImportMode` (not `drivers.DefaultMode`) which returns `ModeObserved`
when no explicit mode is supplied. An imported instance in `Observed` mode:

- **Delete is blocked**: the Delete handler checks `state.Mode` and returns a
  terminal error (409) for Observed-mode resources. This is the hard safety
  guarantee — not a convention, an enforced gate.
- **Drift correction is skipped**: Reconcile reports drift but does not apply
  corrections (same as existing Reconcile behaviour for ModeObserved).

The operator must explicitly pass `--mode managed` to grant the import VO full
lifecycle authority (both drift correction and deletion).

**Remaining risk with `--mode managed`**: if an operator imports an instance with
`--mode managed` AND a template VO also claims the same instance, two managed VOs
exist. This is the operator's responsibility — Praxis does not track cross-VO
ownership of the same AWS resource. The ownership-tag conflict check (§9 below)
protects against accidental template collisions but does not cover the
import+template combination.

**Recommended operator practice**: use `praxis import` for read-only visibility
(`ModeObserved`, the default), and use templates for active lifecycle management.
Do not import and template-manage the same instance simultaneously.

---

### 9. Planning Contract: State-Driven Discovery

See [Plan-time instance resolution](#plan-time-instance-resolution) in §2 for the
complete analysis and the product decision callout.

**This is an explicit product decision.** The `Plan()` adapter reads the Virtual
Object's stored outputs to obtain the instance ID; if no outputs exist, Plan returns
`OpCreate` without consulting AWS.

| S3/SG adapters | EC2 adapter |
|---|---|
| Describe by stable, user-visible name (bucket name / group name) | No stable name — Name tags are mutable and non-unique |
| `OpCreate` if AWS says resource doesn't exist | `OpCreate` if Praxis has no VO state for this key |

**Accepted limitation**: an unmanaged instance that happens to share the same
`metadata.name` and region as a Praxis template will not be detected at plan time.
The pre-flight conflict check in `Provision` (§10) is the last line of defence,
**but it only works when the existing instance already carries the
`praxis:managed-key` tag** — i.e., it was previously provisioned or recovered by
Praxis. If the existing instance was created manually or by another tool and has
no ownership tag, `Provision` will silently launch a second instance. Operators
are responsible for not reusing instance names from pre-existing unmanaged
resources.

**Wiped state case**: if the Restate journal is lost after an instance was
provisioned but the AWS instance is still running, Plan will show `OpCreate`. If
the original instance still carries the `praxis:managed-key` tag, the conflict check
in `Provision` will fire (409), prompting the operator to either restore state or
import the instance under its own key before re-applying the template.

---

### 10. Ownership Tags and Conflict Detection

To surface duplicate-template collisions as terminal errors rather than silent
dual-control, the driver uses a `praxis:managed-key` resource tag:

| What | Value |
|---|---|
| Tag key | `praxis:managed-key` |
| Tag value | `<region>~<metadata.name>` (the Restate VO key) |
| Written by | `RunInstance` → `TagSpecifications` (always, not just when user declares tags) |
| Never removed by | drift correction (UpdateTags skips this key) |
| Checked by | `Provision` pre-flight via `FindByManagedKey` (only when VO state is empty) |

**Conflict response**: if `FindByManagedKey` finds a live instance claiming the same
key, `Provision` returns a terminal error (409):

```text
instance name "web-server" in this region is already managed by Praxis (instanceId: i-0abc123def456);
remove the existing resource or use a different metadata.name
```

**Scope of protection**:

- ✅ Two templates in the *same* Praxis installation managing the same key → 409 on the second Provision
- ✅ Wiped Praxis state + original instance still running with tag → 409 (prompts operator to import or restore)
- ❌ Two *separate* Praxis installations (different Restate clusters) targeting the same region → not protected; tags from installation A are invisible to installation B's ownership policy

**`UpdateTags` must preserve the managed-key tag**: the `UpdateTags` implementation
filters out `praxis:managed-key` from both the delete-tags and create-tags passes,
so drift correction never removes the ownership signal.

---

### 11. Root Device Name: `/dev/xvda`

The `RunInstance` implementation hardcodes the root device name to `/dev/xvda` in
`BlockDeviceMappings`. This is the default for Amazon Linux, Amazon Linux 2, and
Amazon Linux 2023 AMIs. Other AMI families use different root device names:

| AMI Family | Root Device Name |
|---|---|
| Amazon Linux (all versions) | `/dev/xvda` |
| Ubuntu | `/dev/sda1` |
| RHEL / CentOS | `/dev/sda1` |
| Windows | `/dev/sda1` |
| Debian | `/dev/xvda` |

For this iteration, operators must use Amazon Linux or Debian AMIs when specifying
`rootVolume`. If `rootVolume` is omitted (the common case — AWS applies sensible
defaults), the hardcoded device name is never sent and the AMI family doesn't matter.

Calling `DescribeImages` to resolve the AMI's actual root device name before building
`BlockDeviceMappings` would add one API call per provision but make the driver
AMI-family-agnostic. Tracked in FUTURE.md.

---

## Design Decisions (Resolved)

1. **Do you want Plan to detect unmanaged-but-existing instances at all?**
   No. The contract is *create unless Praxis already owns state for this key*.
   Detecting unmanaged instances at plan time requires a tag-based search over
   mutable tags — unreliable and inconsistent with the state-driven model. The
   conflict check fires at `Provision` time, which is sufficient to prevent
   double-provisioning of instances that were previously managed by Praxis
   (i.e., those carrying the `praxis:managed-key` tag). Unmanaged instances
   without the tag are not detected — see Design Decisions §9.

2. **When a template-managed VO and an imported VO point at the same instance, which is authoritative?**
   This is left as operator responsibility. Praxis will not automatically arbitrate
   between two VOs targeting the same AWS resource. The recommended practice is to
   use import for read-only observability (`ModeObserved`, the default) and templates
   for active lifecycle management — never both simultaneously. The import default of
   `ModeObserved` reduces the risk of accidental termination via the Delete-mode
   guard (see Step 6, Delete Handler), but operators who explicitly import with
   `--mode managed` accept dual-control responsibility.

3. **LocalStack EC2 integration test scope:**
   Integration tests cover: create, idempotent re-provision, import, delete, and tag
   drift correction. `stop-modify-start` (instance type change) is covered in unit
   tests with a mocked `EC2API` only — LocalStack's behaviour for this sequence is
   inconsistent enough to make integration coverage unreliable.

4. **Should Observed mode mean read-only for both Reconcile and Delete?**
   Yes. Observed mode is fully read-only across both handlers:
   - **Reconcile**: reports drift but does not apply corrections (already implemented
     in the Reconcile handler's `ModeObserved` branch).
   - **Delete**: returns a terminal error (409) refusing to terminate the instance
     (enforced by the mode guard in the Delete handler).
   This is an explicit contract: Observed mode guarantees the driver will never
   mutate or destroy the underlying AWS resource.

   **Note**: the S3 and SG drivers do not check `state.Mode` in their Delete
   handlers. The EC2 driver establishes the correct pattern. Backporting the mode
   guard to S3 and SG is tracked in FUTURE.md.

5. **When state exists but DescribeInstance returns not-found during Plan, should we
   return OpCreate unconditionally or a more explicit "resource missing" signal?**
   **Product decision**: Plan returns `OpCreate`. This is the simplest and most
   actionable signal — it tells the operator exactly what `praxis apply` will do.
   A separate "resource missing / state stale" status would require new plan
   operation types, CLI formatting changes, and operator documentation, with
   minimal practical benefit: the operator's next action is either `apply` (to
   recreate) or manual state cleanup in either case. The ownership-tag conflict
   check in `Provision` provides the safety net: if the original instance is still
   running with its `praxis:managed-key` tag, Provision will fail with a 409
   instead of silently creating a duplicate.

---

## Checklist

- [x] **Schema**: `schemas/aws/ec2/ec2.cue` created
- [x] **Types**: `internal/drivers/ec2/types.go` created with Spec, Outputs, ObservedState, State
- [x] **AWS API**: `internal/drivers/ec2/aws.go` created with EC2API interface + realEC2API
- [x] **Drift**: `internal/drivers/ec2/drift.go` created with HasDrift, ComputeFieldDiffs
- [x] **Driver**: `internal/drivers/ec2/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/ec2_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated with EC2 adapter
- [x] **Entry point**: EC2 driver bound in `cmd/praxis-compute/main.go`
- [x] **Dockerfile**: `cmd/praxis-compute/Dockerfile` created
- [x] **Docker Compose**: `docker-compose.yaml` updated with praxis-compute service
- [x] **Justfile**: Updated with ec2 build/test/register/log targets
- [x] **Unit tests (drift)**: `internal/drivers/ec2/drift_test.go` created
- [x] **Unit tests (aws helpers)**: `internal/drivers/ec2/aws_test.go` created (includes FindByManagedKey)
- [x] **Unit tests (driver)**: `internal/drivers/ec2/driver_test.go` created with mocked AWS (includes conflict+import-mode tests)
- [x] **Unit tests (adapter)**: `internal/core/provider/ec2_adapter_test.go` created
- [x] **Integration tests**: `tests/integration/ec2_driver_test.go` created
- [x] **Conflict check**: `FindByManagedKey` in EC2API interface + realEC2API implementation (with multi-match corruption error)
- [x] **Ownership tag**: `praxis:managed-key` written by `RunInstance`; preserved by `UpdateTags`
- [x] **Import default mode**: `defaultEC2ImportMode` returns ModeObserved when unspecified
- [x] **Delete mode guard**: Delete handler blocks termination for ModeObserved resources (409)
- [x] **ARN population**: Not populated — requires STS `GetCallerIdentity` for account ID (tracked in FUTURE.md)
- [x] **Root device name**: Hardcoded to `/dev/xvda` (Amazon Linux) — see Design Decisions §11
- [x] **Backport Delete mode guard**: Add `state.Mode` check to S3 and SG Delete handlers (tracked in FUTURE.md)
- [x] **Build passes**: `go build ./...` succeeds
- [x] **Unit tests pass**: `go test ./internal/drivers/ec2/... ./internal/core/provider/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestEC2 -tags=integration`
- [x] **Docker stack runs**: `just up` registers compute driver pack (including EC2) with Restate
