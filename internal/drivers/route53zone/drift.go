package route53zone

import (
	"fmt"
	"sort"
	"strings"
)

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired HostedZoneSpec, observed ObservedState) bool {
	desired, _ = normalizeHostedZoneSpec(desired)
	observed = normalizeObservedState(observed)
	if normalizeZoneComment(desired.Comment) != normalizeZoneComment(observed.Comment) {
		return true
	}
	if !hostedZoneVPCsMatch(desired.VPCs, observed.VPCs) {
		return true
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

func ComputeFieldDiffs(desired HostedZoneSpec, observed ObservedState) []FieldDiffEntry {
	desired, _ = normalizeHostedZoneSpec(desired)
	observed = normalizeObservedState(observed)

	var diffs []FieldDiffEntry
	if desired.Name != "" && observed.Name != "" && desired.Name != observed.Name {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.name (immutable, ignored)", OldValue: observed.Name, NewValue: desired.Name})
	}
	if desired.IsPrivate != observed.IsPrivate {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.isPrivate (immutable, ignored)", OldValue: observed.IsPrivate, NewValue: desired.IsPrivate})
	}
	if normalizeZoneComment(desired.Comment) != normalizeZoneComment(observed.Comment) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.comment", OldValue: observed.Comment, NewValue: desired.Comment})
	}
	diffs = append(diffs, computeVPCDiffs(desired.VPCs, observed.VPCs)...)
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

func normalizeHostedZoneSpec(spec HostedZoneSpec) (HostedZoneSpec, error) {
	spec.Name = normalizeZoneName(spec.Name)
	spec.Comment = normalizeZoneComment(spec.Comment)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.VPCs = normalizeHostedZoneVPCs(spec.VPCs)
	if spec.Name == "" {
		return HostedZoneSpec{}, fmt.Errorf("name is required")
	}
	if spec.IsPrivate && len(spec.VPCs) == 0 {
		return HostedZoneSpec{}, fmt.Errorf("private hosted zones require at least one VPC association")
	}
	if !spec.IsPrivate && len(spec.VPCs) > 0 {
		return HostedZoneSpec{}, fmt.Errorf("public hosted zones cannot specify VPC associations")
	}
	return spec, nil
}

func normalizeObservedState(observed ObservedState) ObservedState {
	observed.Name = normalizeZoneName(observed.Name)
	observed.Comment = normalizeZoneComment(observed.Comment)
	observed.HostedZoneId = normalizeHostedZoneID(observed.HostedZoneId)
	observed.VPCs = normalizeHostedZoneVPCs(observed.VPCs)
	observed.NameServers = normalizeStringSlice(observed.NameServers)
	if observed.Tags == nil {
		observed.Tags = map[string]string{}
	}
	return observed
}

func normalizeZoneName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

func normalizeZoneComment(comment string) string {
	return strings.TrimSpace(comment)
}

func normalizeHostedZoneID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "/hostedzone/")
	id = strings.TrimPrefix(id, "hostedzone/")
	return id
}

func normalizeHostedZoneVPCs(vpcs []HostedZoneVPC) []HostedZoneVPC {
	if len(vpcs) == 0 {
		return nil
	}
	seen := make(map[string]HostedZoneVPC, len(vpcs))
	for _, vpc := range vpcs {
		normalized := HostedZoneVPC{
			VpcId:     strings.TrimSpace(vpc.VpcId),
			VpcRegion: strings.TrimSpace(vpc.VpcRegion),
		}
		if normalized.VpcId == "" || normalized.VpcRegion == "" {
			continue
		}
		seen[hostedZoneVPCKey(normalized)] = normalized
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]HostedZoneVPC, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}
	return out
}

func hostedZoneVPCKey(vpc HostedZoneVPC) string {
	return strings.TrimSpace(vpc.VpcId) + "|" + strings.TrimSpace(vpc.VpcRegion)
}

func hostedZoneVPCsMatch(desired, observed []HostedZoneVPC) bool {
	desired = normalizeHostedZoneVPCs(desired)
	observed = normalizeHostedZoneVPCs(observed)
	if len(desired) != len(observed) {
		return false
	}
	for index := range desired {
		if desired[index] != observed[index] {
			return false
		}
	}
	return true
}

func computeVPCDiffs(desired, observed []HostedZoneVPC) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredSet := make(map[string]HostedZoneVPC, len(desired))
	for _, vpc := range normalizeHostedZoneVPCs(desired) {
		desiredSet[hostedZoneVPCKey(vpc)] = vpc
	}
	observedSet := make(map[string]HostedZoneVPC, len(observed))
	for _, vpc := range normalizeHostedZoneVPCs(observed) {
		observedSet[hostedZoneVPCKey(vpc)] = vpc
	}
	for key, vpc := range desiredSet {
		if _, ok := observedSet[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.vpcs[" + key + "]", OldValue: nil, NewValue: vpc})
		}
	}
	for key, vpc := range observedSet {
		if _, ok := desiredSet[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.vpcs[" + key + "]", OldValue: vpc, NewValue: nil})
		}
	}
	return diffs
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredFiltered := filterPraxisTags(desired)
	observedFiltered := filterPraxisTags(observed)
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
	return diffs
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

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(strings.TrimSuffix(value, "."))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return out
}
