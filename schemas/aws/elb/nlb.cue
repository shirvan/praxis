package elb

#NLB: {
	apiVersion: "praxis.io/v1"
	kind:       "NLB"

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
		subnetMappings?: [...#NLBSubnetMapping]
		crossZoneLoadBalancing: bool | *false
		deletionProtection: bool | *false
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

#NLBSubnetMapping: {
	subnetId:     string
	allocationId?: string
}
