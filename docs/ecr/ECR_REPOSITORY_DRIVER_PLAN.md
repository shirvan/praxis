# ECR Repository Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages AWS ECR repositories, following
> the exact patterns established by the S3, Security Group, EC2, VPC, Lambda Function,
> and SNS Topic drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~repositoryName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned repository ARN
> and URI live only in state/outputs.

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
16. [ECR-Repository-Specific Design Decisions](#ecr-repository-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The ECR Repository driver manages the lifecycle of **Amazon Elastic Container Registry
repositories**. It creates, imports, updates, and deletes repositories along with
their image tag mutability, scanning configuration, resource-based access policy,
and tags.

ECR repositories are the primary storage mechanism for container images in AWS.
They are referenced by ECS task definitions (container image URIs) and Lambda
function deployments (container image deployments). The `repositoryUri` output is
the key downstream consumption value.

**Out of scope**: Lifecycle policies (separate driver), image replication
configuration, pull-through cache rules, registry scanning configuration, registry
policies (account-scoped). Each operates as a distinct resource type with its own
lifecycle.

### Resource Scope for This Plan

| In Scope | Out of Scope (Separate Drivers / Future) |
|---|---|
| Repository creation | Lifecycle policies |
| Image tag mutability | Pull-through cache rules |
| Image scanning configuration | Registry replication configuration |
| Resource-based repository policy | Registry-level scanning configuration |
| Encryption configuration (immutable) | Registry policies |
| Tags | Image management (push/pull) |
| Import and drift detection | |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a repository |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing repository |
| `Delete` | `ObjectContext` (exclusive) | Delete a repository |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return repository outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | API | Notes |
|---|---|---|---|
| `repositoryName` | Immutable | — | Part of Virtual Object key; cannot be changed |
| `encryptionConfiguration` | Immutable | — | Set at creation; cannot be changed. Changing it causes a terminal error |
| `imageTagMutability` | Mutable | `PutImageTagMutability` | `MUTABLE` or `IMMUTABLE` |
| `imageScanningConfiguration.scanOnPush` | Mutable | `PutImageScanningConfiguration` | Automatic vulnerability scanning |
| `repositoryPolicy` | Mutable | `SetRepositoryPolicy` / `DeleteRepositoryPolicy` | JSON resource policy document |
| `tags` | Mutable | `TagResource` / `UntagResource` | Key-value pairs |

### Downstream Consumers

```text
${resources.my-repo.outputs.repositoryUri}    → ECS Task Definition container image URIs
${resources.my-repo.outputs.repositoryUri}    → Lambda Function spec.code.imageUri
${resources.my-repo.outputs.repositoryArn}    → IAM Policy resource statements
${resources.my-repo.outputs.repositoryName}   → ECR Lifecycle Policy spec.repositoryName
${resources.my-repo.outputs.registryId}       → Cross-account pull configuration
```

---

## 2. Key Strategy

### Key Format: `region~repositoryName`

ECR repository names are unique within an account and region. The CUE schema maps
`metadata.name` to the repository name. The adapter produces `region~metadata.name`
as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain a `repositoryArn`,
   describes the repository by name. Otherwise, `OpCreate`.
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the repository name. Matches the Lambda function pattern.

### Tag-Based Ownership

Repository names are unique within an account+region, providing conflict detection
via `CreateRepository` (which returns `RepositoryAlreadyExistsException` if the
repository exists). The driver additionally tags repositories with
`praxis:managed-key=<region~repositoryName>` for cross-installation conflict
detection and `FindByManagedKey` lookups during import.

### Import Semantics

Import and template-based management produce the **same Virtual Object key**:

- `praxis import --kind ECRRepository --region us-east-1 --resource-id my-app`:
  Creates VO key `us-east-1~my-app`.
- Template with `metadata.name: my-app` in `us-east-1`:
  Creates VO key `us-east-1~my-app`.

Both target the same Virtual Object, analogous to the Lambda function and Key Pair
import patterns.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ schemas/aws/ecr/repository.cue                           — CUE schema for ECRRepository
✦ internal/drivers/ecrrepo/types.go                        — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/ecrrepo/aws.go                          — RepositoryAPI interface + realRepositoryAPI impl
✦ internal/drivers/ecrrepo/drift.go                        — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/ecrrepo/driver.go                       — ECRRepositoryDriver Virtual Object
✦ internal/drivers/ecrrepo/driver_test.go                  — Unit tests for driver (mocked AWS)
✦ internal/drivers/ecrrepo/aws_test.go                     — Unit tests for error classification
✦ internal/drivers/ecrrepo/drift_test.go                   — Unit tests for drift detection
✦ internal/core/provider/ecrrepository_adapter.go          — ECRRepositoryAdapter implementing provider.Adapter
✦ internal/core/provider/ecrrepository_adapter_test.go     — Adapter unit tests
✦ tests/integration/ecr_repository_driver_test.go          — Integration tests (Testcontainers + LocalStack)
✎ internal/infra/awsclient/client.go                       — Add NewECRClient() factory
✎ cmd/praxis-compute/main.go                               — Bind ECRRepository driver
✎ internal/core/provider/registry.go                       — Add NewECRRepositoryAdapter() to NewRegistry()
✎ docker-compose.yaml                                      — Add ecr to LocalStack SERVICES
✎ justfile                                                 — Add ECR build/test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ecr/repository.cue`

```cue
package ecr

#ECRRepository: {
    apiVersion: "praxis.io/v1"
    kind:       "ECRRepository"

    metadata: {
        // name is the ECR repository name in AWS.
        // Must be 2-256 characters: lowercase letters, digits, hyphens, underscores, forward slashes, dots.
        // Use slashes to create namespaced repositories (e.g., "team/my-service").
        name: string & =~"^[a-z0-9][a-z0-9/_.-]{1,255}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region where the repository is created.
        region: string

        // imageTagMutability controls whether image tags can be overwritten.
        // MUTABLE allows multiple image pushes with the same tag (overwrites the previous).
        // IMMUTABLE prevents overwriting once a tag has been pushed.
        // Defaults to MUTABLE.
        imageTagMutability: "MUTABLE" | "IMMUTABLE" | *"MUTABLE"

        // imageScanningConfiguration controls automatic vulnerability scanning.
        imageScanningConfiguration?: {
            // scanOnPush enables automatic scanning each time an image is pushed.
            scanOnPush: bool | *false
        }

        // encryptionConfiguration controls server-side encryption for repository images.
        // IMMUTABLE after repository creation — changes require delete and recreate.
        encryptionConfiguration?: {
            // encryptionType is AES256 (default) or KMS.
            // AES256 uses server-side encryption managed by ECR.
            // KMS uses a customer-managed key.
            encryptionType: "AES256" | "KMS" | *"AES256"

            // kmsKey is the ARN, key ID, or alias of the KMS CMK.
            // Required when encryptionType is KMS. Must be in the same region.
            kmsKey?: string
        }

        // repositoryPolicy is a JSON-encoded IAM resource-based policy document.
        // Controls cross-account and cross-service access to this repository.
        // If omitted, no explicit policy is set (implicit owner account access only).
        repositoryPolicy?: string

        // forceDelete allows deleting a repository that still contains images.
        // Defaults to false to prevent accidental data loss.
        // When false, Delete will fail if the repository has images.
        forceDelete: bool | *false

        // tags applied to the repository.
        tags: [string]: string
    }

    outputs?: {
        // repositoryArn is the full ARN of the repository.
        repositoryArn: string
        // repositoryName is the repository name (same as metadata.name).
        repositoryName: string
        // repositoryUri is the URI used to push and pull images.
        // Format: <registryId>.dkr.ecr.<region>.amazonaws.com/<repositoryName>
        repositoryUri: string
        // registryId is the AWS account ID of the registry (12-digit account ID).
        registryId: string
    }
}
```

### Schema Design Notes

- **`name` regex**: ECR repository names support lowercase alphanumeric characters,
  hyphens, underscores, forward slashes (namespacing), and dots. The regex enforces
  length (2–256) and character constraints.
- **`encryptionConfiguration` as an optional nested object**: Omitting it uses ECR's
  default AES256 encryption. Providing it allows opting into KMS.
- **`repositoryPolicy` as a JSON string**: Follows the SNS Topic pattern for
  resource-based policies. The JSON string is passed directly to `SetRepositoryPolicy`.
- **`forceDelete` explicit default**: Defaulting to `false` prevents operators from
  accidentally wiping repositories with pushed images during stack teardown.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **ADD ECR CLIENT FACTORY**

```go
func NewECRClient(cfg aws.Config) *ecr.Client {
    return ecr.NewFromConfig(cfg)
}
```

This requires adding `github.com/aws/aws-sdk-go-v2/service/ecr` to `go.mod`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/ecrrepo/types.go`

