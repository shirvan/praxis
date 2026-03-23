# CloudWatch Metric Alarm Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages CloudWatch Metric Alarms,
> following the exact patterns established by the S3, Security Group, EC2, VPC, EBS,
> Elastic IP, Key Pair, AMI, and Lambda drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~alarmName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned alarm ARN
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
12. [Step 9 — Monitoring Driver Pack Entry Point](#step-9--monitoring-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [Metric-Alarm-Specific Design Decisions](#metric-alarm-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The Metric Alarm driver manages the lifecycle of **CloudWatch metric alarms** only.
Composite alarms, anomaly detection bands, alarm suppression rules, and Metrics
Insights queries are separate resources or future extensions. This document focuses
exclusively on standard metric alarms: creation, threshold/action configuration,
import, deletion, and drift reconciliation.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a metric alarm |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing alarm |
| `Delete` | `ObjectContext` (exclusive) | Delete an alarm |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return alarm outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | API | Notes |
|---|---|---|---|
| Alarm name | Immutable | — | Set at creation; cannot be renamed |
| Alarm ARN | Immutable | — | AWS-assigned; derived from name |
| Namespace | Mutable | `PutMetricAlarm` | CloudWatch metric namespace (e.g., `AWS/EC2`, `AWS/Lambda`) |
| MetricName | Mutable | `PutMetricAlarm` | The metric to monitor |
| Dimensions | Mutable | `PutMetricAlarm` | Key-value pairs identifying the metric source |
| Statistic | Mutable | `PutMetricAlarm` | `SampleCount`, `Average`, `Sum`, `Minimum`, `Maximum` |
| ExtendedStatistic | Mutable | `PutMetricAlarm` | Percentile statistic (e.g., `p99`, `p95.5`) |
| Period | Mutable | `PutMetricAlarm` | Evaluation period in seconds (must be 10, 30, or multiple of 60) |
| EvaluationPeriods | Mutable | `PutMetricAlarm` | Number of periods to evaluate |
| DatapointsToAlarm | Mutable | `PutMetricAlarm` | M-of-N datapoints required to trigger (≤ evaluationPeriods) |
| Threshold | Mutable | `PutMetricAlarm` | Numeric threshold value |
| ComparisonOperator | Mutable | `PutMetricAlarm` | `GreaterThanThreshold`, `LessThanThreshold`, `GreaterThanOrEqualToThreshold`, `LessThanOrEqualToThreshold` |
| TreatMissingData | Mutable | `PutMetricAlarm` | `breaching`, `notBreaching`, `ignore`, `missing` |
| AlarmDescription | Mutable | `PutMetricAlarm` | Free-form description |
| ActionsEnabled | Mutable | `PutMetricAlarm` | Enable/disable alarm actions |
| AlarmActions | Mutable | `PutMetricAlarm` | SNS topic ARNs for `ALARM` state |
| OKActions | Mutable | `PutMetricAlarm` | SNS topic ARNs for `OK` state |
| InsufficientDataActions | Mutable | `PutMetricAlarm` | SNS topic ARNs for `INSUFFICIENT_DATA` state |
| Unit | Mutable | `PutMetricAlarm` | CloudWatch metric unit for filtering |
| Tags | Mutable | `TagResource` / `UntagResource` | Key-value pairs |

### PutMetricAlarm Is an Upsert

Unlike most AWS resources, `PutMetricAlarm` is an **upsert** operation: if the alarm
does not exist, it creates it; if it already exists, it updates all mutable fields.
This simplifies the create vs update decision in the driver — both paths use the
same API call. The driver still tracks state for drift detection but the AWS
interaction is a single converge call.

### What Is NOT In Scope

- **Composite Alarms**: Alarms that combine multiple other alarms with boolean logic.
  Separate resource type (`PutCompositeAlarm`). Future extension.
- **Anomaly Detection Alarms**: Alarms based on anomaly detection bands. Use
  `MetricMathExpression` and anomaly detection models. Future extension.
- **Metric Math**: Multi-metric expressions. The driver supports single-metric alarms
  only. Metric math alarms are a future extension.
- **Alarm State Management**: `SetAlarmState` for testing. Not a provisioning concern.
- **Alarm Suppression**: Suppressor alarms. Future extension.

### Downstream Consumers

```
${resources.my-alarm.outputs.alarmArn}     → Dashboard alarm widgets, SNS subscriptions, CloudFormation
${resources.my-alarm.outputs.alarmName}    → CLI references, other alarm compositions
${resources.my-alarm.outputs.stateValue}   → Observability dashboards
```

---

## 2. Key Strategy

### Key Format: `region~alarmName`

CloudWatch alarm names are unique within a region+account. The CUE schema maps
`metadata.name` to the alarm name. The adapter produces `region~metadata.name`
as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain an alarm ARN,
   describes the alarm by name. Otherwise, `OpCreate`.
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the alarm name. Same key as BuildKey.

### Ownership Tags

Alarm names are unique within a region+account. `PutMetricAlarm` is an upsert, so
calling it on an existing alarm does not error — it updates the alarm. The driver
adds `praxis:managed-key=<region~alarmName>` as a tag to detect whether an existing
alarm is managed by this Praxis installation. During import, if the tag exists and
differs from the expected key, the driver warns about a potential cross-installation
conflict.

**FindByManagedKey** is NOT needed because alarm names are AWS-enforced unique per
region per account.

### Import Semantics

Import and template-based management produce the **same Virtual Object key**:

- `praxis import --kind MetricAlarm --region us-east-1 --resource-id my-alarm`:
  Creates VO key `us-east-1~my-alarm`.
- Template with `metadata.name: "my-alarm"` in `us-east-1`:
  Creates VO key `us-east-1~my-alarm`.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/metricalarm/types.go                — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/metricalarm/aws.go                  — MetricAlarmAPI interface + realMetricAlarmAPI impl
✦ internal/drivers/metricalarm/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/metricalarm/driver.go               — MetricAlarmDriver Virtual Object
✦ internal/drivers/metricalarm/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/metricalarm/aws_test.go             — Unit tests for error classification
✦ internal/drivers/metricalarm/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/metricalarm_adapter.go        — MetricAlarmAdapter implementing provider.Adapter
✦ internal/core/provider/metricalarm_adapter_test.go   — Unit tests for adapter
✦ schemas/aws/cloudwatch/metric_alarm.cue              — CUE schema for MetricAlarm resource
✦ tests/integration/metricalarm_driver_test.go         — Integration tests (Testcontainers + LocalStack)
✎ internal/infra/awsclient/client.go                   — Add NewCloudWatchClient()
✎ cmd/praxis-monitoring/main.go                        — Bind MetricAlarm driver
✎ internal/core/provider/registry.go                   — Add adapter to NewRegistry()
✎ justfile                                             — Add metricalarm test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/cloudwatch/metric_alarm.cue`

```cue
package cloudwatch

#MetricAlarm: {
    apiVersion: "praxis.io/v1"
    kind:       "MetricAlarm"

    metadata: {
        // name is the CloudWatch alarm name in AWS.
        // Must be 1-255 characters.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_\\-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the alarm in.
        region: string

        // account is the optional AWS account alias for credential resolution.
        account?: string

        // namespace is the CloudWatch metric namespace (e.g., "AWS/EC2", "AWS/Lambda", "MyApp").
        namespace: string

        // metricName is the name of the metric to alarm on.
        metricName: string

        // dimensions are key-value pairs that identify the metric source.
        dimensions?: [string]: string

        // statistic is the statistic to apply to the metric.
        // Use this for standard statistics. Mutually exclusive with extendedStatistic.
        statistic?: "SampleCount" | "Average" | "Sum" | "Minimum" | "Maximum"

        // extendedStatistic is a percentile statistic (e.g., "p99", "p95.5").
        // Mutually exclusive with statistic.
        extendedStatistic?: string & =~"^p[0-9]+(\\.[0-9]+)?$"

        // period is the evaluation period in seconds.
        // Must be 10, 30, or a multiple of 60. High-resolution metrics support 10 and 30.
        period: int & (10 | 30 | >=60) & <=86400

        // evaluationPeriods is the number of consecutive periods to evaluate.
        evaluationPeriods: int & >=1

        // datapointsToAlarm is the number of datapoints within evaluationPeriods
        // that must be breaching to trigger the alarm (M-of-N evaluation).
        // Must be <= evaluationPeriods. Defaults to evaluationPeriods if omitted.
        datapointsToAlarm?: int & >=1

        // threshold is the numeric value to compare against.
        threshold: float

        // comparisonOperator defines how the metric value is compared to the threshold.
        comparisonOperator: "GreaterThanThreshold" |
                           "GreaterThanOrEqualToThreshold" |
                           "LessThanThreshold" |
                           "LessThanOrEqualToThreshold"

        // treatMissingData defines how the alarm evaluates missing datapoints.
        treatMissingData: "breaching" | "notBreaching" | "ignore" | "missing" | *"missing"

        // alarmDescription is a human-readable description of the alarm.
        alarmDescription?: string

        // actionsEnabled controls whether alarm actions are executed.
        actionsEnabled: bool | *true

        // alarmActions is a list of ARNs to notify when the alarm transitions to ALARM state.
        alarmActions?: [...string]

        // okActions is a list of ARNs to notify when the alarm transitions to OK state.
        okActions?: [...string]

        // insufficientDataActions is a list of ARNs to notify when the alarm
        // transitions to INSUFFICIENT_DATA state.
        insufficientDataActions?: [...string]

        // unit filters the metric data by CloudWatch unit before evaluating.
        // Omit to consider all units.
        unit?: "Seconds" | "Microseconds" | "Milliseconds" | "Bytes" | "Kilobytes" |
               "Megabytes" | "Gigabytes" | "Terabytes" | "Bits" | "Kilobits" |
               "Megabits" | "Gigabits" | "Terabits" | "Percent" | "Count" |
               "Bytes/Second" | "Kilobytes/Second" | "Megabytes/Second" |
               "Gigabytes/Second" | "Terabytes/Second" | "Bits/Second" |
               "Kilobits/Second" | "Megabits/Second" | "Gigabits/Second" |
               "Terabits/Second" | "Count/Second" | "None"

        // tags on the alarm resource.
        tags: [string]: string
    }

    outputs?: {
        alarmArn:   string
        alarmName:  string
        stateValue: string
        stateReason: string
    }
}
```

### Schema Design Notes

- **`statistic` vs `extendedStatistic`**: Mutually exclusive. `statistic` is for
  standard CloudWatch statistics; `extendedStatistic` is for percentile values.
  CUE cannot easily enforce mutual exclusivity between two optional fields, so
  the driver validates this at runtime and returns a terminal error if both are set.
- **`period` constraints**: Period must be 10, 30, or a multiple of 60. High-resolution
  metrics support 10 and 30; standard metrics require multiples of 60.
- **`datapointsToAlarm`**: Enables M-of-N evaluation. If 3 of 5 evaluation periods
  must be breaching, set `datapointsToAlarm: 3` and `evaluationPeriods: 5`.
- **`threshold` is float**: CloudWatch thresholds are floating-point numbers to
  support metrics like percentages and latencies.
- **`alarmActions` are ARNs**: Typically SNS topic ARNs, but can also be EC2 actions
  (stop, terminate, reboot) or Auto Scaling policy ARNs.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **ADD NewCloudWatchClient()**

```go
import "github.com/aws/aws-sdk-go-v2/service/cloudwatch"

