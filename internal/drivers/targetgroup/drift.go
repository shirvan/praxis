package targetgroup

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

func HasDrift(desired TargetGroupSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.Protocol != observed.Protocol || desired.Port != observed.Port || desired.VpcId != observed.VpcId || desired.TargetType != observed.TargetType || desired.ProtocolVersion != observed.ProtocolVersion {
		return true
	}
	if desired.HealthCheck != observed.HealthCheck {
		return true
	}
	if desired.DeregistrationDelay != observed.DeregistrationDelay {
		return true
	}
	if !stickinessEqual(desired.Stickiness, observed.Stickiness) {
		return true
	}
	if !targetsEqual(desired.Targets, observed.Targets) {
		return true
	}
	return !tagsMatch(desired.Tags, observed.Tags)
}

func ComputeFieldDiffs(desired TargetGroupSpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry

	if desired.Protocol != observed.Protocol {
		diffs = append(diffs, immutableDiff("spec.protocol", observed.Protocol, desired.Protocol))
	}
	if desired.Port != observed.Port {
		diffs = append(diffs, immutableDiff("spec.port", observed.Port, desired.Port))
	}
	if desired.VpcId != observed.VpcId {
		diffs = append(diffs, immutableDiff("spec.vpcId", observed.VpcId, desired.VpcId))
	}
	if desired.TargetType != observed.TargetType {
		diffs = append(diffs, immutableDiff("spec.targetType", observed.TargetType, desired.TargetType))
	}
	if desired.ProtocolVersion != observed.ProtocolVersion {
		diffs = append(diffs, immutableDiff("spec.protocolVersion", observed.ProtocolVersion, desired.ProtocolVersion))
	}
	if desired.HealthCheck != observed.HealthCheck {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.healthCheck", OldValue: observed.HealthCheck, NewValue: desired.HealthCheck})
	}
	if desired.DeregistrationDelay != observed.DeregistrationDelay {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.deregistrationDelay", OldValue: observed.DeregistrationDelay, NewValue: desired.DeregistrationDelay})
	}
	if !stickinessEqual(desired.Stickiness, observed.Stickiness) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.stickiness", OldValue: observed.Stickiness, NewValue: desired.Stickiness})
	}
	for _, diff := range computeTargetDiffs(desired.Targets, observed.Targets) {
		diffs = append(diffs, diff)
	}
	for _, diff := range computeTagDiffs(desired.Tags, observed.Tags) {
		diffs = append(diffs, diff)
	}
	return diffs
}

func immutableDiff(path string, oldValue, newValue any) FieldDiffEntry {
	return FieldDiffEntry{Path: path + " (immutable, requires replacement)", OldValue: oldValue, NewValue: newValue}
}

func computeTargetDiffs(desired, observed []Target) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredSet := targetSet(desired)
	observedSet := targetSet(observed)
	for key, value := range desiredSet {
		if _, ok := observedSet[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: fmt.Sprintf("spec.targets[%s]", key), OldValue: nil, NewValue: value})
		}
	}
	for key, value := range observedSet {
		if _, ok := desiredSet[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: fmt.Sprintf("spec.targets[%s]", key), OldValue: value, NewValue: nil})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
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

func stickinessEqual(a, b *Stickiness) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func targetsEqual(a, b []Target) bool {
	as := targetSet(a)
	bs := targetSet(b)
	if len(as) != len(bs) {
		return false
	}
	for key := range as {
		if _, ok := bs[key]; !ok {
			return false
		}
	}
	return true
}

func targetSet(targets []Target) map[string]Target {
	out := make(map[string]Target, len(targets))
	for _, target := range targets {
		out[targetKey(target)] = target
	}
	return out
}

func targetKey(target Target) string {
	return fmt.Sprintf("%s|%d|%s", target.ID, target.Port, target.AvailabilityZone)
}