```go
package ecrrepo

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "ECRRepository"

// ImageScanningConfiguration mirrors the ECR scanning config sub-object.
type ImageScanningConfiguration struct {
    ScanOnPush bool `json:"scanOnPush"`
}

// EncryptionConfiguration mirrors the ECR encryption config sub-object.
// Immutable after repository creation.
type EncryptionConfiguration struct {
    EncryptionType string `json:"encryptionType"`           // "AES256" or "KMS"
    KmsKey         string `json:"kmsKey,omitempty"`
}

// ECRRepositorySpec is the desired state for an ECR repository.
type ECRRepositorySpec struct {
    Account                    string                      `json:"account,omitempty"`
    Region                     string                      `json:"region"`
    ImageTagMutability         string                      `json:"imageTagMutability"`
    ImageScanningConfiguration *ImageScanningConfiguration `json:"imageScanningConfiguration,omitempty"`
    EncryptionConfiguration    *EncryptionConfiguration    `json:"encryptionConfiguration,omitempty"`
    RepositoryPolicy           string                      `json:"repositoryPolicy,omitempty"`
    ForceDelete                bool                        `json:"forceDelete"`
    Tags                       map[string]string           `json:"tags,omitempty"`
    ManagedKey                 string                      `json:"managedKey,omitempty"`
}

// ECRRepositoryOutputs is produced after provisioning and stored in Restate K/V.
type ECRRepositoryOutputs struct {
    RepositoryArn  string `json:"repositoryArn"`
    RepositoryName string `json:"repositoryName"`
    RepositoryUri  string `json:"repositoryUri"`
    RegistryId     string `json:"registryId"`
}

// ObservedState captures the actual configuration of the repository from AWS.
type ObservedState struct {
    RepositoryArn              string                      `json:"repositoryArn"`
    RepositoryName             string                      `json:"repositoryName"`
    RepositoryUri              string                      `json:"repositoryUri"`
    RegistryId                 string                      `json:"registryId"`
    ImageTagMutability         string                      `json:"imageTagMutability"`
    ImageScanningConfiguration *ImageScanningConfiguration `json:"imageScanningConfiguration,omitempty"`
    EncryptionConfiguration    *EncryptionConfiguration    `json:"encryptionConfiguration,omitempty"`
    RepositoryPolicy           string                      `json:"repositoryPolicy,omitempty"`
    Tags                       map[string]string           `json:"tags"`
}

// ECRRepositoryState is the single atomic state object stored under drivers.StateKey.
type ECRRepositoryState struct {
    Desired            ECRRepositorySpec    `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            ECRRepositoryOutputs `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Why These Fields

- **`ForceDelete` in Spec**: The driver passes this flag directly to the ECR
  `DeleteRepository` API. Exposing it in the spec lets operators opt into forced
  deletion per-resource, matching ECR's own API semantics.
- **`RepositoryUri` in ObservedState and Outputs**: The URI (`<registryId>.dkr.ecr.<region>.amazonaws.com/<name>`)
  is the primary downstream consumption value. It is returned by `DescribeRepositories`
  and stored in both observed state and outputs.
- **`EncryptionConfiguration` preserved in ObservedState**: Stored for drift detection
  (to report immutability violations) and for informational visibility.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/ecrrepo/aws.go`

