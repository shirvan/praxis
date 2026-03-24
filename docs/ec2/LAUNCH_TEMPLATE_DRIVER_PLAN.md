# Launch Template Driver — Implementation Plan

> **Status: Not yet implemented.** This document is a plan only.
>
> Target: A Restate Virtual Object driver that manages EC2 launch templates,
> following the exact patterns established by the S3, Security Group, EC2, VPC,
> EBS, Elastic IP, and Key Pair drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~metadata.name`, permanent
> and immutable for the lifetime of the Virtual Object. The CUE schema maps
> `metadata.name` to the launch template name.

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
16. [Launch-Template-Specific Design Decisions](#launch-template-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The Launch Template driver manages the lifecycle of **EC2 launch templates** only.
Launch templates define instance configuration (AMI, instance type, key pair,
security groups, networking, user data, etc.) as a versioned blueprint. EC2 instances,
Auto Scaling Groups, and Spot Fleet requests reference launch templates at launch
time.

### Versioning Model

AWS launch templates are **versioned resources**. Each template has:

- A template name (unique per region, immutable after creation).
- A template ID (AWS-assigned, immutable).
- One or more **versions** (1, 2, 3, …), each capturing a snapshot of configuration.
- A **default version** pointer (initially version 1).
- An optional **latest version** pointer (highest version number).

**Praxis approach**: Each Provision call with changed spec fields creates a **new
version** and sets it as the default version. This is the natural AWS model — launch
template versions are immutable, and "updating" means adding a version. The driver
tracks which version is the current default and exposes it in outputs.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create template or add version |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing launch template |
| `Delete` | `ObjectContext` (exclusive) | Delete template (all versions) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return template outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| Template name | Immutable | Unique per region, set at creation |
| Template ID | Immutable | AWS-assigned |
| Version data (AMI, instance type, etc.) | Immutable per version | New version = new snapshot |
| Default version number | Mutable | Updated via `ModifyLaunchTemplate` |
| Template-level tags | Mutable | Tags on the template resource itself |
| Description (per version) | Immutable per version | Set at version creation |

### What Is NOT In Scope

- **Auto Scaling Group integration**: ASGs reference launch templates but are
  managed by a future ASG driver.
- **Version cleanup / retention policies**: The driver creates new versions but
  does not delete old ones. AWS allows up to 5,000 versions per template.
- **Launch template data overrides**: EC2 RunInstances can override launch template
  fields — this is handled by the EC2 driver, not this driver.

### Downstream Consumers

```text
${resources.my-lt.outputs.launchTemplateId}      → EC2 instances, ASGs
${resources.my-lt.outputs.launchTemplateName}     → EC2 instances (by name)
${resources.my-lt.outputs.defaultVersionNumber}   → EC2 instances (specific version)
${resources.my-lt.outputs.latestVersionNumber}    → Informational
```

---

## 2. Key Strategy

### Key Format: `region~templateName`

Launch template names are unique within a region. The CUE schema maps
`metadata.name` to the template name. The adapter produces
`region~metadata.name` as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain a template ID,
   describes the template by ID. Otherwise, `OpCreate`.
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the launch template name. Same key as BuildKey — matching
   the S3/KeyPair pattern because template names are AWS-unique per region.

### No Ownership Tags

Like Key Pairs and S3, launch template names are AWS-enforced unique within a
region. `CreateLaunchTemplate` returns a duplicate error if the name exists. This
natural conflict signal eliminates the need for `praxis:managed-key` tags.

---

## 3. File Inventory

```text
✦ internal/drivers/launchtemplate/types.go                — Spec, Outputs, ObservedState, State
✦ internal/drivers/launchtemplate/aws.go                  — LaunchTemplateAPI interface + impl
✦ internal/drivers/launchtemplate/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/launchtemplate/driver.go               — LaunchTemplateDriver Virtual Object
✦ internal/drivers/launchtemplate/driver_test.go          — Unit tests for driver
✦ internal/drivers/launchtemplate/aws_test.go             — Unit tests for error classification
✦ internal/drivers/launchtemplate/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/launchtemplate_adapter.go        — Adapter
✦ internal/core/provider/launchtemplate_adapter_test.go   — Adapter tests
✦ schemas/aws/ec2/launchtemplate.cue                      — CUE schema
✦ tests/integration/launchtemplate_driver_test.go         — Integration tests
✎ cmd/praxis-compute/main.go                             — Bind LaunchTemplate driver
✎ internal/core/provider/registry.go                     — Add adapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ec2/launchtemplate.cue`

```cue
package ec2

