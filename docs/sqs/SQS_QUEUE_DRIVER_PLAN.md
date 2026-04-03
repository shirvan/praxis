# SQS Queue Driver — Implementation Spec

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
12. [Step 9 — Storage Driver Pack Entry Point](#step-9--storage-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [SQS-Queue-Specific Design Decisions](#sqs-queue-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The SQS Queue driver manages the lifecycle of Amazon SQS **queues**. It creates,
imports, updates, and deletes queues along with their visibility timeout, message
retention, delay, encryption (SSE-SQS or SSE-KMS), dead-letter queue configuration,
FIFO settings, and tags.

SQS queues are the core messaging primitive in AWS. Producers send messages to a
queue; consumers poll the queue for messages. In compound templates, the queue is a
dependency of queue policies, Lambda event source mappings, and SNS subscriptions —
the DAG ensures queue creation before dependent resources.

**Out of scope**: Queue policies (separate driver), message operations (send, receive,
delete), dead-letter queue redrive (operational, not infrastructure), and queue
purging. Each operates as a distinct concern with its own lifecycle.

### Resource Scope for This Plan

| In Scope | Out of Scope (Separate Drivers / Concerns) |
|---|---|
| Queue creation (standard and FIFO) | Queue policies (SQSQueuePolicy driver) |
| Visibility timeout | Message operations |
| Message retention period | Queue purging |
| Maximum message size | Dead-letter queue redrive |
| Delay seconds | CloudWatch alarms for queue metrics |
| Receive message wait time (long polling) | |
| Dead-letter queue configuration (redrive policy) | |
| Encryption (SSE-SQS and SSE-KMS) | |
| FIFO settings (fifoQueue, contentBasedDeduplication, deduplicationScope, fifoThroughputLimit) | |
| Tags | |
| Import and drift detection | |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a queue |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing queue |
| `Delete` | `ObjectContext` (exclusive) | Delete a queue |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return queue outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `queueName` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `fifoQueue` | Immutable | Standard vs FIFO is set at creation; cannot be changed |
| `visibilityTimeout` | Mutable | Updated via `SetQueueAttributes` (0–43200 seconds) |
| `messageRetentionPeriod` | Mutable | Updated via `SetQueueAttributes` (60–1209600 seconds) |
| `maximumMessageSize` | Mutable | Updated via `SetQueueAttributes` (1024–262144 bytes) |
| `delaySeconds` | Mutable | Updated via `SetQueueAttributes` (0–900 seconds) |
| `receiveMessageWaitTimeSeconds` | Mutable | Updated via `SetQueueAttributes` (0–20 seconds) |
| `redrivePolicy` | Mutable | Updated via `SetQueueAttributes` (JSON string) |
| `sqsManagedSseEnabled` | Mutable | Updated via `SetQueueAttributes` (SSE-SQS toggle) |
| `kmsMasterKeyId` | Mutable | Updated via `SetQueueAttributes` (SSE-KMS key) |
| `kmsDataKeyReusePeriodSeconds` | Mutable | Updated via `SetQueueAttributes` (60–86400 seconds) |
| `contentBasedDeduplication` | Mutable | FIFO only; updated via `SetQueueAttributes` |
| `deduplicationScope` | Mutable | FIFO only; updated via `SetQueueAttributes` |
| `fifoThroughputLimit` | Mutable | FIFO only; updated via `SetQueueAttributes` |
| `tags` | Mutable | Full replace via `TagQueue` / `UntagQueue` |

### Downstream Consumers

```text
${resources.my-queue.outputs.queueUrl}    → SQS Queue Policy spec (queueUrl)
${resources.my-queue.outputs.queueArn}    → Lambda Event Source Mapping spec.eventSourceArn
${resources.my-queue.outputs.queueArn}    → SNS Subscription spec.endpoint (sqs protocol)
${resources.my-queue.outputs.queueArn}    → SQS Queue spec.redrivePolicy.deadLetterTargetArn (DLQ)
${resources.my-queue.outputs.queueName}   → Cross-references / display
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

SQS queues are regional resources. Queue names are unique within an account and
region. The key is `region~queueName` (e.g., `us-east-1~order-processing`).

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `spec.queueName` (or
  `metadata.name` if queueName is not specified). Returns `region~queueName`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. For queues,
  `resourceID` is the queue name (e.g., `order-processing`) or the queue URL.
  If a full URL is provided, the adapter extracts the queue name from the URL's
  last segment.

### Tag-Based Ownership

Queue names are unique per account+region, providing natural conflict detection via
`CreateQueue` (which is idempotent — creating a queue with the same name and same
attributes returns the existing queue URL). The driver additionally tags queues with
`praxis:managed-key=<region~queueName>` for cross-installation conflict detection
and `FindByManagedKey` lookups.

### CreateQueue Idempotency

`CreateQueue` in SQS is conditionally idempotent. If called with a queue name that
already exists **and the same attributes**, AWS returns the existing queue URL. If
the attributes differ, AWS returns `QueueNameExists`. The driver uses this behavior
for natural conflict detection.

---

## 3. File Inventory

```text
✦ schemas/aws/sqs/queue.cue                              — CUE schema for SQSQueue
✦ internal/drivers/sqs/types.go                           — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/sqs/aws.go                             — QueueAPI interface + realQueueAPI
✦ internal/drivers/sqs/drift.go                           — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/sqs/driver.go                          — SQSQueueDriver Virtual Object
✦ internal/drivers/sqs/driver_test.go                     — Unit tests for driver (mocked AWS)
✦ internal/drivers/sqs/aws_test.go                        — Unit tests for error classification
✦ internal/drivers/sqs/drift_test.go                      — Unit tests for drift detection
✦ internal/core/provider/sqs_adapter.go                   — SQSQueueAdapter implementing provider.Adapter
✦ internal/core/provider/sqs_adapter_test.go              — Unit tests for adapter
✦ tests/integration/sqs_queue_driver_test.go              — Integration tests
✎ internal/infra/awsclient/client.go                      — Add NewSQSClient factory
✎ cmd/praxis-storage/main.go                              — Bind SQSQueue driver
✎ internal/core/provider/registry.go                      — Add NewSQSQueueAdapter to NewRegistry()
✎ docker-compose.yaml                                     — Add sqs to LocalStack SERVICES
✎ justfile                                                — Add SQS build/test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/sqs/queue.cue`

```cue
package sqs

#SQSQueue: {
    apiVersion: "praxis.io/v1"
    kind:       "SQSQueue"

    metadata: {
        // name is the logical name for this queue within the Praxis template.
        // Defaults to queueName if not set separately.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region where the queue is created.
        region: string

        // queueName is the name of the SQS queue.
        // FIFO queues must end with ".fifo".
        // Max 80 characters. Alphanumeric, hyphens, underscores.
        queueName: string & =~"^[a-zA-Z0-9_-]{1,80}(\\.fifo)?$"

        // fifoQueue determines whether this is a FIFO queue.
        // FIFO queues provide strict message ordering and exactly-once delivery.
        // Immutable after creation. queueName must end with ".fifo".
        fifoQueue: bool | *false

        // visibilityTimeout is the duration (in seconds) that a received message
        // is hidden from subsequent receive requests.
        // Range: 0–43200 (0 seconds to 12 hours). Default: 30.
        visibilityTimeout: int & >=0 & <=43200 | *30

        // messageRetentionPeriod is the duration (in seconds) that SQS retains
        // a message. Range: 60–1209600 (1 minute to 14 days). Default: 345600 (4 days).
        messageRetentionPeriod: int & >=60 & <=1209600 | *345600

        // maximumMessageSize is the maximum size (in bytes) of a message body.
        // Range: 1024–262144 (1 KiB to 256 KiB). Default: 262144 (256 KiB).
        maximumMessageSize: int & >=1024 & <=262144 | *262144

        // delaySeconds is the time (in seconds) to delay delivery of all messages
        // sent to the queue. Range: 0–900 (0 to 15 minutes). Default: 0.
        delaySeconds: int & >=0 & <=900 | *0

        // receiveMessageWaitTimeSeconds is the maximum time (in seconds) that a
        // long-poll receive call will wait for a message.
        // Range: 0–20. Default: 0 (short polling).
        receiveMessageWaitTimeSeconds: int & >=0 & <=20 | *0

        // redrivePolicy configures the dead-letter queue for this queue.
        // When a message is received maxReceiveCount times without being deleted,
        // it is moved to the dead-letter queue.
        redrivePolicy?: {
            // deadLetterTargetArn is the ARN of the dead-letter queue.
            deadLetterTargetArn: string
            // maxReceiveCount is the number of times a message can be received
            // before being moved to the DLQ. Range: 1–1000.
            maxReceiveCount: int & >=1 & <=1000
        }

        // sqsManagedSseEnabled enables server-side encryption using SQS-owned
        // encryption keys (SSE-SQS). Default: true (AWS default since 2023).
        // Mutually exclusive with kmsMasterKeyId.
        sqsManagedSseEnabled: bool | *true

        // kmsMasterKeyId is the ID of an AWS KMS key for server-side encryption
        // (SSE-KMS). Can be a key ID, key ARN, alias name, or alias ARN.
        // When set, sqsManagedSseEnabled is implicitly false.
        kmsMasterKeyId?: string

        // kmsDataKeyReusePeriodSeconds is the time (in seconds) that SQS reuses
        // a data key before calling KMS again.
        // Range: 60–86400 (1 minute to 24 hours). Default: 300 (5 minutes).
        // Only used when kmsMasterKeyId is set.
        kmsDataKeyReusePeriodSeconds: int & >=60 & <=86400 | *300

        // contentBasedDeduplication enables content-based deduplication for FIFO queues.
        // When enabled, SQS uses a SHA-256 hash of the message body as the dedup ID.
        // Only valid when fifoQueue is true.
        contentBasedDeduplication: bool | *false

        // deduplicationScope determines the scope of deduplication for FIFO queues.
        // "queue" = queue-level deduplication, "messageGroup" = per-group deduplication.
        // Only valid when fifoQueue is true.
        deduplicationScope?: "queue" | "messageGroup"

        // fifoThroughputLimit determines the throughput quota for FIFO queues.
        // "perQueue" = 300 msg/s, "perMessageGroupId" = 300 msg/s per group (with high throughput mode).
        // Only valid when fifoQueue is true.
        fifoThroughputLimit?: "perQueue" | "perMessageGroupId"

        // tags applied to the queue.
        tags: [string]: string
    }

    outputs?: {
        queueUrl:  string
        queueArn:  string
        queueName: string
    }
}
```

### Key Design Decisions

- **`queueName` separate from `metadata.name`**: The queue name is the AWS-level
  identifier. `metadata.name` is the Praxis template resource name. They may differ
  if the user wants a shorter template name.

- **`redrivePolicy` as structured object**: Unlike the AWS API which accepts a JSON
  string, the CUE schema uses a structured object for type safety. The driver
  serializes it to JSON when calling `SetQueueAttributes`.

- **`sqsManagedSseEnabled` defaults to `true`**: Since 2023, AWS enables SSE-SQS by
  default on all new queues. The default matches AWS behavior.

- **FIFO constraint**: When `fifoQueue` is true, `queueName` must end with `.fifo`.
  The schema regex allows the `.fifo` suffix but does not enforce the coupling —
  the driver validates this at provision time.

- **Mutual exclusion of SSE-SQS and SSE-KMS**: When `kmsMasterKeyId` is set,
  `sqsManagedSseEnabled` is implicitly false. The driver enforces this at provision
  time rather than in the schema to produce clearer error messages.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NEEDS NEW SQS CLIENT FACTORY**

SQS operations use the SQS SDK client.

```go
func NewSQSClient(cfg aws.Config) *sqs.Client {
    return sqs.NewFromConfig(cfg)
}
```

This requires adding `github.com/aws/aws-sdk-go-v2/service/sqs` to `go.mod`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/sqs/types.go`