### RepositoryAPI Interface

```go
type RepositoryAPI interface {
    // CreateRepository creates a new ECR repository with the given spec.
    // Returns the repository outputs (ARN, URI, etc.) on success.
    // Returns RepositoryAlreadyExistsException if a repository with the same name exists.
    CreateRepository(ctx context.Context, repositoryName string, spec ECRRepositorySpec) (ECRRepositoryOutputs, error)

    // DescribeRepository returns the current observed state of a repository.
    // Returns ResourceNotFoundException if the repository does not exist.
    DescribeRepository(ctx context.Context, repositoryName string) (ObservedState, error)

    // PutImageTagMutability updates the tag mutability setting of a repository.
    PutImageTagMutability(ctx context.Context, registryId, repositoryName, mutability string) error

    // PutImageScanningConfiguration updates the image scanning setting.
    PutImageScanningConfiguration(ctx context.Context, registryId, repositoryName string, scanOnPush bool) error

    // SetRepositoryPolicy applies a resource-based policy to the repository.
    SetRepositoryPolicy(ctx context.Context, registryId, repositoryName, policyText string) error

    // GetRepositoryPolicy returns the current repository policy, or empty string if none exists.
    GetRepositoryPolicy(ctx context.Context, registryId, repositoryName string) (string, error)

    // DeleteRepositoryPolicy removes the repository policy.
    DeleteRepositoryPolicy(ctx context.Context, registryId, repositoryName string) error

    // DeleteRepository deletes the repository. If force is true, images are deleted first.
    DeleteRepository(ctx context.Context, registryId, repositoryName string, force bool) error

    // UpdateTags performs a full tag replacement on the repository.
    // Removes all existing tags not in the desired set and adds/updates desired tags.
    UpdateTags(ctx context.Context, repositoryArn string, desired map[string]string) error

    // FindByManagedKey looks up a repository tagged with praxis:managed-key=managedKey.
    // Returns the repository name, or empty string if not found.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### realRepositoryAPI Implementation

```go
type realRepositoryAPI struct {
    client  *ecr.Client
    limiter *ratelimit.Limiter
}

