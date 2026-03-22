// dynamic-subnets.cue — Generate subnets from a struct list variable.
//
// Demonstrates CUE comprehensions with struct-typed list elements.
// A for loop generates one subnet per entry, each wired to the VPC
// via output expressions.
//
// Usage:
//   praxis template register examples/vpc/dynamic-subnets.cue --description "Dynamic subnets from list"
//   praxis deploy dynamic-subnets --account local -f examples/vpc/dynamic-subnets.vars.json --key myapp-vpc --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
	region:      string | *"us-east-1"
	vpcCidr:     string | *"10.0.0.0/16"
	subnets: [...{
		suffix: string
		cidr:   string
		az:     string
		public: bool | *false
	}]
}

// Shared naming helper
let prefix = "\(variables.name)-\(variables.environment)"

resources: {
	// ── VPC ─────────────────────────────────────────────
	vpc: {
		apiVersion: "praxis.io/v1"
		kind:       "VPC"
		metadata: name: "\(prefix)-vpc"
		spec: {
			region:             variables.region
			cidrBlock:          variables.vpcCidr
			enableDnsSupport:   true
			enableDnsHostnames: true
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}

	// ── Dynamic Subnets ─────────────────────────────────
	for _, sub in variables.subnets {
		"subnet-\(sub.suffix)": {
			apiVersion: "praxis.io/v1"
			kind:       "Subnet"
			metadata: name: "\(prefix)-\(sub.suffix)"
			spec: {
				region:              variables.region
				vpcId:               "${resources.vpc.outputs.vpcId}"
				cidrBlock:           sub.cidr
				availabilityZone:    sub.az
				mapPublicIpOnLaunch: sub.public
				tags: {
					app:    variables.name
					env:    variables.environment
					subnet: sub.suffix
				}
			}
		}
	}
}
