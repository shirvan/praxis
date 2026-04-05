// Package ecrrepo – drift.go
//
// This file implements drift detection for AWS ECR Repository.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package ecrrepo

import (
	"encoding/json"
	"github.com/shirvan/praxis/internal/drivers"
)

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.name");
// immutable fields are annotated with "(immutable, requires replacement)".
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares the desired ECRRepository spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired ECRRepositorySpec, observed ObservedState) bool {
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired ECRRepositorySpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.RepositoryName != "" && desired.RepositoryName != observed.RepositoryName {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.repositoryName (immutable, ignored)", OldValue: observed.RepositoryName, NewValue: desired.RepositoryName})
	}
	if desired.ImageTagMutability != "" && desired.ImageTagMutability != observed.ImageTagMutability {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.imageTagMutability", OldValue: observed.ImageTagMutability, NewValue: desired.ImageTagMutability})
	}
	if !scanningEqual(desired.ImageScanningConfiguration, observed.ImageScanningConfiguration) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.imageScanningConfiguration", OldValue: observed.ImageScanningConfiguration, NewValue: desired.ImageScanningConfiguration})
	}
	if !encryptionEqual(desired.EncryptionConfiguration, observed.EncryptionConfiguration) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.encryptionConfiguration (immutable, ignored)", OldValue: observed.EncryptionConfiguration, NewValue: desired.EncryptionConfiguration})
	}
	if normalizeJSON(desired.RepositoryPolicy) != normalizeJSON(observed.RepositoryPolicy) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.repositoryPolicy", OldValue: observed.RepositoryPolicy, NewValue: desired.RepositoryPolicy})
	}
	if !tagsEqual(desired.Tags, observed.Tags) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.tags", OldValue: drivers.FilterPraxisTags(observed.Tags), NewValue: drivers.FilterPraxisTags(desired.Tags)})
	}

	return diffs
}

func scanningEqual(a, b *ImageScanningConfiguration) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil {
		return !b.ScanOnPush
	}
	if b == nil {
		return !a.ScanOnPush
	}
	return a.ScanOnPush == b.ScanOnPush
}

func encryptionEqual(a, b *EncryptionConfiguration) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil {
		return b.EncryptionType == "" || b.EncryptionType == "AES256"
	}
	if b == nil {
		return a.EncryptionType == "" || a.EncryptionType == "AES256"
	}
	return a.EncryptionType == b.EncryptionType && a.KmsKey == b.KmsKey
}

func tagsEqual(a, b map[string]string) bool {
	a = drivers.FilterPraxisTags(a)
	b = drivers.FilterPraxisTags(b)
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func normalizeJSON(value string) string {
	if value == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return value
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return value
	}
	return string(encoded)
}
