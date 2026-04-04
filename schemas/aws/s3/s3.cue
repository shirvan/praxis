package s3

#S3Bucket: {
	apiVersion: "praxis.io/v1"
	kind:       "S3Bucket"

	metadata: {
		// Bucket names must follow S3's naming rules:
		// 3-63 characters, lowercase letters, numbers, hyphens, periods.
		name: string & =~"^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$"
		labels: [string]: string
	}

	spec: {
		region:     string
		versioning: bool | *true // default: enabled (best practice)
		acl:        "private" | "public-read" | *"private"
		encryption: {
			enabled:   bool | *true // default: enabled (AWS best practice since Jan 2023)
			algorithm: *"AES256" | "aws:kms"
		}
		tags: [string]: string
	}

	// Outputs are populated by the driver after provisioning.
	// These fields are referenced in output expressions by dependent resources.
	// Optional at template time — the driver fills them after Provision.
	outputs?: {
		arn:        string
		bucketName: string
		region:     string
		domainName: string
	}
}