// NewCloudWatchClient creates a CloudWatch API client from the given AWS config.
func NewCloudWatchClient(cfg aws.Config) *cloudwatch.Client {
    return cloudwatch.NewFromConfig(cfg)
}
```

This is shared with the Dashboard driver. Both drivers use the `cloudwatch` SDK
package (not `cloudwatchlogs`).

---

## Step 3 — Driver Types

**File**: `internal/drivers/metricalarm/types.go`

```go
package metricalarm

import "github.com/praxiscloud/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for CloudWatch Metric Alarms.
const ServiceName = "MetricAlarm"

// MetricAlarmSpec is the desired state for a CloudWatch metric alarm.
type MetricAlarmSpec struct {
    Account                 string            `json:"account,omitempty"`
    Region                  string            `json:"region"`
    AlarmName               string            `json:"alarmName"`
    Namespace               string            `json:"namespace"`
    MetricName              string            `json:"metricName"`
    Dimensions              map[string]string `json:"dimensions,omitempty"`
    Statistic               string            `json:"statistic,omitempty"`
    ExtendedStatistic       string            `json:"extendedStatistic,omitempty"`
    Period                  int32             `json:"period"`
    EvaluationPeriods       int32             `json:"evaluationPeriods"`
    DatapointsToAlarm       *int32            `json:"datapointsToAlarm,omitempty"`
    Threshold               float64           `json:"threshold"`
    ComparisonOperator      string            `json:"comparisonOperator"`
    TreatMissingData        string            `json:"treatMissingData"`
    AlarmDescription        string            `json:"alarmDescription,omitempty"`
    ActionsEnabled          bool              `json:"actionsEnabled"`
    AlarmActions            []string          `json:"alarmActions,omitempty"`
    OKActions               []string          `json:"okActions,omitempty"`
    InsufficientDataActions []string          `json:"insufficientDataActions,omitempty"`
    Unit                    string            `json:"unit,omitempty"`
    Tags                    map[string]string `json:"tags,omitempty"`
    ManagedKey              string            `json:"managedKey,omitempty"`
}

