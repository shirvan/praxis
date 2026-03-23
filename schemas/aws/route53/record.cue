package route53

#Route53Record: {
	apiVersion: "praxis.io/v1"
	kind:       "Route53Record"

	metadata: {
		name:   string & =~"^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		hostedZoneId: string
		name:         string
		type:         string
		ttl?:         int
		resourceRecords?: [...string]
		aliasTarget?: {
			hostedZoneId:         string
			dnsName:              string
			evaluateTargetHealth: bool | *false
		}
		setIdentifier?: string
		weight?:        int
		region?:        string
		failover?:      "PRIMARY" | "SECONDARY"
		geoLocation?: {
			continentCode?:   string
			countryCode?:     string
			subdivisionCode?: string
		}
		multiValueAnswer?: bool
		healthCheckId?:    string
	}

	outputs?: {
		hostedZoneId:   string
		fqdn:           string
		type:           string
		setIdentifier?: string
	}
}