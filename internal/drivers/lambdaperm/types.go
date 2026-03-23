package lambdaperm

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "LambdaPermission"

type LambdaPermissionSpec struct {
	Account          string `json:"account,omitempty"`
	Region           string `json:"region"`
	FunctionName     string `json:"functionName"`
	StatementId      string `json:"statementId"`
	Action           string `json:"action"`
	Principal        string `json:"principal"`
	SourceArn        string `json:"sourceArn,omitempty"`
	SourceAccount    string `json:"sourceAccount,omitempty"`
	EventSourceToken string `json:"eventSourceToken,omitempty"`
	Qualifier        string `json:"qualifier,omitempty"`
	ManagedKey       string `json:"managedKey,omitempty"`
}

type LambdaPermissionOutputs struct {
	StatementId  string `json:"statementId"`
	FunctionName string `json:"functionName"`
	Statement    string `json:"statement"`
}

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
