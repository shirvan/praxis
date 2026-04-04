package ec2

#ElasticIP: {
	apiVersion: "praxis.io/v1"
	kind:       "ElasticIP"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region:              string
		domain:              "vpc" | *"vpc"
		networkBorderGroup?: string
		publicIpv4Pool?:     string
		tags: [string]: string
	}

	outputs?: {
		allocationId:       string
		publicIp:           string
		arn:                string
		domain:             string
		networkBorderGroup: string
	}
}
