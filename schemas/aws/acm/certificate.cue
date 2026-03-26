package acm

#ACMCertificate: {
	apiVersion: "praxis.io/v1"
	kind:       "ACMCertificate"

	metadata: {
		name:   string & =~"^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		account?: string
		region:   string
		domainName: string & =~"^(\\*\\.)?(([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\\.)+[A-Za-z]{2,})$"
		subjectAlternativeNames?: [...string & =~"^(\\*\\.)?(([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\\.)+[A-Za-z]{2,})$"]
		validationMethod?: "DNS" | "EMAIL" | *"DNS"
		keyAlgorithm?: "RSA_1024" | "RSA_2048" | "RSA_3072" | "RSA_4096" | "EC_prime256v1" | "EC_secp384r1" | "EC_secp521r1" | *"RSA_2048"
		certificateAuthorityArn?: string
		options?: {
			certificateTransparencyLoggingPreference?: "ENABLED" | "DISABLED" | *"ENABLED"
		}
		tags: [string]: string
	}

	outputs?: {
		certificateArn: string
		domainName:     string
		status:         string
		dnsValidationRecords?: [...{
			domainName:          string
			resourceRecordName:  string
			resourceRecordType:  string
			resourceRecordValue: string
		}]
		notBefore?: string
		notAfter?:  string
	}
}