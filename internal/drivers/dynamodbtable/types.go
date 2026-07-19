// Package dynamodbtable implements the Praxis driver for AWS DynamoDB tables.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// the DynamoDB DescribeTable API; the driver state couples both together with
// status tracking.
//
// Scope note: the driver manages the core table — primary key schema, billing
// mode, and provisioned throughput. Global secondary indexes, streams, TTL, and
// global-table replication are intentionally out of scope and left as future
// work; they would each add their own converge/drift surface.
package dynamodbtable

// ServiceName is the Restate Virtual Object service name used to register the AWS DynamoDB table driver.
const ServiceName = "DynamoDBTable"

// Billing modes accepted by the driver.
const (
	BillingModePayPerRequest = "PAY_PER_REQUEST"
	BillingModeProvisioned   = "PROVISIONED"
)

// DynamoDBTableSpec declares the user's desired configuration for a table.
//
// Immutable fields (set at creation; changes surface as requires-replacement diffs):
//   - HashKey / HashKeyType:   partition key attribute name and scalar type
//   - RangeKey / RangeKeyType: optional sort key attribute name and scalar type
//
// Mutable fields (converged in place during reconciliation):
//   - BillingMode:                PAY_PER_REQUEST (on-demand) or PROVISIONED
//   - ReadCapacity / WriteCapacity: provisioned throughput (only when PROVISIONED)
//   - Tags:                       user-defined tags (praxis:-prefixed tags are reserved)
type DynamoDBTableSpec struct {
	Account       string            `json:"account,omitempty"`
	Region        string            `json:"region"`
	Name          string            `json:"name"`
	BillingMode   string            `json:"billingMode,omitempty"`
	HashKey       string            `json:"hashKey"`
	HashKeyType   string            `json:"hashKeyType,omitempty"`
	RangeKey      string            `json:"rangeKey,omitempty"`
	RangeKeyType  string            `json:"rangeKeyType,omitempty"`
	ReadCapacity  int64             `json:"readCapacity,omitempty"`
	WriteCapacity int64             `json:"writeCapacity,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	ManagedKey    string            `json:"managedKey,omitempty"`
}

// DynamoDBTableOutputs holds the values produced after provisioning a table.
type DynamoDBTableOutputs struct {
	ARN       string `json:"arn"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ItemCount int64  `json:"itemCount,omitempty"`
}

// ObservedState captures the live configuration of a table as read from the
// DescribeTable API. It is compared against the spec during drift detection.
type ObservedState struct {
	ARN           string            `json:"arn"`
	Name          string            `json:"name"`
	Status        string            `json:"status"`
	BillingMode   string            `json:"billingMode"`
	HashKey       string            `json:"hashKey"`
	HashKeyType   string            `json:"hashKeyType"`
	RangeKey      string            `json:"rangeKey,omitempty"`
	RangeKeyType  string            `json:"rangeKeyType,omitempty"`
	ReadCapacity  int64             `json:"readCapacity,omitempty"`
	WriteCapacity int64             `json:"writeCapacity,omitempty"`
	ItemCount     int64             `json:"itemCount,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
}
