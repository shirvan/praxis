variables: {
	name:        string
	environment: "dev" | "staging" | "prod"
	vpcName:     string
}

data: {
	existingVpc: {
		kind:   "VPC"
		region: "us-east-1"
		filter: {
			name: variables.vpcName
		}
	}
}

resources: {
	webSG: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(variables.name)-\(variables.environment)-sg"
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-sg"
			description: "Web security group in an existing VPC"
			vpcId:       "${data.existingVpc.outputs.vpcId}"
			ingressRules: [{
				protocol:  "tcp"
				fromPort:  443
				toPort:    443
				cidrBlock: "0.0.0.0/0"
			}]
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}
}