func NewRepositoryAPI(client *ecr.Client) RepositoryAPI {
    return &realRepositoryAPI{
        client:  client,
        limiter: ratelimit.New("ecr-repository", 10, 5),
    }
}
```

### Error Classification

```go
func classifyError(err error) error {
    if err == nil {
        return nil
    }
    var alreadyExists *ecrtypes.RepositoryAlreadyExistsException
    if errors.As(err, &alreadyExists) {
        return restate.TerminalError(fmt.Errorf("repository already exists: %w", err), 409)
    }
    var notFound *ecrtypes.RepositoryNotFoundException
    if errors.As(err, &notFound) {
        return restate.TerminalError(fmt.Errorf("repository not found: %w", err), 404)
    }
    var invalidParam *ecrtypes.InvalidParameterException
    if errors.As(err, &invalidParam) {
        return restate.TerminalError(fmt.Errorf("invalid parameter: %w", err), 400)
    }
    var repoNotEmpty *ecrtypes.RepositoryNotEmptyException
    if errors.As(err, &repoNotEmpty) {
        return restate.TerminalError(fmt.Errorf("repository not empty — set forceDelete: true to delete a repository containing images: %w", err), 409)
    }
    var limitExceeded *ecrtypes.LimitExceededException
    if errors.As(err, &limitExceeded) {
        // Retryable
        return fmt.Errorf("ECR limit exceeded (retryable): %w", err)
    }
    var serverErr *ecrtypes.ServerException
    if errors.As(err, &serverErr) {
        // Retryable
        return fmt.Errorf("ECR server error (retryable): %w", err)
    }
    return err
}
```

### Key Implementation Details

#### `CreateRepository`

```go
func (r *realRepositoryAPI) CreateRepository(ctx context.Context, repositoryName string, spec ECRRepositorySpec) (ECRRepositoryOutputs, error) {
    input := &ecr.CreateRepositoryInput{
        RepositoryName:     aws.String(repositoryName),
        ImageTagMutability: ecrtypes.ImageTagMutability(spec.ImageTagMutability),
        Tags: []ecrtypes.Tag{
            {Key: aws.String("praxis:managed-key"), Value: aws.String(spec.ManagedKey)},
        },
    }

    if spec.ImageScanningConfiguration != nil {
        input.ImageScanningConfiguration = &ecrtypes.ImageScanningConfiguration{
            ScanOnPush: spec.ImageScanningConfiguration.ScanOnPush,
        }
    }

    if spec.EncryptionConfiguration != nil {
        input.EncryptionConfiguration = &ecrtypes.EncryptionConfiguration{
            EncryptionType: ecrtypes.EncryptionType(spec.EncryptionConfiguration.EncryptionType),
        }
        if spec.EncryptionConfiguration.KmsKey != "" {
            input.EncryptionConfiguration.KmsKey = aws.String(spec.EncryptionConfiguration.KmsKey)
        }
    }

    for k, v := range spec.Tags {
        input.Tags = append(input.Tags, ecrtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
    }

    out, err := r.client.CreateRepository(ctx, input)
    if err != nil {
        return ECRRepositoryOutputs{}, classifyError(err)
    }

    repo := out.Repository
    return ECRRepositoryOutputs{
        RepositoryArn:  aws.ToString(repo.RepositoryArn),
        RepositoryName: aws.ToString(repo.RepositoryName),
        RepositoryUri:  aws.ToString(repo.RepositoryUri),
        RegistryId:     aws.ToString(repo.RegistryId),
    }, nil
}
```

#### `DescribeRepository`

```go
func (r *realRepositoryAPI) DescribeRepository(ctx context.Context, repositoryName string) (ObservedState, error) {
    out, err := r.client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
        RepositoryNames: []string{repositoryName},
    })
    if err != nil {
        return ObservedState{}, classifyError(err)
    }
    if len(out.Repositories) == 0 {
        return ObservedState{}, restate.TerminalError(fmt.Errorf("repository %q not found", repositoryName), 404)
    }

    repo := out.Repositories[0]
    obs := ObservedState{
        RepositoryArn:      aws.ToString(repo.RepositoryArn),
        RepositoryName:     aws.ToString(repo.RepositoryName),
        RepositoryUri:      aws.ToString(repo.RepositoryUri),
        RegistryId:         aws.ToString(repo.RegistryId),
        ImageTagMutability: string(repo.ImageTagMutability),
    }

    if repo.ImageScanningConfiguration != nil {
        obs.ImageScanningConfiguration = &ImageScanningConfiguration{
            ScanOnPush: repo.ImageScanningConfiguration.ScanOnPush,
        }
    }

    if repo.EncryptionConfiguration != nil {
        obs.EncryptionConfiguration = &EncryptionConfiguration{
            EncryptionType: string(repo.EncryptionConfiguration.EncryptionType),
            KmsKey:         aws.ToString(repo.EncryptionConfiguration.KmsKey),
        }
    }

    // Fetch tags
    tagsOut, err := r.client.ListTagsForResource(ctx, &ecr.ListTagsForResourceInput{
        ResourceArn: repo.RepositoryArn,
    })
    if err != nil {
        return ObservedState{}, classifyError(err)
    }
    obs.Tags = make(map[string]string)
    for _, t := range tagsOut.Tags {
        obs.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
    }

    // Fetch policy (may not exist)
    policyOut, err := r.GetRepositoryPolicy(ctx, obs.RegistryId, obs.RepositoryName)
    if err == nil {
        obs.RepositoryPolicy = policyOut
    }

    return obs, nil
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/ecrrepo/drift.go`

```go
package ecrrepo

