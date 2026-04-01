package ecr

#ECRRepository: {
	apiVersion: "praxis.io/v1"
	kind:       "ECRRepository"

	metadata: {
		name:   string & =~"^[a-z0-9][a-z0-9/_.-]{1,255}$"
		labels: [string]: string
	}

	spec: {
		region: string

		imageTagMutability?: "MUTABLE" | "IMMUTABLE" | *"MUTABLE"

		imageScanningConfiguration?: {
			scanOnPush: bool | *false
		}

		encryptionConfiguration?: {
			encryptionType: "AES256" | "KMS" | *"AES256"
			kmsKey?:         string
		}

		repositoryPolicy?: string
		forceDelete?:      bool | *false
		tags?:             [string]: string
	}

	outputs?: {
		repositoryArn:  string
		repositoryName: string
		repositoryUri:  string
		registryId:     string
	}
}