package vpc

// LateInitVPC fills in server-defaulted values from the observed VPC state
// without overwriting explicit user input. This prevents false drift on
// fields that AWS auto-populates at creation time.
func LateInitVPC(spec VPCSpec, observed ObservedState) (VPCSpec, bool) {
	changed := false

	// InstanceTenancy defaults to "default" when not specified by the user.
	if spec.InstanceTenancy == "" && observed.InstanceTenancy != "" {
		spec.InstanceTenancy = observed.InstanceTenancy
		changed = true
	}

	return spec, changed
}
