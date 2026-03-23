package route53

#Route53HealthCheck: {
	apiVersion: "praxis.io/v1"
	kind:       "Route53HealthCheck"

	metadata: {
		name:   string & =~"^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		type: "HTTP" | "HTTPS" | "HTTP_STR_MATCH" | "HTTPS_STR_MATCH" | "TCP" | "CALCULATED" | "CLOUDWATCH_METRIC"
		ipAddress?: string
		port?:      int
		resourcePath?: string
		fqdn?:         string
		searchString?: string
		requestInterval?: 10 | 30
		failureThreshold?: int
		childHealthChecks?: [...string]
		healthThreshold?:   int
		cloudWatchAlarmName?:   string
		cloudWatchAlarmRegion?: string
		insufficientDataHealthStatus?: "Healthy" | "Unhealthy" | "LastKnownStatus"
		disabled?:          bool | *false
		invertHealthCheck?: bool | *false
		enableSNI?:         bool | *false
		regions?:           [...string]
		tags?:              [string]: string
	}

	outputs?: {
		healthCheckId: string
	}
}