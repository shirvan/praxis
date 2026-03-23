package route53record

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

func HasDrift(desired RecordSpec, observed ObservedState) bool {
	desired, _ = normalizeRecordSpec(desired)
	observed = normalizeObservedState(observed)
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

func ComputeFieldDiffs(desired RecordSpec, observed ObservedState) []FieldDiffEntry {
	desired, _ = normalizeRecordSpec(desired)
	observed = normalizeObservedState(observed)
	var diffs []FieldDiffEntry
	if observed.Name != "" && desired.Name != observed.Name {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.name (immutable, ignored)", OldValue: observed.Name, NewValue: desired.Name})
	}
	if observed.Type != "" && desired.Type != observed.Type {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.type (immutable, ignored)", OldValue: observed.Type, NewValue: desired.Type})
	}
	if observed.HostedZoneId != "" && desired.HostedZoneId != observed.HostedZoneId {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.hostedZoneId (immutable, ignored)", OldValue: observed.HostedZoneId, NewValue: desired.HostedZoneId})
	}
	if observed.SetIdentifier != "" && desired.SetIdentifier != observed.SetIdentifier {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.setIdentifier (immutable, ignored)", OldValue: observed.SetIdentifier, NewValue: desired.SetIdentifier})
	}
	diffs = appendIfDiff(diffs, "spec.ttl", observed.TTL, desired.TTL)
	diffs = appendIfDiff(diffs, "spec.weight", observed.Weight, desired.Weight)
	diffs = appendIfDiff(diffs, "spec.region", observed.Region, desired.Region)
	diffs = appendIfDiff(diffs, "spec.failover", observed.Failover, desired.Failover)
	diffs = appendIfDiff(diffs, "spec.multiValueAnswer", observed.MultiValueAnswer, desired.MultiValueAnswer)
	diffs = appendIfDiff(diffs, "spec.healthCheckId", observed.HealthCheckId, desired.HealthCheckId)
	if !resourceRecordsMatch(desired.ResourceRecords, observed.ResourceRecords) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.resourceRecords", OldValue: observed.ResourceRecords, NewValue: desired.ResourceRecords})
	}
	if !aliasTargetsMatch(desired.AliasTarget, observed.AliasTarget) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.aliasTarget", OldValue: observed.AliasTarget, NewValue: desired.AliasTarget})
	}
	if !geoLocationsMatch(desired.GeoLocation, observed.GeoLocation) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.geoLocation", OldValue: observed.GeoLocation, NewValue: desired.GeoLocation})
	}
	return diffs
}

func normalizeRecordSpec(spec RecordSpec) (RecordSpec, error) {
	spec.HostedZoneId = normalizeHostedZoneID(spec.HostedZoneId)
	spec.Name = normalizeRecordName(spec.Name)
	spec.Type = strings.TrimSpace(strings.ToUpper(spec.Type))
	spec.SetIdentifier = strings.TrimSpace(spec.SetIdentifier)
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Failover = strings.TrimSpace(strings.ToUpper(spec.Failover))
	spec.HealthCheckId = strings.TrimSpace(spec.HealthCheckId)
	spec.ResourceRecords = normalizeStringSlice(spec.ResourceRecords)
	if spec.AliasTarget != nil {
		spec.AliasTarget = &AliasTarget{
			HostedZoneId:         normalizeHostedZoneID(spec.AliasTarget.HostedZoneId),
			DNSName:              strings.TrimSuffix(strings.TrimSpace(spec.AliasTarget.DNSName), "."),
			EvaluateTargetHealth: spec.AliasTarget.EvaluateTargetHealth,
		}
	}
	if spec.GeoLocation != nil {
		spec.GeoLocation = &GeoLocation{
			ContinentCode:   strings.TrimSpace(spec.GeoLocation.ContinentCode),
			CountryCode:     strings.TrimSpace(spec.GeoLocation.CountryCode),
			SubdivisionCode: strings.TrimSpace(spec.GeoLocation.SubdivisionCode),
		}
	}
	if spec.HostedZoneId == "" {
		return RecordSpec{}, fmt.Errorf("hostedZoneId is required")
	}
	if spec.Name == "" {
		return RecordSpec{}, fmt.Errorf("name is required")
	}
	if spec.Type == "" {
		return RecordSpec{}, fmt.Errorf("type is required")
	}
	if spec.AliasTarget == nil {
		if spec.TTL == 0 {
			return RecordSpec{}, fmt.Errorf("ttl is required for standard records")
		}
		if len(spec.ResourceRecords) == 0 {
			return RecordSpec{}, fmt.Errorf("resourceRecords are required for standard records")
		}
	} else {
		if spec.AliasTarget.HostedZoneId == "" || spec.AliasTarget.DNSName == "" {
			return RecordSpec{}, fmt.Errorf("aliasTarget.hostedZoneId and aliasTarget.dnsName are required")
		}
		if spec.TTL != 0 || len(spec.ResourceRecords) > 0 {
			return RecordSpec{}, fmt.Errorf("ttl and resourceRecords must be omitted for alias records")
		}
	}
	return spec, nil
}

