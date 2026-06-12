package ssm

#SSMParameter: {
	apiVersion: "praxis.io/v1"
	kind:       "SSMParameter"

	metadata: {
		// Parameter names may be hierarchical paths, e.g. "/praxis/dev/db-host".
		name: string & =~"^[a-zA-Z0-9_.\\-/]{1,2048}$"
		labels: [string]: string
	}

	spec: {
		region: string
		type:   "String" | "StringList" | "SecureString" | *"String"
		value:  string
		description?: string
		tier: "Standard" | "Advanced" | "Intelligent-Tiering" | *"Standard"
		// kmsKeyId is only valid for SecureString parameters; when omitted,
		// the account default key (alias/aws/ssm) is used.
		kmsKeyId?:       string
		allowedPattern?: string
		dataType: "text" | "aws:ec2:image" | "aws:ssm:integration" | *"text"
		tags: [string]: string
	}

	// Outputs intentionally exclude the parameter value so SecureString
	// values never flow into deployment state. Reference values with the
	// ssm:// resolver instead.
	outputs?: {
		arn:           string
		parameterName: string
		type:          string
		version:       int
		tier:          string
		dataType?:     string
	}
}
