// Package snstopic implements the Praxis driver for AWS SNS Topic resources.
//
// This file defines the spec, outputs, and observed-state types that flow
// through the generic driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon Simple Notification Service (SNS); the driver state couples both together with status tracking.
package snstopic

// ServiceName is the Restate Virtual Object name for SNS topics.
const ServiceName = "SNSTopic"

// SNSTopicSpec is the desired state for an SNS topic. Optional provider
// attributes are declarative: omission requests the SNS default or absence; it
// never means "leave an existing value unmanaged".
type SNSTopicSpec struct {
	Account                   string            `json:"account,omitempty"`
	Region                    string            `json:"region"`
	TopicName                 string            `json:"topicName"`
	DisplayName               string            `json:"displayName,omitempty"`
	FifoTopic                 bool              `json:"fifoTopic"`
	ContentBasedDeduplication bool              `json:"contentBasedDeduplication"`
	Policy                    string            `json:"policy,omitempty"`
	DeliveryPolicy            string            `json:"deliveryPolicy,omitempty"`
	KmsMasterKeyId            string            `json:"kmsMasterKeyId,omitempty"`
	Tags                      map[string]string `json:"tags,omitempty"`
	ManagedKey                string            `json:"managedKey,omitempty"`
}

// SNSTopicOutputs is produced after provisioning and stored in Restate K/V.
type SNSTopicOutputs struct {
	TopicArn  string `json:"topicArn"`
	TopicName string `json:"topicName"`
	Owner     string `json:"owner"`
}

// ObservedState captures the actual configuration from AWS.
type ObservedState struct {
	TopicArn                  string            `json:"topicArn"`
	TopicName                 string            `json:"topicName"`
	DisplayName               string            `json:"displayName"`
	FifoTopic                 bool              `json:"fifoTopic"`
	ContentBasedDeduplication bool              `json:"contentBasedDeduplication"`
	Policy                    string            `json:"policy,omitempty"`
	DeliveryPolicy            string            `json:"deliveryPolicy,omitempty"`
	KmsMasterKeyId            string            `json:"kmsMasterKeyId,omitempty"`
	Owner                     string            `json:"owner"`
	Tags                      map[string]string `json:"tags"`
}
