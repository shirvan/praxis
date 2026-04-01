package ecrpolicy

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired ECRLifecyclePolicySpec, observed ObservedState) bool {
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

func ComputeFieldDiffs(desired ECRLifecyclePolicySpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	if desired.RepositoryName != observed.RepositoryName {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.repositoryName (immutable, ignored)", OldValue: observed.RepositoryName, NewValue: desired.RepositoryName})
	}
	if normalizePolicy(desired.LifecyclePolicyText) != normalizePolicy(observed.LifecyclePolicyText) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.lifecyclePolicyText", OldValue: observed.LifecyclePolicyText, NewValue: desired.LifecyclePolicyText})
	}
	return diffs
}