```go
package sqs

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "SQSQueue"

// SQSQueueSpec is the desired state for an SQS queue.
type SQSQueueSpec struct {
    Account                       string            `json:"account,omitempty"`
    Region                        string            `json:"region"`
    QueueName                     string            `json:"queueName"`
    FifoQueue                     bool              `json:"fifoQueue"`
    VisibilityTimeout             int               `json:"visibilityTimeout"`
    MessageRetentionPeriod        int               `json:"messageRetentionPeriod"`
    MaximumMessageSize            int               `json:"maximumMessageSize"`
    DelaySeconds                  int               `json:"delaySeconds"`
    ReceiveMessageWaitTimeSeconds int               `json:"receiveMessageWaitTimeSeconds"`
    RedrivePolicy                 *RedrivePolicy    `json:"redrivePolicy,omitempty"`
    SqsManagedSseEnabled          bool              `json:"sqsManagedSseEnabled"`
    KmsMasterKeyId                string            `json:"kmsMasterKeyId,omitempty"`
    KmsDataKeyReusePeriodSeconds  int               `json:"kmsDataKeyReusePeriodSeconds"`
    ContentBasedDeduplication     bool              `json:"contentBasedDeduplication"`
    DeduplicationScope            string            `json:"deduplicationScope,omitempty"`
    FifoThroughputLimit           string            `json:"fifoThroughputLimit,omitempty"`
    Tags                          map[string]string `json:"tags,omitempty"`
    ManagedKey                    string            `json:"managedKey,omitempty"`
}

// RedrivePolicy configures the dead-letter queue.
type RedrivePolicy struct {
    DeadLetterTargetArn string `json:"deadLetterTargetArn"`
    MaxReceiveCount     int    `json:"maxReceiveCount"`
}

// SQSQueueOutputs is produced after provisioning and stored in Restate K/V.
type SQSQueueOutputs struct {
    QueueUrl  string `json:"queueUrl"`
    QueueArn  string `json:"queueArn"`
    QueueName string `json:"queueName"`
}

// ObservedState captures the actual configuration from AWS.
type ObservedState struct {
    QueueUrl                      string            `json:"queueUrl"`
    QueueArn                      string            `json:"queueArn"`
    QueueName                     string            `json:"queueName"`
    FifoQueue                     bool              `json:"fifoQueue"`
    VisibilityTimeout             int               `json:"visibilityTimeout"`
    MessageRetentionPeriod        int               `json:"messageRetentionPeriod"`
    MaximumMessageSize            int               `json:"maximumMessageSize"`
    DelaySeconds                  int               `json:"delaySeconds"`
    ReceiveMessageWaitTimeSeconds int               `json:"receiveMessageWaitTimeSeconds"`
    RedrivePolicy                 *RedrivePolicy    `json:"redrivePolicy,omitempty"`
    SqsManagedSseEnabled          bool              `json:"sqsManagedSseEnabled"`
    KmsMasterKeyId                string            `json:"kmsMasterKeyId,omitempty"`
    KmsDataKeyReusePeriodSeconds  int               `json:"kmsDataKeyReusePeriodSeconds"`
    ContentBasedDeduplication     bool              `json:"contentBasedDeduplication"`
    DeduplicationScope            string            `json:"deduplicationScope,omitempty"`
    FifoThroughputLimit           string            `json:"fifoThroughputLimit,omitempty"`
    ApproximateNumberOfMessages   int64             `json:"approximateNumberOfMessages"`
    CreatedTimestamp               string            `json:"createdTimestamp"`
    LastModifiedTimestamp          string            `json:"lastModifiedTimestamp"`
    Tags                          map[string]string `json:"tags"`
}

// SQSQueueState is the single atomic state object stored under drivers.StateKey.
type SQSQueueState struct {
    Desired            SQSQueueSpec         `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            SQSQueueOutputs      `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Why These Fields

- **`QueueUrl` in ObservedState and Outputs**: SQS API operations require the queue
  URL (not the ARN or name). The URL is returned by `CreateQueue` and `GetQueueUrl`
  and must be stored for all subsequent operations.
- **`ApproximateNumberOfMessages`**: Returned by `GetQueueAttributes`. Informational
  only — not used for drift detection — but useful for visibility and health checks.
- **`CreatedTimestamp` / `LastModifiedTimestamp`**: AWS tracks these automatically.
  Informational only, stored for observability.
- **`RedrivePolicy` as struct**: Stored as a typed struct internally, serialized to
  JSON string for the SQS API. This avoids repeated parse/format cycles in drift
  detection.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/sqs/aws.go`