// MetricAlarmOutputs is produced after provisioning and stored in Restate K/V.
type MetricAlarmOutputs struct {
    AlarmArn    string `json:"alarmArn"`
    AlarmName   string `json:"alarmName"`
    StateValue  string `json:"stateValue"`
    StateReason string `json:"stateReason,omitempty"`
}

// ObservedState captures the actual configuration of an alarm from AWS.
type ObservedState struct {
    AlarmArn                string            `json:"alarmArn"`
    AlarmName               string            `json:"alarmName"`
    Namespace               string            `json:"namespace"`
    MetricName              string            `json:"metricName"`
    Dimensions              map[string]string `json:"dimensions,omitempty"`
    Statistic               string            `json:"statistic,omitempty"`
    ExtendedStatistic       string            `json:"extendedStatistic,omitempty"`
    Period                  int32             `json:"period"`
    EvaluationPeriods       int32             `json:"evaluationPeriods"`
    DatapointsToAlarm       int32             `json:"datapointsToAlarm"`
    Threshold               float64           `json:"threshold"`
    ComparisonOperator      string            `json:"comparisonOperator"`
    TreatMissingData        string            `json:"treatMissingData"`
    AlarmDescription        string            `json:"alarmDescription,omitempty"`
    ActionsEnabled          bool              `json:"actionsEnabled"`
    AlarmActions            []string          `json:"alarmActions,omitempty"`
    OKActions               []string          `json:"okActions,omitempty"`
    InsufficientDataActions []string          `json:"insufficientDataActions,omitempty"`
    Unit                    string            `json:"unit,omitempty"`
    StateValue              string            `json:"stateValue"`
    StateReason             string            `json:"stateReason,omitempty"`
    Tags                    map[string]string `json:"tags,omitempty"`
}

// MetricAlarmState is the single atomic state object stored under drivers.StateKey.
type MetricAlarmState struct {
    Desired            MetricAlarmSpec       `json:"desired"`
    Observed           ObservedState         `json:"observed"`
    Outputs            MetricAlarmOutputs    `json:"outputs"`
    Status             types.ResourceStatus  `json:"status"`
    Mode               types.Mode            `json:"mode"`
    Error              string                `json:"error,omitempty"`
    Generation         int64                 `json:"generation"`
    LastReconcile      string                `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                  `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **`DatapointsToAlarm` is `*int32` in spec, `int32` in observed**: In the spec, nil
  means "default to evaluationPeriods". In observed, AWS always returns the concrete
  value.
- **`ObservedState.StateValue`**: The alarm's current state (`OK`, `ALARM`,
  `INSUFFICIENT_DATA`). This is a read-only output, not a managed attribute. The
  driver never attempts to set alarm state.
