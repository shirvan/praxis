package eventing

const (
	ResourceEventOwnerServiceName  = "ResourceEventOwner"
	ResourceEventBridgeServiceName = "ResourceEventBridge"

	DriftEventDetected       = "detected"
	DriftEventCorrected      = "corrected"
	DriftEventExternalDelete = "external_delete"
)

type ResourceEventOwner struct {
	ResourceKey  string `json:"resourceKey,omitempty"`
	StreamKey    string `json:"streamKey"`
	Workspace    string `json:"workspace,omitempty"`
	Generation   int64  `json:"generation,omitempty"`
	ResourceName string `json:"resourceName"`
	ResourceKind string `json:"resourceKind"`
}

type DriftReportRequest struct {
	ResourceKey  string `json:"resourceKey"`
	ResourceKind string `json:"resourceKind,omitempty"`
	EventType    string `json:"eventType"`
	Error        string `json:"error,omitempty"`
}