import (
    "encoding/json"
    "fmt"

    restate "github.com/restatedev/sdk-go"
    "github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if the desired spec differs from the observed state.
// Encryption configuration changes are treated as terminal errors (immutable).
func HasDrift(desired ECRRepositorySpec, observed ObservedState) (bool, error) {
    if err := checkImmutableFields(desired, observed); err != nil {
        return false, err
    }
    diffs := ComputeFieldDiffs(desired, observed)
    return len(diffs) > 0, nil
}

// checkImmutableFields returns a terminal error if any immutable field has changed.
func checkImmutableFields(desired ECRRepositorySpec, observed ObservedState) error {
    desiredEnc := normalizeEncryption(desired.EncryptionConfiguration)
    observedEnc := normalizeEncryption(observed.EncryptionConfiguration)
    if desiredEnc != observedEnc {
        return restate.TerminalError(fmt.Errorf(
            "encryptionConfiguration is immutable: cannot change from %q to %q — delete and recreate the repository",
            observedEnc, desiredEnc,
        ), 400)
    }
    return nil
}

// normalizeEncryption returns a canonical string for comparison.
func normalizeEncryption(enc *EncryptionConfiguration) string {
    if enc == nil {
        return "AES256:"
    }
    return enc.EncryptionType + ":" + enc.KmsKey
}

// FieldDiffEntry describes a single detected drift field.
type FieldDiffEntry struct {
    Field   string
    Desired string
    Actual  string
}

// ComputeFieldDiffs returns a list of field-level drift entries.
func ComputeFieldDiffs(desired ECRRepositorySpec, observed ObservedState) []FieldDiffEntry {
    var diffs []FieldDiffEntry

    // imageTagMutability
    if desired.ImageTagMutability != observed.ImageTagMutability {
        diffs = append(diffs, FieldDiffEntry{
            Field:   "imageTagMutability",
            Desired: desired.ImageTagMutability,
            Actual:  observed.ImageTagMutability,
        })
    }

    // imageScanningConfiguration
    desiredScan := false
    if desired.ImageScanningConfiguration != nil {
        desiredScan = desired.ImageScanningConfiguration.ScanOnPush
    }
    observedScan := false
    if observed.ImageScanningConfiguration != nil {
        observedScan = observed.ImageScanningConfiguration.ScanOnPush
    }
    if desiredScan != observedScan {
        diffs = append(diffs, FieldDiffEntry{
            Field:   "imageScanningConfiguration.scanOnPush",
            Desired: fmt.Sprintf("%v", desiredScan),
            Actual:  fmt.Sprintf("%v", observedScan),
        })
    }

    // repositoryPolicy — JSON semantic equality
    if !jsonEqual(desired.RepositoryPolicy, observed.RepositoryPolicy) {
        diffs = append(diffs, FieldDiffEntry{
            Field:   "repositoryPolicy",
            Desired: desired.RepositoryPolicy,
            Actual:  observed.RepositoryPolicy,
        })
    }

    // tags — full map equality (excluding praxis: managed keys)
    desiredNorm := drivers.FilterManagedTags(desired.Tags)
    observedNorm := drivers.FilterManagedTags(observed.Tags)
    if !drivers.TagsEqual(desiredNorm, observedNorm) {
        diffs = append(diffs, FieldDiffEntry{
            Field:   "tags",
            Desired: fmt.Sprintf("%v", desiredNorm),
            Actual:  fmt.Sprintf("%v", observedNorm),
        })
    }

    return diffs
}

// jsonEqual performs a semantic JSON equality check by unmarshaling both strings.
// Empty string is equivalent to {}.
func jsonEqual(a, b string) bool {
    if a == b {
        return true
    }
    normalize := func(s string) interface{} {
        if s == "" {
            return nil
        }
        var v interface{}
        if err := json.Unmarshal([]byte(s), &v); err != nil {
            return s // fall back to string comparison
        }
        return v
    }
    na, nb := normalize(a), normalize(b)
    ab, _ := json.Marshal(na)
    bb, _ := json.Marshal(nb)
    return string(ab) == string(bb)
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/ecrrepo/driver.go`

### Constructor

```go
type ECRRepositoryDriver struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) RepositoryAPI
}

func NewECRRepositoryDriver(accounts *auth.Registry) *ECRRepositoryDriver {
    return NewECRRepositoryDriverWithFactory(accounts, func(cfg aws.Config) RepositoryAPI {
        return NewRepositoryAPI(awsclient.NewECRClient(cfg))
    })
}

func NewECRRepositoryDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) RepositoryAPI) *ECRRepositoryDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) RepositoryAPI {
            return NewRepositoryAPI(awsclient.NewECRClient(cfg))
        }
    }
    return &ECRRepositoryDriver{auth: accounts, apiFactory: factory}
}

func (ECRRepositoryDriver) ServiceName() string { return ServiceName }

