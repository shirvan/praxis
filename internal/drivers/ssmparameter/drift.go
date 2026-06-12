// Package ssmparameter – drift.go
//
// This file implements drift detection for SSM parameters.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// SecureString values are masked in field diffs so secrets never appear in
// plan output or logs.
package ssmparameter

import (
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

const sensitivePlaceholder = "(sensitive)"

// HasDrift compares the desired SSMParameter spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired SSMParameterSpec, observed ObservedState) bool {
	if parameterFieldsDrift(desired, observed) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// parameterFieldsDrift reports whether any field converged via PutParameter
// (everything except tags) has diverged from the observed state.
func parameterFieldsDrift(desired SSMParameterSpec, observed ObservedState) bool {
	if desired.Value != observed.Value {
		return true
	}
	if desired.Type != observed.Type {
		return true
	}
	if strings.TrimSpace(desired.Description) != strings.TrimSpace(observed.Description) {
		return true
	}
	if !tierMatch(desired.Tier, observed.Tier) {
		return true
	}
	if !kmsKeyMatch(desired.Type, desired.KmsKeyID, observed.KmsKeyID) {
		return true
	}
	if strings.TrimSpace(desired.AllowedPattern) != strings.TrimSpace(observed.AllowedPattern) {
		return true
	}
	if !dataTypeMatch(desired.DataType, observed.DataType) {
		return true
	}
	return false
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. SecureString values are masked.
func ComputeFieldDiffs(desired SSMParameterSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	if desired.Value != observed.Value {
		oldValue, newValue := any(observed.Value), any(desired.Value)
		if desired.Type == "SecureString" || observed.Type == "SecureString" {
			oldValue, newValue = sensitivePlaceholder, sensitivePlaceholder
		}
		diffs = append(diffs, FieldDiffEntry{Path: "spec.value", OldValue: oldValue, NewValue: newValue})
	}
	if desired.Type != observed.Type {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.type", OldValue: observed.Type, NewValue: desired.Type})
	}
	if strings.TrimSpace(desired.Description) != strings.TrimSpace(observed.Description) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if !tierMatch(desired.Tier, observed.Tier) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.tier", OldValue: normalizeTier(observed.Tier), NewValue: normalizeTier(desired.Tier)})
	}
	if !kmsKeyMatch(desired.Type, desired.KmsKeyID, observed.KmsKeyID) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.kmsKeyId", OldValue: observed.KmsKeyID, NewValue: desired.KmsKeyID})
	}
	if strings.TrimSpace(desired.AllowedPattern) != strings.TrimSpace(observed.AllowedPattern) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.allowedPattern", OldValue: observed.AllowedPattern, NewValue: desired.AllowedPattern})
	}
	if !dataTypeMatch(desired.DataType, observed.DataType) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.dataType", OldValue: normalizeDataType(observed.DataType), NewValue: normalizeDataType(desired.DataType)})
	}
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.value").
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// tierMatch treats an unset tier as Standard, which is what AWS reports for
// parameters created without an explicit tier.
func tierMatch(desired, observed string) bool {
	return normalizeTier(desired) == normalizeTier(observed)
}

func normalizeTier(tier string) string {
	tier = strings.TrimSpace(tier)
	if tier == "" {
		return "Standard"
	}
	return tier
}

// dataTypeMatch treats an unset data type as "text", AWS's default.
func dataTypeMatch(desired, observed string) bool {
	return normalizeDataType(desired) == normalizeDataType(observed)
}

func normalizeDataType(dataType string) string {
	dataType = strings.TrimSpace(dataType)
	if dataType == "" {
		return "text"
	}
	return dataType
}

// kmsKeyMatch compares KMS key configuration. A SecureString created without
// an explicit key is encrypted with the account default alias/aws/ssm, so an
// empty desired key matches that observed default.
func kmsKeyMatch(desiredType, desired, observed string) bool {
	desired = strings.TrimSpace(desired)
	observed = strings.TrimSpace(observed)
	if desired == observed {
		return true
	}
	if desiredType == "SecureString" && desired == "" && observed == "alias/aws/ssm" {
		return true
	}
	return false
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	cleanDesired := drivers.FilterPraxisTags(desired)
	cleanObserved := drivers.FilterPraxisTags(observed)
	for key, value := range cleanDesired {
		if current, ok := cleanObserved[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range cleanObserved {
		if _, ok := cleanDesired[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
