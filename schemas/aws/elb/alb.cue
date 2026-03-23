package elb

#ALB: {
	apiVersion: "praxis.io/v1"
	kind:       "ALB"

	metadata: {
		name: string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"
		labels: [string]: string
	}

	spec: {
		region:  string
		account?: string
		name:    string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"
		scheme:  "internet-facing" | "internal"
		ipAddressType: "ipv4" | "dualstack" | *"ipv4"
		subnets?: [...string]
		subnetMappings?: [...#SubnetMapping]
		securityGroups: [...string] & [_, ...]
		accessLogs?: {
			enabled: bool | *false
			bucket:  string
			prefix?: string
		}
		deletionProtection: bool | *false
		idleTimeout: int & >=1 & <=4000 | *60
		tags: [string]: string
	}

	outputs?: {
		loadBalancerArn:       string
		dnsName:               string
		hostedZoneId:          string
		vpcId:                 string
		canonicalHostedZoneId: string
	}
}

#SubnetMapping: {
	subnetId:     string
	allocationId?: string
}