func (d *ECRRepositoryDriver) apiForAccount(account string) (RepositoryAPI, error) {
    if d == nil || d.auth == nil || d.apiFactory == nil {
        return nil, fmt.Errorf("ECRRepositoryDriver is not configured with an auth registry")
    }
    awsCfg, err := d.auth.Resolve(account)
    if err != nil {
        return nil, fmt.Errorf("resolve ECR account %q: %w", account, err)
    }
    return d.apiFactory(awsCfg), nil
}
```

### Provision Handler

```go
func (d *ECRRepositoryDriver) Provision(ctx restate.ObjectContext, spec ECRRepositorySpec) (ECRRepositoryOutputs, error) {
    key := restate.Key(ctx)
    parts := strings.SplitN(key, "~", 2)
    repositoryName := parts[1]

    spec.ManagedKey = key
    restate.Set(ctx, drivers.StateKey, ECRRepositoryState{
        Desired: spec,
        Status:  types.StatusProvisioning,
    })

    // Check existing state
    existing, err := restate.Get[*ECRRepositoryState](ctx, drivers.StateKey)
    if err != nil {
        return ECRRepositoryOutputs{}, err
    }

    api := d.apiFactory(spec.Region)

    // Try to describe existing repository
    observed, descErr := restate.Run(ctx, func(ctx restate.RunContext) (ObservedState, error) {
        return api.DescribeRepository(ctx, repositoryName)
    })

    if descErr != nil {
        // Check if it's a not-found (terminal 404 = needs creation)
        var te *restate.TerminalError
        if !errors.As(descErr, &te) || te.Code() != 404 {
            return ECRRepositoryOutputs{}, descErr
        }

        // Repository does not exist — create it
        outputs, createErr := restate.Run(ctx, func(ctx restate.RunContext) (ECRRepositoryOutputs, error) {
            return api.CreateRepository(ctx, repositoryName, spec)
        })
        if createErr != nil {
            return ECRRepositoryOutputs{}, createErr
        }

        // Apply repository policy if set
        if spec.RepositoryPolicy != "" {
            if _, err := restate.Run(ctx, func(ctx restate.RunContext) (struct{}, error) {
                return struct{}{}, api.SetRepositoryPolicy(ctx, outputs.RegistryId, repositoryName, spec.RepositoryPolicy)
            }); err != nil {
                return ECRRepositoryOutputs{}, err
            }
        }

        restate.Set(ctx, drivers.StateKey, ECRRepositoryState{
            Desired: spec,
            Outputs: outputs,
            Status:  types.StatusReady,
        })
        return outputs, nil
    }

    // Repository exists — check for drift and convergence
    _ = existing // silence unused warning; state loaded above

    hasDrift, driftErr := HasDrift(spec, observed)
    if driftErr != nil {
        return ECRRepositoryOutputs{}, driftErr
    }

    if !hasDrift {
        // No drift — idempotent success
        outputs := ECRRepositoryOutputs{
            RepositoryArn:  observed.RepositoryArn,
            RepositoryName: observed.RepositoryName,
            RepositoryUri:  observed.RepositoryUri,
            RegistryId:     observed.RegistryId,
        }
        restate.Set(ctx, drivers.StateKey, ECRRepositoryState{
            Desired:  spec,
            Observed: observed,
            Outputs:  outputs,
            Status:   types.StatusReady,
        })
        return outputs, nil
    }

    // Apply convergence updates
    diffs := ComputeFieldDiffs(spec, observed)
    for _, diff := range diffs {
        switch diff.Field {
        case "imageTagMutability":
            if _, err := restate.Run(ctx, func(ctx restate.RunContext) (struct{}, error) {
                return struct{}{}, api.PutImageTagMutability(ctx, observed.RegistryId, repositoryName, spec.ImageTagMutability)
            }); err != nil {
                return ECRRepositoryOutputs{}, err
            }

        case "imageScanningConfiguration.scanOnPush":
            scanOnPush := spec.ImageScanningConfiguration != nil && spec.ImageScanningConfiguration.ScanOnPush
            if _, err := restate.Run(ctx, func(ctx restate.RunContext) (struct{}, error) {
                return struct{}{}, api.PutImageScanningConfiguration(ctx, observed.RegistryId, repositoryName, scanOnPush)
            }); err != nil {
                return ECRRepositoryOutputs{}, err
            }

        case "repositoryPolicy":
            if spec.RepositoryPolicy != "" {
                if _, err := restate.Run(ctx, func(ctx restate.RunContext) (struct{}, error) {
                    return struct{}{}, api.SetRepositoryPolicy(ctx, observed.RegistryId, repositoryName, spec.RepositoryPolicy)
                }); err != nil {
                    return ECRRepositoryOutputs{}, err
                }
            } else {
                if _, err := restate.Run(ctx, func(ctx restate.RunContext) (struct{}, error) {
                    return struct{}{}, api.DeleteRepositoryPolicy(ctx, observed.RegistryId, repositoryName)
                }); err != nil {
                    return ECRRepositoryOutputs{}, err
                }
            }

        case "tags":
            if _, err := restate.Run(ctx, func(ctx restate.RunContext) (struct{}, error) {
                return struct{}{}, api.UpdateTags(ctx, observed.RepositoryArn, spec.Tags)
            }); err != nil {
                return ECRRepositoryOutputs{}, err
            }
        }
    }

    outputs := ECRRepositoryOutputs{
        RepositoryArn:  observed.RepositoryArn,
        RepositoryName: observed.RepositoryName,
        RepositoryUri:  observed.RepositoryUri,
        RegistryId:     observed.RegistryId,
    }
    restate.Set(ctx, drivers.StateKey, ECRRepositoryState{
        Desired: spec,
        Outputs: outputs,
        Status:  types.StatusReady,
    })
    return outputs, nil
}
```

### Delete Handler

```go
func (d *ECRRepositoryDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*ECRRepositoryState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state == nil {
        return nil // already deleted
    }

    spec := state.Desired
    repositoryName := state.Outputs.RepositoryName
    registryId := state.Outputs.RegistryId
    api := d.apiFactory(spec.Region)

    if _, err := restate.Run(ctx, func(ctx restate.RunContext) (struct{}, error) {
        return struct{}{}, api.DeleteRepository(ctx, registryId, repositoryName, spec.ForceDelete)
    }); err != nil {
        return err
    }

    restate.Clear(ctx, drivers.StateKey)
    return nil
}
```

### Import Handler

```go
func (d *ECRRepositoryDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ECRRepositoryOutputs, error) {
    key := restate.Key(ctx)
    parts := strings.SplitN(key, "~", 2)
    region := parts[0]
    repositoryName := parts[1]

    api := d.apiFactory(region)

    observed, err := restate.Run(ctx, func(ctx restate.RunContext) (ObservedState, error) {
        return api.DescribeRepository(ctx, repositoryName)
    })
    if err != nil {
        return ECRRepositoryOutputs{}, err
    }

    outputs := ECRRepositoryOutputs{
        RepositoryArn:  observed.RepositoryArn,
        RepositoryName: observed.RepositoryName,
        RepositoryUri:  observed.RepositoryUri,
        RegistryId:     observed.RegistryId,
    }

    importedSpec := ECRRepositorySpec{
        Region:                     region,
        ImageTagMutability:         observed.ImageTagMutability,
        ImageScanningConfiguration: observed.ImageScanningConfiguration,
        EncryptionConfiguration:    observed.EncryptionConfiguration,
        RepositoryPolicy:           observed.RepositoryPolicy,
        Tags:                       observed.Tags,
        ManagedKey:                 key,
    }

    restate.Set(ctx, drivers.StateKey, ECRRepositoryState{
        Desired:  importedSpec,
        Observed: observed,
        Outputs:  outputs,
        Status:   types.StatusReady,
        Mode:     types.ModeManaged,
    })

    return outputs, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/ecrrepository_adapter.go`

```go
package provider

import (
    "encoding/json"
    "fmt"

    "github.com/aws/aws-sdk-go-v2/aws"
    restate "github.com/restatedev/sdk-go"

    "github.com/shirvan/praxis/internal/core/auth"
    "github.com/shirvan/praxis/internal/drivers/ecrrepo"
    "github.com/shirvan/praxis/internal/infra/awsclient"
    "github.com/shirvan/praxis/pkg/types"
)

const ecrRepositoryKind = "ECRRepository"

type ECRRepositoryAdapter struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) ecrrepo.RepositoryAPI
}

