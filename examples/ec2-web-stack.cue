// ec2-web-stack.cue — EC2 instance fronted by a security group.
//
// Demonstrates multi-resource composition with CEL dependencies:
// the instance's securityGroupIds is resolved at dispatch time from
// the security group's outputs.
//
// Usage:
//   # Register
//   praxis template register examples/ec2-web-stack.cue --description "Web server with security group"
//
//   # Preview
//   praxis deploy ec2-web-stack --account local -f examples/ec2-web-stack.vars.json --dry-run
//
//   # Deploy
//   praxis deploy ec2-web-stack --account local -f examples/ec2-web-stack.vars.json --key web-dev --wait

variables: {
	name:         string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:  "dev" | "staging" | "prod"
	vpcId:        string
	subnetId:     string
	instanceType: string | *"t3.micro"
	imageId:      string | *"ami-0885b1f6bd170450c" // Amazon Linux 2 (us-east-1)
}

resources: {
	// Security group — created first.
	webSG: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: {
			name: "\(variables.name)-\(variables.environment)-sg"
		}
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-sg"
			description: "Security group for \(variables.name) \(variables.environment)"
			vpcId:       variables.vpcId
			ingressRules: [
				{
					protocol:  "tcp"
					fromPort:  80
					toPort:    80
					cidrBlock: "0.0.0.0/0"
				},
				{
					protocol:  "tcp"
					fromPort:  443
					toPort:    443
					cidrBlock: "0.0.0.0/0"
				},
			]
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}

	// EC2 instance — depends on webSG via CEL expression.
	// The orchestrator builds a DAG edge from webSG → server so the
	// instance is only provisioned after the security group is ready.
	server: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: {
			name: "\(variables.name)-\(variables.environment)"
		}
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: variables.instanceType
			subnetId:     variables.subnetId
			securityGroupIds: ["${cel:resources.webSG.outputs.groupId}"]
			monitoring: variables.environment == "prod"
			rootVolume: {
				sizeGiB:    20
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}
}
