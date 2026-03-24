package template

import "encoding/json"

// DataSourceSpec is one entry from the template's top-level data block.
type DataSourceSpec struct {
	Kind    string           `json:"kind"`
	Region  string           `json:"region,omitempty"`
	Account string           `json:"account,omitempty"`
	Filter  DataSourceFilter `json:"filter"`
}

// DataSourceFilter contains the supported data source lookup selectors.
type DataSourceFilter struct {
	ID   string            `json:"id,omitempty"`
	Name string            `json:"name,omitempty"`
	Tag  map[string]string `json:"tag,omitempty"`
}

// EvaluationResult is the output of template evaluation.
type EvaluationResult struct {
	Resources   map[string]json.RawMessage
	DataSources map[string]DataSourceSpec
}
