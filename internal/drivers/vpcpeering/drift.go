package vpcpeering

import "strings"

func HasDrift(desired VPCPeeringSpec, observed ObservedState) bool {
	if observed.Status != "active" {
		return false
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	if optionsDrift(desired.RequesterOptions, observed.RequesterOptions) {
		return true
	}
	if optionsDrift(desired.AccepterOptions, observed.AccepterOptions) {
		return true
	}
	return false
}

func ComputeFieldDiffs(desired VPCPeeringSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	desiredFiltered := filterPraxisTags(desired.Tags)
	observedFiltered := filterPraxisTags(observed.Tags)
	for key, value := range desiredFiltered {
		if observedValue, ok := observedFiltered[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range observedFiltered {
		if _, ok := desiredFiltered[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}

	if optionsDrift(desired.RequesterOptions, observed.RequesterOptions) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.requesterOptions.allowDnsResolutionFromRemoteVpc",
			OldValue: optionValue(observed.RequesterOptions),
			NewValue: optionValue(desired.RequesterOptions),
		})
	}
	if optionsDrift(desired.AccepterOptions, observed.AccepterOptions) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.accepterOptions.allowDnsResolutionFromRemoteVpc",
			OldValue: optionValue(observed.AccepterOptions),
			NewValue: optionValue(desired.AccepterOptions),
		})
	}

	if desired.RequesterVpcId != observed.RequesterVpcId && observed.RequesterVpcId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.requesterVpcId (immutable, requires replacement)",
			OldValue: observed.RequesterVpcId,
			NewValue: desired.RequesterVpcId,
		})
	}
	if desired.AccepterVpcId != observed.AccepterVpcId && observed.AccepterVpcId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.accepterVpcId (immutable, requires replacement)",
			OldValue: observed.AccepterVpcId,
			NewValue: desired.AccepterVpcId,
		})
	}

	return diffs
}

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
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

func optionsDrift(desired *PeeringOptions, observed *PeeringOptions) bool {
	return optionValue(desired) != optionValue(observed)
}

func optionValue(options *PeeringOptions) bool {
	if options == nil {
		return false
	}
	return options.AllowDnsResolutionFromRemoteVpc
}
