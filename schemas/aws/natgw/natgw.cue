package natgw

#NATGateway: {
	apiVersion: "praxis.io/v1"
	kind:       "NATGateway"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region: string
		subnetId: string
		connectivityType: "public" | "private" | *"public"
		allocationId?: string
		tags: [string]: string
	}

	outputs?: {
		natGatewayId:       string
		subnetId:           string
		vpcId:              string
		connectivityType:   string
		state:              string
		publicIp?:          string
		privateIp:          string
		allocationId?:      string
		networkInterfaceId: string
	}
}