#LaunchTemplate: {
    apiVersion: "praxis.io/v1"
    kind:       "LaunchTemplate"

    metadata: {
        // name is the launch template name in AWS.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._()/-]{0,127}$"
        labels: [string]: string
    }

    spec: {
        region: string

        // description for this version of the launch template.
        description?: string

        // imageId is the AMI to launch instances from.
        imageId?: string

        // instanceType is the EC2 instance type.
        instanceType?: string

        // keyName is the SSH key pair name.
        keyName?: string

        // securityGroupIds to attach to the instance.
        securityGroupIds?: [...string]

        // subnetId for the primary network interface.
        // Typically set at launch, not in the template.
        subnetId?: string

        // userData is base64-encoded user data script.
        userData?: string

        // iamInstanceProfile is the IAM instance profile name or ARN.
        iamInstanceProfile?: string

        // monitoring enables detailed CloudWatch monitoring.
        monitoring?: bool

        // rootVolume configures the root EBS volume.
        rootVolume?: {
            sizeGiB:    int & >0
            volumeType: "gp2" | "gp3" | "io1" | "io2" | "st1" | "sc1" | *"gp3"
            encrypted:  bool | *false
            iops?:      int
            throughput?: int
        }

        // networkInterfaces configures network interfaces.
        networkInterfaces?: [...{
            deviceIndex:            int
            associatePublicIp?:     bool
            deleteOnTermination?:   bool | *true
            securityGroupIds?:      [...string]
            subnetId?:              string
        }]

        // blockDeviceMappings configures additional EBS volumes.
        blockDeviceMappings?: [...{
            deviceName: string
            ebs: {
                volumeSize:  int & >0
                volumeType:  "gp2" | "gp3" | "io1" | "io2" | "st1" | "sc1" | *"gp3"
                encrypted:   bool | *false
                iops?:       int
                throughput?: int
                deleteOnTermination?: bool | *true
            }
        }]

        // tags on the launch template resource itself (not instance tags).
        tags: [string]: string

        // instanceTags are tags applied to instances launched from this template.
        instanceTags?: [string]: string
    }

    outputs?: {
        launchTemplateId:     string
        launchTemplateName:   string
        defaultVersionNumber: int
        latestVersionNumber:  int
    }
}
```

### Schema Design Notes

- Most spec fields are optional because launch templates allow partial configuration
  — callers can override any field at launch time.
- `networkInterfaces` and `blockDeviceMappings` are arrays of structs, matching
  the AWS API structure.
- `instanceTags` are separate from `tags` — `tags` apply to the template resource
  itself, `instanceTags` are propagated to instances via `TagSpecifications`.
- `rootVolume` is a convenience abstraction (maps to a block device mapping for
  the root device).

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

Launch template operations are methods on the EC2 SDK client.

---

## Step 3 — Driver Types

**File**: `internal/drivers/launchtemplate/types.go`

```go
package launchtemplate

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "LaunchTemplate"

type LaunchTemplateSpec struct {
    Account              string                  `json:"account,omitempty"`
    Region               string                  `json:"region"`
    TemplateName         string                  `json:"templateName"`
    Description          string                  `json:"description,omitempty"`
    ImageId              string                  `json:"imageId,omitempty"`
    InstanceType         string                  `json:"instanceType,omitempty"`
    KeyName              string                  `json:"keyName,omitempty"`
    SecurityGroupIds     []string                `json:"securityGroupIds,omitempty"`
    SubnetId             string                  `json:"subnetId,omitempty"`
    UserData             string                  `json:"userData,omitempty"`
    IamInstanceProfile   string                  `json:"iamInstanceProfile,omitempty"`
    Monitoring           *bool                   `json:"monitoring,omitempty"`
    RootVolume           *RootVolumeSpec         `json:"rootVolume,omitempty"`
    NetworkInterfaces    []NetworkInterfaceSpec   `json:"networkInterfaces,omitempty"`
    BlockDeviceMappings  []BlockDeviceMappingSpec `json:"blockDeviceMappings,omitempty"`
    Tags                 map[string]string        `json:"tags,omitempty"`
    InstanceTags         map[string]string        `json:"instanceTags,omitempty"`
    ManagedKey           string                  `json:"managedKey,omitempty"`
}

