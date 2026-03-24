# SNS Topic Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages SNS Topics, providing full
> lifecycle management including creation, import, deletion, drift detection, and
> drift correction for topic attributes, access policies, encryption, and tags.
>
> Key scope: `KeyScopeRegion` — key format is `region~topicName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned topic ARN lives
> only in state/outputs.

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
16. [SNS-Topic-Specific Design Decisions](#sns-topic-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The SNS Topic driver manages the lifecycle of Amazon SNS **topics**. It creates,
imports, updates, and deletes topics along with their display name, access policy,
delivery policy, encryption configuration, FIFO settings, and tags.

SNS topics are the central broadcasting mechanism in AWS messaging. Publishers
send messages to a topic; the topic fans out messages to all active subscriptions.
In compound templates, the topic is a dependency of all subscriptions — the DAG
ensures topic creation before subscription creation.

**Out of scope**: Subscriptions (separate driver), message publishing, platform
applications (mobile push), SMS sandbox configuration. Each operates as a distinct
resource type with its own lifecycle.

### Resource Scope for This Plan

| In Scope | Out of Scope (Separate Drivers) |
|---|---|
| Topic creation (standard and FIFO) | Subscriptions |
| Display name | Message publishing |
| Access policy (resource-based policy) | Platform applications |
| Delivery policy (HTTP/S retry config) | SMS sandbox |
| KMS encryption | CloudWatch alarms for topic metrics |
| FIFO settings (fifoTopic, contentBasedDeduplication) | |
| Tags | |
| Import and drift detection | |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a topic |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing topic |
| `Delete` | `ObjectContext` (exclusive) | Delete a topic |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return topic outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `topicName` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `fifoTopic` | Immutable | Standard vs FIFO is set at creation; cannot be changed |
| `displayName` | Mutable | Updated via `SetTopicAttributes` |
| `policy` | Mutable | Access policy; updated via `SetTopicAttributes` |
| `deliveryPolicy` | Mutable | HTTP/S retry config; updated via `SetTopicAttributes` |
| `kmsMasterKeyId` | Mutable | Encryption key; updated via `SetTopicAttributes` (empty string removes encryption) |
| `contentBasedDeduplication` | Mutable | FIFO only; updated via `SetTopicAttributes` |
| `tags` | Mutable | Full replace via `TagResource` / `UntagResource` |

### Downstream Consumers

```text
${resources.my-topic.outputs.topicArn}    → SNS Subscription spec.topicArn
${resources.my-topic.outputs.topicArn}    → Lambda Permission spec.sourceArn
${resources.my-topic.outputs.topicName}   → Cross-references / display
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

SNS topics are regional resources. Topic names are unique within an account and
region. The key is `region~topicName` (e.g., `us-east-1~order-notifications`).

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `spec.topicName` (or
  `metadata.name` if topicName is not specified). Returns `region~topicName`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. For topics,
  `resourceID` is the topic name (e.g., `order-notifications`) or the topic ARN.
  If a full ARN is provided, the adapter extracts the topic name from the ARN's
  last segment.

### Tag-Based Ownership

