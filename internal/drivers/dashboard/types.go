// Package dashboard implements the Praxis driver for AWS CloudWatch Dashboard resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon CloudWatch; the driver state couples both together with status tracking.
package dashboard

// ServiceName is the Restate Virtual Object service name used to register the AWS CloudWatch Dashboard driver.
const ServiceName = "Dashboard"

// DashboardSpec declares the user's desired configuration for a AWS CloudWatch Dashboard.
// Fields are validated before any AWS call and mapped to Amazon CloudWatch API inputs.
type DashboardSpec struct {
	Account       string `json:"account,omitempty"`
	Region        string `json:"region"`
	DashboardName string `json:"dashboardName"`
	DashboardBody string `json:"dashboardBody"`
	ManagedKey    string `json:"managedKey,omitempty"`
}

// DashboardOutputs holds the values produced after provisioning a AWS CloudWatch Dashboard.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type DashboardOutputs struct {
	DashboardArn  string `json:"dashboardArn"`
	DashboardName string `json:"dashboardName"`
}

// ObservedState captures the live configuration of a AWS CloudWatch Dashboard
// as read from Amazon CloudWatch. It is compared against the spec
// during drift detection.
type ObservedState struct {
	DashboardArn  string `json:"dashboardArn"`
	DashboardName string `json:"dashboardName"`
	DashboardBody string `json:"dashboardBody"`
}

// ValidationMessage carries a dashboard body validation error with an optional data-path locator.
type ValidationMessage struct {
	DataPath string `json:"dataPath,omitempty"`
	Message  string `json:"message"`
}
