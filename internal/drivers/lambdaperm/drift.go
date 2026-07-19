// Drift detection for Lambda Permissions.
// Permissions are replace-only (no in-place update), so all fields are compared.
// Reconcile detects drift but does not auto-correct.
package lambdaperm

import "github.com/shirvan/praxis/internal/drivers"

// HasDrift returns true if any permission field differs between desired and observed.
func HasDrift(desired LambdaPermissionSpec, observed ObservedState) bool {
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

// ComputeFieldDiffs returns per-field diffs. Function name and statement ID are
// included so the generic lifecycle routes immutable identity changes through
// Converge, which rejects them with delete-and-reprovision guidance.
func ComputeFieldDiffs(desired LambdaPermissionSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	if desired.FunctionName != observed.FunctionName {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.functionName (immutable, requires replacement)", OldValue: observed.FunctionName, NewValue: desired.FunctionName})
	}
	if desired.StatementId != observed.StatementId {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.statementId (immutable, requires replacement)", OldValue: observed.StatementId, NewValue: desired.StatementId})
	}
	if desired.Action != observed.Action {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.action", OldValue: observed.Action, NewValue: desired.Action})
	}
	if desired.Principal != observed.Principal {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.principal", OldValue: observed.Principal, NewValue: desired.Principal})
	}
	if desired.SourceArn != observed.SourceArn {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.sourceArn", OldValue: observed.SourceArn, NewValue: desired.SourceArn})
	}
	if desired.SourceAccount != observed.SourceAccount {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.sourceAccount", OldValue: observed.SourceAccount, NewValue: desired.SourceAccount})
	}
	if desired.EventSourceToken != observed.EventSourceToken {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.eventSourceToken", OldValue: observed.EventSourceToken, NewValue: desired.EventSourceToken})
	}
	return diffs
}
