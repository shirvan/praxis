package types

// ImportRef is the input to every driver's Import handler.
// It identifies an existing cloud resource to bring under Praxis management.
type ImportRef struct {
	// ResourceID is the cloud-provider-native identifier.
	// For S3: the bucket name. For RDS: the DB instance identifier.
	ResourceID string `json:"resourceId"`

	// Mode determines whether the imported resource will be actively
	// managed (drift corrected) or passively observed (drift reported).
	// Defaults to ModeManaged if empty.
	Mode Mode `json:"mode,omitempty"`

	// Account selects which configured AWS identity should be used to look up
	// and manage the imported resource.
	Account string `json:"account,omitempty"`
}
