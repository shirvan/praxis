package vpc

#VPC: {
	apiVersion: "praxis.io/v1"
	kind:       "VPC"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		// region is the AWS region to create the VPC in.
		region: string

		// cidrBlock is the primary IPv4 CIDR block for the VPC.
		// Must be a valid CIDR in the range /16 to /28.
		// Immutable after creation — changing this requires VPC replacement.
		cidrBlock: string & =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$"

		// enableDnsHostnames controls whether instances in the VPC receive
		// public DNS hostnames. Requires enableDnsSupport to be true.
		// Default: false for non-default VPCs.
		enableDnsHostnames: bool | *false

		// enableDnsSupport controls whether DNS resolution is supported in the VPC.
		// Default: true.
		enableDnsSupport: bool | *true

		// instanceTenancy defines the default tenancy for instances launched
		// into this VPC. Once set to "dedicated", it cannot be changed back
		// to "default" (AWS restriction).
		// - "default": instances launch on shared hardware (most common).
		// - "dedicated": instances launch on single-tenant hardware (higher cost).
		// Immutable after creation.
		instanceTenancy: "default" | "dedicated" | *"default"

		// tags applied to the VPC resource.
		tags: [string]: string
	}

	outputs?: {
		vpcId:              string
		arn:                string
		cidrBlock:          string
		state:              string
		enableDnsHostnames: bool
		enableDnsSupport:   bool
		instanceTenancy:    string
		ownerId:            string
		dhcpOptionsId:      string
		isDefault:          bool
	}
}
