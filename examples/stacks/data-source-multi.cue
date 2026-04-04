variables: {
	name:        string
	environment: "dev" | "staging" | "prod"
	bucketName:  string
	roleName:    string
}

data: {
	existingBucket: {
		kind: "S3Bucket"
		filter: name: variables.bucketName
	}
	existingRole: {
		kind: "IAMRole"
		filter: name: variables.roleName
	}
}

resources: artifactsBucket: {
	apiVersion: "praxis.io/v1"
	kind:       "S3Bucket"
	metadata: name: "\(variables.name)-\(variables.environment)-artifacts"
	spec: {
		region: "us-east-1"
		tags: {
			sourceBucket:  "${data.existingBucket.outputs.bucketName}"
			executionRole: "${data.existingRole.outputs.roleName}"
		}
	}
}
