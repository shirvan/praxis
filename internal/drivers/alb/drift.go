package alb

import (
	"sort"
	"strings"
)

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired ALBSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.IpAddressType != observed.IpAddressType {
		return true
	}
	if !sortedStringsEqual(resolveSubnets(desired), observed.Subnets) {
		return true
	}
	if !sortedStringsEqual(sortedCopy(desired.SecurityGroups), observed.SecurityGroups) {
		return true
	}
	if !accessLogsEqual(desired.AccessLogs, observed.AccessLogs) {
		return true
	}
	if desired.DeletionProtection != observed.DeletionProtection {
		return true
	}
	if desired.IdleTimeout != observed.IdleTimeout {
		return true
	}
	return !tagsMatch(desired.Tags, observed.Tags)
}

func ComputeFieldDiffs(desired ALBSpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry

	if desired.Scheme != observed.Scheme {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.scheme (immutable, requires replacement)", OldValue: observed.Scheme, NewValue: desired.Scheme})
	}
	if desired.IpAddressType != observed.IpAddressType {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.ipAddressType", OldValue: observed.IpAddressType, NewValue: desired.IpAddressType})
	}
	desiredSubnets := resolveSubnets(desired)
	if !sortedStringsEqual(desiredSubnets, observed.Subnets) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.subnets", OldValue: observed.Subnets, NewValue: desiredSubnets})
	}
	desiredSGs := sortedCopy(desired.SecurityGroups)
	if !sortedStringsEqual(desiredSGs, observed.SecurityGroups) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.securityGroups", OldValue: observed.SecurityGroups, NewValue: desiredSGs})
	}
	if !accessLogsEqual(desired.AccessLogs, observed.AccessLogs) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.accessLogs", OldValue: observed.AccessLogs, NewValue: desired.AccessLogs})
	}
	if desired.DeletionProtection != observed.DeletionProtection {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.deletionProtection", OldValue: observed.DeletionProtection, NewValue: desired.DeletionProtection})
	}
	if desired.IdleTimeout != observed.IdleTimeout {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.idleTimeout", OldValue: observed.IdleTimeout, NewValue: desired.IdleTimeout})
	}
	for _, diff := range computeTagDiffs(desired.Tags, observed.Tags) {
		diffs = append(diffs, diff)
	}
	return diffs
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	fd := filterPraxisTags(desired)
	fo := filterPraxisTags(observed)
	for key, value := range fd {
		if oldValue, ok := fo[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if oldValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: oldValue, NewValue: value})
		}
	}
	for key, value := range fo {
		if _, ok := fd[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
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

func accessLogsEqual(a, b *AccessLogConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func resolveSubnets(spec ALBSpec) []string {
	if len(spec.SubnetMappings) > 0 {
		return normalizeSubnets(spec.SubnetMappings)
	}
	return sortedCopy(spec.Subnets)
}

func sortedCopy(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	sort.Strings(out)
	return out
}

func sortedStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func accessLogsFromSpec(spec ALBSpec) *AccessLogConfig {
	if spec.AccessLogs == nil {
		return &AccessLogConfig{Enabled: false}
	}
	return spec.AccessLogs
}

// resolveSubnetMappings returns either SubnetMappings or converts Subnets.
func resolveSubnetMappings(spec ALBSpec) []SubnetMapping {
	if len(spec.SubnetMappings) > 0 {
		return spec.SubnetMappings
	}
	return subnetsToMappings(spec.Subnets)
}

var _ = strings.HasPrefix // keep import