func normalizeObservedState(observed ObservedState) ObservedState {
	observed.HostedZoneId = normalizeHostedZoneID(observed.HostedZoneId)
	observed.Name = normalizeRecordName(observed.Name)
	observed.Type = strings.TrimSpace(strings.ToUpper(observed.Type))
	observed.SetIdentifier = strings.TrimSpace(observed.SetIdentifier)
	observed.Region = strings.TrimSpace(observed.Region)
	observed.Failover = strings.TrimSpace(strings.ToUpper(observed.Failover))
	observed.HealthCheckId = strings.TrimSpace(observed.HealthCheckId)
	observed.ResourceRecords = normalizeStringSlice(observed.ResourceRecords)
	if observed.AliasTarget != nil {
		observed.AliasTarget = &AliasTarget{
			HostedZoneId:         normalizeHostedZoneID(observed.AliasTarget.HostedZoneId),
			DNSName:              strings.TrimSuffix(strings.TrimSpace(observed.AliasTarget.DNSName), "."),
			EvaluateTargetHealth: observed.AliasTarget.EvaluateTargetHealth,
		}
	}
	if observed.GeoLocation != nil {
		observed.GeoLocation = &GeoLocation{
			ContinentCode:   strings.TrimSpace(observed.GeoLocation.ContinentCode),
			CountryCode:     strings.TrimSpace(observed.GeoLocation.CountryCode),
			SubdivisionCode: strings.TrimSpace(observed.GeoLocation.SubdivisionCode),
		}
	}
	return observed
}

func appendIfDiff(diffs []FieldDiffEntry, path string, oldValue, newValue any) []FieldDiffEntry {
	if fmt.Sprint(oldValue) != fmt.Sprint(newValue) {
		return append(diffs, FieldDiffEntry{Path: path, OldValue: oldValue, NewValue: newValue})
	}
	return diffs
}

func normalizeHostedZoneID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "/hostedzone/")
	id = strings.TrimPrefix(id, "hostedzone/")
	return id
}

func normalizeRecordName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
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

func resourceRecordsMatch(desired, observed []string) bool {
	desired = normalizeStringSlice(desired)
	observed = normalizeStringSlice(observed)
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

func aliasTargetsMatch(a, b *AliasTarget) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return normalizeHostedZoneID(a.HostedZoneId) == normalizeHostedZoneID(b.HostedZoneId) && strings.TrimSuffix(strings.TrimSpace(a.DNSName), ".") == strings.TrimSuffix(strings.TrimSpace(b.DNSName), ".") && a.EvaluateTargetHealth == b.EvaluateTargetHealth
}

func geoLocationsMatch(a, b *GeoLocation) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return strings.TrimSpace(a.ContinentCode) == strings.TrimSpace(b.ContinentCode) && strings.TrimSpace(a.CountryCode) == strings.TrimSpace(b.CountryCode) && strings.TrimSpace(a.SubdivisionCode) == strings.TrimSpace(b.SubdivisionCode)
}