### QueueAPI Interface

```go
type QueueAPI interface {
    // CreateQueue creates a new SQS queue.
    // Returns the queue URL. Conditionally idempotent — returns existing URL
    // if queue exists with the same attributes.
    CreateQueue(ctx context.Context, spec SQSQueueSpec) (string, error)

    // GetQueueUrl resolves a queue name to its URL.
    GetQueueUrl(ctx context.Context, queueName string) (string, error)

    // GetQueueAttributes returns the observed state of a queue.
    GetQueueAttributes(ctx context.Context, queueUrl string) (ObservedState, error)

    // SetQueueAttributes sets attributes on a queue.
    SetQueueAttributes(ctx context.Context, queueUrl string, attrs map[string]string) error

    // DeleteQueue deletes a queue.
    DeleteQueue(ctx context.Context, queueUrl string) error

    // UpdateTags replaces all user-managed tags on the queue.
    UpdateTags(ctx context.Context, queueUrl string, tags map[string]string) error

    // GetTags returns the tags on a queue.
    GetTags(ctx context.Context, queueUrl string) (map[string]string, error)

    // FindByManagedKey searches for queues tagged with
    // praxis:managed-key=managedKey.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### realQueueAPI Implementation

```go
type realQueueAPI struct {
    client  *sqs.Client
    limiter *ratelimit.Limiter
}

func NewQueueAPI(client *sqs.Client) QueueAPI {
    return &realQueueAPI{
        client:  client,
        limiter: ratelimit.New("sqs", 50, 20),
    }
}
```

### Key Implementation Details

#### `CreateQueue`

```go
func (r *realQueueAPI) CreateQueue(ctx context.Context, spec SQSQueueSpec) (string, error) {
    input := &sqs.CreateQueueInput{
        QueueName: aws.String(spec.QueueName),
    }

    attrs := make(map[string]string)
    attrs["VisibilityTimeout"] = strconv.Itoa(spec.VisibilityTimeout)
    attrs["MessageRetentionPeriod"] = strconv.Itoa(spec.MessageRetentionPeriod)
    attrs["MaximumMessageSize"] = strconv.Itoa(spec.MaximumMessageSize)
    attrs["DelaySeconds"] = strconv.Itoa(spec.DelaySeconds)
    attrs["ReceiveMessageWaitTimeSeconds"] = strconv.Itoa(spec.ReceiveMessageWaitTimeSeconds)

    // Encryption
    if spec.KmsMasterKeyId != "" {
        attrs["KmsMasterKeyId"] = spec.KmsMasterKeyId
        attrs["KmsDataKeyReusePeriodSeconds"] = strconv.Itoa(spec.KmsDataKeyReusePeriodSeconds)
        attrs["SqsManagedSseEnabled"] = "false"
    } else {
        attrs["SqsManagedSseEnabled"] = strconv.FormatBool(spec.SqsManagedSseEnabled)
    }

    // Redrive policy
    if spec.RedrivePolicy != nil {
        rpJSON, _ := json.Marshal(spec.RedrivePolicy)
        attrs["RedrivePolicy"] = string(rpJSON)
    }

    // FIFO attributes
    if spec.FifoQueue {
        attrs["FifoQueue"] = "true"
        attrs["ContentBasedDeduplication"] = strconv.FormatBool(spec.ContentBasedDeduplication)
        if spec.DeduplicationScope != "" {
            attrs["DeduplicationScope"] = spec.DeduplicationScope
        }
        if spec.FifoThroughputLimit != "" {
            attrs["FifoThroughputLimit"] = spec.FifoThroughputLimit
        }
    }

    input.Attributes = attrs

    // Tags
    if len(spec.Tags) > 0 {
        input.Tags = spec.Tags
    }

    out, err := r.client.CreateQueue(ctx, input)
    if err != nil {
        return "", err
    }

    return aws.ToString(out.QueueUrl), nil
}
```

> **CreateQueue idempotency**: If the queue already exists with the same name and
> same attributes, AWS returns the existing queue URL. If the queue exists but the
> attributes differ, AWS returns `QueueNameExists`. The driver uses this behavior
> for conflict detection.

#### `GetQueueUrl`

```go
func (r *realQueueAPI) GetQueueUrl(ctx context.Context, queueName string) (string, error) {
    out, err := r.client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{
        QueueName: aws.String(queueName),
    })
    if err != nil {
        return "", err
    }
    return aws.ToString(out.QueueUrl), nil
}
```

#### `GetQueueAttributes`

```go
func (r *realQueueAPI) GetQueueAttributes(ctx context.Context, queueUrl string) (ObservedState, error) {
    out, err := r.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
        QueueUrl:       aws.String(queueUrl),
        AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameAll},
    })
    if err != nil {
        return ObservedState{}, err
    }

    attrs := out.Attributes
    obs := ObservedState{
        QueueUrl:  queueUrl,
        QueueArn:  attrs["QueueArn"],
        QueueName: extractQueueName(queueUrl),
    }

    // Parse integer attributes
    obs.VisibilityTimeout, _ = strconv.Atoi(attrs["VisibilityTimeout"])
    obs.MessageRetentionPeriod, _ = strconv.Atoi(attrs["MessageRetentionPeriod"])
    obs.MaximumMessageSize, _ = strconv.Atoi(attrs["MaximumMessageSize"])
    obs.DelaySeconds, _ = strconv.Atoi(attrs["DelaySeconds"])
    obs.ReceiveMessageWaitTimeSeconds, _ = strconv.Atoi(attrs["ReceiveMessageWaitTimeSeconds"])
    obs.KmsDataKeyReusePeriodSeconds, _ = strconv.Atoi(attrs["KmsDataKeyReusePeriodSeconds"])

    // Boolean attributes
    obs.FifoQueue = attrs["FifoQueue"] == "true"
    obs.ContentBasedDeduplication = attrs["ContentBasedDeduplication"] == "true"
    obs.SqsManagedSseEnabled = attrs["SqsManagedSseEnabled"] == "true"

    // Optional string attributes
    obs.KmsMasterKeyId = attrs["KmsMasterKeyId"]
    obs.DeduplicationScope = attrs["DeduplicationScope"]
    obs.FifoThroughputLimit = attrs["FifoThroughputLimit"]
    obs.CreatedTimestamp = attrs["CreatedTimestamp"]
    obs.LastModifiedTimestamp = attrs["LastModifiedTimestamp"]

    // Redrive policy (JSON string → struct)
    if rp := attrs["RedrivePolicy"]; rp != "" {
        var rdp RedrivePolicy
        if json.Unmarshal([]byte(rp), &rdp) == nil {
            obs.RedrivePolicy = &rdp
        }
    }

    // Approximate message count
    if v, ok := attrs["ApproximateNumberOfMessages"]; ok {
        obs.ApproximateNumberOfMessages, _ = strconv.ParseInt(v, 10, 64)
    }

    // Tags (separate API call)
    tags, err := r.GetTags(ctx, queueUrl)
    if err != nil {
        return ObservedState{}, fmt.Errorf("get tags for queue %s: %w", queueUrl, err)
    }
    obs.Tags = tags

    return obs, nil
}

