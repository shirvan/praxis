// basic-vpc.cue — A simple VPC with DNS support.
//
// Usage:
//   praxis template register examples/vpc/basic-vpc.cue --description "Basic VPC with DNS"
//   praxis deploy basic-vpc --account local -f examples/vpc/basic-vpc.vars.json --key dev-vpc --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
	cidrBlock:   string | *"10.0.0.0/16"
}

resources: vpc: {
	apiVersion: "praxis.io/v1"
	kind:       "VPC"
	metadata: name: "\(variables.name)-\(variables.environment)-vpc"
	spec: {
		region:             "us-east-1"
		cidrBlock:          variables.cidrBlock
		enableDnsHostnames: true
		enableDnsSupport:   true
		tags: {
			app: variables.name
			env: variables.environment
		}
	}
}
