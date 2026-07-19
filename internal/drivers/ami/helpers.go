package ami

import (
	"fmt"
	"github.com/shirvan/praxis/internal/drivers"
	"maps"
	"strings"
)

func validateSource(s SourceSpec) error {
	a := s.FromSnapshot != nil
	b := s.FromAMI != nil
	if !a && !b {
		return fmt.Errorf("exactly one of source.fromSnapshot or source.fromAMI must be specified")
	}
	if a && b {
		return fmt.Errorf("cannot specify both source.fromSnapshot and source.fromAMI")
	}
	if a && strings.TrimSpace(s.FromSnapshot.SnapshotId) == "" {
		return fmt.Errorf("source.fromSnapshot.snapshotId is required")
	}
	if b && strings.TrimSpace(s.FromAMI.SourceImageId) == "" {
		return fmt.Errorf("source.fromAMI.sourceImageId is required")
	}
	return nil
}
func desiredTags(s AMISpec) map[string]string {
	return mergeTags(s.Tags, map[string]string{"Name": s.Name, "praxis:managed-key": s.ManagedKey})
}
func mergeTags(base, extra map[string]string) map[string]string {
	o := make(map[string]string, len(base)+len(extra))
	maps.Copy(o, base)
	for k, v := range extra {
		if strings.TrimSpace(v) != "" {
			o[k] = v
		}
	}
	return o
}
func outputsFromObserved(o ObservedState) AMIOutputs {
	return AMIOutputs{ImageId: o.ImageId, Name: o.Name, State: o.State, Architecture: o.Architecture, VirtualizationType: o.VirtualizationType, RootDeviceName: o.RootDeviceName, OwnerId: o.OwnerId, CreationDate: o.CreationDate}
}
func specFromObserved(o ObservedState) AMISpec {
	s := AMISpec{Name: o.Name, Description: o.Description, Source: SourceSpec{FromAMI: &FromAMISpec{SourceImageId: o.ImageId}}, Tags: drivers.FilterPraxisTags(o.Tags)}
	if p := launchPermsFromObserved(o); p != nil {
		s.LaunchPermissions = p
	}
	if o.DeprecationTime != "" {
		s.Deprecation = &DeprecationSpec{DeprecateAt: o.DeprecationTime}
	}
	return s
}
func looksLikeAMIID(v string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(v)), "ami-")
}