// extractQueueName extracts the queue name from a queue URL.
// URL format: https://sqs.<region>.amazonaws.com/<account>/<queueName>
func extractQueueName(url string) string {
    parts := strings.Split(url, "/")
    if len(parts) > 0 {
        return parts[len(parts)-1]
    }
    return url
}
```

> **API call count**: `GetQueueAttributes` makes 2 API calls: `GetQueueAttributes`
> (which returns all attributes as a string map) + `ListQueueTags`. Tags require
> a separate API call.

#### `SetQueueAttributes`

```go
func (r *realQueueAPI) SetQueueAttributes(ctx context.Context, queueUrl string, attrs map[string]string) error {
    _, err := r.client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
        QueueUrl:   aws.String(queueUrl),
        Attributes: attrs,
    })
    return err
}
```

> **Bulk attribute updates**: Unlike SNS (which requires per-attribute API calls),
> SQS supports bulk attribute updates via a single `SetQueueAttributes` call with
> multiple key-value pairs. The driver sets all changed attributes in one call,
> wrapped in a single `restate.Run` block.

#### `DeleteQueue`

```go
func (r *realQueueAPI) DeleteQueue(ctx context.Context, queueUrl string) error {
    _, err := r.client.DeleteQueue(ctx, &sqs.DeleteQueueInput{
        QueueUrl: aws.String(queueUrl),
    })
    return err
}
```

> **DeleteQueue behavior**: `DeleteQueue` is NOT idempotent — calling it on a
> non-existent queue returns `QueueDoesNotExist`. The driver classifies this as
> not-found and treats it as success (already gone). After deletion, AWS enforces
> a 60-second cooldown before a queue with the same name can be recreated.

#### `UpdateTags`

```go
func (r *realQueueAPI) UpdateTags(ctx context.Context, queueUrl string, tags map[string]string) error {
    // Get current tags
    current, err := r.GetTags(ctx, queueUrl)
    if err != nil {
        return fmt.Errorf("list tags: %w", err)
    }

    // Compute removals: keys in current but not in desired
    var removeKeys []string
    for key := range current {
        if strings.HasPrefix(key, "praxis:") {
            continue // preserve system tags
        }
        if _, keep := tags[key]; !keep {
            removeKeys = append(removeKeys, key)
        }
    }

    // Remove old tags
    if len(removeKeys) > 0 {
        if _, err := r.client.UntagQueue(ctx, &sqs.UntagQueueInput{
            QueueUrl: aws.String(queueUrl),
            TagKeys:  removeKeys,
        }); err != nil {
            return fmt.Errorf("untag: %w", err)
        }
    }

    // Add/update desired tags
    if len(tags) > 0 {
        if _, err := r.client.TagQueue(ctx, &sqs.TagQueueInput{
            QueueUrl: aws.String(queueUrl),
            Tags:     tags,
        }); err != nil {
            return fmt.Errorf("tag: %w", err)
        }
    }

    return nil
}
```

#### `GetTags`

```go
func (r *realQueueAPI) GetTags(ctx context.Context, queueUrl string) (map[string]string, error) {
    out, err := r.client.ListQueueTags(ctx, &sqs.ListQueueTagsInput{
        QueueUrl: aws.String(queueUrl),
    })
    if err != nil {
        return nil, err
    }
    if out.Tags == nil {
        return make(map[string]string), nil
    }
    return out.Tags, nil
}
```

#### `FindByManagedKey`

```go
func (r *realQueueAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    var nextToken *string
    for {
        out, err := r.client.ListQueues(ctx, &sqs.ListQueuesInput{
            NextToken: nextToken,
        })
        if err != nil {
            return "", err
        }

        for _, url := range out.QueueUrls {
            tags, err := r.GetTags(ctx, url)
            if err != nil {
                continue
            }
            if tags["praxis:managed-key"] == managedKey {
                return url, nil
            }
        }

        if out.NextToken == nil {
            break
        }
        nextToken = out.NextToken
    }
    return "", nil
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var qne *sqstypes.QueueDoesNotExist
    if errors.As(err, &qne) {
        return true
    }
    return strings.Contains(err.Error(), "QueueDoesNotExist") ||
        strings.Contains(err.Error(), "NonExistentQueue") ||
        strings.Contains(err.Error(), "AWS.SimpleQueueService.NonExistentQueue")
}

func isAlreadyExists(err error) bool {
    var qne *sqstypes.QueueNameExists
    if errors.As(err, &qne) {
        return true
    }
    return strings.Contains(err.Error(), "QueueNameExists") ||
        strings.Contains(err.Error(), "QueueAlreadyExists")
}

func isQueueDeletedRecently(err error) bool {
    return strings.Contains(err.Error(), "QueueDeletedRecently") ||
        strings.Contains(err.Error(), "You must wait 60 seconds")
}