func NewECRRepositoryAdapterWithRegistry(accounts *auth.Registry) *ECRRepositoryAdapter {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    return &ECRRepositoryAdapter{
        auth: accounts,
        apiFactory: func(cfg aws.Config) ecrrepo.RepositoryAPI {
            return ecrrepo.NewRepositoryAPI(awsclient.NewECRClient(cfg))
        },
    }
}

func (a *ECRRepositoryAdapter) Kind() string        { return ecrRepositoryKind }
func (a *ECRRepositoryAdapter) ServiceName() string { return ecrrepo.ServiceName }
func (a *ECRRepositoryAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *ECRRepositoryAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    var doc struct {
        Metadata struct{ Name string `json:"name"` } `json:"metadata"`
        Spec     struct{ Region string `json:"region"` } `json:"spec"`
    }
    if err := json.Unmarshal(resourceDoc, &doc); err != nil {
        return "", fmt.Errorf("ECRRepositoryAdapter.BuildKey: %w", err)
    }
    return JoinKey(doc.Spec.Region, doc.Metadata.Name), nil
}

func (a *ECRRepositoryAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return JoinKey(region, resourceID), nil
}

func (a *ECRRepositoryAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
    return decodeSpec[ecrrepo.ECRRepositorySpec](resourceDoc)
}

// Provision, Delete, NormalizeOutputs, Plan, Import follow the standard adapter
// pattern — see S3Adapter for the full implementation reference.
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go`

```go
func NewRegistry() *Registry {
    accounts := auth.LoadFromEnv()
    return NewRegistryWithAdapters(
        // ... existing adapters ...
        NewECRRepositoryAdapterWithRegistry(accounts),
    )
}
```

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go`

```go
import (
    // ... existing imports ...
    ecrrepo "github.com/shirvan/praxis/internal/drivers/ecrrepo"
)

srv := server.NewRestate().
    // ... existing bindings ...
    Bind(restate.Reflect(ecrrepo.NewECRRepositoryDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### docker-compose.yaml

Add `ecr` to LocalStack SERVICES:

```yaml
environment:
  SERVICES: s3,ec2,iam,lambda,ecr,...
```

### justfile

```makefile
test-ecr-repository:
    go test ./internal/drivers/ecrrepo/... -v -timeout 120s

test-ecr-integration:
    go test ./tests/integration/ -run TestECRRepository -v -timeout 300s
