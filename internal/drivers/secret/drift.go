// Package secret – drift.go
//
// This file implements drift detection for Secrets Manager secrets.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// The secret value is masked in field diffs so secrets never appear in plan
// output or logs.
package secret

import (
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

const sensitivePlaceholder = "(sensitive)"

// HasDrift compares the desired secret spec against the observed state from AWS
// and returns true if any mutable field has diverged. It is called during
// Reconcile to decide whether drift correction is needed.
func HasDrift(desired SecretsManagerSecretSpec, observed ObservedState) bool {
	if desired.Name != observed.Name {
		return true
	}
	// A secret scheduled for deletion is drifted by definition: the desired
	// state is "exists", and correction (convergeMutableFields) restores it.
	if observed.ScheduledForDeletion {
		return true
	}
	if secretFieldsDrift(desired, observed) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// secretFieldsDrift reports whether any non-tag mutable field (value,
// description, KMS key) has diverged from the observed state.
func secretFieldsDrift(desired SecretsManagerSecretSpec, observed ObservedState) bool {
	if desired.SecretString != observed.SecretString {
		return true
	}
	if strings.TrimSpace(desired.Description) != strings.TrimSpace(observed.Description) {
		return true
	}
	if !kmsKeyMatch(desired.KmsKeyID, observed.KmsKeyID) {
		return true
	}
	return false
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. The secret value is always masked.
func ComputeFieldDiffs(desired SecretsManagerSecretSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	if desired.Name != observed.Name {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.name (immutable, requires replacement)", OldValue: observed.Name, NewValue: desired.Name})
	}
	if desired.SecretString != observed.SecretString {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.secretString", OldValue: sensitivePlaceholder, NewValue: sensitivePlaceholder})
	}
	if strings.TrimSpace(desired.Description) != strings.TrimSpace(observed.Description) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if !kmsKeyMatch(desired.KmsKeyID, observed.KmsKeyID) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.kmsKeyId", OldValue: observed.KmsKeyID, NewValue: desired.KmsKeyID})
	}
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// kmsKeyMatch compares KMS key configuration. A secret created without an
// explicit key is encrypted with the account default alias/aws/secretsmanager,
// so an empty desired key matches that observed default.
func kmsKeyMatch(desired, observed string) bool {
	desired = strings.TrimSpace(desired)
	observed = strings.TrimSpace(observed)
	if desired == observed {
		return true
	}
	if desired == "" && observed == "alias/aws/secretsmanager" {
		return true
	}
	return false
}

func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	cleanDesired := drivers.FilterPraxisTags(desired)
	cleanObserved := drivers.FilterPraxisTags(observed)
	for key, value := range cleanDesired {
		if current, ok := cleanObserved[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range cleanObserved {
		if _, ok := cleanDesired[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