func isInvalidInput(err error) bool {
    var iae *sqstypes.InvalidAttributeName
    if errors.As(err, &iae) {
        return true
    }
    var iave *sqstypes.InvalidAttributeValue
    if errors.As(err, &iave) {
        return true
    }
    return strings.Contains(err.Error(), "InvalidAttribute")
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/sqs/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| `visibilityTimeout` | External change via console/CLI | Integer comparison |
| `messageRetentionPeriod` | External change via console/CLI | Integer comparison |
| `maximumMessageSize` | External change via console/CLI | Integer comparison |
| `delaySeconds` | External change via console/CLI | Integer comparison |
| `receiveMessageWaitTimeSeconds` | External change via console/CLI | Integer comparison |
| `redrivePolicy` | External change via console/CLI | Structured comparison |
| `sqsManagedSseEnabled` | External change via console/CLI | Boolean comparison |
| `kmsMasterKeyId` | External change via console/CLI | String comparison |
| `kmsDataKeyReusePeriodSeconds` | External change via console/CLI | Integer comparison (only when KMS is set) |
| `contentBasedDeduplication` | External change via console/CLI | Boolean comparison (FIFO only) |
| `deduplicationScope` | External change via console/CLI | String comparison (FIFO only) |
| `fifoThroughputLimit` | External change via console/CLI | String comparison (FIFO only) |
| `tags` | External change via console/CLI | Key-value map comparison |

> **Not drift-detected**: `fifoQueue` (immutable — cannot change after creation),
> `queueName` (immutable), message counts (informational only), timestamps
> (informational only).

### HasDrift

```go
func HasDrift(desired SQSQueueSpec, observed ObservedState) bool {
    if desired.VisibilityTimeout != observed.VisibilityTimeout {
        return true
    }
    if desired.MessageRetentionPeriod != observed.MessageRetentionPeriod {
        return true
    }
    if desired.MaximumMessageSize != observed.MaximumMessageSize {
        return true
    }
    if desired.DelaySeconds != observed.DelaySeconds {
        return true
    }
    if desired.ReceiveMessageWaitTimeSeconds != observed.ReceiveMessageWaitTimeSeconds {
        return true
    }
    if !redrivePolicyEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
        return true
    }
    if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
        return true
    }
    if desired.KmsMasterKeyId != "" {
        if desired.KmsDataKeyReusePeriodSeconds != observed.KmsDataKeyReusePeriodSeconds {
            return true
        }
    } else {
        if desired.SqsManagedSseEnabled != observed.SqsManagedSseEnabled {
            return true
        }
    }
    if desired.FifoQueue {
        if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
            return true
        }
        if desired.DeduplicationScope != "" && desired.DeduplicationScope != observed.DeduplicationScope {
            return true
        }
        if desired.FifoThroughputLimit != "" && desired.FifoThroughputLimit != observed.FifoThroughputLimit {
            return true
        }
    }
    if !tagsEqual(desired.Tags, observed.Tags) {
        return true
    }
    return false
}
```

### redrivePolicyEqual

```go
// redrivePolicyEqual compares two redrive policies.
// Treats nil and a zero-value struct as equivalent (no redrive policy).
func redrivePolicyEqual(a, b *RedrivePolicy) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    return a.DeadLetterTargetArn == b.DeadLetterTargetArn &&
        a.MaxReceiveCount == b.MaxReceiveCount
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired SQSQueueSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff

    addIntDiff := func(field string, d, o int) {
        if d != o {
            diffs = append(diffs, types.FieldDiff{
                Field: field, Desired: strconv.Itoa(d), Observed: strconv.Itoa(o),
            })
        }
    }

    addIntDiff("visibilityTimeout", desired.VisibilityTimeout, observed.VisibilityTimeout)
    addIntDiff("messageRetentionPeriod", desired.MessageRetentionPeriod, observed.MessageRetentionPeriod)
    addIntDiff("maximumMessageSize", desired.MaximumMessageSize, observed.MaximumMessageSize)
    addIntDiff("delaySeconds", desired.DelaySeconds, observed.DelaySeconds)
    addIntDiff("receiveMessageWaitTimeSeconds", desired.ReceiveMessageWaitTimeSeconds, observed.ReceiveMessageWaitTimeSeconds)

    if !redrivePolicyEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
        diffs = append(diffs, types.FieldDiff{
            Field:    "redrivePolicy",
            Desired:  fmt.Sprintf("%+v", desired.RedrivePolicy),
            Observed: fmt.Sprintf("%+v", observed.RedrivePolicy),
        })
    }

    if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
        diffs = append(diffs, types.FieldDiff{
            Field: "kmsMasterKeyId", Desired: desired.KmsMasterKeyId, Observed: observed.KmsMasterKeyId,
        })
    }
    if desired.KmsMasterKeyId != "" {
        addIntDiff("kmsDataKeyReusePeriodSeconds", desired.KmsDataKeyReusePeriodSeconds, observed.KmsDataKeyReusePeriodSeconds)
    } else if desired.SqsManagedSseEnabled != observed.SqsManagedSseEnabled {
        diffs = append(diffs, types.FieldDiff{
            Field:    "sqsManagedSseEnabled",
            Desired:  strconv.FormatBool(desired.SqsManagedSseEnabled),
            Observed: strconv.FormatBool(observed.SqsManagedSseEnabled),
        })
    }

    if desired.FifoQueue {
        if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
            diffs = append(diffs, types.FieldDiff{
                Field:    "contentBasedDeduplication",
                Desired:  strconv.FormatBool(desired.ContentBasedDeduplication),
                Observed: strconv.FormatBool(observed.ContentBasedDeduplication),
            })
        }
        if desired.DeduplicationScope != "" && desired.DeduplicationScope != observed.DeduplicationScope {
            diffs = append(diffs, types.FieldDiff{
                Field: "deduplicationScope", Desired: desired.DeduplicationScope, Observed: observed.DeduplicationScope,
            })
        }
        if desired.FifoThroughputLimit != "" && desired.FifoThroughputLimit != observed.FifoThroughputLimit {
            diffs = append(diffs, types.FieldDiff{
                Field: "fifoThroughputLimit", Desired: desired.FifoThroughputLimit, Observed: observed.FifoThroughputLimit,
            })
        }
    }

    if !tagsEqual(desired.Tags, observed.Tags) {
        diffs = append(diffs, types.FieldDiff{
            Field: "tags", Desired: fmt.Sprintf("%v", desired.Tags), Observed: fmt.Sprintf("%v", observed.Tags),
        })
    }

    return diffs
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/sqs/driver.go`

### Constructor

```go
type SQSQueueDriver struct {
    auth authservice.AuthClient
    apiFactory func(aws.Config) QueueAPI
}

func NewSQSQueueDriver(auth authservice.AuthClient) *SQSQueueDriver {
    return &SQSQueueDriver{accounts: accounts}
}

func NewSQSQueueDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) QueueAPI) *SQSQueueDriver {
    return &SQSQueueDriver{accounts: accounts, apiFactory: factory}
}

func (SQSQueueDriver) ServiceName() string { return ServiceName }
```

### Provision

Provision handles three cases:

1. **New queue**: Create the queue with all attributes and tags.
2. **Unchanged queue**: Return existing outputs (idempotent).
3. **Changed attributes**: Update the changed attributes.

```go
func (d *SQSQueueDriver) Provision(ctx restate.ObjectContext, spec SQSQueueSpec) (SQSQueueOutputs, error) {
    state, _ := restate.Get[*SQSQueueState](ctx, drivers.StateKey)
    api := d.buildAPI(spec.Account, spec.Region)

    // Validate FIFO consistency
    if spec.FifoQueue && !strings.HasSuffix(spec.QueueName, ".fifo") {
        return SQSQueueOutputs{}, restate.TerminalError(
            fmt.Errorf("FIFO queues must have a name ending with .fifo"), 400)
    }

    // Validate encryption mutual exclusion
    if spec.KmsMasterKeyId != "" && spec.SqsManagedSseEnabled {
        return SQSQueueOutputs{}, restate.TerminalError(
            fmt.Errorf("kmsMasterKeyId and sqsManagedSseEnabled are mutually exclusive"), 400)
    }

    // If existing state and spec hasn't changed, return early
    if state != nil && state.Outputs.QueueUrl != "" && !specChanged(spec, state.Desired) {
        return state.Outputs, nil
    }

    // Write pending state
    newState := &SQSQueueState{
        Desired:    spec,
        Status:     types.StatusProvisioning,
        Mode:       drivers.DefaultMode(""),
        Generation: stateGeneration(state) + 1,
    }
    restate.Set(ctx, drivers.StateKey, newState)

    // Attempt to get existing queue URL (for updates)
    var queueUrl string
    if state != nil && state.Outputs.QueueUrl != "" {
        queueUrl = state.Outputs.QueueUrl
    }

    if queueUrl == "" {
        // Create the queue
        url, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.CreateQueue(rc, spec)
        })
        if err != nil {
            if isAlreadyExists(err) {
                return SQSQueueOutputs{}, restate.TerminalError(
                    fmt.Errorf("queue %q already exists with different attributes: %w", spec.QueueName, err), 409)
            }
            if isQueueDeletedRecently(err) {
                // Retryable — Restate will retry after backoff
                return SQSQueueOutputs{}, fmt.Errorf("queue %q was recently deleted, must wait 60s: %w", spec.QueueName, err)
            }
            if isInvalidInput(err) {
                return SQSQueueOutputs{}, restate.TerminalError(
                    fmt.Errorf("invalid queue configuration: %w", err), 400)
            }
            return SQSQueueOutputs{}, err
        }
        queueUrl = url
    } else {
        // Update existing queue attributes
        if err := d.convergeAttributes(ctx, api, queueUrl, spec); err != nil {
            return SQSQueueOutputs{}, err
        }
    }

    // Tag with managed key
    managedKey := restate.Key(ctx)
    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.UpdateTags(rc, queueUrl, mergeTags(spec.Tags, map[string]string{
            "praxis:managed-key": managedKey,
        }))
    }); err != nil {
        // Non-fatal — queue was created
        slog.Warn("failed to set managed-key tag", "queue", queueUrl, "err", err)
    }

    // Get observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetQueueAttributes(rc, queueUrl)
    })
    if err != nil {
        observed = ObservedState{QueueUrl: queueUrl, QueueName: spec.QueueName}
    }

    outputs := SQSQueueOutputs{
        QueueUrl:  queueUrl,
        QueueArn:  observed.QueueArn,
        QueueName: spec.QueueName,
    }

    newState.Observed = observed
    newState.Outputs = outputs
    newState.Status = types.StatusReady
    newState.Error = ""
    restate.Set(ctx, drivers.StateKey, newState)

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### convergeAttributes

```go
// convergeAttributes sets all mutable attributes on the queue in a single API call.
func (d *SQSQueueDriver) convergeAttributes(ctx restate.ObjectContext, api QueueAPI, queueUrl string, spec SQSQueueSpec) error {
    attrs := make(map[string]string)
    attrs["VisibilityTimeout"] = strconv.Itoa(spec.VisibilityTimeout)
    attrs["MessageRetentionPeriod"] = strconv.Itoa(spec.MessageRetentionPeriod)
    attrs["MaximumMessageSize"] = strconv.Itoa(spec.MaximumMessageSize)
    attrs["DelaySeconds"] = strconv.Itoa(spec.DelaySeconds)
    attrs["ReceiveMessageWaitTimeSeconds"] = strconv.Itoa(spec.ReceiveMessageWaitTimeSeconds)

    // Encryption
    if spec.KmsMasterKeyId != "" {
        attrs["KmsMasterKeyId"] = spec.KmsMasterKeyId
        attrs["KmsDataKeyReusePeriodSeconds"] = strconv.Itoa(spec.KmsDataKeyReusePeriodSeconds)
        attrs["SqsManagedSseEnabled"] = "false"
    } else {
        attrs["SqsManagedSseEnabled"] = strconv.FormatBool(spec.SqsManagedSseEnabled)
        // Clear KMS key if switching from SSE-KMS to SSE-SQS
        attrs["KmsMasterKeyId"] = ""
    }

    // Redrive policy
    if spec.RedrivePolicy != nil {
        rpJSON, _ := json.Marshal(spec.RedrivePolicy)
        attrs["RedrivePolicy"] = string(rpJSON)
    } else {
        attrs["RedrivePolicy"] = ""
    }

    // FIFO attributes (only for FIFO queues)
    if spec.FifoQueue {
        attrs["ContentBasedDeduplication"] = strconv.FormatBool(spec.ContentBasedDeduplication)
        if spec.DeduplicationScope != "" {
            attrs["DeduplicationScope"] = spec.DeduplicationScope
        }
        if spec.FifoThroughputLimit != "" {
            attrs["FifoThroughputLimit"] = spec.FifoThroughputLimit
        }
    }

    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.SetQueueAttributes(rc, queueUrl, attrs)
    }); err != nil {
        return fmt.Errorf("set queue attributes: %w", err)
    }

    return nil
}
```

### Import

```go
func (d *SQSQueueDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SQSQueueOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    // ResourceID can be a queue name or queue URL
    var queueUrl string
    if strings.HasPrefix(ref.ResourceID, "https://") || strings.HasPrefix(ref.ResourceID, "http://") {
        queueUrl = ref.ResourceID
    } else {
        url, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.GetQueueUrl(rc, ref.ResourceID)
        })
        if err != nil {
            if isNotFound(err) {
                return SQSQueueOutputs{}, restate.TerminalError(
                    fmt.Errorf("queue %q not found in %s", ref.ResourceID, ref.Region), 404)
            }
            return SQSQueueOutputs{}, err
        }
        queueUrl = url
    }

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetQueueAttributes(rc, queueUrl)
    })
    if err != nil {
        if isNotFound(err) {
            return SQSQueueOutputs{}, restate.TerminalError(
                fmt.Errorf("queue %q not found", queueUrl), 404)
        }
        return SQSQueueOutputs{}, err
    }

    spec := specFromObserved(observed, ref)
    outputs := SQSQueueOutputs{
        QueueUrl:  queueUrl,
        QueueArn:  observed.QueueArn,
        QueueName: observed.QueueName,
    }

    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &SQSQueueState{
        Desired:    spec,
        Observed:   observed,
        Outputs:    outputs,
        Status:     types.StatusReady,
        Mode:       mode,
        Generation: 1,
    })

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### Delete

