# SNS Subscription Driver — Implementation Spec

---

## Table of Contents

1. [Overview & Scope](#1-overview--scope)
2. [Key Strategy](#2-key-strategy)
3. [File Inventory](#3-file-inventory)
4. [Step 1 — CUE Schema](#step-1--cue-schema)
5. [Step 2 — Driver Types](#step-2--driver-types)
6. [Step 3 — AWS API Abstraction Layer](#step-3--aws-api-abstraction-layer)
7. [Step 4 — Drift Detection](#step-4--drift-detection)
8. [Step 5 — Driver Implementation](#step-5--driver-implementation)
9. [Step 6 — Provider Adapter](#step-6--provider-adapter)
10. [Step 7 — Registry Integration](#step-7--registry-integration)
11. [Step 8 — Storage Driver Pack Entry Point](#step-8--storage-driver-pack-entry-point)
12. [Step 9 — Docker Compose & Justfile](#step-9--docker-compose--justfile)
13. [Step 10 — Unit Tests](#step-10--unit-tests)
14. [Step 11 — Integration Tests](#step-11--integration-tests)
15. [SNS-Subscription-Specific Design Decisions](#sns-subscription-specific-design-decisions)
16. [Design Decisions (Resolved)](#design-decisions-resolved)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The SNS Subscription driver manages the lifecycle of Amazon SNS **subscriptions**.
A subscription connects an SNS topic to a delivery endpoint via a chosen protocol.
When a message is published to the topic, SNS delivers a copy to each active
subscription's endpoint according to its protocol, filter policy, and delivery
configuration.

Subscriptions are the fan-out mechanism in SNS. In compound templates, the
subscription depends on both the topic and the target endpoint (Lambda function,
SQS queue, HTTP URL, etc.). The DAG ensures the topic and endpoint exist before the
subscription is created.

**Out of scope**: Topics (separate driver), message publishing, platform
applications (mobile push), SMS sandbox configuration, direct Lambda invocation
permissions (Lambda Permission driver). Each operates as a distinct resource type
with its own lifecycle.

### Resource Scope for This Plan

| In Scope | Out of Scope (Separate Drivers) |
|---|---|
| Subscription creation (all protocols) | Topics |
| Filter policies (MessageAttributes and MessageBody) | Target endpoint creation (Lambda, SQS, etc.) |
| Raw message delivery | Lambda invoke permissions |
| Redrive policy (dead-letter queue) | SQS queue policies |
| Delivery policy (HTTP/S retry config) | Message publishing |
| Subscription confirmation tracking | Platform applications |
| Import and drift detection | SMS sandbox |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a subscription |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing subscription |
| `Delete` | `ObjectContext` (exclusive) | Remove a subscription |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return subscription outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `topicArn` | Immutable | Subscription is bound to a specific topic; requires delete + recreate |
| `protocol` | Immutable | Delivery protocol; requires delete + recreate |
| `endpoint` | Immutable | Delivery target; requires delete + recreate |
| `filterPolicy` | Mutable | Updated via `SetSubscriptionAttributes` |
| `filterPolicyScope` | Mutable | Updated via `SetSubscriptionAttributes`; `MessageAttributes` (default) or `MessageBody` |
| `rawMessageDelivery` | Mutable | Updated via `SetSubscriptionAttributes`; only for SQS, HTTP/S, Firehose |
| `deliveryPolicy` | Mutable | Updated via `SetSubscriptionAttributes`; only for HTTP/S |
| `redrivePolicy` | Mutable | Updated via `SetSubscriptionAttributes`; dead-letter queue configuration |
| `subscriptionRoleArn` | Mutable | Updated via `SetSubscriptionAttributes`; only for Firehose protocol |

### Downstream Consumers

```text
${resources.my-sub.outputs.subscriptionArn}  → Informational references
${resources.my-sub.outputs.topicArn}         → Cross-references / display
${resources.my-sub.outputs.protocol}         → Cross-references / display
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeCustom`

SNS subscriptions do not have a user-chosen unique name. A subscription is uniquely
identified by the combination of topic ARN, protocol, and endpoint. The key encodes
all three components:

```text
region~topicArn~protocol~endpoint
```

Example:

```text
us-east-1~arn:aws:sns:us-east-1:123456789012:order-notifications~lambda~arn:aws:lambda:us-east-1:123456789012:function:process-order
```

> **Key length**: These keys can be long due to embedded ARNs. This is acceptable
> because Restate Virtual Object keys have no practical length limit, and the key
> is only used for RPC routing — never stored as an AWS resource name.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region`, `spec.topicArn`,
  `spec.protocol`, and `spec.endpoint`. Returns
  `region~topicArn~protocol~endpoint`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. The
  `resourceID` is the subscription ARN. The adapter resolves the subscription ARN
  to extract the topic ARN, protocol, and endpoint via `GetSubscriptionAttributes`,
  then rebuilds the key. This requires a two-phase import: the adapter calls AWS to
  look up the subscription details before constructing the key.

### No Ownership Tags

Subscriptions are identified by their AWS-assigned ARN (globally unique). There is
no name-collision risk. The combination of topic+protocol+endpoint is enforced as
unique by AWS — subscribing the same endpoint with the same protocol to the same
topic returns the existing subscription ARN (idempotent).

---

## 3. File Inventory

```text
✦ schemas/aws/sns/subscription.cue                       — CUE schema for SNSSubscription
✦ internal/drivers/snssub/types.go                        — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/snssub/aws.go                          — SubscriptionAPI interface + realSubscriptionAPI
✦ internal/drivers/snssub/drift.go                        — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/snssub/driver.go                       — SNSSubscriptionDriver Virtual Object
✦ internal/drivers/snssub/driver_test.go                  — Unit tests for driver (mocked AWS)
✦ internal/drivers/snssub/aws_test.go                     — Unit tests for error classification
✦ internal/drivers/snssub/drift_test.go                   — Unit tests for drift detection
✦ internal/core/provider/snssub_adapter.go                — SNSSubscriptionAdapter implementing provider.Adapter
✦ internal/core/provider/snssub_adapter_test.go           — Unit tests for adapter
✦ tests/integration/sns_subscription_driver_test.go       — Integration tests
✎ internal/infra/awsclient/client.go                      — Uses existing NewSNSClient factory (shared with Topic)
✎ cmd/praxis-storage/main.go                              — Bind SNSSubscription driver
✎ internal/core/provider/registry.go                      — Add NewSNSSubscriptionAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/sns/subscription.cue`

```cue
package sns

#SNSSubscription: {
    apiVersion: "praxis.io/v1"
    kind:       "SNSSubscription"

    metadata: {
        // name is the logical name for this subscription within the Praxis template.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region where the subscription is created.
        region: string

        // topicArn is the ARN of the SNS topic to subscribe to.
        topicArn: string & =~"^arn:aws:sns:[a-z0-9-]+:[0-9]{12}:.+$"

        // protocol is the delivery protocol.
        // Determines how messages are delivered to the endpoint.
        protocol: "http" | "https" | "email" | "email-json" | "sms" | "sqs" | "lambda" | "firehose" | "application"

        // endpoint is the delivery target.
        // The format depends on the protocol:
        //   lambda    — Lambda function ARN
        //   sqs       — SQS queue ARN
        //   http/s    — URL
        //   email     — email address
        //   sms       — phone number (E.164 format)
        //   firehose  — Firehose delivery stream ARN
        //   application — platform application endpoint ARN
        endpoint: string

        // filterPolicy is a JSON filter policy document.
        // Messages that do not match the filter policy are not delivered.
        // Supports exact value matching, prefix matching, numeric matching,
        // exists/not-exists, and IP address matching.
        filterPolicy?: string

        // filterPolicyScope determines where the filter policy is applied.
        // "MessageAttributes" (default) — filter on message attributes
        // "MessageBody" — filter on the message body (payload-based filtering)
        filterPolicyScope?: "MessageAttributes" | "MessageBody"

        // rawMessageDelivery enables raw message delivery.
        // When true, the original message is delivered without the SNS metadata wrapper.
        // Only supported for SQS, HTTP/S, and Firehose protocols.
        rawMessageDelivery?: bool

        // deliveryPolicy is a JSON delivery policy for HTTP/S subscriptions.
        // Controls retry behavior (backoff function, max retries, throttle) for
        // HTTP/S endpoints. Only relevant for http and https protocols.
        deliveryPolicy?: string

        // redrivePolicy is a JSON redrive policy for dead-letter queue configuration.
        // Specifies the ARN of the SQS queue to use as a dead-letter queue when
        // message delivery fails after all retries are exhausted.
        // Format: {"deadLetterTargetArn": "arn:aws:sqs:..."}
        redrivePolicy?: string

        // subscriptionRoleArn is the IAM role ARN for Firehose delivery.
        // Only required when protocol is "firehose". SNS assumes this role to
        // deliver messages to the Firehose delivery stream.
        subscriptionRoleArn?: string
    }

    outputs?: {
        subscriptionArn: string
        topicArn:        string
        protocol:        string
        endpoint:        string
        owner:           string
    }
}
```

### Key Design Decisions

- **`topicArn` as full ARN**: Subscriptions always reference a topic by ARN. This is
  what AWS requires for the `Subscribe` API call. In templates, users typically use
  an output expression: `"${resources.my-topic.outputs.topicArn}"`.

- **`protocol` as enum**: The set of valid protocols is fixed and well-defined by AWS.
  Using a CUE enum provides compile-time validation.

- **`filterPolicy` as JSON string**: Filter policies have a complex nested structure
  with multiple operator types. JSON string is the simplest representation and
  matches the AWS API.

- **`redrivePolicy` as JSON string**: The redrive policy is a simple JSON object
  with a single key (`deadLetterTargetArn`). Keeping it as JSON matches the AWS
  API and avoids introducing a nested struct for a single field.

- **`subscriptionRoleArn` only for Firehose**: Only the Firehose protocol requires
  an IAM role for delivery. The schema documents this restriction; the driver
  validates it at provision time.

---

## Step 2 — Driver Types

**File**: `internal/drivers/snssub/types.go`

```go
package snssub

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "SNSSubscription"

// SNSSubscriptionSpec is the desired state for an SNS subscription.
type SNSSubscriptionSpec struct {
    Account              string `json:"account,omitempty"`
    Region               string `json:"region"`
    TopicArn             string `json:"topicArn"`
    Protocol             string `json:"protocol"`
    Endpoint             string `json:"endpoint"`
    FilterPolicy         string `json:"filterPolicy,omitempty"`
    FilterPolicyScope    string `json:"filterPolicyScope,omitempty"`
    RawMessageDelivery   bool   `json:"rawMessageDelivery,omitempty"`
    DeliveryPolicy       string `json:"deliveryPolicy,omitempty"`
    RedrivePolicy        string `json:"redrivePolicy,omitempty"`
    SubscriptionRoleArn  string `json:"subscriptionRoleArn,omitempty"`
    ManagedKey           string `json:"managedKey,omitempty"`
}

// SNSSubscriptionOutputs is produced after provisioning and stored in Restate K/V.
type SNSSubscriptionOutputs struct {
    SubscriptionArn string `json:"subscriptionArn"`
    TopicArn        string `json:"topicArn"`
    Protocol        string `json:"protocol"`
    Endpoint        string `json:"endpoint"`
    Owner           string `json:"owner"`
}

// ObservedState captures the actual configuration from AWS.
type ObservedState struct {
    SubscriptionArn      string `json:"subscriptionArn"`
    TopicArn             string `json:"topicArn"`
    Protocol             string `json:"protocol"`
    Endpoint             string `json:"endpoint"`
    Owner                string `json:"owner"`
    FilterPolicy         string `json:"filterPolicy,omitempty"`
    FilterPolicyScope    string `json:"filterPolicyScope,omitempty"`
    RawMessageDelivery   bool   `json:"rawMessageDelivery"`
    DeliveryPolicy       string `json:"deliveryPolicy,omitempty"`
    RedrivePolicy        string `json:"redrivePolicy,omitempty"`
    SubscriptionRoleArn  string `json:"subscriptionRoleArn,omitempty"`
    ConfirmationStatus   string `json:"confirmationStatus"`
    PendingConfirmation  bool   `json:"pendingConfirmation"`
}

// SNSSubscriptionState is the single atomic state object stored under drivers.StateKey.
type SNSSubscriptionState struct {
    Desired            SNSSubscriptionSpec    `json:"desired"`
    Observed           ObservedState          `json:"observed"`
    Outputs            SNSSubscriptionOutputs `json:"outputs"`
    Status             types.ResourceStatus   `json:"status"`
    Mode               types.Mode             `json:"mode"`
    Error              string                 `json:"error,omitempty"`
    Generation         int64                  `json:"generation"`
    LastReconcile      string                 `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                   `json:"reconcileScheduled"`
}
```

### Why These Fields

- **`ConfirmationStatus` in ObservedState**: Tracks whether the subscription is
  confirmed, pending confirmation, or deleted. HTTP/HTTPS and email subscriptions
  require endpoint confirmation before they become active.
- **`PendingConfirmation`**: Boolean shortcut for checking if the subscription is in
  `PendingConfirmation` state. The driver tracks this to avoid attempting attribute
  updates on unconfirmed subscriptions (AWS returns errors for most attribute changes
  on pending subscriptions).
- **`Owner`**: The AWS account ID that owns the subscription. Returned by
  `GetSubscriptionAttributes`.
- **No tags**: SNS subscriptions do not support tags. This is an AWS limitation.

---

## Step 3 — AWS API Abstraction Layer

**File**: `internal/drivers/snssub/aws.go`

### SubscriptionAPI Interface

```go
type SubscriptionAPI interface {
    // Subscribe creates a new subscription to a topic.
    // Returns the subscription ARN. For protocols requiring confirmation
    // (HTTP/S, email), the ARN is "PendingConfirmation" until confirmed.
    // For auto-confirmed protocols (Lambda, SQS), returns the actual ARN.
    Subscribe(ctx context.Context, spec SNSSubscriptionSpec) (string, error)

    // GetSubscriptionAttributes returns the observed state of a subscription.
    GetSubscriptionAttributes(ctx context.Context, subscriptionArn string) (ObservedState, error)

    // SetSubscriptionAttribute sets a single attribute on a subscription.
    SetSubscriptionAttribute(ctx context.Context, subscriptionArn, attrName, attrValue string) error

    // Unsubscribe deletes a subscription.
    Unsubscribe(ctx context.Context, subscriptionArn string) error

    // ListSubscriptionsByTopic lists all subscriptions for a topic.
    // Used for import and FindByTopicProtocolEndpoint lookups.
    ListSubscriptionsByTopic(ctx context.Context, topicArn string) ([]SubscriptionSummary, error)

    // FindByTopicProtocolEndpoint finds a subscription matching the given
    // topic, protocol, and endpoint combination.
    FindByTopicProtocolEndpoint(ctx context.Context, topicArn, protocol, endpoint string) (string, error)
}

// SubscriptionSummary is a lightweight subscription record returned by ListSubscriptions.
type SubscriptionSummary struct {
    SubscriptionArn string
    TopicArn        string
    Protocol        string
    Endpoint        string
    Owner           string
}
```

### realSubscriptionAPI Implementation

```go
type realSubscriptionAPI struct {
    client  *sns.Client
    limiter *ratelimit.Limiter
}

func NewSubscriptionAPI(client *sns.Client) SubscriptionAPI {
    return &realSubscriptionAPI{
        client:  client,
        limiter: ratelimit.New("sns-subscription", 30, 10),
    }
}
```

### Key Implementation Details

#### `Subscribe`

```go
func (r *realSubscriptionAPI) Subscribe(ctx context.Context, spec SNSSubscriptionSpec) (string, error) {
    input := &sns.SubscribeInput{
        TopicArn: aws.String(spec.TopicArn),
        Protocol: aws.String(spec.Protocol),
        Endpoint: aws.String(spec.Endpoint),
    }

    // Set subscription attributes at creation time
    attrs := make(map[string]string)
    if spec.FilterPolicy != "" {
        attrs["FilterPolicy"] = spec.FilterPolicy
    }
    if spec.FilterPolicyScope != "" {
        attrs["FilterPolicyScope"] = spec.FilterPolicyScope
    }
    if spec.RawMessageDelivery {
        attrs["RawMessageDelivery"] = "true"
    }
    if spec.DeliveryPolicy != "" {
        attrs["DeliveryPolicy"] = spec.DeliveryPolicy
    }
    if spec.RedrivePolicy != "" {
        attrs["RedrivePolicy"] = spec.RedrivePolicy
    }
    if spec.SubscriptionRoleArn != "" {
        attrs["SubscriptionRoleArn"] = spec.SubscriptionRoleArn
    }
    if len(attrs) > 0 {
        input.Attributes = attrs
    }

    // For Lambda and SQS, enable return of the subscription ARN
    // (without this, Subscribe returns "pending confirmation" for all protocols)
    input.ReturnSubscriptionArn = aws.Bool(true)

    out, err := r.client.Subscribe(ctx, input)
    if err != nil {
        return "", err
    }

    return aws.ToString(out.SubscriptionArn), nil
}
```

> **Subscribe idempotency**: Calling `Subscribe` with the same topic, protocol, and
> endpoint returns the existing subscription ARN (for confirmed subscriptions). For
> pending subscriptions, it may resend the confirmation request.
>
> **ReturnSubscriptionArn**: Setting `ReturnSubscriptionArn=true` ensures the actual
> subscription ARN is returned even before confirmation. Without this flag, AWS
> returns `"pending confirmation"` as the ARN for protocols requiring confirmation.

#### `GetSubscriptionAttributes`

```go
func (r *realSubscriptionAPI) GetSubscriptionAttributes(ctx context.Context, subscriptionArn string) (ObservedState, error) {
    out, err := r.client.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
        SubscriptionArn: aws.String(subscriptionArn),
    })
    if err != nil {
        return ObservedState{}, err
    }

    attrs := out.Attributes
    obs := ObservedState{
        SubscriptionArn:   subscriptionArn,
        TopicArn:          attrs["TopicArn"],
        Protocol:          attrs["Protocol"],
        Endpoint:          attrs["Endpoint"],
        Owner:             attrs["Owner"],
    }

    // Optional attributes
    if v, ok := attrs["FilterPolicy"]; ok && v != "" {
        obs.FilterPolicy = v
    }
    if v, ok := attrs["FilterPolicyScope"]; ok && v != "" {
        obs.FilterPolicyScope = v
    }
    if v, ok := attrs["RawMessageDelivery"]; ok {
        obs.RawMessageDelivery = v == "true"
    }
    if v, ok := attrs["DeliveryPolicy"]; ok && v != "" {
        obs.DeliveryPolicy = v
    }
    if v, ok := attrs["RedrivePolicy"]; ok && v != "" {
        obs.RedrivePolicy = v
    }
    if v, ok := attrs["SubscriptionRoleArn"]; ok && v != "" {
        obs.SubscriptionRoleArn = v
    }

    // Confirmation status
    if v, ok := attrs["PendingConfirmation"]; ok {
        obs.PendingConfirmation = v == "true"
    }
    if v, ok := attrs["ConfirmationWasAuthenticated"]; ok {
        if v == "true" {
            obs.ConfirmationStatus = "confirmed-authenticated"
        } else if !obs.PendingConfirmation {
            obs.ConfirmationStatus = "confirmed"
        }
    }
    if obs.PendingConfirmation {
        obs.ConfirmationStatus = "pending"
    }

    return obs, nil
}
```

> **No tags API**: SNS subscriptions do not support tags. There is no
> `ListTagsForResource` call for subscriptions.

#### `SetSubscriptionAttribute`

```go
func (r *realSubscriptionAPI) SetSubscriptionAttribute(ctx context.Context, subscriptionArn, attrName, attrValue string) error {
    _, err := r.client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
        SubscriptionArn: aws.String(subscriptionArn),
        AttributeName:   aws.String(attrName),
        AttributeValue:  aws.String(attrValue),
    })
    return err
}
```

> **Per-attribute updates**: Like topics, SNS uses `SetSubscriptionAttributes` with
> one attribute at a time. The driver calls `SetSubscriptionAttribute` in separate
> `restate.Run` blocks for each changed attribute.
>
> **Pending subscriptions**: Most attribute updates fail on subscriptions in
> `PendingConfirmation` state. The driver checks `PendingConfirmation` before
> attempting updates and returns a descriptive error.

#### `Unsubscribe`

```go
func (r *realSubscriptionAPI) Unsubscribe(ctx context.Context, subscriptionArn string) error {
    _, err := r.client.Unsubscribe(ctx, &sns.UnsubscribeInput{
        SubscriptionArn: aws.String(subscriptionArn),
    })
    return err
}
```

> **Unsubscribe behavior**: `Unsubscribe` is idempotent — calling it on a
> non-existent or already-deleted subscription does not return an error. However,
> calling it with an invalid ARN format returns `InvalidParameterException`.
>
> **Owner-only deletion**: Only the subscription owner or the topic owner can
> unsubscribe. Cross-account subscriptions require careful permission management.

#### `ListSubscriptionsByTopic`

```go
func (r *realSubscriptionAPI) ListSubscriptionsByTopic(ctx context.Context, topicArn string) ([]SubscriptionSummary, error) {
    var subs []SubscriptionSummary
    var nextToken *string

    for {
        out, err := r.client.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{
            TopicArn:  aws.String(topicArn),
            NextToken: nextToken,
        })
        if err != nil {
            return nil, err
        }

        for _, s := range out.Subscriptions {
            arn := aws.ToString(s.SubscriptionArn)
            // Skip pending or deleted subscriptions
            if arn == "PendingConfirmation" || arn == "Deleted" {
                continue
            }
            subs = append(subs, SubscriptionSummary{
                SubscriptionArn: arn,
                TopicArn:        aws.ToString(s.TopicArn),
                Protocol:        aws.ToString(s.Protocol),
                Endpoint:        aws.ToString(s.Endpoint),
                Owner:           aws.ToString(s.Owner),
            })
        }

        if out.NextToken == nil {
            break
        }
        nextToken = out.NextToken
    }

    return subs, nil
}
```

#### `FindByTopicProtocolEndpoint`

```go
func (r *realSubscriptionAPI) FindByTopicProtocolEndpoint(ctx context.Context, topicArn, protocol, endpoint string) (string, error) {
    subs, err := r.ListSubscriptionsByTopic(ctx, topicArn)
    if err != nil {
        return "", err
    }

    for _, s := range subs {
        if s.Protocol == protocol && s.Endpoint == endpoint {
            return s.SubscriptionArn, nil
        }
    }

    return "", nil
}
```

> **Linear scan**: `FindByTopicProtocolEndpoint` iterates all subscriptions for a
> topic. For topics with many subscriptions (>100), this could be slow. In practice,
> most topics have fewer than 100 subscriptions. If this becomes a performance
> concern, the driver can cache subscription lists in Restate state during reconcile.

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

func isSubscriptionLimitExceeded(err error) bool {
    var sle *snstypes.SubscriptionLimitExceededException
    if errors.As(err, &sle) {
        return true
    }
    return strings.Contains(err.Error(), "SubscriptionLimitExceeded")
}

func isFilterPolicyLimitExceeded(err error) bool {
    var fple *snstypes.FilterPolicyLimitExceededException
    if errors.As(err, &fple) {
        return true
    }
    return strings.Contains(err.Error(), "FilterPolicyLimitExceeded")
}
```

---

## Step 4 — Drift Detection

**File**: `internal/drivers/snssub/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| `filterPolicy` | External change via console/CLI | JSON filter policy document |
| `filterPolicyScope` | External change via console/CLI | MessageAttributes or MessageBody |
| `rawMessageDelivery` | External change via console/CLI | Boolean flag |
| `deliveryPolicy` | External change via console/CLI | JSON delivery policy (HTTP/S only) |
| `redrivePolicy` | External change via console/CLI | JSON redrive policy |
| `subscriptionRoleArn` | External change via console/CLI | Firehose delivery role |

> **Not drift-detected**: `topicArn` (immutable — part of the key), `protocol`
> (immutable — part of the key), `endpoint` (immutable — part of the key),
> `confirmationStatus` (informational only — cannot be controlled by the driver).

### HasDrift

```go
func HasDrift(desired SNSSubscriptionSpec, observed ObservedState) bool {
    if !policiesEqual(desired.FilterPolicy, observed.FilterPolicy) {
        return true
    }
    if desired.FilterPolicyScope != "" && desired.FilterPolicyScope != observed.FilterPolicyScope {
        return true
    }
    if desired.RawMessageDelivery != observed.RawMessageDelivery {
        return true
    }
    if !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
        return true
    }
    if !policiesEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
        return true
    }
    if desired.SubscriptionRoleArn != observed.SubscriptionRoleArn {
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
func ComputeFieldDiffs(desired SNSSubscriptionSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff

    if !policiesEqual(desired.FilterPolicy, observed.FilterPolicy) {
        diffs = append(diffs, types.FieldDiff{
            Field: "filterPolicy", Desired: desired.FilterPolicy, Observed: observed.FilterPolicy,
        })
    }
    if desired.FilterPolicyScope != "" && desired.FilterPolicyScope != observed.FilterPolicyScope {
        diffs = append(diffs, types.FieldDiff{
            Field: "filterPolicyScope", Desired: desired.FilterPolicyScope, Observed: observed.FilterPolicyScope,
        })
    }
    if desired.RawMessageDelivery != observed.RawMessageDelivery {
        diffs = append(diffs, types.FieldDiff{
            Field:    "rawMessageDelivery",
            Desired:  fmt.Sprintf("%v", desired.RawMessageDelivery),
            Observed: fmt.Sprintf("%v", observed.RawMessageDelivery),
        })
    }
    if !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
        diffs = append(diffs, types.FieldDiff{
            Field: "deliveryPolicy", Desired: desired.DeliveryPolicy, Observed: observed.DeliveryPolicy,
        })
    }
    if !policiesEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
        diffs = append(diffs, types.FieldDiff{
            Field: "redrivePolicy", Desired: desired.RedrivePolicy, Observed: observed.RedrivePolicy,
        })
    }
    if desired.SubscriptionRoleArn != observed.SubscriptionRoleArn {
        diffs = append(diffs, types.FieldDiff{
            Field: "subscriptionRoleArn", Desired: desired.SubscriptionRoleArn, Observed: observed.SubscriptionRoleArn,
        })
    }

    return diffs
}
```

---

## Step 5 — Driver Implementation

**File**: `internal/drivers/snssub/driver.go`

### Constructor

```go
type SNSSubscriptionDriver struct {
    auth authservice.AuthClient
    apiFactory func(aws.Config) SubscriptionAPI
}

func NewSNSSubscriptionDriver(auth authservice.AuthClient) *SNSSubscriptionDriver {
    return NewSNSSubscriptionDriverWithFactory(auth, func(cfg aws.Config) SubscriptionAPI {
        return NewSubscriptionAPI(awsclient.NewSNSClient(cfg))
    })
}

func NewSNSSubscriptionDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SubscriptionAPI) *SNSSubscriptionDriver {
    if accounts == nil {
        auth = authservice.NewAuthClient()
    }
    if factory == nil {
        factory = func(cfg aws.Config) SubscriptionAPI {
            return NewSubscriptionAPI(awsclient.NewSNSClient(cfg))
        }
    }
    return &SNSSubscriptionDriver{accounts: accounts, apiFactory: factory}
}

func (SNSSubscriptionDriver) ServiceName() string { return ServiceName }
```

### Provision

Provision handles three cases:

1. **New subscription**: Create the subscription and set attributes.
2. **Unchanged subscription**: Return existing outputs (idempotent).
3. **Changed attributes**: Update the changed attributes.

```go
func (d *SNSSubscriptionDriver) Provision(ctx restate.ObjectContext, spec SNSSubscriptionSpec) (SNSSubscriptionOutputs, error) {
    state, _ := restate.Get[*SNSSubscriptionState](ctx, drivers.StateKey)
    api := d.buildAPI(spec.Account, spec.Region)

    // Validate protocol-specific constraints
    if err := validateProtocolConstraints(spec); err != nil {
        return SNSSubscriptionOutputs{}, restate.TerminalError(err, 400)
    }

    // If existing state and spec hasn't changed, return early
    if state != nil && state.Outputs.SubscriptionArn != "" && !specChanged(spec, state.Desired) {
        return state.Outputs, nil
    }

    // Write pending state
    newState := &SNSSubscriptionState{
        Desired:    spec,
        Status:     types.StatusProvisioning,
        Mode:       drivers.DefaultMode(""),
        Generation: stateGeneration(state) + 1,
    }
    restate.Set(ctx, drivers.StateKey, newState)

    // Subscribe (idempotent)
    subscriptionArn, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
        return api.Subscribe(rc, spec)
    })
    if err != nil {
        if isInvalidParameter(err) {
            return SNSSubscriptionOutputs{}, restate.TerminalError(
                fmt.Errorf("invalid subscription configuration: %w", err), 400)
        }
        if isSubscriptionLimitExceeded(err) {
            return SNSSubscriptionOutputs{}, restate.TerminalError(
                fmt.Errorf("subscription limit exceeded for topic: %w", err), 429)
        }
        return SNSSubscriptionOutputs{}, err
    }

    // Check for pending confirmation
    isPending := subscriptionArn == "PendingConfirmation" || subscriptionArn == "pending confirmation"
    if isPending {
        // For HTTP/HTTPS/email — subscription exists but requires external confirmation
        // Try to find the actual subscription ARN
        realArn, findErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.FindByTopicProtocolEndpoint(rc, spec.TopicArn, spec.Protocol, spec.Endpoint)
        })
        if findErr == nil && realArn != "" {
            subscriptionArn = realArn
        }
    }

    // Update mutable attributes if this is a convergence (spec changed)
    if state != nil && state.Outputs.SubscriptionArn != "" && !isPending {
        if err := d.convergeAttributes(ctx, api, state.Outputs.SubscriptionArn, spec, state.Desired); err != nil {
            return SNSSubscriptionOutputs{}, err
        }
    }

    // Get observed state (only for confirmed subscriptions)
    var observed ObservedState
    if !isPending && subscriptionArn != "" {
        obs, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
            return api.GetSubscriptionAttributes(rc, subscriptionArn)
        })
        if err != nil {
            observed = ObservedState{
                SubscriptionArn: subscriptionArn,
                TopicArn:        spec.TopicArn,
                Protocol:        spec.Protocol,
                Endpoint:        spec.Endpoint,
            }
        } else {
            observed = obs
        }
    } else {
        observed = ObservedState{
            SubscriptionArn:    subscriptionArn,
            TopicArn:           spec.TopicArn,
            Protocol:           spec.Protocol,
            Endpoint:           spec.Endpoint,
            PendingConfirmation: true,
            ConfirmationStatus: "pending",
        }
    }

    outputs := SNSSubscriptionOutputs{
        SubscriptionArn: subscriptionArn,
        TopicArn:        spec.TopicArn,
        Protocol:        spec.Protocol,
        Endpoint:        spec.Endpoint,
        Owner:           observed.Owner,
    }

    status := types.StatusReady
    if isPending {
        status = types.StatusPending
    }

    newState.Observed = observed
    newState.Outputs = outputs
    newState.Status = status
    newState.Error = ""
    restate.Set(ctx, drivers.StateKey, newState)

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### validateProtocolConstraints

```go
func validateProtocolConstraints(spec SNSSubscriptionSpec) error {
    switch spec.Protocol {
    case "lambda":
        if !strings.HasPrefix(spec.Endpoint, "arn:aws:lambda:") {
            return fmt.Errorf("lambda protocol requires a Lambda function ARN as endpoint")
        }
    case "sqs":
        if !strings.HasPrefix(spec.Endpoint, "arn:aws:sqs:") {
            return fmt.Errorf("sqs protocol requires an SQS queue ARN as endpoint")
        }
    case "firehose":
        if !strings.HasPrefix(spec.Endpoint, "arn:aws:firehose:") {
            return fmt.Errorf("firehose protocol requires a Firehose delivery stream ARN as endpoint")
        }
        if spec.SubscriptionRoleArn == "" {
            return fmt.Errorf("firehose protocol requires a subscriptionRoleArn")
        }
    case "email", "email-json":
        if !strings.Contains(spec.Endpoint, "@") {
            return fmt.Errorf("%s protocol requires an email address as endpoint", spec.Protocol)
        }
    case "http":
        if !strings.HasPrefix(spec.Endpoint, "http://") {
            return fmt.Errorf("http protocol requires an HTTP URL as endpoint")
        }
    case "https":
        if !strings.HasPrefix(spec.Endpoint, "https://") {
            return fmt.Errorf("https protocol requires an HTTPS URL as endpoint")
        }
    case "sms":
        if !strings.HasPrefix(spec.Endpoint, "+") {
            return fmt.Errorf("sms protocol requires a phone number in E.164 format (e.g., +12065551234)")
        }
    }

    // RawMessageDelivery validation
    if spec.RawMessageDelivery {
        switch spec.Protocol {
        case "sqs", "http", "https", "firehose":
            // Valid — these protocols support raw message delivery
        default:
            return fmt.Errorf("rawMessageDelivery is only supported for sqs, http, https, and firehose protocols")
        }
    }

    // DeliveryPolicy validation
    if spec.DeliveryPolicy != "" {
        switch spec.Protocol {
        case "http", "https":
            // Valid — delivery policy applies to HTTP/S subscriptions
        default:
            return fmt.Errorf("deliveryPolicy is only supported for http and https protocols")
        }
    }

    return nil
}
```

### convergeAttributes

```go
// convergeAttributes updates each subscription attribute that has changed.
func (d *SNSSubscriptionDriver) convergeAttributes(ctx restate.ObjectContext, api SubscriptionAPI, subscriptionArn string, desired, previous SNSSubscriptionSpec) error {
    type attrUpdate struct {
        name string
        val  string
    }

    var updates []attrUpdate

    if desired.FilterPolicy != previous.FilterPolicy {
        val := desired.FilterPolicy
        if val == "" {
            val = "{}" // Empty filter policy removes filtering
        }
        updates = append(updates, attrUpdate{"FilterPolicy", val})
    }
    if desired.FilterPolicyScope != previous.FilterPolicyScope {
        updates = append(updates, attrUpdate{"FilterPolicyScope", desired.FilterPolicyScope})
    }
    if desired.RawMessageDelivery != previous.RawMessageDelivery {
        val := "false"
        if desired.RawMessageDelivery {
            val = "true"
        }
        updates = append(updates, attrUpdate{"RawMessageDelivery", val})
    }
    if desired.DeliveryPolicy != previous.DeliveryPolicy {
        val := desired.DeliveryPolicy
        if val == "" {
            val = "{}" // Empty delivery policy resets to default
        }
        updates = append(updates, attrUpdate{"DeliveryPolicy", val})
    }
    if desired.RedrivePolicy != previous.RedrivePolicy {
        val := desired.RedrivePolicy
        if val == "" {
            val = "{}" // Empty redrive policy removes the DLQ
        }
        updates = append(updates, attrUpdate{"RedrivePolicy", val})
    }
    if desired.SubscriptionRoleArn != previous.SubscriptionRoleArn {
        updates = append(updates, attrUpdate{"SubscriptionRoleArn", desired.SubscriptionRoleArn})
    }

    for _, u := range updates {
        name, val := u.name, u.val
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.SetSubscriptionAttribute(rc, subscriptionArn, name, val)
        }); err != nil {
            return fmt.Errorf("set attribute %s: %w", name, err)
        }
    }

    return nil
}
```

### Import

```go
func (d *SNSSubscriptionDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SNSSubscriptionOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    // ResourceID should be the subscription ARN
    subscriptionArn := ref.ResourceID
    if !strings.HasPrefix(subscriptionArn, "arn:aws:sns:") {
        return SNSSubscriptionOutputs{}, restate.TerminalError(
            fmt.Errorf("SNSSubscription import requires a subscription ARN as resourceID, got: %s", ref.ResourceID), 400)
    }

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetSubscriptionAttributes(rc, subscriptionArn)
    })
    if err != nil {
        if isNotFound(err) {
            return SNSSubscriptionOutputs{}, restate.TerminalError(
                fmt.Errorf("subscription %q not found", subscriptionArn), 404)
        }
        return SNSSubscriptionOutputs{}, err
    }

    spec := specFromObserved(observed, ref)
    outputs := SNSSubscriptionOutputs{
        SubscriptionArn: subscriptionArn,
        TopicArn:        observed.TopicArn,
        Protocol:        observed.Protocol,
        Endpoint:        observed.Endpoint,
        Owner:           observed.Owner,
    }

    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &SNSSubscriptionState{
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
func (d *SNSSubscriptionDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*SNSSubscriptionState](ctx, drivers.StateKey)
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
        return restate.Void{}, api.Unsubscribe(rc, state.Outputs.SubscriptionArn)
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
func (d *SNSSubscriptionDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*SNSSubscriptionState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    if state == nil {
        return types.ReconcileResult{Status: "no-state"}, nil
    }

    state.ReconcileScheduled = false
    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    // Handle pending confirmation subscriptions
    if state.Observed.PendingConfirmation {
        // Try to find the subscription ARN (may have been confirmed since last check)
        realArn, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.FindByTopicProtocolEndpoint(rc, state.Desired.TopicArn, state.Desired.Protocol, state.Desired.Endpoint)
        })
        if err != nil || realArn == "" {
            // Still pending — reschedule
            state.LastReconcile = time.Now().UTC().Format(time.RFC3339)
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "pending-confirmation"}, nil
        }

        // Subscription was confirmed — update ARN and continue
        state.Outputs.SubscriptionArn = realArn
    }

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetSubscriptionAttributes(rc, state.Outputs.SubscriptionArn)
    })
    if err != nil {
        if isNotFound(err) {
            state.Status = types.StatusError
            state.Error = "subscription not found — may have been deleted externally"
            state.Observed = ObservedState{}
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }
        return types.ReconcileResult{}, err
    }

    state.Observed = observed
    state.LastReconcile = time.Now().UTC().Format(time.RFC3339)

    // Update pending confirmation status
    if !observed.PendingConfirmation && state.Status == types.StatusPending {
        state.Status = types.StatusReady
    }

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
        // Only correct drift on confirmed subscriptions
        if observed.PendingConfirmation {
            result.Status = "drift-detected-pending"
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return result, nil
        }

        // Correct drift
        if err := d.convergeAttributes(ctx, api, state.Outputs.SubscriptionArn, state.Desired, specFromObserved(observed, types.ImportRef{})); err != nil {
            state.Error = fmt.Sprintf("drift correction failed: %v", err)
            state.Status = types.StatusError
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
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

Follow the standard pattern (identical to SNS Topic and other drivers).

---

## Step 6 — Provider Adapter

**File**: `internal/core/provider/snssub_adapter.go`

```go
type SNSSubscriptionAdapter struct {
    auth authservice.AuthClient
}

func NewSNSSubscriptionAdapterWithAuth(auth authservice.AuthClient) *SNSSubscriptionAdapter {
    return &SNSSubscriptionAdapter{accounts: accounts}
}

func (a *SNSSubscriptionAdapter) Kind() string       { return snssub.ServiceName }
func (a *SNSSubscriptionAdapter) ServiceName() string { return snssub.ServiceName }
func (a *SNSSubscriptionAdapter) Scope() KeyScope     { return KeyScopeCustom }

func (a *SNSSubscriptionAdapter) BuildKey(doc json.RawMessage) (string, error) {
    var parsed struct {
        Spec struct {
            Region   string `json:"region"`
            TopicArn string `json:"topicArn"`
            Protocol string `json:"protocol"`
            Endpoint string `json:"endpoint"`
        } `json:"spec"`
    }
    if err := json.Unmarshal(doc, &parsed); err != nil {
        return "", err
    }
    if parsed.Spec.Region == "" || parsed.Spec.TopicArn == "" ||
        parsed.Spec.Protocol == "" || parsed.Spec.Endpoint == "" {
        return "", fmt.Errorf("SNSSubscription requires spec.region, spec.topicArn, spec.protocol, and spec.endpoint")
    }
    return parsed.Spec.Region + "~" + parsed.Spec.TopicArn + "~" +
        parsed.Spec.Protocol + "~" + parsed.Spec.Endpoint, nil
}

func (a *SNSSubscriptionAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // For import, resourceID is the subscription ARN.
    // We need to look up the subscription attributes to extract topic/protocol/endpoint.
    // The adapter cannot make AWS calls directly — it returns a partial key using the ARN.
    // The driver's Import handler resolves the full key.
    return region + "~" + resourceID, nil
}
```

> **Import key limitation**: Unlike most adapters, the subscription adapter cannot
> build the canonical key from `BuildImportKey` alone because the canonical key
> requires the topic ARN, protocol, and endpoint — which are only available from
> `GetSubscriptionAttributes`. The driver's `Import` handler resolves this by
> looking up the subscription details and constructing the correct key. The import
> key returned here is sufficient for initial routing.

---

## Step 7 — Registry Integration

**File**: `internal/core/provider/registry.go` — **MODIFY**

Add `NewSNSSubscriptionAdapterWithAuth(auth)` to `NewRegistry()`.

---

## Step 8 — Storage Driver Pack Entry Point

**File**: `cmd/praxis-storage/main.go` — **MODIFY**

```go
import "github.com/shirvan/praxis/internal/drivers/snssub"

Bind(restate.Reflect(snssub.NewSNSSubscriptionDriver(auth)))
```

---

## Step 9 — Docker Compose & Justfile

### Docker Compose

No additional changes beyond what the SNS Topic driver requires. Both drivers share
the praxis-storage container and the `sns` service in LocalStack.

### Justfile Additions

```just
test-snssub:
    go test ./internal/drivers/snssub/... -v -count=1 -race

test-snssub-integration:
    go test ./tests/integration/... -run TestSNSSubscription -v -timeout=3m
```

---

## Step 10 — Unit Tests

**File**: `internal/drivers/snssub/driver_test.go`

| Test | Description |
|---|---|
| `TestProvision_LambdaSubscription` | Creates Lambda subscription; verifies auto-confirmed ARN and outputs |
| `TestProvision_SQSSubscription` | Creates SQS subscription; verifies auto-confirmed ARN and outputs |
| `TestProvision_HTTPSSubscription` | Creates HTTPS subscription; verifies pending confirmation handling |
| `TestProvision_EmailSubscription` | Creates email subscription; verifies pending confirmation state |
| `TestProvision_FirehoseSubscription` | Creates Firehose subscription with role ARN; verifies outputs |
| `TestProvision_NoChange` | Same spec; verifies idempotent return |
| `TestProvision_UpdateFilterPolicy` | Changed filter policy; verifies attribute update |
| `TestProvision_UpdateRawMessageDelivery` | Changed raw message delivery; verifies attribute update |
| `TestProvision_UpdateRedrivePolicy` | Changed redrive policy; verifies attribute update |
| `TestProvision_InvalidProtocolEndpoint` | Lambda protocol with SQS ARN; verifies 400 error |
| `TestProvision_RawMessageDeliveryInvalidProtocol` | RawMessageDelivery on email; verifies 400 error |
| `TestProvision_FirehoseWithoutRole` | Firehose without subscriptionRoleArn; verifies 400 error |
| `TestProvision_SubscriptionLimitExceeded` | Subscription limit; verifies 429 error |
| `TestImport_Success` | Imports existing subscription by ARN; verifies state |
| `TestImport_NotFound` | Subscription doesn't exist; verifies 404 |
| `TestImport_InvalidResourceID` | Non-ARN resource ID; verifies 400 |
| `TestDelete_Managed` | Deletes subscription; verifies cleanup |
| `TestDelete_Observed` | Cannot delete observed; verifies 403 |
| `TestDelete_AlreadyGone` | Subscription already deleted; verifies idempotent |
| `TestReconcile_NoDrift` | Attributes match; verifies ok |
| `TestReconcile_FilterPolicyDrifted` | Filter policy changed externally; verifies drift correction |
| `TestReconcile_RawMessageDeliveryDrifted` | Raw message delivery changed; verifies drift correction |
| `TestReconcile_PendingConfirmation` | Subscription still pending; verifies pending status |
| `TestReconcile_ConfirmationResolved` | Previously pending subscription now confirmed; verifies status update |
| `TestReconcile_SubscriptionDeleted` | Subscription deleted externally; verifies error state |

**File**: `internal/drivers/snssub/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | All fields match; no drift |
| `TestHasDrift_FilterPolicyChanged` | Filter policy differs; drift detected |
| `TestHasDrift_FilterPolicyWhitespace` | Filter policy JSON differs only in whitespace; no drift |
| `TestHasDrift_RawMessageDeliveryChanged` | Raw message delivery differs; drift detected |
| `TestHasDrift_RedrivePolicyChanged` | Redrive policy differs; drift detected |
| `TestHasDrift_DeliveryPolicyChanged` | Delivery policy differs; drift detected |
| `TestHasDrift_SubscriptionRoleArnChanged` | Subscription role ARN differs; drift detected |

**File**: `internal/drivers/snssub/aws_test.go`

| Test | Description |
|---|---|
| `TestIsNotFound` | Validates NotFoundException classification |
| `TestIsInvalidParameter` | Validates InvalidParameterException classification |
| `TestIsThrottled` | Validates ThrottledException classification |
| `TestIsAuthError` | Validates AuthorizationErrorException classification |
| `TestIsSubscriptionLimitExceeded` | Validates SubscriptionLimitExceededException classification |
| `TestIsFilterPolicyLimitExceeded` | Validates FilterPolicyLimitExceededException classification |

---

## Step 11 — Integration Tests

**File**: `tests/integration/sns_subscription_driver_test.go`

| Test | Description |
|---|---|
| `TestSNSSubscription_LambdaProtocol` | Create topic + Lambda subscription, verify attributes in AWS |
| `TestSNSSubscription_SQSProtocol` | Create topic + SQS subscription, verify attributes |
| `TestSNSSubscription_SQSWithFilterPolicy` | Create with filter policy, verify filter applied |
| `TestSNSSubscription_SQSWithRawMessageDelivery` | Create with raw message delivery, verify attribute |
| `TestSNSSubscription_SQSWithRedrivePolicy` | Create with DLQ redrive policy, verify attribute |
| `TestSNSSubscription_HTTPSProtocol` | Create HTTPS subscription, verify pending state |
| `TestSNSSubscription_UpdateFilterPolicy` | Create, update filter policy, verify change |
| `TestSNSSubscription_RemoveFilterPolicy` | Create with filter policy, remove it, verify removal |
| `TestSNSSubscription_Import` | Create via AWS API, import by ARN, verify state |
| `TestSNSSubscription_Delete` | Create then delete, verify subscription gone |
| `TestSNSSubscription_Reconcile` | Create, externally change filter policy, reconcile in managed mode |
| `TestSNSSubscription_IdempotentSubscribe` | Subscribe twice with same parameters, verify same ARN returned |

### LocalStack Considerations

- LocalStack supports SNS `Subscribe`, `Unsubscribe`, `GetSubscriptionAttributes`,
  `SetSubscriptionAttributes`, and `ListSubscriptionsByTopic`.
- Lambda and SQS subscriptions are auto-confirmed in LocalStack.
- HTTP/HTTPS subscription confirmation may behave differently in LocalStack
  (auto-confirmed or always pending, depending on version).
- Filter policies and raw message delivery are supported in LocalStack.
- Redrive policies require SQS to be available in LocalStack's `SERVICES` list.

---

## SNS-Subscription-Specific Design Decisions

### 1. Composite Key Strategy

**Decision**: The Virtual Object key is `region~topicArn~protocol~endpoint` using
`KeyScopeCustom`.

**Rationale**: SNS subscriptions have no user-chosen name. AWS identifies them by a
system-assigned ARN. The natural unique identifier is the combination of topic,
protocol, and endpoint — you cannot have two subscriptions with the same
topic+protocol+endpoint. Using this combination as the key ensures idempotent
provisioning and correct routing.

### 2. Pending Confirmation Handling

**Decision**: The driver returns `StatusPending` for subscriptions that require
confirmation (HTTP/HTTPS, email). Reconcile periodically checks whether the
subscription has been confirmed.

**Rationale**: Some protocols require the endpoint owner to confirm the
subscription. This is an external action the driver cannot automate. The driver
creates the subscription and tracks its pending state. Reconcile checks for
confirmation and transitions to `StatusReady` once confirmed. This allows the
DAG to complete with dependent resources in a provisioned-but-pending state.

### 3. Protocol-Specific Validation

**Decision**: The driver validates protocol-endpoint consistency before calling AWS.

**Rationale**: Catching protocol-endpoint mismatches early (e.g., Lambda protocol
with an SQS ARN) provides clearer error messages than relying on AWS API errors.
The validation is lightweight and covers the most common mistakes.

### 4. No Ownership Tags

**Decision**: The subscription driver does not use `praxis:managed-key` tags.

**Rationale**: SNS subscriptions do not support resource tags (AWS limitation).
Subscriptions are uniquely identified by their AWS-assigned ARN and the
topic+protocol+endpoint combination. The `Subscribe` API is idempotent —
subscribing the same endpoint returns the existing subscription ARN.

### 5. Subscribe with ReturnSubscriptionArn

**Decision**: The driver always sets `ReturnSubscriptionArn=true` in the
`Subscribe` call.

**Rationale**: Without this flag, `Subscribe` returns `"pending confirmation"` as
the subscription ARN for protocols requiring confirmation. With the flag set, AWS
returns the actual subscription ARN even before confirmation. This allows the
driver to track the subscription immediately.

### 6. Empty JSON for Attribute Removal

**Decision**: To remove a filter policy, delivery policy, or redrive policy, the
driver sets the attribute value to `"{}"` (empty JSON object).

**Rationale**: `SetSubscriptionAttributes` does not accept null or empty string for
JSON policy attributes. AWS interprets an empty JSON object as "no policy" for
filter policies and redrive policies. This is the documented AWS approach for
clearing these attributes.

### 7. Confirmation Polling via Reconcile

**Decision**: The driver does not poll for confirmation in the `Provision` handler.
Instead, the reconcile loop checks for confirmation transitions.

**Rationale**: Confirmation may take minutes (HTTP), hours (email), or never happen.
Blocking `Provision` on confirmation would tie up the Restate invocation
indefinitely. Returning `StatusPending` immediately and checking during reconcile
is consistent with how Restate drivers handle long-running external processes.

### 8. Import Requires Subscription ARN

**Decision**: `Import` requires the full subscription ARN as `resourceID`, unlike
the Topic driver which accepts both name and ARN.

**Rationale**: Subscriptions cannot be looked up by name (they have no name). The
topic+protocol+endpoint combination could theoretically be used, but parsing a
composite ID is error-prone. The subscription ARN is the canonical identifier and
is always available from the AWS console or CLI.

---

## Design Decisions (Resolved)

### Key Scope

**Decision**: `KeyScopeCustom` with key format `region~topicArn~protocol~endpoint`.

**Rationale**: Subscriptions lack a user-chosen name and are naturally identified by
the topic+protocol+endpoint triple. `KeyScopeCustom` allows the adapter to build
this composite key from the spec.

### Runtime Pack

**Decision**: Hosted in `praxis-storage` alongside the SNS Topic driver.

**Rationale**: Same domain grouping as the Topic driver. The docker-compose.yaml
header lists SNS as a praxis-storage service.

### Default Import Mode

**Decision**: Import defaults to `ModeObserved`.

**Rationale**: Subscriptions connect topics to endpoints that may be managed by
other teams. Observed mode prevents accidental unsubscription. Users can override to
`ModeManaged` for full lifecycle control.

### Auto-Confirmed vs Manual Protocols

**Decision**: The driver handles both auto-confirmed (Lambda, SQS, Firehose) and
manual-confirmation protocols (HTTP/S, email) in a unified code path.

**Rationale**: The Subscribe API is the same for all protocols. The difference is
only in the confirmation requirement. A single code path with a pending-state check
is simpler than separate protocol-specific handlers.

---

## Checklist

### Implementation

- [x] `schemas/aws/sns/subscription.cue`
- [x] `internal/drivers/snssub/types.go`
- [x] `internal/drivers/snssub/aws.go`
- [x] `internal/drivers/snssub/drift.go`
- [x] `internal/drivers/snssub/driver.go`
- [x] `internal/core/provider/snssub_adapter.go`

### Tests

- [x] `internal/drivers/snssub/driver_test.go`
- [x] `internal/drivers/snssub/aws_test.go`
- [x] `internal/drivers/snssub/drift_test.go`
- [x] `internal/core/provider/snssub_adapter_test.go`
- [x] `tests/integration/sns_subscription_driver_test.go`

### Integration

- [x] `cmd/praxis-storage/main.go` — Bind driver
- [x] `internal/core/provider/registry.go` — Register adapter
- [x] `justfile` — Add test targets
