// Package snssub implements the Praxis driver for AWS SNS Subscription resources.
//
// This file defines the spec, outputs, and observed-state types that flow
// through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon Simple Notification Service (SNS). Generic lifecycle state is owned by
// the shared kernel.
package snssub

// ServiceName is the Restate Virtual Object name for SNS subscriptions.
const ServiceName = "SNSSubscription"

// SNSSubscriptionSpec is the desired state for an SNS subscription. Optional
// provider attributes are declarative: omission requests the SNS default or
// absence; it never means "leave an existing value unmanaged".
type SNSSubscriptionSpec struct {
	Account             string `json:"account,omitempty"`
	Region              string `json:"region"`
	TopicArn            string `json:"topicArn"`
	Protocol            string `json:"protocol"`
	Endpoint            string `json:"endpoint"`
	FilterPolicy        string `json:"filterPolicy,omitempty"`
	FilterPolicyScope   string `json:"filterPolicyScope,omitempty"`
	RawMessageDelivery  bool   `json:"rawMessageDelivery"`
	DeliveryPolicy      string `json:"deliveryPolicy,omitempty"`
	RedrivePolicy       string `json:"redrivePolicy,omitempty"`
	SubscriptionRoleArn string `json:"subscriptionRoleArn,omitempty"`
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
	SubscriptionArn     string `json:"subscriptionArn"`
	TopicArn            string `json:"topicArn"`
	Protocol            string `json:"protocol"`
	Endpoint            string `json:"endpoint"`
	Owner               string `json:"owner"`
	FilterPolicy        string `json:"filterPolicy,omitempty"`
	FilterPolicyScope   string `json:"filterPolicyScope,omitempty"`
	RawMessageDelivery  bool   `json:"rawMessageDelivery"`
	DeliveryPolicy      string `json:"deliveryPolicy,omitempty"`
	RedrivePolicy       string `json:"redrivePolicy,omitempty"`
	SubscriptionRoleArn string `json:"subscriptionRoleArn,omitempty"`
	PendingConfirmation bool   `json:"pendingConfirmation"`
	ConfirmationStatus  string `json:"confirmationStatus,omitempty"`
}
