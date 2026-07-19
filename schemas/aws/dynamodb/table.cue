package dynamodb

#DynamoDBTable: {
	apiVersion: "praxis.io/alpha"
	kind:       "DynamoDBTable"

	metadata: {
		// Table names are 3-255 chars: letters, digits, underscores, hyphens, dots.
		name: string & =~"^[a-zA-Z0-9_.\\-]{3,255}$"
		labels: [string]: string
	}

	spec: {
		region: string

		// Mutable — billing mode. PAY_PER_REQUEST (on-demand) or PROVISIONED.
		// readCapacity/writeCapacity are only meaningful for PROVISIONED.
		billingMode: "PAY_PER_REQUEST" | "PROVISIONED" | *"PAY_PER_REQUEST"

		// Immutable after creation — the primary key schema. Changing any of
		// these surfaces as an informational, requires-replacement diff.
		hashKey:      string
		hashKeyType:  "S" | "N" | "B" | *"S"
		rangeKey?:    string
		rangeKeyType: "S" | "N" | "B" | *"S"

		// Mutable — provisioned throughput. Only applied when billingMode is
		// PROVISIONED; ignored for PAY_PER_REQUEST.
		readCapacity:  int & >=1 | *5
		writeCapacity: int & >=1 | *5

		tags: [string]: string
	}

	outputs?: {
		arn:        string
		name:       string
		status:     string
		itemCount?: int
	}
}
