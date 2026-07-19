package lambda

#LambdaPermission: {
	apiVersion: "praxis.io/alpha"
	kind:       "LambdaPermission"
	metadata: {
		name: =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$"
	}
	spec: {
		region:            string
		functionName:      string
		statementId?:      string
		action?:           string | *"lambda:InvokeFunction"
		principal:         string
		sourceArn?:        string
		sourceAccount?:    string
		eventSourceToken?: string
		qualifier?:        string
	}
	outputs?: {
		statementId:  string
		functionName: string
		statement:    string
	}
}
