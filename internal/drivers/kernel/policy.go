package kernel

import (
	"fmt"

	"github.com/shirvan/praxis/pkg/types"
)

func validateLifecyclePolicy(policy types.LifecyclePolicy) error {
	return types.ValidateLifecyclePolicy(policy)
}

// actionableDrift applies lifecycle.ignoreChanges to the provider-specific
// semantic field diffs. HasDrift and FieldDiffs must describe the same state;
// checking that invariant prevents an unlisted difference from being silently
// suppressed merely because an ignore list is present.
func actionableDrift[S, O, Obs any](descriptor Descriptor[S, O, Obs], desired S, observed Obs, ignoreChanges []string) (bool, error) {
	rawDrift := descriptor.HasDrift(desired, observed)
	diffs := descriptor.FieldDiffs(desired, observed)
	if rawDrift != (len(diffs) > 0) {
		return false, fmt.Errorf("kernel descriptor %s returned inconsistent drift and field-diff results", descriptor.ServiceName)
	}
	return len(types.FilterIgnoredFieldDiffs(diffs, ignoreChanges)) > 0, nil
}
