package lambda

#LambdaLayer: {
	apiVersion: "praxis.io/v1"
	kind:       "LambdaLayer"
	metadata: {
		name: =~"^[A-Za-z0-9-_]+$"
	}
	spec: {
		region:    string
		account?:  string
		description?: string
		licenseInfo?: string
		compatibleRuntimes?: [...string]
		compatibleArchitectures?: [..."x86_64" | "arm64"]
		code: {
			s3?: {
				bucket: string
				key:    string
				objectVersion?: string
			}
			zipFile?: string
		}
		permissions?: {
			accountIds?: [...string]
			public?: bool
		}
	}
}