- **`Dimensions` is `map[string]string`**: CloudWatch dimensions are Name→Value
  pairs. The AWS SDK models them as a list of `Dimension` structs, but for drift
  detection and CUE simplicity, the driver normalizes them to a flat map.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/metricalarm/aws.go`

### MetricAlarmAPI Interface

```go
type MetricAlarmAPI interface {
    // PutMetricAlarm creates or updates a metric alarm (upsert).
    PutMetricAlarm(ctx context.Context, spec MetricAlarmSpec) error

    // DescribeAlarm returns the current state of an alarm.
    // Returns (nil, nil) if the alarm does not exist.
    DescribeAlarm(ctx context.Context, alarmName string) (*ObservedState, error)

    // DeleteAlarm deletes an alarm by name.
    DeleteAlarm(ctx context.Context, alarmName string) error

    // TagResource adds or overwrites tags on an alarm.
    TagResource(ctx context.Context, alarmArn string, tags map[string]string) error

    // UntagResource removes tag keys from an alarm.
    UntagResource(ctx context.Context, alarmArn string, keys []string) error

    // ListTagsForResource returns all tags on an alarm.
    ListTagsForResource(ctx context.Context, alarmArn string) (map[string]string, error)
}
```

### Implementation: realMetricAlarmAPI

```go
type realMetricAlarmAPI struct {
    client  *cloudwatch.Client
    limiter ratelimit.Limiter
}

func newRealMetricAlarmAPI(client *cloudwatch.Client, limiter ratelimit.Limiter) MetricAlarmAPI {
    return &realMetricAlarmAPI{client: client, limiter: limiter}
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var rnf *cwtypes.ResourceNotFoundException
    return errors.As(err, &rnf)
}

func isInvalidParam(err error) bool {
    var ipv *cwtypes.InvalidParameterValueException
    if errors.As(err, &ipv) {
        return true
    }
    var ipc *cwtypes.InvalidParameterCombinationException
    return errors.As(err, &ipc)
}

func isLimitExceeded(err error) bool {
    var le *cwtypes.LimitExceededFault
    return errors.As(err, &le)
}

func isThrottled(err error) bool {
    var te *cwtypes.ThrottlingException
    return errors.As(err, &te)
}
```

### Key Implementation Details

#### PutMetricAlarm

```go
func (r *realMetricAlarmAPI) PutMetricAlarm(ctx context.Context, spec MetricAlarmSpec) error {
    r.limiter.Wait(ctx)

    input := &cloudwatch.PutMetricAlarmInput{
        AlarmName:          &spec.AlarmName,
        Namespace:          &spec.Namespace,
        MetricName:         &spec.MetricName,
        Period:             &spec.Period,
        EvaluationPeriods:  &spec.EvaluationPeriods,
        Threshold:          &spec.Threshold,
        ComparisonOperator: cwtypes.ComparisonOperator(spec.ComparisonOperator),
        TreatMissingData:   &spec.TreatMissingData,
        ActionsEnabled:     &spec.ActionsEnabled,
        Tags:               toTagList(spec.Tags),
    }

    // Set statistic or extended statistic (mutually exclusive)
    if spec.Statistic != "" {
        input.Statistic = cwtypes.Statistic(spec.Statistic)
    }
    if spec.ExtendedStatistic != "" {
        input.ExtendedStatistic = &spec.ExtendedStatistic
    }

    // Set dimensions
    if len(spec.Dimensions) > 0 {
        input.Dimensions = toDimensionList(spec.Dimensions)
    }

    // Set optional fields
    if spec.DatapointsToAlarm != nil {
        input.DatapointsToAlarm = spec.DatapointsToAlarm
    }
    if spec.AlarmDescription != "" {
        input.AlarmDescription = &spec.AlarmDescription
    }
    if spec.Unit != "" {
        input.Unit = cwtypes.StandardUnit(spec.Unit)
    }

    // Set action ARNs
    if len(spec.AlarmActions) > 0 {
        input.AlarmActions = spec.AlarmActions
    }
    if len(spec.OKActions) > 0 {
        input.OKActions = spec.OKActions
    }
    if len(spec.InsufficientDataActions) > 0 {
        input.InsufficientDataActions = spec.InsufficientDataActions
    }

    _, err := r.client.PutMetricAlarm(ctx, input)
    return err
}
```

#### DescribeAlarm

```go
func (r *realMetricAlarmAPI) DescribeAlarm(ctx context.Context, alarmName string) (*ObservedState, error) {
    r.limiter.Wait(ctx)

    out, err := r.client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
        AlarmNames: []string{alarmName},
        AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeMetricAlarm},
    })
    if err != nil {
        return nil, err
    }

    if len(out.MetricAlarms) == 0 {
        return nil, nil
    }

    alarm := out.MetricAlarms[0]
    observed := &ObservedState{
        AlarmArn:                deref(alarm.AlarmArn),
        AlarmName:               deref(alarm.AlarmName),
        Namespace:               deref(alarm.Namespace),
        MetricName:              deref(alarm.MetricName),
        Dimensions:              fromDimensionList(alarm.Dimensions),
        Period:                  derefInt32(alarm.Period),
        EvaluationPeriods:       derefInt32(alarm.EvaluationPeriods),
        DatapointsToAlarm:       derefInt32(alarm.DatapointsToAlarm),
        Threshold:               derefFloat64(alarm.Threshold),
        ComparisonOperator:      string(alarm.ComparisonOperator),
        TreatMissingData:        deref(alarm.TreatMissingData),
        AlarmDescription:        deref(alarm.AlarmDescription),
        ActionsEnabled:          derefBool(alarm.ActionsEnabled),
        AlarmActions:            alarm.AlarmActions,
        OKActions:               alarm.OKActions,
        InsufficientDataActions: alarm.InsufficientDataActions,
        StateValue:              string(alarm.StateValue),
        StateReason:             deref(alarm.StateReason),
    }

    if alarm.Statistic != "" {
        observed.Statistic = string(alarm.Statistic)
    }
    if alarm.ExtendedStatistic != nil {
        observed.ExtendedStatistic = *alarm.ExtendedStatistic
    }
    if alarm.Unit != "" {
        observed.Unit = string(alarm.Unit)
    }

    return observed, nil
}
```

**Note**: `DescribeAlarms` does NOT return tags. Tags must be fetched separately
via `ListTagsForResource` using the alarm ARN. The driver calls both in sequence
during observe/reconcile.

---

## Step 5 — Drift Detection

**File**: `internal/drivers/metricalarm/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| Namespace | Console, CLI | Metric namespace changed |
| MetricName | Console, CLI | Metric name changed |
| Dimensions | Console, CLI | Dimension key-value pairs changed |
| Statistic | Console, CLI | Standard statistic changed |
| ExtendedStatistic | Console, CLI | Percentile statistic changed |
| Period | Console, CLI | Evaluation period changed |
| EvaluationPeriods | Console, CLI | Number of periods changed |
| DatapointsToAlarm | Console, CLI | M-of-N threshold changed |
| Threshold | Console, CLI | Numeric threshold changed |
| ComparisonOperator | Console, CLI | Comparison direction changed |
| TreatMissingData | Console, CLI | Missing data behavior changed |
| AlarmDescription | Console, CLI | Description text changed |
| ActionsEnabled | Console, CLI | Actions enabled/disabled |
| AlarmActions | Console, CLI | ALARM action ARNs changed |
| OKActions | Console, CLI | OK action ARNs changed |
| InsufficientDataActions | Console, CLI | INSUFFICIENT_DATA action ARNs changed |
| Unit | Console, CLI | Metric unit filter changed |
| Tags | Console, CLI, other tools | Tags added/removed/changed |

