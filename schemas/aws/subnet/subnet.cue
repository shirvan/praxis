package subnet

#Subnet: {
	apiVersion: "praxis.io/v1"
	kind:       "Subnet"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		// region is the AWS region for the subnet.
		region: string

		// vpcId is the VPC to create the subnet in.
		vpcId: string

		// cidrBlock is the IPv4 CIDR block for the subnet.
		cidrBlock: string & =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$"

		// availabilityZone is the AZ to create the subnet in.
		availabilityZone: string

		// mapPublicIpOnLaunch controls whether instances launched in this subnet
		// receive a public IPv4 address by default.
		mapPublicIpOnLaunch: bool | *false

		// tags applied to the subnet resource.
		tags: [string]: string
	}

	outputs?: {
		subnetId:            string
		arn:                 string
		vpcId:               string
		cidrBlock:           string
		availabilityZone:    string
		availabilityZoneId:  string
		mapPublicIpOnLaunch: bool
		state:               string
		ownerId:             string
		availableIpCount:    int
	}
}
