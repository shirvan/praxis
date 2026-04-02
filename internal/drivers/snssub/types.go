package snssub

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for SNS subscriptions.
const ServiceName = "SNSSubscription"

// SNSSubscriptionSpec is the desired state for an SNS subscription.
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
