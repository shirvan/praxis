package route53

#Route53HostedZone: {
	apiVersion: "praxis.io/v1"
	kind:       "Route53HostedZone"

	metadata: {
		name: string & =~"^[A-Za-z0-9][A-Za-z0-9.-]{0,253}[A-Za-z0-9]$"
		labels: [string]: string
	}

	spec: {
		isPrivate?: bool | *false
		comment?:   string
		vpcs?: [...{
			vpcId:     string
			vpcRegion: string
		}]
		tags?: [string]: string
	}

	outputs?: {
		hostedZoneId: string
		name:         string
		nameServers?: [...string]
		isPrivate:   bool
		recordCount: int
	}
}