```go
func (d *SQSQueueDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*SQSQueueState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state == nil {
        return nil
    }
    if state.Mode == types.ModeObserved {
        return restate.TerminalError(fmt.Errorf("cannot delete observed resource"), 403)
    }

    state.Status = types.StatusDeleting
    restate.Set(ctx, drivers.StateKey, state)

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.DeleteQueue(rc, state.Outputs.QueueUrl)
    }); err != nil {
        if !isNotFound(err) {
            return err
        }
    }

    state.Status = types.StatusDeleted
    restate.Set(ctx, drivers.StateKey, state)
    restate.Clear(ctx, drivers.StateKey)

    return nil
}
```

### Reconcile

```go
func (d *SQSQueueDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*SQSQueueState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    if state == nil {
        return types.ReconcileResult{Status: "no-state"}, nil
    }

    state.ReconcileScheduled = false
    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetQueueAttributes(rc, state.Outputs.QueueUrl)
    })
    if err != nil {
        if isNotFound(err) {
            state.Status = types.StatusError
            state.Error = "queue not found — may have been deleted externally"
            state.Observed = ObservedState{}
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }
        return types.ReconcileResult{}, err
    }

    state.Observed = observed
    state.LastReconcile = time.Now().UTC().Format(time.RFC3339)

    if !HasDrift(state.Desired, observed) {
        state.Status = types.StatusReady
        state.Error = ""
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx)
        return types.ReconcileResult{Status: "ok"}, nil
    }

    diffs := ComputeFieldDiffs(state.Desired, observed)
    result := types.ReconcileResult{
        Status: "drift-detected",
        Drifts: diffs,
    }

    if state.Mode == types.ModeManaged {
        // Correct drift: update all attributes in one call
        if err := d.convergeAttributes(ctx, api, state.Outputs.QueueUrl, state.Desired); err != nil {
            state.Error = fmt.Sprintf("drift correction failed: %v", err)
            state.Status = types.StatusError
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }

        // Update tags if drifted
        if !tagsEqual(state.Desired.Tags, observed.Tags) {
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateTags(rc, state.Outputs.QueueUrl, mergeTags(
                    state.Desired.Tags, map[string]string{"praxis:managed-key": restate.Key(ctx)},
                ))
            }); err != nil {
                state.Error = fmt.Sprintf("drift correction (tags) failed: %v", err)
                state.Status = types.StatusError
                restate.Set(ctx, drivers.StateKey, state)
                d.scheduleReconcile(ctx)
                return types.ReconcileResult{Status: "error", Error: state.Error}, nil
            }
        }

        result.Status = "drift-corrected"
        state.Status = types.StatusReady
        state.Error = ""
    }

    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx)
    return result, nil
}
```

