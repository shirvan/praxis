package route53healthcheck

import (
	"fmt"
	"sort"
	"strings"
)

// FieldDiffEntry describes a single field-level difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift returns true if any mutable field differs between desired and observed state.
// Type and requestInterval are immutable and excluded from actionable drift.
func HasDrift(desired HealthCheckSpec, observed ObservedState) bool {
	desired, _ = normalizeHealthCheckSpec(desired)
	observed = normalizeObservedState(observed)
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

// ComputeFieldDiffs returns a per-field list of differences. Reports type and requestInterval
// as informational immutable diffs; checks all other mutable fields, including tags.
func ComputeFieldDiffs(desired HealthCheckSpec, observed ObservedState) []FieldDiffEntry {
	desired, _ = normalizeHealthCheckSpec(desired)
	observed = normalizeObservedState(observed)

	var diffs []FieldDiffEntry
	if desired.Type != "" && observed.Type != "" && desired.Type != observed.Type {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.type (immutable, ignored)", OldValue: observed.Type, NewValue: desired.Type})
	}
	if desired.RequestInterval != 0 && observed.RequestInterval != 0 && desired.RequestInterval != observed.RequestInterval {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.requestInterval (immutable, ignored)", OldValue: observed.RequestInterval, NewValue: desired.RequestInterval})
	}
	diffs = appendIfDiff(diffs, "spec.ipAddress", observed.IPAddress, desired.IPAddress)
	diffs = appendIfDiff(diffs, "spec.port", observed.Port, desired.Port)
	diffs = appendIfDiff(diffs, "spec.resourcePath", observed.ResourcePath, desired.ResourcePath)
	diffs = appendIfDiff(diffs, "spec.fqdn", observed.FQDN, desired.FQDN)
	diffs = appendIfDiff(diffs, "spec.searchString", observed.SearchString, desired.SearchString)
	diffs = appendIfDiff(diffs, "spec.failureThreshold", observed.FailureThreshold, desired.FailureThreshold)
	diffs = appendIfDiff(diffs, "spec.healthThreshold", observed.HealthThreshold, desired.HealthThreshold)
	diffs = appendIfDiff(diffs, "spec.cloudWatchAlarmName", observed.CloudWatchAlarmName, desired.CloudWatchAlarmName)
	diffs = appendIfDiff(diffs, "spec.cloudWatchAlarmRegion", observed.CloudWatchAlarmRegion, desired.CloudWatchAlarmRegion)
	diffs = appendIfDiff(diffs, "spec.insufficientDataHealthStatus", observed.InsufficientDataHealthStatus, desired.InsufficientDataHealthStatus)
	diffs = appendIfDiff(diffs, "spec.disabled", observed.Disabled, desired.Disabled)
	diffs = appendIfDiff(diffs, "spec.invertHealthCheck", observed.InvertHealthCheck, desired.InvertHealthCheck)
	diffs = appendIfDiff(diffs, "spec.enableSNI", observed.EnableSNI, desired.EnableSNI)
	diffs = append(diffs, computeSliceDiff("spec.childHealthChecks", desired.ChildHealthChecks, observed.ChildHealthChecks)...)
	diffs = append(diffs, computeSliceDiff("spec.regions", desired.Regions, observed.Regions)...)
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

func normalizeHealthCheckSpec(spec HealthCheckSpec) (HealthCheckSpec, error) {
	spec.Type = strings.TrimSpace(spec.Type)
	spec.IPAddress = strings.TrimSpace(spec.IPAddress)
	spec.ResourcePath = strings.TrimSpace(spec.ResourcePath)
	spec.FQDN = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(spec.FQDN), "."))
	spec.SearchString = strings.TrimSpace(spec.SearchString)
	spec.CloudWatchAlarmName = strings.TrimSpace(spec.CloudWatchAlarmName)
	spec.CloudWatchAlarmRegion = strings.TrimSpace(spec.CloudWatchAlarmRegion)
	spec.InsufficientDataHealthStatus = strings.TrimSpace(spec.InsufficientDataHealthStatus)
	if spec.RequestInterval == 0 {
		spec.RequestInterval = 30
	}
	if spec.FailureThreshold == 0 {
		spec.FailureThreshold = 3
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.ChildHealthChecks = normalizeStringSlice(spec.ChildHealthChecks)
	spec.Regions = normalizeStringSlice(spec.Regions)
	if spec.Type == "" {
		return HealthCheckSpec{}, fmt.Errorf("type is required")
	}
	switch spec.Type {
	case "HTTP", "HTTPS", "HTTP_STR_MATCH", "HTTPS_STR_MATCH", "TCP":
		if spec.IPAddress == "" && spec.FQDN == "" {
			return HealthCheckSpec{}, fmt.Errorf("endpoint health checks require ipAddress or fqdn")
		}
		if spec.Port == 0 {
			spec.Port = defaultPortForHealthCheck(spec.Type)
		}
		if (spec.Type == "HTTP_STR_MATCH" || spec.Type == "HTTPS_STR_MATCH") && spec.SearchString == "" {
			return HealthCheckSpec{}, fmt.Errorf("searchString is required for %s health checks", spec.Type)
		}
	case "CALCULATED":
		if len(spec.ChildHealthChecks) == 0 {
			return HealthCheckSpec{}, fmt.Errorf("calculated health checks require childHealthChecks")
		}
	case "CLOUDWATCH_METRIC":
		if spec.CloudWatchAlarmName == "" || spec.CloudWatchAlarmRegion == "" {
			return HealthCheckSpec{}, fmt.Errorf("cloudwatch metric health checks require cloudWatchAlarmName and cloudWatchAlarmRegion")
		}
		if spec.InsufficientDataHealthStatus == "" {
			spec.InsufficientDataHealthStatus = "LastKnownStatus"
		}
	default:
		return HealthCheckSpec{}, fmt.Errorf("unsupported health check type %q", spec.Type)
	}
	return spec, nil
}

func normalizeObservedState(observed ObservedState) ObservedState {
	observed.HealthCheckId = strings.TrimSpace(observed.HealthCheckId)
	observed.Type = strings.TrimSpace(observed.Type)
	observed.IPAddress = strings.TrimSpace(observed.IPAddress)
	observed.ResourcePath = strings.TrimSpace(observed.ResourcePath)
	observed.FQDN = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(observed.FQDN), "."))
	observed.SearchString = strings.TrimSpace(observed.SearchString)
	observed.CloudWatchAlarmName = strings.TrimSpace(observed.CloudWatchAlarmName)
	observed.CloudWatchAlarmRegion = strings.TrimSpace(observed.CloudWatchAlarmRegion)
	observed.InsufficientDataHealthStatus = strings.TrimSpace(observed.InsufficientDataHealthStatus)
	observed.ChildHealthChecks = normalizeStringSlice(observed.ChildHealthChecks)
	observed.Regions = normalizeStringSlice(observed.Regions)
	if observed.Tags == nil {
		observed.Tags = map[string]string{}
	}
	return observed
}

func defaultPortForHealthCheck(checkType string) int32 {
	switch checkType {
	case "HTTPS", "HTTPS_STR_MATCH":
		return 443
	default:
		return 80
	}
}

func appendIfDiff(diffs []FieldDiffEntry, path string, oldValue, newValue any) []FieldDiffEntry {
	if fmt.Sprint(oldValue) != fmt.Sprint(newValue) {
		return append(diffs, FieldDiffEntry{Path: path, OldValue: oldValue, NewValue: newValue})
	}
	return diffs
}

func computeSliceDiff(path string, desired, observed []string) []FieldDiffEntry {
	desired = normalizeStringSlice(desired)
	observed = normalizeStringSlice(observed)
	if len(desired) != len(observed) {
		return []FieldDiffEntry{{Path: path, OldValue: observed, NewValue: desired}}
	}
	for index := range desired {
		if desired[index] != observed[index] {
			return []FieldDiffEntry{{Path: path, OldValue: observed, NewValue: desired}}
		}
	}
	return nil
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
