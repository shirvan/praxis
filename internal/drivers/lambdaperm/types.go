// Package lambdaperm implements the Praxis driver for AWS Lambda resource-based
// permissions (policy statements). Permissions are replace-only — updates require
// remove + add since individual statements cannot be modified in-place.
package lambdaperm

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for the Lambda Permission driver.
const ServiceName = "LambdaPermission"

// LambdaPermissionSpec defines the desired state for a Lambda permission statement.
// All fields are effectively immutable — changes trigger remove + re-add.
type LambdaPermissionSpec struct {
	Account          string `json:"account,omitempty"`          // Praxis account alias.
	Region           string `json:"region"`                     // AWS region.
	FunctionName     string `json:"functionName"`               // Target Lambda function.
	StatementId      string `json:"statementId"`                // Unique statement identifier.
	Action           string `json:"action"`                     // IAM action (default: lambda:InvokeFunction).
	Principal        string `json:"principal"`                  // AWS service or account principal.
	SourceArn        string `json:"sourceArn,omitempty"`        // Condition: source ARN.
	SourceAccount    string `json:"sourceAccount,omitempty"`    // Condition: source account.
	EventSourceToken string `json:"eventSourceToken,omitempty"` // Condition: event source token.
	Qualifier        string `json:"qualifier,omitempty"`        // Function version or alias.
	ManagedKey       string `json:"managedKey,omitempty"`       // praxis:managed-key tag value.
}

// LambdaPermissionOutputs are the user-facing outputs after provisioning.
type LambdaPermissionOutputs struct {
	StatementId  string `json:"statementId"`
	FunctionName string `json:"functionName"`
	Statement    string `json:"statement"` // Raw JSON of the IAM policy statement.
}

// ObservedState captures the parsed permission statement from the Lambda policy.
type ObservedState struct {
	StatementId      string `json:"statementId"`
	FunctionName     string `json:"functionName"`
	Action           string `json:"action"`
	Principal        string `json:"principal"`
	SourceArn        string `json:"sourceArn,omitempty"`
	SourceAccount    string `json:"sourceAccount,omitempty"`
	EventSourceToken string `json:"eventSourceToken,omitempty"`
	Condition        string `json:"condition,omitempty"`
}

// LambdaPermissionState is the full durable state stored in the Restate Virtual Object.
type LambdaPermissionState struct {
	Desired            LambdaPermissionSpec    `json:"desired"`
	Observed           ObservedState           `json:"observed"`
	Outputs            LambdaPermissionOutputs `json:"outputs"`
	Status             types.ResourceStatus    `json:"status"`
	Mode               types.Mode              `json:"mode"`
	Error              string                  `json:"error,omitempty"`
	Generation         int64                   `json:"generation"`
	LastReconcile      string                  `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                    `json:"reconcileScheduled"`
}
