// wildcard-certificate.cue — Wildcard ACM certificate with SANs and DNS validation.
//
// Issues a wildcard certificate (*.example.com) that also covers the apex domain.
// Outputs DNS validation records for use with Route 53.
//
// Usage:
//   praxis template register examples/acm/wildcard-certificate.cue --description "Wildcard ACM certificate with SANs"
//   praxis deploy wildcard-certificate --account local -f examples/acm/wildcard-certificate.vars.json --key wildcard-cert --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
	baseDomain:  string & =~"^(([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\\.)+[a-zA-Z]{2,})$"
}

resources: cert: {
	apiVersion: "praxis.io/v1"
	kind:       "ACMCertificate"
	metadata: name: "\(variables.name)-\(variables.environment)-wildcard"
	spec: {
		region:     "us-east-1"
		domainName: "*.\(variables.baseDomain)"
		subjectAlternativeNames: [
			variables.baseDomain,
		]
		validationMethod: "DNS"
		keyAlgorithm:     "EC_prime256v1"
		options: certificateTransparencyLoggingPreference: "ENABLED"
		tags: {
			app:  variables.name
			env:  variables.environment
			type: "wildcard"
		}
	}
}