### Fields NOT Drift-Detected

- **StateValue**: Read-only; reflects the alarm's current evaluation state, not
  configuration.
- **StateReason**: Read-only; human-readable reason for the current state.

### HasDrift

```go
func HasDrift(desired MetricAlarmSpec, observed ObservedState) bool {
    if desired.Namespace != observed.Namespace { return true }
    if desired.MetricName != observed.MetricName { return true }
    if !dimensionsMatch(desired.Dimensions, observed.Dimensions) { return true }
    if desired.Statistic != observed.Statistic { return true }
    if desired.ExtendedStatistic != observed.ExtendedStatistic { return true }
    if desired.Period != observed.Period { return true }
    if desired.EvaluationPeriods != observed.EvaluationPeriods { return true }
    if !datapointsMatch(desired.DatapointsToAlarm, observed.DatapointsToAlarm, desired.EvaluationPeriods) { return true }
    if desired.Threshold != observed.Threshold { return true }
    if desired.ComparisonOperator != observed.ComparisonOperator { return true }
    if desired.TreatMissingData != observed.TreatMissingData { return true }
    if desired.AlarmDescription != observed.AlarmDescription { return true }
    if desired.ActionsEnabled != observed.ActionsEnabled { return true }
    if !slicesEqual(desired.AlarmActions, observed.AlarmActions) { return true }
    if !slicesEqual(desired.OKActions, observed.OKActions) { return true }
    if !slicesEqual(desired.InsufficientDataActions, observed.InsufficientDataActions) { return true }
    if desired.Unit != observed.Unit { return true }
    if !tagsMatch(desired.Tags, observed.Tags) { return true }
    return false
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired MetricAlarmSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff

    if desired.Namespace != observed.Namespace {
        diffs = append(diffs, types.FieldDiff{Field: "namespace", Desired: desired.Namespace, Observed: observed.Namespace})
    }
    if desired.MetricName != observed.MetricName {
        diffs = append(diffs, types.FieldDiff{Field: "metricName", Desired: desired.MetricName, Observed: observed.MetricName})
    }
    if desired.Threshold != observed.Threshold {
        diffs = append(diffs, types.FieldDiff{
            Field:    "threshold",
            Desired:  fmt.Sprintf("%g", desired.Threshold),
            Observed: fmt.Sprintf("%g", observed.Threshold),
        })
    }
    // ... similar for all drift-detectable fields ...
    return diffs
}
```

### DatapointsToAlarm Matching

```go
// datapointsMatch compares desired and observed datapointsToAlarm.
// When desired is nil, it defaults to evaluationPeriods.
func datapointsMatch(desired *int32, observed int32, evaluationPeriods int32) bool {
    effective := evaluationPeriods
    if desired != nil {
        effective = *desired
    }
    return effective == observed
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/metricalarm/driver.go`

### Constructor

```go
type MetricAlarmDriver struct {
    accounts *auth.Registry
}

func NewMetricAlarmDriver(accounts *auth.Registry) *MetricAlarmDriver {
    return &MetricAlarmDriver{accounts: accounts}
}

func (MetricAlarmDriver) ServiceName() string { return ServiceName }
```

