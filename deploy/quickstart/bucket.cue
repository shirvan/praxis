resources: bucket: {
	apiVersion: "praxis.io/alpha"
	kind:       "S3Bucket"
	metadata: name: "praxis-alpha-quickstart"
	spec: {
		region:     "us-east-1"
		versioning: true
		acl:        "private"
		encryption: {
			enabled:   true
			algorithm: "AES256"
		}
		tags: purpose: "quickstart"
	}
}
