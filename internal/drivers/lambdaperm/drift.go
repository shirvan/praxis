package lambdaperm

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired LambdaPermissionSpec, observed ObservedState) bool {
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

func ComputeFieldDiffs(desired LambdaPermissionSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	if desired.Action != observed.Action {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.action", OldValue: observed.Action, NewValue: desired.Action})
	}
	if desired.Principal != observed.Principal {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.principal", OldValue: observed.Principal, NewValue: desired.Principal})
	}
	if desired.SourceArn != observed.SourceArn {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.sourceArn", OldValue: observed.SourceArn, NewValue: desired.SourceArn})
	}
	if desired.SourceAccount != observed.SourceAccount {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.sourceAccount", OldValue: observed.SourceAccount, NewValue: desired.SourceAccount})
	}
	if desired.EventSourceToken != observed.EventSourceToken {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.eventSourceToken", OldValue: observed.EventSourceToken, NewValue: desired.EventSourceToken})
	}
	return diffs
}