### GetStatus / GetOutputs (Shared Handlers)

Follow the standard pattern (identical to S3 and SNS drivers).

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/sqs_adapter.go`

```go
type SQSQueueAdapter struct {
    auth authservice.AuthClient
}

func NewSQSQueueAdapterWithAuth(auth authservice.AuthClient) *SQSQueueAdapter {
    return &SQSQueueAdapter{accounts: accounts}
}

func (a *SQSQueueAdapter) Kind() string        { return sqs.ServiceName }
func (a *SQSQueueAdapter) ServiceName() string  { return sqs.ServiceName }
func (a *SQSQueueAdapter) Scope() KeyScope      { return KeyScopeRegion }

func (a *SQSQueueAdapter) BuildKey(doc json.RawMessage) (string, error) {
    var parsed struct {
        Spec struct {
            Region    string `json:"region"`
            QueueName string `json:"queueName"`
        } `json:"spec"`
        Metadata struct {
            Name string `json:"name"`
        } `json:"metadata"`
    }
    if err := json.Unmarshal(doc, &parsed); err != nil {
        return "", err
    }
    region := parsed.Spec.Region
    queueName := parsed.Spec.QueueName
    if queueName == "" {
        queueName = parsed.Metadata.Name
    }
    if region == "" || queueName == "" {
        return "", fmt.Errorf("SQSQueue requires spec.region and spec.queueName (or metadata.name)")
    }
    return region + "~" + queueName, nil
}

