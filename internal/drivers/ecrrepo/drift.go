package ecrrepo

import "encoding/json"

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired ECRRepositorySpec, observed ObservedState) bool {
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

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
		diffs = append(diffs, FieldDiffEntry{Path: "spec.tags", OldValue: filterPraxisTags(observed.Tags), NewValue: filterPraxisTags(desired.Tags)})
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
	a = filterPraxisTags(a)
	b = filterPraxisTags(b)
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

func filterPraxisTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		if len(key) >= 7 && key[:7] == "praxis:" {
			continue
		}
		out[key] = value
	}
	return out
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