```

---

## Step 11 — Unit Tests

### driver_test.go — Key Test Cases

```text
TestProvision_CreateNew              — creates a repository that does not exist
TestProvision_IdempotentNoDrift      — no-op when repository matches spec exactly
TestProvision_ConvergeTagMutability  — updates imageTagMutability on drift
TestProvision_ConvergeScanOnPush     — updates scanning configuration on drift
TestProvision_ConvergePolicy         — updates repository policy on drift
TestProvision_RemovePolicy           — deletes policy when desired policy is empty
TestProvision_ConvergeTags           — updates tags on drift
TestProvision_EncryptionChangeError  — terminal error if encryptionConfiguration changes
TestDelete_Existing                  — deletes a provisioned repository
TestDelete_NotFound                  — no-op when already deleted
TestDelete_ForceDelete               — passes force=true when ForceDelete=true
TestDelete_NotEmptyWithoutForce      — terminal error when repo has images and !forceDelete
TestImport_ExistingRepository        — imports an existing repository
TestImport_NotFound                  — terminal error when repository does not exist
TestGetStatus_Ready                  — returns Ready status
TestGetOutputs_FullOutputs           — returns all output fields
```

### aws_test.go — Error Classification

```text
TestClassifyError_AlreadyExists      — RepositoryAlreadyExistsException → terminal 409
TestClassifyError_NotFound           — RepositoryNotFoundException → terminal 404
TestClassifyError_InvalidParam       — InvalidParameterException → terminal 400
TestClassifyError_NotEmpty           — RepositoryNotEmptyException → terminal 409
TestClassifyError_LimitExceeded      — LimitExceededException → retryable
TestClassifyError_ServerException    — ServerException → retryable
```

### drift_test.go — Drift Cases

```text
TestHasDrift_NoDrift                 — identical spec and observed → false
TestHasDrift_TagMutabilityDrift      — imageTagMutability mismatch → true
TestHasDrift_ScanOnPushDrift         — scanOnPush mismatch → true
TestHasDrift_PolicyDrift             — repository policy mismatch → true
TestHasDrift_TagsDrift               — tags mismatch → true
TestHasDrift_EncryptionChange        — encryptionConfiguration change → terminal error
TestJSONEqual_Equivalent             — reordered JSON keys → equal
TestJSONEqual_Different              — different policy documents → not equal
```

---

## Step 12 — Integration Tests

**File**: `tests/integration/ecr_repository_driver_test.go`

```go
func TestECRRepository_FullLifecycle(t *testing.T) {
    // 1. Provision a new repository
    // 2. Verify it exists in LocalStack ECR
    // 3. Check outputs: repositoryArn, repositoryName, repositoryUri, registryId
    // 4. Provision again (idempotency check)
    // 5. Update imageTagMutability (drift convergence)
    // 6. Update tags (drift convergence)
    // 7. Delete the repository
    // 8. Verify it no longer exists
}

func TestECRRepository_Import(t *testing.T) {
    // 1. Create a repository directly via AWS SDK (simulating pre-existing)
    // 2. Import it via the driver
    // 3. Verify outputs match AWS state
    // 4. Reconcile (should report no drift)
}

func TestECRRepository_ForceDelete(t *testing.T) {
    // 1. Provision a repository
    // 2. Push an image (or mock image manifest)
    // 3. Attempt Delete without forceDelete (expect error)
    // 4. Enable forceDelete in spec and re-provision
    // 5. Delete — should succeed
}

func TestECRRepository_PolicyManagement(t *testing.T) {
    // 1. Provision with a repository policy
    // 2. Verify policy is set
    // 3. Remove policy (set to empty in spec)
    // 4. Verify policy is deleted
}
```

---

## ECR-Repository-Specific Design Decisions

### 1. `repositoryUri` as Primary Output

The `repositoryUri` (`<registryId>.dkr.ecr.<region>.amazonaws.com/<name>`) is the
most commonly referenced output in downstream resources. ECS task definitions and
Lambda container functions use it directly as the image source. It is surfaced as
a top-level output field rather than requiring consumers to construct it manually.

### 2. `encryptionConfiguration` Immutability

ECR does not support changing encryption settings on existing repositories. Detecting
a desired encryption change produces a `restate.TerminalError` with a human-readable
message directing the operator to delete and recreate the repository. This is
preferable to silently ignoring the change or attempting a no-op update that would
confuse drift reports.

### 3. `forceDelete` Default of `false`

ECR refuses to delete repositories containing images unless `force: true` is passed
to `DeleteRepository`. Defaulting to `false` is intentionally conservative — it
prevents data loss when a full stack teardown would otherwise silently fail or
(if `force` were the default) delete all pushed images. Operators must explicitly
opt into destructive deletion.

### 4. Repository Policy vs. Lifecycle Policy Split

Repository policies (resource-based IAM policies controlling push/pull access) are
managed by this driver. Lifecycle policies (rules that expire old images) are managed
by the separate `ECRLifecyclePolicy` driver. This separation allows lifecycle policies
to be managed independently from the repository resource itself, matching Terraform's
`aws_ecr_repository` vs. `aws_ecr_lifecycle_policy` split.

### 5. No `FindByManagedKey` Path for Plan

`CreateRepository` returns `RepositoryAlreadyExistsException` instead of
idempotently returning the existing repository. Unlike `CreateTopic` in SNS (which
is idempotent), ECR requires explicit conflict detection. The driver's `Provision`
handler calls `DescribeRepositories` first to detect existence, then branches to
create vs. update. `FindByManagedKey` is available for the `Import` path only,
supporting cross-installation ownership lookups.

---

## Checklist

- [ ] CUE schema (`schemas/aws/ecr/repository.cue`)
- [ ] Driver types (`internal/drivers/ecrrepo/types.go`)
- [ ] AWS API abstraction (`internal/drivers/ecrrepo/aws.go`)
- [ ] Drift detection (`internal/drivers/ecrrepo/drift.go`)
- [ ] Driver Virtual Object (`internal/drivers/ecrrepo/driver.go`)
- [ ] Unit tests: driver, aws, drift
- [ ] Provider adapter (`internal/core/provider/ecrrepository_adapter.go`)
- [ ] Adapter unit tests
- [ ] Registry integration (`internal/core/provider/registry.go`)
- [ ] Entry point bind (`cmd/praxis-compute/main.go`)
- [ ] AWS client factory (`internal/infra/awsclient/client.go`)
- [ ] Integration tests (`tests/integration/ecr_repository_driver_test.go`)
- [ ] LocalStack SERVICES includes `ecr` (`docker-compose.yaml`)
- [ ] Justfile targets
