package template

import "encoding/json"

// DataSourceSpec is one entry from the template's top-level data block.
// Data sources are read-only lookups that resolve existing AWS resources
// (e.g. find an AMI by name tag) so their attributes can be referenced
// by other resources in the template. The spec is extracted during CUE
// evaluation and later dispatched to the matching driver's Lookup handler.
type DataSourceSpec struct {
	// Kind identifies the resource type to look up (e.g. "AMI", "VPC").
	// This must match a registered adapter kind in the provider registry.
	Kind string `json:"kind"`
	// Region is the AWS region for the lookup. Falls back to the account
	// default if omitted.
	Region string `json:"region,omitempty"`
	// Account is the AWS account alias for multi-account lookups.
	Account string `json:"account,omitempty"`
	// Filter specifies the selectors used to find the resource.
	Filter DataSourceFilter `json:"filter"`
}

// DataSourceFilter contains the supported data source lookup selectors.
// At least one field must be set. The driver will use the most specific
// selector available: ID takes precedence over Name, which takes
// precedence over Tag.
type DataSourceFilter struct {
	// ID is a provider-native resource ID (e.g. "ami-0abc123").
	ID string `json:"id,omitempty"`
	// Name matches the AWS "Name" tag or native name field.
	Name string `json:"name,omitempty"`
	// Tag is an arbitrary key/value set for tag-based filtering.
	Tag map[string]string `json:"tag,omitempty"`
}

// EvaluationResult is the output of template evaluation.
// It contains the per-resource JSON specs ready for provisioning and
// the data source specs that need to be resolved before deployment.
type EvaluationResult struct {
	// Resources maps resource name → raw JSON spec. Each value is the
	// complete resource document including kind, metadata, and spec fields.
	Resources map[string]json.RawMessage
	// DataSources maps data source name → DataSourceSpec. These are resolved
	// by the command service before the orchestrator begins provisioning.
	DataSources map[string]DataSourceSpec
}