Topic names are unique per account+region, providing natural conflict detection via
`CreateTopic` (which is idempotent — creating a topic with the same name returns the
existing topic's ARN). The driver additionally tags topics with
`praxis:managed-key=<region~topicName>` for cross-installation conflict detection
and `FindByManagedKey` lookups.

### CreateTopic Idempotency

`CreateTopic` in SNS is naturally idempotent. If called with a topic name that
already exists, AWS returns the existing topic's ARN rather than creating a new one.
However, if the attributes (e.g., FIFO settings) differ from the existing topic,
AWS returns an `InvalidParameterException`. The driver uses this behavior for
natural conflict detection.

---

## 3. File Inventory

```text
✦ schemas/aws/sns/topic.cue                              — CUE schema for SNSTopic
✦ internal/drivers/snstopic/types.go                      — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/snstopic/aws.go                        — TopicAPI interface + realTopicAPI
✦ internal/drivers/snstopic/drift.go                      — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/snstopic/driver.go                     — SNSTopicDriver Virtual Object
✦ internal/drivers/snstopic/driver_test.go                — Unit tests for driver (mocked AWS)
✦ internal/drivers/snstopic/aws_test.go                   — Unit tests for error classification
✦ internal/drivers/snstopic/drift_test.go                 — Unit tests for drift detection
✦ internal/core/provider/snstopic_adapter.go              — SNSTopicAdapter implementing provider.Adapter
✦ internal/core/provider/snstopic_adapter_test.go         — Unit tests for adapter
✦ tests/integration/sns_topic_driver_test.go              — Integration tests
✎ internal/infra/awsclient/client.go                      — Add NewSNSClient factory
✎ cmd/praxis-storage/main.go                              — Bind SNSTopic driver
✎ internal/core/provider/registry.go                      — Add NewSNSTopicAdapter to NewRegistry()
✎ docker-compose.yaml                                     — Add sns to LocalStack SERVICES
✎ justfile                                                — Add SNS build/test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/sns/topic.cue`

```cue
package sns

#SNSTopic: {
    apiVersion: "praxis.io/v1"
    kind:       "SNSTopic"

    metadata: {
        // name is the logical name for this topic within the Praxis template.
        // Defaults to topicName if not set separately.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region where the topic is created.
        region: string

        // topicName is the name of the SNS topic.
        // FIFO topics must end with ".fifo".
        // Max 256 characters. Alphanumeric, hyphens, underscores.
        topicName: string & =~"^[a-zA-Z0-9_-]{1,256}(\\.fifo)?$"

        // displayName is the human-readable name used in the "From" field
        // for SMS and email subscriptions. Max 100 characters.
        displayName?: string & strings.MaxRunes(100)

        // fifoTopic determines whether this is a FIFO topic.
        // FIFO topics provide strict message ordering and exactly-once delivery.
        // Immutable after creation. topicName must end with ".fifo".
        fifoTopic: bool | *false

        // contentBasedDeduplication enables content-based deduplication for FIFO topics.
        // When enabled, SNS uses a SHA-256 hash of the message body as the dedup ID.
        // Only valid when fifoTopic is true.
        contentBasedDeduplication: bool | *false

        // policy is the JSON access policy for the topic.
        // Defines who can publish to or subscribe to the topic.
        // If omitted, SNS creates a default policy allowing the topic owner full access.
        policy?: string

        // deliveryPolicy is the JSON delivery policy for HTTP/S subscriptions.
        // Controls retry behavior (backoff, max retries, throttle) for HTTP endpoints.
        deliveryPolicy?: string

        // kmsMasterKeyId is the ID of an AWS KMS key for server-side encryption.
        // Can be a key ID, key ARN, alias name, or alias ARN.
        // Omit or set to empty string to disable encryption.
        kmsMasterKeyId?: string

        // tags applied to the topic.
        tags: [string]: string
    }

    outputs?: {
        topicArn:  string
        topicName: string
        owner:     string
    }
}
```

### Key Design Decisions

- **`topicName` separate from `metadata.name`**: The topic name is the AWS-level
  identifier. `metadata.name` is the Praxis template resource name. They may differ
  if the user wants a shorter template name.

- **`policy` as JSON string**: SNS access policies are standard IAM policy documents.
  Storing as a JSON string follows the same pattern as S3 bucket policies.

- **`deliveryPolicy` as JSON string**: Delivery policies have a complex nested
  structure. JSON string is the simplest representation and matches the AWS API.

- **FIFO constraint**: When `fifoTopic` is true, `topicName` must end with `.fifo`.
  The schema regex allows the `.fifo` suffix but does not enforce the coupling —
  the driver validates this at provision time.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NEEDS NEW SNS CLIENT FACTORY**

SNS operations use the SNS SDK client.

```go
func NewSNSClient(cfg aws.Config) *sns.Client {
    return sns.NewFromConfig(cfg)
}
```

This requires adding `github.com/aws/aws-sdk-go-v2/service/sns` to `go.mod`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/snstopic/types.go`

```go
package snstopic

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "SNSTopic"

// SNSTopicSpec is the desired state for an SNS topic.
type SNSTopicSpec struct {
    Account                    string            `json:"account,omitempty"`
    Region                     string            `json:"region"`
    TopicName                  string            `json:"topicName"`
    DisplayName                string            `json:"displayName,omitempty"`
    FifoTopic                  bool              `json:"fifoTopic"`
    ContentBasedDeduplication  bool              `json:"contentBasedDeduplication"`
    Policy                     string            `json:"policy,omitempty"`
    DeliveryPolicy             string            `json:"deliveryPolicy,omitempty"`
    KmsMasterKeyId             string            `json:"kmsMasterKeyId,omitempty"`
    Tags                       map[string]string `json:"tags,omitempty"`
    ManagedKey                 string            `json:"managedKey,omitempty"`
}

// SNSTopicOutputs is produced after provisioning and stored in Restate K/V.
type SNSTopicOutputs struct {
    TopicArn  string `json:"topicArn"`
    TopicName string `json:"topicName"`
    Owner     string `json:"owner"`
}

// ObservedState captures the actual configuration from AWS.
type ObservedState struct {
    TopicArn                   string            `json:"topicArn"`
    TopicName                  string            `json:"topicName"`
    DisplayName                string            `json:"displayName"`
    FifoTopic                  bool              `json:"fifoTopic"`
    ContentBasedDeduplication  bool              `json:"contentBasedDeduplication"`
    Policy                     string            `json:"policy,omitempty"`
    DeliveryPolicy             string            `json:"deliveryPolicy,omitempty"`
    KmsMasterKeyId             string            `json:"kmsMasterKeyId,omitempty"`
    Owner                      string            `json:"owner"`
    SubscriptionsConfirmed     int64             `json:"subscriptionsConfirmed"`
    SubscriptionsPending       int64             `json:"subscriptionsPending"`
    SubscriptionsDeleted       int64             `json:"subscriptionsDeleted"`
    Tags                       map[string]string `json:"tags"`
}

// SNSTopicState is the single atomic state object stored under drivers.StateKey.
type SNSTopicState struct {
    Desired            SNSTopicSpec         `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            SNSTopicOutputs      `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Why These Fields

- **`Owner` in ObservedState**: The AWS account ID that owns the topic. Useful for
  cross-account access policy validation.
- **Subscription counts**: `SubscriptionsConfirmed`, `SubscriptionsPending`,
  `SubscriptionsDeleted` are returned by `GetTopicAttributes`. They are informational
  only — not used for drift detection — but useful for visibility.
- **No `EffectiveDeliveryPolicy`**: AWS returns both the user-set `DeliveryPolicy`
  and a computed `EffectiveDeliveryPolicy`. The driver only tracks the user-set
  policy for drift detection.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/snstopic/aws.go`

### TopicAPI Interface

```go
type TopicAPI interface {
    // CreateTopic creates a new SNS topic.
    // Returns the topic ARN. Idempotent — returns existing ARN if topic already exists.
    CreateTopic(ctx context.Context, spec SNSTopicSpec) (string, error)

    // GetTopicAttributes returns the observed state of a topic.
    GetTopicAttributes(ctx context.Context, topicArn string) (ObservedState, error)

    // SetTopicAttribute sets a single attribute on a topic.
    SetTopicAttribute(ctx context.Context, topicArn, attrName, attrValue string) error

    // DeleteTopic deletes a topic and all its subscriptions.
    DeleteTopic(ctx context.Context, topicArn string) error

    // UpdateTags replaces all user-managed tags on the topic.
    UpdateTags(ctx context.Context, topicArn string, tags map[string]string) error

    // FindByManagedKey searches for topics tagged with
    // praxis:managed-key=managedKey.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)

    // FindByName searches for a topic by name.
    // Uses CreateTopic idempotency or ListTopics to resolve name → ARN.
    FindByName(ctx context.Context, topicName string) (string, error)
}
```

### realTopicAPI Implementation

```go
type realTopicAPI struct {
    client  *sns.Client
    limiter *ratelimit.Limiter
}

func NewTopicAPI(client *sns.Client) TopicAPI {
    return &realTopicAPI{
        client:  client,
        limiter: ratelimit.New("sns-topic", 30, 10),
    }
}
```

### Key Implementation Details

#### `CreateTopic`

```go
func (r *realTopicAPI) CreateTopic(ctx context.Context, spec SNSTopicSpec) (string, error) {
    input := &sns.CreateTopicInput{
        Name: aws.String(spec.TopicName),
    }

    // Set topic attributes
    attrs := make(map[string]string)
    if spec.DisplayName != "" {
        attrs["DisplayName"] = spec.DisplayName
    }
    if spec.FifoTopic {
        attrs["FifoTopic"] = "true"
    }
    if spec.ContentBasedDeduplication {
        attrs["ContentBasedDeduplication"] = "true"
    }
    if spec.KmsMasterKeyId != "" {
        attrs["KmsMasterKeyId"] = spec.KmsMasterKeyId
    }
    if len(attrs) > 0 {
        input.Attributes = attrs
    }

    // Tags
    if len(spec.Tags) > 0 {
        tags := make([]snstypes.Tag, 0, len(spec.Tags))
        for k, v := range spec.Tags {
            tags = append(tags, snstypes.Tag{
                Key:   aws.String(k),
                Value: aws.String(v),
            })
        }
        input.Tags = tags
    }

    out, err := r.client.CreateTopic(ctx, input)
    if err != nil {
        return "", err
    }

    return aws.ToString(out.TopicArn), nil
}
```

> **CreateTopic idempotency**: If the topic already exists with the same name and
> same attributes, AWS returns the existing ARN. If the topic exists but the
> attributes differ (e.g., FIFO mismatch), AWS returns `InvalidParameterException`.

#### `GetTopicAttributes`

```go
func (r *realTopicAPI) GetTopicAttributes(ctx context.Context, topicArn string) (ObservedState, error) {
    // 1. GetTopicAttributes — all topic attributes
    out, err := r.client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{
        TopicArn: aws.String(topicArn),
    })
    if err != nil {
        return ObservedState{}, err
    }

    attrs := out.Attributes
    obs := ObservedState{
        TopicArn:    topicArn,
        TopicName:   extractTopicName(topicArn),
        DisplayName: attrs["DisplayName"],
        Owner:       attrs["Owner"],
    }

    // Parse boolean attributes
    obs.FifoTopic = attrs["FifoTopic"] == "true"
    obs.ContentBasedDeduplication = attrs["ContentBasedDeduplication"] == "true"

    // Optional attributes
    if v, ok := attrs["Policy"]; ok {
        obs.Policy = v
    }
    if v, ok := attrs["DeliveryPolicy"]; ok {
        obs.DeliveryPolicy = v
    }
    if v, ok := attrs["KmsMasterKeyId"]; ok {
        obs.KmsMasterKeyId = v
    }

    // Subscription counts
    if v, ok := attrs["SubscriptionsConfirmed"]; ok {
        obs.SubscriptionsConfirmed, _ = strconv.ParseInt(v, 10, 64)
    }
    if v, ok := attrs["SubscriptionsPending"]; ok {
        obs.SubscriptionsPending, _ = strconv.ParseInt(v, 10, 64)
    }
    if v, ok := attrs["SubscriptionsDeleted"]; ok {
        obs.SubscriptionsDeleted, _ = strconv.ParseInt(v, 10, 64)
    }

    // 2. ListTagsForResource — tags
    tagOut, err := r.client.ListTagsForResource(ctx, &sns.ListTagsForResourceInput{
        ResourceArn: aws.String(topicArn),
    })
    if err != nil {
        return ObservedState{}, fmt.Errorf("list tags for topic %s: %w", topicArn, err)
    }

    obs.Tags = make(map[string]string)
    for _, tag := range tagOut.Tags {
        obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
    }

    return obs, nil
}

// extractTopicName extracts the topic name from a topic ARN.
// ARN format: arn:aws:sns:<region>:<account>:<topicName>
func extractTopicName(arn string) string {
    parts := strings.Split(arn, ":")
    if len(parts) >= 6 {
        return parts[5]
    }
    return arn
}
```

> **API call count**: `GetTopicAttributes` makes 2 API calls: `GetTopicAttributes`
> (which returns all attributes as a string map) + `ListTagsForResource`. Tags
> require a separate API call.

#### `SetTopicAttribute`

```go
func (r *realTopicAPI) SetTopicAttribute(ctx context.Context, topicArn, attrName, attrValue string) error {
    _, err := r.client.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{
        TopicArn:       aws.String(topicArn),
        AttributeName:  aws.String(attrName),
        AttributeValue: aws.String(attrValue),
    })
    return err
}
```

> **Per-attribute updates**: SNS uses `SetTopicAttributes` with one attribute at a
> time. There is no bulk-update API. The driver calls `SetTopicAttribute` in
> separate `restate.Run` blocks for each changed attribute, ensuring each mutation
> is journaled independently.

#### `DeleteTopic`

```go
func (r *realTopicAPI) DeleteTopic(ctx context.Context, topicArn string) error {
    _, err := r.client.DeleteTopic(ctx, &sns.DeleteTopicInput{
        TopicArn: aws.String(topicArn),
    })
    return err
}
```

> **DeleteTopic behavior**: `DeleteTopic` is idempotent — calling it on a
> non-existent topic does not return an error. It also deletes all subscriptions
> to the topic automatically.

#### `UpdateTags`

```go
func (r *realTopicAPI) UpdateTags(ctx context.Context, topicArn string, tags map[string]string) error {
    // Get current tags
    tagOut, err := r.client.ListTagsForResource(ctx, &sns.ListTagsForResourceInput{
        ResourceArn: aws.String(topicArn),
    })
    if err != nil {
        return fmt.Errorf("list tags: %w", err)
    }

    // Compute removals: keys in current but not in desired
    var removeKeys []string
    for _, tag := range tagOut.Tags {
        key := aws.ToString(tag.Key)
        if strings.HasPrefix(key, "praxis:") {
            continue // preserve system tags
        }
        if _, keep := tags[key]; !keep {
            removeKeys = append(removeKeys, key)
        }
    }

    // Remove old tags
    if len(removeKeys) > 0 {
        if _, err := r.client.UntagResource(ctx, &sns.UntagResourceInput{
            ResourceArn: aws.String(topicArn),
            TagKeys:     removeKeys,
        }); err != nil {
            return fmt.Errorf("untag: %w", err)
        }
    }

    // Add/update desired tags
    if len(tags) > 0 {
        snsTags := make([]snstypes.Tag, 0, len(tags))
        for k, v := range tags {
            snsTags = append(snsTags, snstypes.Tag{
                Key:   aws.String(k),
                Value: aws.String(v),
            })
        }
        if _, err := r.client.TagResource(ctx, &sns.TagResourceInput{
            ResourceArn: aws.String(topicArn),
            Tags:        snsTags,
        }); err != nil {
            return fmt.Errorf("tag: %w", err)
        }
    }

    return nil
}
```

#### `FindByManagedKey`

```go
func (r *realTopicAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    var nextToken *string
    for {
        out, err := r.client.ListTopics(ctx, &sns.ListTopicsInput{
            NextToken: nextToken,
        })
        if err != nil {
            return "", err
        }

        for _, topic := range out.Topics {
            arn := aws.ToString(topic.TopicArn)
            tagOut, err := r.client.ListTagsForResource(ctx, &sns.ListTagsForResourceInput{
                ResourceArn: aws.String(arn),
            })
            if err != nil {
                continue
            }
            for _, tag := range tagOut.Tags {
                if aws.ToString(tag.Key) == "praxis:managed-key" && aws.ToString(tag.Value) == managedKey {
                    return arn, nil
                }
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

#### `FindByName`

```go
func (r *realTopicAPI) FindByName(ctx context.Context, topicName string) (string, error) {
    var nextToken *string
    for {
        out, err := r.client.ListTopics(ctx, &sns.ListTopicsInput{
            NextToken: nextToken,
        })
        if err != nil {
            return "", err
        }

        for _, topic := range out.Topics {
            arn := aws.ToString(topic.TopicArn)
            if extractTopicName(arn) == topicName {
                return arn, nil
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

> **ListTopics pagination**: `FindByName` paginates through all topics in the
> account and region. Topic ARNs embed the topic name as the last colon-delimited
> segment, so `extractTopicName` resolves the match without a per-topic
> `GetTopicAttributes` call. For accounts with many topics (>100), this may be
> slower than a tag-based lookup — in those cases, prefer using
> `FindByManagedKey` when the managed key is known.

### Error Classification

```go
func isNotFound(err error) bool {
    var nfe *snstypes.NotFoundException
    if errors.As(err, &nfe) {
        return true
    }
    return strings.Contains(err.Error(), "NotFoundException") ||
        strings.Contains(err.Error(), "NotFound")
}

func isInvalidParameter(err error) bool {
    var ipe *snstypes.InvalidParameterException
    if errors.As(err, &ipe) {
        return true
    }
    var ipve *snstypes.InvalidParameterValueException
    if errors.As(err, &ipve) {
        return true
    }
    return strings.Contains(err.Error(), "InvalidParameter")
}

func isAuthError(err error) bool {
    var aee *snstypes.AuthorizationErrorException
    if errors.As(err, &aee) {
        return true
    }
    return strings.Contains(err.Error(), "AuthorizationError")
}

func isThrottled(err error) bool {
    var te *snstypes.ThrottledException
    if errors.As(err, &te) {
        return true
    }
    return strings.Contains(err.Error(), "Throttl")
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/snstopic/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| `displayName` | External change via console/CLI | Topic attribute |
| `policy` | External change via console/CLI | JSON policy document |
| `deliveryPolicy` | External change via console/CLI | JSON delivery policy |
| `kmsMasterKeyId` | External change via console/CLI | Encryption key reference |
| `contentBasedDeduplication` | External change via console/CLI | FIFO topics only |
| `tags` | External change via console/CLI | Key-value pairs |

> **Not drift-detected**: `fifoTopic` (immutable — cannot change after creation),
> `topicName` (immutable), subscription counts (informational only).

### HasDrift

```go
func HasDrift(desired SNSTopicSpec, observed ObservedState) bool {
    if desired.DisplayName != observed.DisplayName {
        return true
    }
    if !policiesEqual(desired.Policy, observed.Policy) {
        return true
    }
    if !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
        return true
    }
    if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
        return true
    }
    if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
        return true
    }
    if !tagsEqual(desired.Tags, observed.Tags) {
        return true
    }
    return false
}
```

### policiesEqual

```go
// policiesEqual compares two JSON policy strings semantically.
// Handles whitespace and key ordering differences.
func policiesEqual(a, b string) bool {
    if a == b {
        return true
    }
    if a == "" || b == "" {
        return false
    }
    var aObj, bObj interface{}
    if json.Unmarshal([]byte(a), &aObj) != nil {
        return a == b
    }
    if json.Unmarshal([]byte(b), &bObj) != nil {
        return a == b
    }
    aNorm, _ := json.Marshal(aObj)
    bNorm, _ := json.Marshal(bObj)
    return string(aNorm) == string(bNorm)
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired SNSTopicSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff
    if desired.DisplayName != observed.DisplayName {
        diffs = append(diffs, types.FieldDiff{
            Field: "displayName", Desired: desired.DisplayName, Observed: observed.DisplayName,
        })
    }
    if !policiesEqual(desired.Policy, observed.Policy) {
        diffs = append(diffs, types.FieldDiff{
            Field: "policy", Desired: desired.Policy, Observed: observed.Policy,
        })
    }
    if !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
        diffs = append(diffs, types.FieldDiff{
            Field: "deliveryPolicy", Desired: desired.DeliveryPolicy, Observed: observed.DeliveryPolicy,
        })
    }
    if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
        diffs = append(diffs, types.FieldDiff{
            Field: "kmsMasterKeyId", Desired: desired.KmsMasterKeyId, Observed: observed.KmsMasterKeyId,
        })
    }
    if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
        diffs = append(diffs, types.FieldDiff{
            Field: "contentBasedDeduplication",
            Desired: fmt.Sprintf("%v", desired.ContentBasedDeduplication),
            Observed: fmt.Sprintf("%v", observed.ContentBasedDeduplication),
        })
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

**File**: `internal/drivers/snstopic/driver.go`

### Constructor

```go
type SNSTopicDriver struct {
    accounts   *auth.Registry
    apiFactory func(aws.Config) TopicAPI
}

func NewSNSTopicDriver(accounts *auth.Registry) *SNSTopicDriver {
    return NewSNSTopicDriverWithFactory(accounts, func(cfg aws.Config) TopicAPI {
        return NewTopicAPI(awsclient.NewSNSClient(cfg))
    })
}

func NewSNSTopicDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) TopicAPI) *SNSTopicDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) TopicAPI {
            return NewTopicAPI(awsclient.NewSNSClient(cfg))
        }
    }
    return &SNSTopicDriver{accounts: accounts, apiFactory: factory}
}

