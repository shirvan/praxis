// basic-certificate.cue — DNS-validated ACM certificate for a single domain.
//
// Usage:
//   praxis template register examples/acm/basic-certificate.cue --description "Basic ACM certificate with DNS validation"
//   praxis deploy basic-certificate --account local -f examples/acm/basic-certificate.vars.json --key api-cert --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
	domainName:  string & =~"^(\\*\\.)?(([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\\.)+[a-zA-Z]{2,})$"
}

resources: {
	cert: {
		apiVersion: "praxis.io/v1"
		kind:       "ACMCertificate"
		metadata: name: "\(variables.name)-\(variables.environment)-cert"
		spec: {
			region:           "us-east-1"
			domainName:       variables.domainName
			validationMethod: "DNS"
			keyAlgorithm:     "RSA_2048"
			options: certificateTransparencyLoggingPreference: "ENABLED"
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}
}