### Provision

Because `PutMetricAlarm` is an upsert, the create and update paths converge on the
same API call. The driver still tracks state for drift detection and generation
counting.

```go
func (d *MetricAlarmDriver) Provision(ctx restate.ObjectContext, spec MetricAlarmSpec) (MetricAlarmOutputs, error) {
    // Validate statistic exclusivity
    if spec.Statistic != "" && spec.ExtendedStatistic != "" {
        return MetricAlarmOutputs{}, restate.TerminalError(
            fmt.Errorf("statistic and extendedStatistic are mutually exclusive"), 400)
    }
    if spec.Statistic == "" && spec.ExtendedStatistic == "" {
        return MetricAlarmOutputs{}, restate.TerminalError(
            fmt.Errorf("one of statistic or extendedStatistic is required"), 400)
    }

    // Load existing state
    state, _ := restate.Get[*MetricAlarmState](ctx, drivers.StateKey)

    api := d.buildAPI(spec.Account, spec.Region)

    gen := int64(1)
    if state != nil {
        gen = state.Generation + 1
    }

    // Write pending state
    restate.Set(ctx, drivers.StateKey, &MetricAlarmState{
        Desired:    spec,
        Status:     types.StatusProvisioning,
        Mode:       drivers.DefaultMode(state),
        Generation: gen,
    })

    // Put alarm (upsert — works for both create and update)
    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.PutMetricAlarm(rc, spec)
    }); err != nil {
        if isInvalidParam(err) {
            return MetricAlarmOutputs{}, restate.TerminalError(
                fmt.Errorf("invalid alarm configuration: %w", err), 400)
        }
        if isLimitExceeded(err) {
            return MetricAlarmOutputs{}, restate.TerminalError(
                fmt.Errorf("alarm limit exceeded: %w", err), 429)
        }
        return MetricAlarmOutputs{}, err
    }

    // Describe to populate observed state and get ARN
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.DescribeAlarm(rc, spec.AlarmName)
    })
    if err != nil || observed == nil {
        return MetricAlarmOutputs{}, fmt.Errorf("failed to describe alarm after put: %w", err)
    }

    // Fetch tags
    tags, err := restate.Run(ctx, func(rc restate.RunContext) (map[string]string, error) {
        return api.ListTagsForResource(rc, observed.AlarmArn)
    })
    if err != nil {
        return MetricAlarmOutputs{}, err
    }
    observed.Tags = tags

    outputs := MetricAlarmOutputs{
        AlarmArn:    observed.AlarmArn,
        AlarmName:   observed.AlarmName,
        StateValue:  observed.StateValue,
        StateReason: observed.StateReason,
    }

    restate.Set(ctx, drivers.StateKey, &MetricAlarmState{
        Desired:    spec,
        Observed:   *observed,
        Outputs:    outputs,
        Status:     types.StatusReady,
        Mode:       types.ModeManaged,
        Generation: gen,
    })

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### Import

```go
func (d *MetricAlarmDriver) Import(ctx restate.ObjectContext, req drivers.ImportRequest) (MetricAlarmOutputs, error) {
    api := d.buildAPI(req.Account, req.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.DescribeAlarm(rc, req.ResourceID)
    })
    if err != nil {
        return MetricAlarmOutputs{}, err
    }
    if observed == nil {
        return MetricAlarmOutputs{}, restate.TerminalError(
            fmt.Errorf("alarm %q not found in %s", req.ResourceID, req.Region), 404)
    }

    tags, err := restate.Run(ctx, func(rc restate.RunContext) (map[string]string, error) {
        return api.ListTagsForResource(rc, observed.AlarmArn)
    })
    if err != nil {
        return MetricAlarmOutputs{}, err
    }
    observed.Tags = tags

    outputs := MetricAlarmOutputs{
        AlarmArn:    observed.AlarmArn,
        AlarmName:   observed.AlarmName,
        StateValue:  observed.StateValue,
        StateReason: observed.StateReason,
    }

    mode := drivers.ImportMode(req.Observe)

    restate.Set(ctx, drivers.StateKey, &MetricAlarmState{
        Desired:  specFromObserved(observed, req),
        Observed: *observed,
        Outputs:  outputs,
        Status:   types.StatusReady,
        Mode:     mode,
    })

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### Delete

```go
func (d *MetricAlarmDriver) Delete(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*MetricAlarmState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.DeleteAlarm(rc, state.Desired.AlarmName)
    }); err != nil {
        if isNotFound(err) {
            restate.ClearAll(ctx)
            return nil
        }
        return err
    }

    restate.ClearAll(ctx)
    return nil
}
```

### Reconcile

