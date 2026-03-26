// https-stack.cue — ACM certificate with Route 53 DNS validation and ALB HTTPS listener.
//
// DAG:
//   cert → validationRecord (DNS CNAME for ACM validation)
//   cert → httpsListener (certificate ARN for HTTPS termination)
//
// This template demonstrates the full ACM workflow:
//   1. Request a DNS-validated certificate
//   2. Create the Route 53 CNAME record for validation using certificate outputs
//   3. Attach the certificate to an ALB HTTPS listener
//
// Prerequisites:
//   - An existing Route 53 hosted zone (referenced via hostedZoneId variable)
//   - An existing ALB and target group (referenced via albArn and targetGroupArn variables)
//
// Usage:
//   praxis template register examples/acm/https-stack.cue --description "ACM + Route53 validation + HTTPS listener"
//   praxis deploy https-stack --account local -f examples/acm/https-stack.vars.json --key api-https --wait

variables: {
	name:           string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:    "dev" | "staging" | "prod"
	domainName:     string & =~"^(([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\\.)+[a-zA-Z]{2,})$"
	hostedZoneId:   string
	albArn:         string
	targetGroupArn: string
}

resources: {
	// ═══════════════════════════════════════════════════
	// TLS CERTIFICATE
	// ═══════════════════════════════════════════════════

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

	// ═══════════════════════════════════════════════════
	// DNS VALIDATION RECORD
	// ═══════════════════════════════════════════════════

	validationRecord: {
		apiVersion: "praxis.io/v1"
		kind:       "DNSRecord"
		metadata: name: "\(variables.name)-\(variables.environment)-cert-validation"
		spec: {
			region:       "us-east-1"
			hostedZoneId: variables.hostedZoneId
			name:         "${resources.cert.outputs.dnsValidationRecords[0].resourceRecordName}"
			type:         "CNAME"
			ttl:          300
			records: [
				"${resources.cert.outputs.dnsValidationRecords[0].resourceRecordValue}",
			]
			tags: {
				app:     variables.name
				env:     variables.environment
				purpose: "acm-validation"
			}
		}
	}

	// ═══════════════════════════════════════════════════
	// HTTPS LISTENER
	// ═══════════════════════════════════════════════════

	httpsListener: {
		apiVersion: "praxis.io/v1"
		kind:       "Listener"
		metadata: name: "\(variables.name)-\(variables.environment)-https"
		spec: {
			region:          "us-east-1"
			loadBalancerArn: variables.albArn
			port:            443
			protocol:        "HTTPS"
			certificateArn:  "${resources.cert.outputs.certificateArn}"
			defaultAction: {
				type:           "forward"
				targetGroupArn: variables.targetGroupArn
			}
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}
}