func (SNSTopicDriver) ServiceName() string { return ServiceName }
```

### Provision

Provision handles three cases:

1. **New topic**: Create the topic and set attributes.
2. **Unchanged topic**: Return existing outputs (idempotent).
3. **Changed attributes**: Update the changed attributes.

```go
func (d *SNSTopicDriver) Provision(ctx restate.ObjectContext, spec SNSTopicSpec) (SNSTopicOutputs, error) {
    state, _ := restate.Get[*SNSTopicState](ctx, drivers.StateKey)
    api := d.buildAPI(spec.Account, spec.Region)

    // Validate FIFO consistency
    if spec.FifoTopic && !strings.HasSuffix(spec.TopicName, ".fifo") {
        return SNSTopicOutputs{}, restate.TerminalError(
            fmt.Errorf("FIFO topics must have a name ending with .fifo"), 400)
    }

    // If existing state and spec hasn't changed, return early
    if state != nil && state.Outputs.TopicArn != "" && !specChanged(spec, state.Desired) {
        return state.Outputs, nil
    }

    // Write pending state
    newState := &SNSTopicState{
        Desired:    spec,
        Status:     types.StatusProvisioning,
        Mode:       drivers.DefaultMode(""),
        Generation: stateGeneration(state) + 1,
    }
    restate.Set(ctx, drivers.StateKey, newState)

    // Create the topic (idempotent)
    topicArn, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
        return api.CreateTopic(rc, spec)
    })
    if err != nil {
        if isInvalidParameter(err) {
            return SNSTopicOutputs{}, restate.TerminalError(
                fmt.Errorf("invalid topic configuration: %w", err), 400)
        }
        return SNSTopicOutputs{}, err
    }

    // Tag with managed key
    managedKey := restate.Key(ctx)
    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.UpdateTags(rc, topicArn, mergeTags(spec.Tags, map[string]string{
            "praxis:managed-key": managedKey,
        }))
    }); err != nil {
        // Non-fatal — topic was created
        slog.Warn("failed to set managed-key tag", "topic", topicArn, "err", err)
    }

    // Update mutable attributes if this is a convergence (spec changed)
    if state != nil && state.Outputs.TopicArn != "" {
        if err := d.convergeAttributes(ctx, api, topicArn, spec, state.Desired); err != nil {
            return SNSTopicOutputs{}, err
        }
    }

    // Get observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetTopicAttributes(rc, topicArn)
    })
    if err != nil {
        observed = ObservedState{TopicArn: topicArn, TopicName: spec.TopicName}
    }

    outputs := SNSTopicOutputs{
        TopicArn:  topicArn,
        TopicName: spec.TopicName,
        Owner:     observed.Owner,
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
// convergeAttributes updates each topic attribute that has changed.
func (d *SNSTopicDriver) convergeAttributes(ctx restate.ObjectContext, api TopicAPI, topicArn string, desired, previous SNSTopicSpec) error {
    type attrUpdate struct {
        name string
        val  string
    }

    var updates []attrUpdate
    if desired.DisplayName != previous.DisplayName {
        updates = append(updates, attrUpdate{"DisplayName", desired.DisplayName})
    }
    if desired.Policy != previous.Policy {
        updates = append(updates, attrUpdate{"Policy", desired.Policy})
    }
    if desired.DeliveryPolicy != previous.DeliveryPolicy {
        updates = append(updates, attrUpdate{"DeliveryPolicy", desired.DeliveryPolicy})
    }
    if desired.KmsMasterKeyId != previous.KmsMasterKeyId {
        updates = append(updates, attrUpdate{"KmsMasterKeyId", desired.KmsMasterKeyId})
    }
    if desired.ContentBasedDeduplication != previous.ContentBasedDeduplication {
        val := "false"
        if desired.ContentBasedDeduplication {
            val = "true"
        }
        updates = append(updates, attrUpdate{"ContentBasedDeduplication", val})
    }

    for _, u := range updates {
        name, val := u.name, u.val
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.SetTopicAttribute(rc, topicArn, name, val)
        }); err != nil {
            return fmt.Errorf("set attribute %s: %w", name, err)
        }
    }

    return nil
}
```

### Import

```go
func (d *SNSTopicDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SNSTopicOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    // ResourceID can be a topic name or topic ARN
    topicArn := ref.ResourceID
    if !strings.HasPrefix(topicArn, "arn:aws:sns:") {
        // It's a name — find the ARN
        arn, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.FindByName(rc, ref.ResourceID)
        })
        if err != nil || arn == "" {
            return SNSTopicOutputs{}, restate.TerminalError(
                fmt.Errorf("topic %q not found in %s", ref.ResourceID, ref.Region), 404)
        }
        topicArn = arn
    }

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetTopicAttributes(rc, topicArn)
    })
    if err != nil {
        if isNotFound(err) {
            return SNSTopicOutputs{}, restate.TerminalError(
                fmt.Errorf("topic %q not found", topicArn), 404)
        }
        return SNSTopicOutputs{}, err
    }

    spec := specFromObserved(observed, ref)
    outputs := SNSTopicOutputs{
        TopicArn:  topicArn,
        TopicName: observed.TopicName,
        Owner:     observed.Owner,
    }

    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &SNSTopicState{
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
func (d *SNSTopicDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*SNSTopicState](ctx, drivers.StateKey)
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
        return restate.Void{}, api.DeleteTopic(rc, state.Outputs.TopicArn)
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
func (d *SNSTopicDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*SNSTopicState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    if state == nil {
        return types.ReconcileResult{Status: "no-state"}, nil
    }

    state.ReconcileScheduled = false
    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetTopicAttributes(rc, state.Outputs.TopicArn)
    })
    if err != nil {
        if isNotFound(err) {
            state.Status = types.StatusError
            state.Error = "topic not found — may have been deleted externally"
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
        // Correct drift: update each changed attribute
        if err := d.convergeAttributes(ctx, api, state.Outputs.TopicArn, state.Desired, specFromObserved(observed, types.ImportRef{})); err != nil {
            state.Error = fmt.Sprintf("drift correction failed: %v", err)
            state.Status = types.StatusError
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }

        // Update tags if drifted
        if !tagsEqual(state.Desired.Tags, observed.Tags) {
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateTags(rc, state.Outputs.TopicArn, mergeTags(
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

### GetStatus / GetOutputs

Follow the standard pattern (identical to S3 and EC2 drivers).

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/snstopic_adapter.go`

```go
type SNSTopicAdapter struct {
    accounts *auth.Registry
}

func NewSNSTopicAdapterWithRegistry(accounts *auth.Registry) *SNSTopicAdapter {
    return &SNSTopicAdapter{accounts: accounts}
}

func (a *SNSTopicAdapter) Kind() string        { return snstopic.ServiceName }
func (a *SNSTopicAdapter) ServiceName() string  { return snstopic.ServiceName }
func (a *SNSTopicAdapter) Scope() KeyScope      { return KeyScopeRegion }

func (a *SNSTopicAdapter) BuildKey(doc json.RawMessage) (string, error) {
    var parsed struct {
        Spec struct {
            Region    string `json:"region"`
            TopicName string `json:"topicName"`
        } `json:"spec"`
        Metadata struct {
            Name string `json:"name"`
        } `json:"metadata"`
    }
    if err := json.Unmarshal(doc, &parsed); err != nil {
        return "", err
    }
    region := parsed.Spec.Region
    topicName := parsed.Spec.TopicName
    if topicName == "" {
        topicName = parsed.Metadata.Name
    }
    if region == "" || topicName == "" {
        return "", fmt.Errorf("SNSTopic requires spec.region and spec.topicName (or metadata.name)")
    }
    return region + "~" + topicName, nil
}

func (a *SNSTopicAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // resourceID can be a topic name or ARN
    topicName := resourceID
    if strings.HasPrefix(resourceID, "arn:aws:sns:") {
        parts := strings.Split(resourceID, ":")
        if len(parts) >= 6 {
            topicName = parts[5]
        }
    }
    return region + "~" + topicName, nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — **MODIFY**

Add `NewSNSTopicAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Storage Driver Pack Entry Point

**File**: `cmd/praxis-storage/main.go` — **MODIFY**

```go
import "github.com/shirvan/praxis/internal/drivers/snstopic"

Bind(restate.Reflect(snstopic.NewSNSTopicDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

**File**: `docker-compose.yaml` — **MODIFY**

Add `sns` to LocalStack's `SERVICES` environment variable:

```yaml
- SERVICES=s3,ssm,sts,ec2,iam,route53,sns
```

No new container is needed — SNS drivers are hosted in the existing praxis-storage
service.

### Justfile Additions

```just
test-snstopic:
    go test ./internal/drivers/snstopic/... -v -count=1 -race

test-snstopic-integration:
    go test ./tests/integration/... -run TestSNSTopic -v -timeout=3m
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/snstopic/driver_test.go`

| Test | Description |
|---|---|
| `TestProvision_NewTopic` | Creates topic; verifies ARN, outputs, and state |
| `TestProvision_NoChange` | Same spec; verifies idempotent return |
| `TestProvision_UpdateDisplayName` | Changed display name; verifies attribute update |
| `TestProvision_UpdatePolicy` | Changed access policy; verifies attribute update |
| `TestProvision_UpdateEncryption` | Changed KMS key; verifies attribute update |
| `TestProvision_FifoTopic` | Creates FIFO topic; verifies FIFO attributes |
| `TestProvision_FifoNameMismatch` | FIFO flag without .fifo suffix; verifies 400 error |
| `TestImport_Success` | Imports existing topic by name; verifies state |
| `TestImport_ByArn` | Imports existing topic by full ARN; verifies state |
| `TestImport_NotFound` | Topic doesn't exist; verifies 404 |
| `TestDelete_Managed` | Deletes topic; verifies cleanup |
| `TestDelete_Observed` | Cannot delete observed; verifies 403 |
| `TestDelete_AlreadyGone` | Topic already deleted; verifies idempotent |
| `TestReconcile_NoDrift` | Attributes match; verifies ok |
| `TestReconcile_DisplayNameDrifted` | Display name changed externally; verifies drift correction |
| `TestReconcile_PolicyDrifted` | Policy changed externally; verifies drift correction |
| `TestReconcile_TopicDeleted` | Topic deleted externally; verifies error state |

**File**: `internal/drivers/snstopic/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | All fields match; no drift |
| `TestHasDrift_DisplayNameChanged` | Display name differs; drift detected |
| `TestHasDrift_PolicyChanged` | Policy JSON differs (semantic comparison); drift detected |
| `TestHasDrift_PolicyWhitespace` | Policy JSON differs only in whitespace; no drift |
| `TestHasDrift_KmsKeyChanged` | KMS key differs; drift detected |
| `TestHasDrift_TagsChanged` | Tags differ; drift detected |

**File**: `internal/drivers/snstopic/aws_test.go`

| Test | Description |
|---|---|
| `TestIsNotFound` | Validates NotFoundException classification |
| `TestIsInvalidParameter` | Validates InvalidParameterException classification |
| `TestIsThrottled` | Validates ThrottledException classification |
| `TestIsAuthError` | Validates AuthorizationErrorException classification |

---

## Step 12 — Integration Tests

**File**: `tests/integration/sns_topic_driver_test.go`

| Test | Description |
|---|---|
| `TestSNSTopic_CreateAndVerify` | Create topic, verify attributes in AWS |
| `TestSNSTopic_CreateFifo` | Create FIFO topic, verify FIFO attributes |
| `TestSNSTopic_UpdateDisplayName` | Create, update display name, verify change |
| `TestSNSTopic_UpdatePolicy` | Create, set access policy, verify policy |
| `TestSNSTopic_Import` | Create via AWS API, import, verify state |
| `TestSNSTopic_ImportByArn` | Create via AWS API, import by ARN, verify state |
| `TestSNSTopic_Delete` | Create then delete, verify topic gone |
| `TestSNSTopic_Reconcile` | Create, externally change display name, reconcile in managed mode |
| `TestSNSTopic_EncryptionRoundTrip` | Create with KMS key, verify encryption, remove encryption |

### LocalStack Considerations

- LocalStack supports full SNS topic lifecycle (`CreateTopic`, `DeleteTopic`,
  `GetTopicAttributes`, `SetTopicAttributes`, `TagResource`, `UntagResource`,
  `ListTagsForResource`).
- FIFO topics are supported in LocalStack.
- KMS encryption may have limited fidelity in LocalStack — integration tests should
  verify attribute storage without relying on actual encryption.
- SNS must be added to the `SERVICES` list in LocalStack.

---

## SNS-Topic-Specific Design Decisions

### 1. Attribute-Based API Model

**Decision**: The driver uses `SetTopicAttributes` for individual attribute updates,
not a single "update topic" call.

**Rationale**: SNS does not provide a bulk-update API for topic attributes. Each
attribute must be set individually via `SetTopicAttributes`. The driver wraps each
attribute update in a separate `restate.Run` block, ensuring each mutation is journaled
independently and can be retried individually on failure.

### 2. CreateTopic Idempotency Instead of CallerReference

**Decision**: Unlike Route 53 (which requires a `CallerReference`), SNS
`CreateTopic` is inherently idempotent by topic name.

**Rationale**: If `CreateTopic` is called with the same name and compatible
attributes, AWS returns the existing topic ARN. If the name matches but attributes
differ (e.g., FIFO mismatch), AWS returns `InvalidParameterException`. This
natural behavior eliminates the need for a caller reference strategy.

### 3. JSON Policy Semantic Comparison

**Decision**: Policy drift detection uses JSON semantic comparison (parse → marshal
→ compare) rather than string comparison.

**Rationale**: AWS may return policies with different key ordering or whitespace
than the user-provided JSON. String comparison would produce false drift detections.
Semantic comparison normalizes both sides before comparing.

### 4. Tags as Attribute + API Combination

**Decision**: Tags are managed via `TagResource` / `UntagResource`, not via
`SetTopicAttributes`.

**Rationale**: SNS topic tags use a separate API surface (`TagResource`,
`UntagResource`, `ListTagsForResource`) rather than the attribute-based API.
This is consistent with most AWS services that have dedicated tag management APIs.

### 5. DeleteTopic Cascades Subscriptions

**Decision**: The driver does not explicitly delete subscriptions before deleting a
topic.

**Rationale**: `DeleteTopic` automatically removes all subscriptions to the topic.
The DAG scheduler should still order subscription deletions before topic deletion
(to keep the subscription driver's state consistent), but if the topic is deleted
directly, subscriptions are cleaned up by AWS.

### 6. Import Accepts Name or ARN

**Decision**: The `Import` handler accepts either a topic name or a full topic ARN
as the `resourceID`.

**Rationale**: Users may know either the topic name or the ARN. Accepting both
reduces friction. If a name is provided, the driver resolves it to an ARN via
`FindByName`.

---

## Design Decisions (Resolved)

### Key Scope

**Decision**: `KeyScopeRegion` with key format `region~topicName`.

**Rationale**: SNS topics are regional. Topic names are unique per account+region.
The region prefix follows the established pattern for regional resources (EC2, S3,
Lambda, etc.).

### Runtime Pack

**Decision**: SNS drivers are hosted in `praxis-storage`.

**Rationale**: SNS is a messaging/data-plane service. The docker-compose.yaml header
already lists SNS as a future praxis-storage service alongside SQS. This grouping
aligns with the established domain model.

### Default Import Mode

**Decision**: Import defaults to `ModeObserved`.

**Rationale**: SNS topics are often shared infrastructure — multiple applications
may publish to the same topic. Importing as observed prevents accidental modification
or deletion. Users can override to `ModeManaged` if they want Praxis to take
ownership.

---

## Checklist

### Implementation

- [ ] `schemas/aws/sns/topic.cue`
- [ ] `internal/drivers/snstopic/types.go`
- [ ] `internal/drivers/snstopic/aws.go`
- [ ] `internal/drivers/snstopic/drift.go`
- [ ] `internal/drivers/snstopic/driver.go`
- [ ] `internal/core/provider/snstopic_adapter.go`

### Tests

- [ ] `internal/drivers/snstopic/driver_test.go`
- [ ] `internal/drivers/snstopic/aws_test.go`
- [ ] `internal/drivers/snstopic/drift_test.go`
- [ ] `internal/core/provider/snstopic_adapter_test.go`
- [ ] `tests/integration/sns_topic_driver_test.go`

### Integration

- [ ] `internal/infra/awsclient/client.go` — Add `NewSNSClient()`
- [ ] `cmd/praxis-storage/main.go` — Bind driver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `docker-compose.yaml` — Add `sns` to LocalStack SERVICES
- [ ] `justfile` — Add test targets