```go
func (d *MetricAlarmDriver) Reconcile(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*MetricAlarmState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.DescribeAlarm(rc, state.Desired.AlarmName)
    })
    if err != nil {
        return err
    }

    if observed == nil {
        state.Status = types.StatusDrifted
        state.Error = "alarm deleted externally"
        state.Observed = ObservedState{}
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx)
        return nil
    }

    tags, err := restate.Run(ctx, func(rc restate.RunContext) (map[string]string, error) {
        return api.ListTagsForResource(rc, observed.AlarmArn)
    })
    if err != nil {
        return err
    }
    observed.Tags = tags
    state.Observed = *observed

    if HasDrift(state.Desired, *observed) {
        state.Status = types.StatusDrifted

        if state.Mode == types.ModeManaged {
            // Auto-correct via PutMetricAlarm (upsert idempotently restores config)
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.PutMetricAlarm(rc, state.Desired)
            }); err != nil {
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                d.scheduleReconcile(ctx)
                return nil
            }

            // Re-describe after correction
            observed, err = restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
                return api.DescribeAlarm(rc, state.Desired.AlarmName)
            })
            if err != nil || observed == nil {
                d.scheduleReconcile(ctx)
                return nil
            }
            state.Observed = *observed
            state.Status = types.StatusReady
            state.Error = ""
        }
    } else {
        state.Status = types.StatusReady
        state.Error = ""
    }

    state.Outputs = MetricAlarmOutputs{
        AlarmArn:    observed.AlarmArn,
        AlarmName:   observed.AlarmName,
        StateValue:  observed.StateValue,
        StateReason: observed.StateReason,
    }
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx)
    return nil
}
```

### GetStatus / GetOutputs

```go
func (d *MetricAlarmDriver) GetStatus(ctx restate.ObjectSharedContext) (types.ResourceStatus, error) {
    state, _ := restate.Get[*MetricAlarmState](ctx, drivers.StateKey)
    if state == nil {
        return types.StatusNotFound, nil
    }
    return state.Status, nil
}

func (d *MetricAlarmDriver) GetOutputs(ctx restate.ObjectSharedContext) (MetricAlarmOutputs, error) {
    state, _ := restate.Get[*MetricAlarmState](ctx, drivers.StateKey)
    if state == nil {
        return MetricAlarmOutputs{}, nil
    }
    return state.Outputs, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/metricalarm_adapter.go`