func (a *SQSQueueAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // resourceID can be a queue name or queue URL
    queueName := resourceID
    if strings.HasPrefix(resourceID, "https://") || strings.HasPrefix(resourceID, "http://") {
        // Extract queue name from URL
        parts := strings.Split(resourceID, "/")
        if len(parts) > 0 {
            queueName = parts[len(parts)-1]
        }
    }
    return region + "~" + queueName, nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — **MODIFY**

Add `NewSQSQueueAdapterWithAuth(auth)` to `NewRegistry()`.

---

## Step 9 — Storage Driver Pack Entry Point

**File**: `cmd/praxis-storage/main.go` — **MODIFY**

```go
import "github.com/shirvan/praxis/internal/drivers/sqs"

Bind(restate.Reflect(sqs.NewSQSQueueDriver(auth)))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

**File**: `docker-compose.yaml` — **MODIFY**

Add `sqs` to LocalStack's `SERVICES` environment variable:

```yaml
- SERVICES=s3,ssm,sts,ec2,iam,route53,sqs
```

No new container is needed — SQS drivers are hosted in the existing praxis-storage
service.

### Justfile Additions

```just
test-sqs:
    go test ./internal/drivers/sqs/... -v -count=1 -race

test-sqs-integration:
    go test ./tests/integration/ -run TestSQSQueue -v -count=1 -tags=integration -timeout=5m

ls-sqs:
    aws --endpoint-url=http://localhost:4566 sqs list-queues --region us-east-1
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/sqs/driver_test.go`

| Test | Description |
|---|---|
| `TestProvision_NewQueue` | Creates standard queue; verifies URL, ARN, outputs, and state |
| `TestProvision_NoChange` | Same spec; verifies idempotent return |
| `TestProvision_UpdateVisibilityTimeout` | Changed visibility timeout; verifies attribute update |
| `TestProvision_UpdateRetention` | Changed message retention; verifies attribute update |
| `TestProvision_UpdateRedrivePolicy` | Changed dead-letter config; verifies redrive policy update |
| `TestProvision_RemoveRedrivePolicy` | Remove DLQ config; verifies redrive policy cleared |
| `TestProvision_UpdateEncryption` | Switch from SSE-SQS to SSE-KMS; verifies encryption update |
| `TestProvision_FifoQueue` | Creates FIFO queue; verifies FIFO attributes |
| `TestProvision_FifoNameMismatch` | FIFO flag without .fifo suffix; verifies 400 error |
| `TestProvision_SseKmsMutualExclusion` | SSE-KMS + SSE-SQS enabled; verifies 400 error |
| `TestProvision_QueueDeletedRecently` | Queue was recently deleted; verifies retryable error |
| `TestImport_ByName` | Imports existing queue by name; verifies state |
| `TestImport_ByUrl` | Imports existing queue by URL; verifies state |
| `TestImport_NotFound` | Queue doesn't exist; verifies 404 |
| `TestDelete_Managed` | Deletes queue; verifies cleanup |
| `TestDelete_Observed` | Cannot delete observed; verifies 403 |
| `TestDelete_AlreadyGone` | Queue already deleted; verifies idempotent |
| `TestReconcile_NoDrift` | Attributes match; verifies ok |
| `TestReconcile_VisibilityDrifted` | Visibility timeout changed externally; verifies drift correction |
| `TestReconcile_RedriveDrifted` | Redrive policy changed externally; verifies drift correction |
| `TestReconcile_QueueDeleted` | Queue deleted externally; verifies error state |
| `TestSpecFromObserved` | Round-trip: observed → spec preserves all fields |
| `TestServiceName` | `NewSQSQueueDriver(nil).ServiceName()` returns `"SQSQueue"` |

**File**: `internal/drivers/sqs/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | All fields match; no drift |
| `TestHasDrift_VisibilityChanged` | Visibility timeout differs; drift detected |
| `TestHasDrift_RetentionChanged` | Message retention differs; drift detected |
| `TestHasDrift_DelayChanged` | Delay seconds differs; drift detected |
| `TestHasDrift_RedriveAdded` | Redrive policy added externally; drift detected |
| `TestHasDrift_RedriveRemoved` | Redrive policy removed externally; drift detected |
| `TestHasDrift_RedriveMaxReceiveChanged` | Max receive count changed; drift detected |
| `TestHasDrift_KmsKeyChanged` | KMS key differs; drift detected |
| `TestHasDrift_SseSqsChanged` | SSE-SQS toggled; drift detected |
| `TestHasDrift_FifoDeduplicationChanged` | Content dedup changed (FIFO); drift detected |
| `TestHasDrift_TagsChanged` | Tags differ; drift detected |
| `TestHasDrift_EmptyTagsNoDrift` | `{}` vs `nil` tags → no drift |
| `TestRedrivePolicyEqual_BothNil` | Both nil → equal |
| `TestRedrivePolicyEqual_OneNil` | One nil → not equal |
| `TestRedrivePolicyEqual_DifferentArn` | Different DLQ ARN → not equal |
| `TestComputeFieldDiffs_NoDrift` | No diffs → empty slice |
| `TestComputeFieldDiffs_MultipleDrifts` | Multiple fields drifted → correct diff entries |

**File**: `internal/drivers/sqs/aws_test.go`

| Test | Description |
|---|---|
| `TestIsNotFound_QueueDoesNotExist` | Validates QueueDoesNotExist classification |
| `TestIsNotFound_StringFallback` | Validates string fallback for NonExistentQueue |
| `TestIsAlreadyExists` | Validates QueueNameExists classification |
| `TestIsQueueDeletedRecently` | Validates QueueDeletedRecently string matching |
| `TestIsInvalidInput` | Validates InvalidAttributeName classification |

---

## Step 12 — Integration Tests

**File**: `tests/integration/sqs_queue_driver_test.go`

| Test | Description |
|---|---|
| `TestSQSQueue_CreateStandard` | Create standard queue, verify attributes in AWS |
| `TestSQSQueue_CreateFifo` | Create FIFO queue, verify FIFO attributes |
| `TestSQSQueue_UpdateVisibilityTimeout` | Create, update visibility timeout, verify change |
| `TestSQSQueue_UpdateRedrivePolicy` | Create, set DLQ config, verify redrive policy |
| `TestSQSQueue_UpdateEncryption` | Create with SSE-SQS, switch to SSE-KMS, verify |
| `TestSQSQueue_Import` | Create via AWS API, import, verify state |
| `TestSQSQueue_ImportByUrl` | Create via AWS API, import by URL, verify state |
| `TestSQSQueue_Delete` | Create then delete, verify queue gone |
| `TestSQSQueue_DeleteRecently` | Delete, immediately re-create, verify retryable error |
| `TestSQSQueue_Reconcile` | Create, externally change visibility, reconcile in managed mode |
| `TestSQSQueue_DeadLetterQueue` | Create DLQ and main queue with redrive, verify configuration |

### LocalStack Considerations

- LocalStack supports full SQS queue lifecycle (`CreateQueue`, `DeleteQueue`,
  `GetQueueAttributes`, `SetQueueAttributes`, `GetQueueUrl`, `ListQueues`,
  `TagQueue`, `UntagQueue`, `ListQueueTags`).
- FIFO queues are supported in LocalStack.
- KMS encryption may have limited fidelity in LocalStack — integration tests should
  verify attribute storage without relying on actual encryption.
- The 60-second deletion cooldown may not be enforced by LocalStack — the
  `TestSQSQueue_DeleteRecently` test should use a conditional skip if LocalStack
  does not enforce this constraint.
- LocalStack already includes `sqs` in the shared `SERVICES` list used by the integration suite.

---

## SQS-Queue-Specific Design Decisions

### 1. Bulk Attribute Updates via SetQueueAttributes

**Decision**: The driver uses a single `SetQueueAttributes` call with all changed
attributes, not per-attribute calls.

**Rationale**: Unlike SNS (which requires `SetTopicAttributes` per attribute), SQS
`SetQueueAttributes` accepts a map of attribute name → value pairs and applies them
atomically. This reduces API calls and simplifies the convergence logic. A single
`restate.Run` block wraps the entire attribute update.

### 2. Queue URL as Primary Identifier

**Decision**: All SQS operations use the queue URL (not the queue name or ARN).

**Rationale**: The SQS API requires queue URLs for all operations except
`CreateQueue` and `GetQueueUrl`. The URL is returned at creation time and stored in
outputs. For imports, the driver resolves the queue name to a URL via `GetQueueUrl`.

### 3. CreateQueue Conditional Idempotency

**Decision**: The driver treats `QueueNameExists` as a terminal conflict error (409),
not as a "queue already exists, proceed" case.

**Rationale**: SQS `CreateQueue` only returns the existing URL if the attributes
match exactly. If any attribute differs, it returns `QueueNameExists`. Unlike SNS
`CreateTopic` (which returns the existing ARN regardless of attributes), SQS is
strict. A `QueueNameExists` error means someone else created a queue with different
settings — this is a genuine conflict that cannot be auto-resolved.

### 4. QueueDeletedRecently — Retryable, Not Terminal

**Decision**: The driver returns `QueueDeletedRecently` as a retryable error, not
a terminal error.

**Rationale**: AWS enforces a 60-second cooldown after queue deletion before allowing
recreation with the same name. This is a transient condition — Restate's retry
mechanism with exponential backoff will automatically retry after the cooldown period.
Making this terminal would require manual operator intervention for a self-resolving
condition.

### 5. Redrive Policy as Structured Type

**Decision**: The `RedrivePolicy` is stored as a Go struct internally, serialized
to/from JSON for the SQS API.

**Rationale**: The SQS API accepts redrive policy as a JSON string attribute. Storing
it as a typed struct enables proper drift detection (field-by-field comparison instead
of JSON string comparison) and provides type safety in the CUE schema and Go code.

### 6. SSE-SQS Default Matches AWS Behavior

**Decision**: `sqsManagedSseEnabled` defaults to `true`.

**Rationale**: Since 2023, AWS enables SSE-SQS by default on all new queues. The
default matches AWS behavior, preventing phantom drift after importing queues that
have the AWS default encryption. Users can explicitly set it to `false` if needed
(uncommon).

### 7. KmsDataKeyReusePeriodSeconds — Only Compared When KMS Is Set

**Decision**: The drift detection for `KmsDataKeyReusePeriodSeconds` only runs when
`kmsMasterKeyId` is set.

**Rationale**: This attribute is only meaningful when SSE-KMS encryption is active.
When using SSE-SQS, the attribute may have a stale value from a previous KMS
configuration. Comparing it would produce false drift detections.

### 8. Import Accepts Name or URL

**Decision**: The `Import` handler accepts either a queue name or a full queue URL
as the `resourceID`.

**Rationale**: Users may know either the queue name or the URL. Accepting both
reduces friction. If a name is provided, the driver resolves it to a URL via
`GetQueueUrl`.

### 9. FIFO Attributes — Only Drift-Detected When fifoQueue Is True

**Decision**: FIFO-specific attributes (`contentBasedDeduplication`,
`deduplicationScope`, `fifoThroughputLimit`) are only included in drift detection
when the queue is a FIFO queue.

**Rationale**: These attributes are not meaningful for standard queues. AWS may
return empty strings or default values for these attributes on standard queues.
Comparing them would produce false positives.

---

## Design Decisions (Resolved)

### Key Scope

**Decision**: `KeyScopeRegion` with key format `region~queueName`.

**Rationale**: SQS queues are regional. Queue names are unique per account+region.
The region prefix follows the established pattern for regional resources.

### Runtime Pack

**Decision**: SQS drivers are hosted in `praxis-storage`.

**Rationale**: SQS is a messaging/data-plane service. The docker-compose.yaml header
already lists SQS as a future praxis-storage service alongside SNS. This grouping
aligns with the established domain model.

### Default Import Mode

**Decision**: Import defaults to `ModeObserved`.

**Rationale**: SQS queues are often shared infrastructure — multiple services may
produce or consume from the same queue. Importing as observed prevents accidental
modification or deletion. Users can override to `ModeManaged` if they want Praxis
to take ownership.

---

## Checklist

### Implementation

- [x] `schemas/aws/sqs/queue.cue`
- [x] `internal/drivers/sqs/types.go`
- [x] `internal/drivers/sqs/aws.go`
- [x] `internal/drivers/sqs/drift.go`
- [x] `internal/drivers/sqs/driver.go`
- [x] `internal/core/provider/sqs_adapter.go`

### Tests

- [x] `internal/drivers/sqs/driver_test.go`
- [x] `internal/drivers/sqs/aws_test.go`
- [x] `internal/drivers/sqs/drift_test.go`
- [x] `internal/core/provider/sqs_adapter_test.go`
- [x] `tests/integration/sqs_queue_driver_test.go`

### Integration

- [x] `internal/infra/awsclient/client.go` — Add `NewSQSClient()`
- [x] `cmd/praxis-storage/main.go` — Bind driver
- [x] `internal/core/provider/registry.go` — Register adapter
- [x] `docker-compose.yaml` — LocalStack already includes `sqs` in `SERVICES`
- [x] `justfile` — Add test targets
