package lambda

#LambdaLayer: {
	apiVersion: "praxis.io/alpha"
	kind:       "LambdaLayer"
	metadata: {
		name: =~"^[A-Za-z0-9-_]+$"
	}
	spec: {
		region:       string
		account?:     string
		description?: string
		licenseInfo?: string
		compatibleRuntimes?: [...string]
		compatibleArchitectures?: [..."x86_64" | "arm64"]
		code: {
			s3?: {
				bucket:         string
				key:            string
				objectVersion?: string
			}
			// zipFile must be a base64-encoded deployment package.
			zipFile?: string & =~"^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$"
		}
		permissions?: {
			accountIds?: [...string]
			public?: bool
		}
	}
	outputs?: {
		layerArn:        string
		layerVersionArn: string
		layerName:       string
		version:         int
		codeSize:        int
		codeSha256?:     string
		createdDate?:    string
	}
}
