package lambda

#LambdaFunction: {
	apiVersion: "praxis.io/v1"
	kind:       "LambdaFunction"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$"
		labels?: [string]: string
	}

	spec: {
		region: string
		role:   string
		packageType?: "Zip" | "Image"
		runtime?: string
		handler?: string
		description?: string
		code: {
			s3?: {
				bucket: string
				key: string
				objectVersion?: string
			}
			zipFile?: string
			imageUri?: string
		}
		memorySize?: int & >=128 & <=10240
		timeout?: int & >=1 & <=900
		environment?: [string]: string
		layers?: [...string]
		vpcConfig?: {
			subnetIds?: [...string]
			securityGroupIds?: [...string]
		}
		deadLetterConfig?: {
			targetArn: string
		}
		tracingConfig?: {
			mode: "Active" | "PassThrough"
		}
		architectures?: [...("x86_64" | "arm64")]
		ephemeralStorage?: {
			size: int & >=512 & <=10240
		}
		tags?: [string]: string
	}

	outputs?: {
		functionArn:       string
		functionName:      string
		version?:          string
		state?:            string
		lastModified?:     string
		lastUpdateStatus?: string
		codeSha256?:       string
	}
}