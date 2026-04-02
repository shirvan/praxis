package snstopic

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for SNS topics.
const ServiceName = "SNSTopic"

// SNSTopicSpec is the desired state for an SNS topic.
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
