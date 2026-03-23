package dbparametergroup

import "strings"

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired DBParameterGroupSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if !parametersEqual(desired.Parameters, observed.Parameters) {
		return true
	}
	return !tagsMatch(desired.Tags, observed.Tags)
}

func ComputeFieldDiffs(desired DBParameterGroupSpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry

	if desired.GroupName != observed.GroupName && observed.GroupName != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.groupName (immutable, ignored)", OldValue: observed.GroupName, NewValue: desired.GroupName})
	}
	if desired.Type != observed.Type && observed.Type != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.type (immutable, ignored)", OldValue: observed.Type, NewValue: desired.Type})
	}
	if desired.Family != observed.Family && observed.Family != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.family (immutable, ignored)", OldValue: observed.Family, NewValue: desired.Family})
	}
	if desired.Description != observed.Description && observed.Description != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.description (immutable, ignored)", OldValue: observed.Description, NewValue: desired.Description})
	}
	for key, value := range desired.Parameters {
		if current, ok := observed.Parameters[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.parameters." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.parameters." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range observed.Parameters {
		if _, ok := desired.Parameters[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.parameters." + key, OldValue: value, NewValue: nil})
		}
	}
	for key, value := range filterPraxisTags(desired.Tags) {
		if current, ok := filterPraxisTags(observed.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range filterPraxisTags(observed.Tags) {
		if _, ok := filterPraxisTags(desired.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

func applyDefaults(spec DBParameterGroupSpec) DBParameterGroupSpec {
	if strings.TrimSpace(spec.Type) == "" {
		spec.Type = TypeDB
	}
	if spec.Parameters == nil {
		spec.Parameters = map[string]string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func parametersEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func tagsMatch(a, b map[string]string) bool {
	fa := filterPraxisTags(a)
	fb := filterPraxisTags(b)
	if len(fa) != len(fb) {
		return false
	}
	for key, value := range fa {
		if other, ok := fb[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func filterPraxisTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		if !strings.HasPrefix(key, "praxis:") {
			out[key] = value
		}
	}
	return out
}