type RootVolumeSpec struct {
    SizeGiB    int32  `json:"sizeGiB"`
    VolumeType string `json:"volumeType"`
    Encrypted  bool   `json:"encrypted"`
    Iops       int32  `json:"iops,omitempty"`
    Throughput int32  `json:"throughput,omitempty"`
}

type NetworkInterfaceSpec struct {
    DeviceIndex         int      `json:"deviceIndex"`
    AssociatePublicIp   *bool    `json:"associatePublicIp,omitempty"`
    DeleteOnTermination *bool    `json:"deleteOnTermination,omitempty"`
    SecurityGroupIds    []string `json:"securityGroupIds,omitempty"`
    SubnetId            string   `json:"subnetId,omitempty"`
}

type BlockDeviceMappingSpec struct {
    DeviceName          string `json:"deviceName"`
    VolumeSize          int32  `json:"volumeSize"`
    VolumeType          string `json:"volumeType"`
    Encrypted           bool   `json:"encrypted"`
    Iops                int32  `json:"iops,omitempty"`
    Throughput          int32  `json:"throughput,omitempty"`
    DeleteOnTermination *bool  `json:"deleteOnTermination,omitempty"`
}

type LaunchTemplateOutputs struct {
    LaunchTemplateId     string `json:"launchTemplateId"`
    LaunchTemplateName   string `json:"launchTemplateName"`
    DefaultVersionNumber int64  `json:"defaultVersionNumber"`
    LatestVersionNumber  int64  `json:"latestVersionNumber"`
}

type ObservedVersionData struct {
    VersionNumber        int64                    `json:"versionNumber"`
    Description          string                   `json:"description,omitempty"`
    ImageId              string                   `json:"imageId,omitempty"`
    InstanceType         string                   `json:"instanceType,omitempty"`
    KeyName              string                   `json:"keyName,omitempty"`
    SecurityGroupIds     []string                 `json:"securityGroupIds,omitempty"`
    UserData             string                   `json:"userData,omitempty"`
    IamInstanceProfile   string                   `json:"iamInstanceProfile,omitempty"`
    Monitoring           bool                     `json:"monitoring"`
    RootVolumeType       string                   `json:"rootVolumeType,omitempty"`
    RootVolumeSizeGiB    int32                    `json:"rootVolumeSizeGiB,omitempty"`
    RootVolumeEncrypted  bool                     `json:"rootVolumeEncrypted"`
    NetworkInterfaces    []NetworkInterfaceSpec    `json:"networkInterfaces,omitempty"`
    BlockDeviceMappings  []BlockDeviceMappingSpec  `json:"blockDeviceMappings,omitempty"`
    InstanceTags         map[string]string         `json:"instanceTags,omitempty"`
}

type ObservedState struct {
    LaunchTemplateId     string              `json:"launchTemplateId"`
    LaunchTemplateName   string              `json:"launchTemplateName"`
    Tags                 map[string]string   `json:"tags"`
    DefaultVersion       ObservedVersionData `json:"defaultVersion"`
    LatestVersionNumber  int64               `json:"latestVersionNumber"`
}

