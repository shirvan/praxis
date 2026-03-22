package igw

#InternetGateway: {
	apiVersion: "praxis.io/v1"
	kind:       "InternetGateway"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		// region is the AWS region.
		region: string

		// vpcId is the VPC to attach the internet gateway to.
		vpcId: string

		// tags applied to the internet gateway resource.
		tags: [string]: string
	}

	outputs?: {
		internetGatewayId: string
		vpcId:             string
		ownerId:           string
		state:             string
	}
}