package ec2

// LateInitEC2Instance fills in server-defaulted values from the observed
// instance state without overwriting explicit user input. This prevents
// false drift on fields that AWS auto-populates at launch time.
func LateInitEC2Instance(spec EC2InstanceSpec, observed ObservedState) (EC2InstanceSpec, bool) {
	changed := false

	// Root volume type defaults to gp2 if the user didn't specify one.
	if spec.RootVolume != nil && spec.RootVolume.VolumeType == "" && observed.RootVolumeType != "" {
		spec.RootVolume.VolumeType = observed.RootVolumeType
		changed = true
	}

	// Root volume size: if user didn't set it, adopt the AMI default.
	if spec.RootVolume != nil && spec.RootVolume.SizeGiB == 0 && observed.RootVolumeSizeGiB > 0 {
		spec.RootVolume.SizeGiB = observed.RootVolumeSizeGiB
		changed = true
	}

	return spec, changed
}