type LaunchTemplateState struct {
    Desired            LaunchTemplateSpec       `json:"desired"`
    Observed           ObservedState            `json:"observed"`
    Outputs            LaunchTemplateOutputs    `json:"outputs"`
    Status             types.ResourceStatus     `json:"status"`
    Mode               types.Mode               `json:"mode"`
    Error              string                   `json:"error,omitempty"`
    Generation         int64                    `json:"generation"`
    LastReconcile      string                   `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                     `json:"reconcileScheduled"`
}
```

### Types Design Notes

- `ObservedState` captures both the template-level metadata (ID, name, tags) and
  the **default version** data. The default version is what matters for drift
  detection — it's what instances will use unless they specify a different version.
- `ObservedVersionData` flattens the `LaunchTemplateVersion` response into
  driver-friendly fields rather than mirroring the nested AWS SDK types.
- `LaunchTemplateSpec.Monitoring` is a `*bool` pointer to distinguish "not set"
  from "false" — launch templates allow partial specs.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/launchtemplate/aws.go`

### LaunchTemplateAPI Interface

```go
type LaunchTemplateAPI interface {
    // CreateLaunchTemplate creates a new launch template with version 1.
    CreateLaunchTemplate(ctx context.Context, spec LaunchTemplateSpec) (
        templateId string, version int64, err error)

    // CreateLaunchTemplateVersion adds a new version and sets it as default.
    CreateLaunchTemplateVersion(ctx context.Context, templateId string, spec LaunchTemplateSpec) (
        version int64, err error)

    // DescribeLaunchTemplate returns the template metadata.
    DescribeLaunchTemplate(ctx context.Context, templateId string) (ObservedState, error)

    // DescribeLaunchTemplateByName returns the template metadata by name.
    DescribeLaunchTemplateByName(ctx context.Context, name string) (ObservedState, error)

    // DeleteLaunchTemplate deletes the template and all versions.
    DeleteLaunchTemplate(ctx context.Context, templateId string) error

    // UpdateTags replaces template-level tags.
    UpdateTags(ctx context.Context, templateId string, tags map[string]string) error
}
```

### Key Implementation Details

#### `CreateLaunchTemplate`

Builds `LaunchTemplateData` from `LaunchTemplateSpec`:

- Maps spec fields to `ec2types.RequestLaunchTemplateData`.
- Sets `TagSpecifications` for both the template resource and instance tags.
- Returns the template ID and version number (always 1 for creation).

```go
func (r *realLaunchTemplateAPI) CreateLaunchTemplate(ctx context.Context, spec LaunchTemplateSpec) (string, int64, error) {
    data := buildLaunchTemplateData(spec)

    tagSpecs := []ec2types.LaunchTemplateTagSpecificationRequest{}
    // Add template-level tags
    if len(spec.Tags) > 0 {
        tagSpecs = append(tagSpecs, ec2types.LaunchTemplateTagSpecificationRequest{
            ResourceType: ec2types.ResourceTypeLaunchTemplate,
            Tags:         toEC2Tags(spec.Tags),
        })
    }
    // Add instance tags
    if len(spec.InstanceTags) > 0 {
        tagSpecs = append(tagSpecs, ec2types.LaunchTemplateTagSpecificationRequest{
            ResourceType: ec2types.ResourceTypeInstance,
            Tags:         toEC2Tags(spec.InstanceTags),
        })
    }

    input := &ec2sdk.CreateLaunchTemplateInput{
        LaunchTemplateName: aws.String(spec.TemplateName),
        LaunchTemplateData: data,
    }
    if spec.Description != "" {
        input.VersionDescription = aws.String(spec.Description)
    }
    if len(tagSpecs) > 0 {
        input.TagSpecifications = /* ... template-level tags ... */
    }

    out, err := r.client.CreateLaunchTemplate(ctx, input)
    if err != nil {
        return "", 0, err
    }
    return aws.ToString(out.LaunchTemplate.LaunchTemplateId),
           aws.ToInt64(out.LaunchTemplate.LatestVersionNumber),
           nil
}
```

#### `CreateLaunchTemplateVersion`

Creates a new version with updated data and immediately sets it as the default
version via `ModifyLaunchTemplate`.

```go
func (r *realLaunchTemplateAPI) CreateLaunchTemplateVersion(ctx context.Context, templateId string, spec LaunchTemplateSpec) (int64, error) {
    data := buildLaunchTemplateData(spec)

    out, err := r.client.CreateLaunchTemplateVersion(ctx, &ec2sdk.CreateLaunchTemplateVersionInput{
        LaunchTemplateId:   aws.String(templateId),
        LaunchTemplateData: data,
        VersionDescription: aws.String(spec.Description),
    })
    if err != nil {
        return 0, err
    }

    newVersion := aws.ToInt64(out.LaunchTemplateVersion.VersionNumber)

    // Set new version as default
    _, err = r.client.ModifyLaunchTemplate(ctx, &ec2sdk.ModifyLaunchTemplateInput{
        LaunchTemplateId:   aws.String(templateId),
        DefaultVersion:     aws.String(fmt.Sprintf("%d", newVersion)),
    })
    if err != nil {
        return newVersion, fmt.Errorf("version %d created but failed to set as default: %w", newVersion, err)
    }

    return newVersion, nil
}
```

#### `DescribeLaunchTemplate`

Combines `DescribeLaunchTemplates` (for metadata + tags) with
`DescribeLaunchTemplateVersions` (for the default version data) into a single
`ObservedState`:

```go
func (r *realLaunchTemplateAPI) DescribeLaunchTemplate(ctx context.Context, templateId string) (ObservedState, error) {
    // 1. Describe template metadata
    descOut, err := r.client.DescribeLaunchTemplates(ctx, &ec2sdk.DescribeLaunchTemplatesInput{
        LaunchTemplateIds: []string{templateId},
    })
    // ...

    // 2. Describe default version data
    verOut, err := r.client.DescribeLaunchTemplateVersions(ctx, &ec2sdk.DescribeLaunchTemplateVersionsInput{
        LaunchTemplateId: aws.String(templateId),
        Versions:         []string{"$Default"},
    })
    // ...

    // 3. Combine into ObservedState
    return ObservedState{
        LaunchTemplateId:    templateId,
        LaunchTemplateName:  aws.ToString(lt.LaunchTemplateName),
        Tags:                extractTags(lt.Tags),
        DefaultVersion:      parseVersionData(verOut.LaunchTemplateVersions[0]),
        LatestVersionNumber: aws.ToInt64(lt.LatestVersionNumber),
    }, nil
}
```

### Error Classification

```go
func IsNotFound(err error) bool {
    // invalidlaunchtemplateName.NotFoundException or
    // InvalidLaunchTemplateId.NotFound
}

func IsDuplicate(err error) bool {
    // InvalidLaunchTemplateName.AlreadyExistsException
}

func IsVersionLimitExceeded(err error) bool {
    // VersionLimitExceeded — 5,000 versions per template
}
```

### Helper: `buildLaunchTemplateData`

Maps `LaunchTemplateSpec` → `ec2types.RequestLaunchTemplateData`:

```go
func buildLaunchTemplateData(spec LaunchTemplateSpec) *ec2types.RequestLaunchTemplateData {
    data := &ec2types.RequestLaunchTemplateData{}

    if spec.ImageId != "" {
        data.ImageId = aws.String(spec.ImageId)
    }
    if spec.InstanceType != "" {
        data.InstanceType = ec2types.InstanceType(spec.InstanceType)
    }
    if spec.KeyName != "" {
        data.KeyName = aws.String(spec.KeyName)
    }
    if len(spec.SecurityGroupIds) > 0 {
        data.SecurityGroupIds = spec.SecurityGroupIds
    }
    if spec.UserData != "" {
        data.UserData = aws.String(spec.UserData)
    }
    if spec.IamInstanceProfile != "" {
        data.IamProfile = &ec2types.LaunchTemplateIamInstanceProfileSpecificationRequest{
            Name: aws.String(spec.IamInstanceProfile),
        }
    }
    if spec.Monitoring != nil && *spec.Monitoring {
        data.Monitoring = &ec2types.LaunchTemplatesMonitoringRequest{
            Enabled: aws.Bool(true),
        }
    }
    // Map rootVolume to block device mapping for root device...
    // Map networkInterfaces...
    // Map blockDeviceMappings...
    // Map instanceTags into TagSpecifications...
    return data
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/launchtemplate/drift.go`

Drift is detected by comparing the desired spec against the **default version**
data in `ObservedState`. Because launch template versions are immutable, drift
means the current default version doesn't match what we want — the fix is to
create a new version.

```go
func HasDrift(desired LaunchTemplateSpec, observed ObservedState) bool {
    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }
    return hasVersionDrift(desired, observed.DefaultVersion)
}

func hasVersionDrift(desired LaunchTemplateSpec, ver ObservedVersionData) bool {
    if desired.ImageId != "" && desired.ImageId != ver.ImageId {
        return true
    }
    if desired.InstanceType != "" && desired.InstanceType != ver.InstanceType {
        return true
    }
    if desired.KeyName != "" && desired.KeyName != ver.KeyName {
        return true
    }
    if !stringSlicesEqual(desired.SecurityGroupIds, ver.SecurityGroupIds) {
        return true
    }
    if desired.UserData != "" && desired.UserData != ver.UserData {
        return true
    }
    if desired.IamInstanceProfile != "" && desired.IamInstanceProfile != ver.IamInstanceProfile {
        return true
    }
    if desired.Monitoring != nil && *desired.Monitoring != ver.Monitoring {
        return true
    }
    // Compare rootVolume, networkInterfaces, blockDeviceMappings...
    if !instanceTagsMatch(desired.InstanceTags, ver.InstanceTags) {
        return true
    }
    return false
}
```

### Drift Correction Strategy

| Drift Type | Correction |
|---|---|
| Template-level tags changed | `UpdateTags` (in-place) |
| Version data changed (AMI, instance type, etc.) | Create new version + set as default |
| Both | Update tags + create new version |

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/launchtemplate/driver.go`

### Provision Handler

```go
func (d *LaunchTemplateDriver) Provision(ctx restate.ObjectContext, spec LaunchTemplateSpec) (LaunchTemplateOutputs, error) {
    // Validate required fields
    // Load existing state from VO
    // Update state.Desired, state.Status, state.Generation

    existingTemplateId := state.Outputs.LaunchTemplateId

    if existingTemplateId == "" {
        // First provision: CreateLaunchTemplate
        templateId, version, err := restate.Run(ctx, func(rc restate.RunContext) (createResult, error) {
            id, ver, err := api.CreateLaunchTemplate(rc, spec)
            if err != nil {
                if IsDuplicate(err) {
                    return createResult{}, restate.TerminalError(err, 409)
                }
                return createResult{}, err
            }
            return createResult{templateId: id, version: ver}, nil
        })
        // Set outputs from created template
    } else {
        // Re-provision: compare desired vs observed default version
        // If version data differs → CreateLaunchTemplateVersion
        // If only tags differ → UpdateTags
        // If nothing changed → no-op (convergent)

        if hasVersionDrift(spec, state.Observed.DefaultVersion) {
            newVersion, err := restate.Run(ctx, func(rc restate.RunContext) (int64, error) {
                ver, err := api.CreateLaunchTemplateVersion(rc, existingTemplateId, spec)
                if err != nil {
                    if IsVersionLimitExceeded(err) {
                        return 0, restate.TerminalError(err, 503)
                    }
                    return 0, err
                }
                return ver, nil
            })
            // Update outputs with new version number
        }

        if !tagsMatch(spec.Tags, state.Observed.Tags) {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateTags(rc, existingTemplateId, spec.Tags)
            })
        }
    }

    // Describe to refresh observed state
    // Save state, schedule reconcile
    return outputs, nil
}
```

### Delete Handler

```go
func (d *LaunchTemplateDriver) Delete(ctx restate.ObjectContext) error {
    // Load state
    // Mode guard: reject ModeObserved (409)
    // DeleteLaunchTemplate (deletes all versions)
    // Not found → already gone, idempotent success
    // Set status = Deleted
}
```

Key points:

- `DeleteLaunchTemplate` deletes the template and all its versions in one call.
- No need to delete versions individually.
- Instances already launched from the template are not affected.

### Import Handler

Defaults to `ModeObserved` — launch templates may be referenced by running ASGs
or launch configs. Destroying a template could prevent ASG scaling events from
launching new instances.

### Reconcile Handler

1. Describe the template and default version.
2. Compare against desired spec.
3. If tags drifted → `UpdateTags` (Managed mode only).
4. If version data drifted → create new version + set default (Managed mode only).
5. If `ModeObserved` → update observed state, report drift, take no action.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/launchtemplate_adapter.go`

```go
func (a *LaunchTemplateAdapter) Kind() string       { return launchtemplate.ServiceName }
func (a *LaunchTemplateAdapter) ServiceName() string { return launchtemplate.ServiceName }
func (a *LaunchTemplateAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *LaunchTemplateAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    // Extract region and metadata.name from CUE-validated document.
    // Return region~metadata.name
}

func (a *LaunchTemplateAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // region~resourceID (resourceID = template name)
    // Same key as BuildKey — matching S3/KeyPair pattern.
}
```

### Plan Logic

Plan reads stored template ID from `GetOutputs`, then describes by ID (stable
identifier). If outputs are empty, plan shows `OpCreate`. If the template exists,
plan compares spec fields against the default version to determine if a new version
is needed.

The plan output for version changes describes it as "update (new version)" rather
than a simple "update" — to make it clear that the existing version is preserved
and a new version is added.

---

## Step 8 — Registry Integration

Add `NewLaunchTemplateAdapterWithRegistry(accounts)` to `NewRegistry()` in
`internal/core/provider/registry.go`.

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go`

Add `.Bind(restate.Reflect(launchtemplateDriver))` alongside EC2 and (future)
KeyPair bindings. Launch templates are compute configuration resources.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes — launch templates join `praxis-compute`. Add
`test-launchtemplate` and `ls-launchtemplate` to the justfile.

---

## Step 11 — Unit Tests

### `internal/drivers/launchtemplate/drift_test.go`

1. `TestHasDrift_NoDrift` — identical spec and observed.
2. `TestHasDrift_ImageIdChanged` — version data drift.
3. `TestHasDrift_InstanceTypeChanged` — version data drift.
4. `TestHasDrift_SecurityGroupsChanged` — version data drift.
5. `TestHasDrift_TagsChanged` — template tag drift.
6. `TestHasDrift_InstanceTagsChanged` — instance tag drift.
7. `TestHasDrift_RootVolumeChanged` — root volume spec drift.
8. `TestHasVersionDrift_PartialSpec` — optional fields properly skipped.
9. `TestComputeFieldDiffs_AllFields` — correct diff entries.

### `internal/drivers/launchtemplate/aws_test.go`

1. `TestIsNotFound_ById` — InvalidLaunchTemplateId.NotFound.
2. `TestIsNotFound_ByName` — invalidlaunchtemplateName.NotFoundException.
3. `TestIsDuplicate` — InvalidLaunchTemplateName.AlreadyExistsException.
4. `TestIsVersionLimitExceeded` — VersionLimitExceeded.
5. `TestBuildLaunchTemplateData_FullSpec` — all fields mapped.
6. `TestBuildLaunchTemplateData_MinimalSpec` — optional fields omitted.

### `internal/drivers/launchtemplate/driver_test.go`

1. `TestServiceName` — returns "LaunchTemplate".
2. `TestProvision_CreateVersion_OnUpdate` — re-provision creates new version.
3. `TestProvision_NoOp_WhenConverged` — no version created when spec matches.
4. `TestOutputsFromObserved` — correct output mapping.

### `internal/core/provider/launchtemplate_adapter_test.go`

1. `TestLaunchTemplateAdapter_BuildKey` — returns `region~templateName`.
2. `TestLaunchTemplateAdapter_BuildImportKey` — same key pattern.
3. `TestLaunchTemplateAdapter_Kind` — returns "LaunchTemplate".
4. `TestLaunchTemplateAdapter_Scope` — returns `KeyScopeRegion`.

---

## Step 12 — Integration Tests

**File**: `tests/integration/launchtemplate_driver_test.go`

1. **TestLaunchTemplateProvision_CreatesTemplate** — Creates a launch template with
   full spec, verifies version 1 is default.
2. **TestLaunchTemplateProvision_UpdateCreatesNewVersion** — Provisions twice with
   different imageId, verifies version 2 is default, version 1 still exists.
3. **TestLaunchTemplateProvision_Idempotent** — Two identical provisions, no new
   version created.
4. **TestLaunchTemplateImport_ExistingTemplate** — Creates via SDK, imports.
5. **TestLaunchTemplateDelete_RemovesTemplate** — Provisions, deletes, verifies gone.
6. **TestLaunchTemplateReconcile_DetectsTagDrift** — External tag change detected
   and corrected (Managed mode).
7. **TestLaunchTemplateReconcile_VersionDrift** — External version data change
   detected (creates corrective version).

---

## Launch-Template-Specific Design Decisions

### 1. Version-Per-Update Model

Each Provision call that changes version-relevant spec fields creates a new launch
template version and sets it as the default. This is the natural AWS model:

- Versions are immutable snapshots; there's no "edit version" API.
- Setting the new version as default means new instance launches use the updated
  configuration automatically.
- Old versions remain available — ASGs or other consumers pinned to a specific
  version number are unaffected until they're updated.

### 2. No Version Cleanup

The driver does not delete old versions. AWS allows up to 5,000 versions per
template. For the unlikely case of hitting this limit, the driver returns
`VersionLimitExceeded` as a terminal 503 error. Manual cleanup of old versions
is a future enhancement (not in scope for this plan).

### 3. Partial Spec Handling

Launch templates allow partial configuration — you can create a template with just
an instance type and no AMI. The drift detection respects this: an empty spec
field (e.g., `ImageId: ""`) means "not specified" and does NOT trigger drift even
if the observed version has that field populated from a previous version or external
modification.

### 4. Import Defaults to ModeObserved

Unlike Key Pairs (which default to ModeManaged), launch templates default to
ModeObserved on import. Launch templates may be referenced by running ASGs — deleting
or modifying the template could prevent ASG scaling events from launching new
instances. The conservative default protects running infrastructure.

### 5. Instance Tags vs Template Tags

Two separate tag fields:

- `Tags`: applied to the launch template AWS resource itself. Mutable via
  `CreateTags` / `DeleteTags` without creating a new version.
- `InstanceTags`: applied to instances launched from the template, embedded in
  `TagSpecifications` within the launch template data. Changing instance tags
  requires a new template version.

### 6. Delete Removes All Versions

`DeleteLaunchTemplate` removes the template and all versions in a single API call.
There is no need to delete versions individually. Instances already launched from
the template continue running unaffected.

### 7. Driver Pack: praxis-compute

Launch templates are EC2 configuration resources. They define instance configuration
and are referenced by EC2 instances and ASGs. They belong in the compute driver pack
alongside EC2 and Key Pair drivers.

---

## Checklist

- [ ] **Schema**: `schemas/aws/ec2/launchtemplate.cue` created
- [ ] **Types**: `internal/drivers/launchtemplate/types.go` created
- [ ] **AWS API**: `internal/drivers/launchtemplate/aws.go` created
- [ ] **Drift**: `internal/drivers/launchtemplate/drift.go` created
- [ ] **Driver**: `internal/drivers/launchtemplate/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/launchtemplate_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: LaunchTemplate driver bound in `cmd/praxis-compute/main.go`
- [ ] **Justfile**: Updated with launchtemplate targets
- [ ] **Unit tests (drift)**: `internal/drivers/launchtemplate/drift_test.go`
- [ ] **Unit tests (aws helpers)**: `internal/drivers/launchtemplate/aws_test.go`
- [ ] **Unit tests (driver)**: `internal/drivers/launchtemplate/driver_test.go`
- [ ] **Unit tests (adapter)**: `internal/core/provider/launchtemplate_adapter_test.go`
- [ ] **Integration tests**: `tests/integration/launchtemplate_driver_test.go`
- [ ] **Version-per-update**: Provision creates new version when spec changes
- [ ] **ModifyLaunchTemplate**: New version set as default after creation
- [ ] **Partial spec**: Empty fields don't trigger drift
- [ ] **Import default mode**: `ModeObserved` (protects ASG references)
- [ ] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/launchtemplate/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestLaunchTemplate -tags=integration`