```go
type MetricAlarmAdapter struct {
    accounts *auth.Registry
}

func NewMetricAlarmAdapterWithRegistry(accounts *auth.Registry) *MetricAlarmAdapter {
    return &MetricAlarmAdapter{accounts: accounts}
}

func (a *MetricAlarmAdapter) Kind() string           { return "MetricAlarm" }
func (a *MetricAlarmAdapter) Service() string         { return metricalarm.ServiceName }
func (a *MetricAlarmAdapter) KeyScope() types.KeyScope { return types.KeyScopeRegion }

func (a *MetricAlarmAdapter) BuildKey(doc types.ResourceDoc) (string, error) {
    region := doc.Spec["region"].(string)
    name := doc.Metadata.Name
    return region + "~" + name, nil
}

func (a *MetricAlarmAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return region + "~" + resourceID, nil
}

func (a *MetricAlarmAdapter) BuildSpec(doc types.ResourceDoc) (any, error) {
    // Build MetricAlarmSpec from the resource document
    // Map CUE spec fields to Go struct fields
    return metricAlarmSpecFromDoc(doc), nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — add to `NewRegistry()`:

```go
r.Register(NewMetricAlarmAdapterWithRegistry(accounts))
```

---

## Step 9 — Monitoring Driver Pack Entry Point

**File**: `cmd/praxis-monitoring/main.go`

```go
srv := server.NewRestate().
    Bind(restate.Reflect(loggroup.NewLogGroupDriver(cfg.Auth()))).
    Bind(restate.Reflect(metricalarm.NewMetricAlarmDriver(cfg.Auth()))).
    Bind(restate.Reflect(dashboard.NewDashboardDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

CloudWatch drivers are hosted in the `praxis-monitoring` service on port 9087.
See the [pack overview](CLOUDWATCH_DRIVER_PACK_OVERVIEW.md) for the docker-compose
service definition.

### Justfile

```just
test-metricalarm:    go test ./internal/drivers/metricalarm/...    -v -count=1 -race
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/metricalarm/driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestProvision_CreateNew` | Creates a new alarm with threshold and actions |
| `TestProvision_WithExtendedStatistic` | Creates with `p99` percentile statistic |
| `TestProvision_WithDimensions` | Creates with multiple dimensions |
| `TestProvision_MofNEvaluation` | Creates with datapointsToAlarm < evaluationPeriods |
| `TestProvision_UpdateThreshold` | Updates threshold on existing alarm |
| `TestProvision_UpdateActions` | Changes alarm actions ARNs |
| `TestProvision_DisableActions` | Sets actionsEnabled to false |
| `TestProvision_StatisticExclusivity` | Terminal error when both statistic and extendedStatistic set |
| `TestProvision_StatisticRequired` | Terminal error when neither statistic type set |
| `TestProvision_InvalidParam` | Terminal error on invalid parameter |
| `TestProvision_LimitExceeded` | Terminal error on alarm limit |
| `TestImport_Existing` | Imports existing alarm in managed mode |
| `TestImport_Observed` | Imports existing alarm in observed mode |
| `TestImport_NotFound` | Returns 404 terminal error |
| `TestDelete_Success` | Deletes alarm and clears state |
| `TestDelete_AlreadyGone` | No-op when already deleted |
| `TestReconcile_NoDrift` | Status remains `Ready` |
| `TestReconcile_ThresholdDrift` | Detects and corrects threshold change |
| `TestReconcile_ActionsDrift` | Detects and corrects alarm actions change |
| `TestReconcile_ExternalDeletion` | Detects external deletion, sets `Drifted` |
| `TestReconcile_ObservedMode` | Detects drift but does not correct |
| `TestGetStatus_NotFound` | Returns `StatusNotFound` |

**File**: `internal/drivers/metricalarm/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | Identical config → no drift |
| `TestHasDrift_ThresholdChanged` | 5.0 → 10.0 → drift |
| `TestHasDrift_NamespaceChanged` | Namespace changed → drift |
| `TestHasDrift_DimensionsChanged` | Dimension added/removed → drift |
| `TestHasDrift_StatisticChanged` | Sum → Average → drift |
| `TestHasDrift_PeriodChanged` | 60 → 300 → drift |
| `TestHasDrift_ActionsChanged` | Action ARN added → drift |
| `TestHasDrift_TagsChanged` | Tag modified → drift |
| `TestHasDrift_DatapointsToAlarmDefault` | nil desired (defaults to evaluationPeriods) → no drift |

**File**: `internal/drivers/metricalarm/aws_test.go`

| Test | Description |
|---|---|
| `TestIsNotFound` | Classifies `ResourceNotFoundException` |
| `TestIsInvalidParam_Value` | Classifies `InvalidParameterValueException` |
| `TestIsInvalidParam_Combination` | Classifies `InvalidParameterCombinationException` |
| `TestIsLimitExceeded` | Classifies `LimitExceededFault` |
| `TestIsThrottled` | Classifies `ThrottlingException` |

---

## Step 12 — Integration Tests

**File**: `tests/integration/metricalarm_driver_test.go`

### Prerequisites

- LocalStack with CloudWatch support
- Restate dev server
- praxis-monitoring pack running

### Test Scenarios

| Test | Description |
|---|---|
| `TestMetricAlarm_CreateAndDescribe` | Create → describe → verify threshold and actions |
| `TestMetricAlarm_UpdateThreshold` | Create → change threshold → verify |
| `TestMetricAlarm_UpdateActions` | Create → change alarm actions → verify |
| `TestMetricAlarm_WithDimensions` | Create with dimensions → verify dimension persistence |
| `TestMetricAlarm_ExtendedStatistic` | Create with `p99` → verify |
| `TestMetricAlarm_MofN` | Create with M-of-N evaluation → verify datapointsToAlarm |
| `TestMetricAlarm_Tags` | Create with tags → add/remove tags → verify |
| `TestMetricAlarm_Import` | Create externally → import → verify outputs |
| `TestMetricAlarm_Delete` | Create → delete → verify deletion |
| `TestMetricAlarm_DriftCorrection` | Create → externally change threshold → reconcile → verify corrected |

---

## Metric-Alarm-Specific Design Decisions

### 1. PutMetricAlarm Is an Upsert

Unlike most AWS resources where create and update are separate API calls,
`PutMetricAlarm` handles both. This means the driver doesn't need separate
create vs. update paths — a single `PutMetricAlarm` call converges the alarm
to the desired state. This simplifies the Provision handler significantly.

However, the driver still differentiates between initial creation (generation = 1)
and updates (generation > 1) for state tracking and event reporting purposes.

### 2. Statistic vs ExtendedStatistic Mutual Exclusivity

AWS enforces that exactly one of `Statistic` or `ExtendedStatistic` must be set.
The CUE schema marks both as optional; the driver validates mutual exclusivity at
runtime. This is a pragmatic choice because CUE's type system cannot cleanly express
"exactly one of two optional fields must be present."

### 3. DatapointsToAlarm Defaults

When `datapointsToAlarm` is omitted, AWS defaults it to `evaluationPeriods` (i.e.,
all periods must be breaching). The drift detection logic accounts for this default:
if the desired `datapointsToAlarm` is nil and the observed value equals
`evaluationPeriods`, there is no drift.

### 4. Dimension Normalization

AWS returns dimensions as a list of `{Name, Value}` structs. The driver normalizes
these to a `map[string]string` for CUE compatibility and simpler drift comparison.
Dimension order does not matter — the driver compares dimension maps, not lists.

### 5. Tags Are Separate from PutMetricAlarm

While `PutMetricAlarm` accepts a `Tags` parameter, it only applies tags on initial
creation. Updating tags on an existing alarm requires separate `TagResource` /
`UntagResource` calls. The driver handles tag convergence as a post-`Put` step
during updates. During initial creation, tags can be included in the `PutMetricAlarm`
call for efficiency.

### 6. Alarm State Is Read-Only

The alarm's state (`OK`, `ALARM`, `INSUFFICIENT_DATA`) reflects the current
evaluation of the metric against the threshold. The driver reports this state in
outputs but never attempts to set or change it. `SetAlarmState` exists for testing
purposes and is out of scope.

---

## Checklist

### Files
- [ ] `schemas/aws/cloudwatch/metric_alarm.cue`
- [ ] `internal/drivers/metricalarm/types.go`
- [ ] `internal/drivers/metricalarm/aws.go`
- [ ] `internal/drivers/metricalarm/drift.go`
- [ ] `internal/drivers/metricalarm/driver.go`
- [ ] `internal/drivers/metricalarm/driver_test.go`
- [ ] `internal/drivers/metricalarm/aws_test.go`
- [ ] `internal/drivers/metricalarm/drift_test.go`
- [ ] `internal/core/provider/metricalarm_adapter.go`
- [ ] `internal/core/provider/metricalarm_adapter_test.go`
- [ ] `tests/integration/metricalarm_driver_test.go`

### Modifications
- [ ] `internal/infra/awsclient/client.go` — `NewCloudWatchClient()`
- [ ] `cmd/praxis-monitoring/main.go` — Bind MetricAlarmDriver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `justfile` — Add `test-metricalarm` target
