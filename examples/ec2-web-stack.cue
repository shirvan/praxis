// ec2-web-stack.cue — VPC + security group + EC2 instance.
//
// Demonstrates multi-resource composition with output expressions:
//   VPC → Security Group (vpcId) → EC2 Instance (securityGroupIds)
//
// The orchestrator builds a DAG so resources are provisioned in
// dependency order and cross-resource outputs are resolved at
// dispatch time.
//
// Usage:
//   # Register
//   praxis template register examples/ec2-web-stack.cue --description "Web server with VPC and security group"
//
//   # Preview
//   praxis deploy ec2-web-stack --account local -f examples/ec2-web-stack.vars.json --dry-run
//
//   # Deploy
//   praxis deploy ec2-web-stack --account local -f examples/ec2-web-stack.vars.json --key web-dev --wait

variables: {
	name:         string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:  "dev" | "staging" | "prod"
	cidrBlock:    string | *"10.0.0.0/16"
	subnetId:     string
	instanceType: string | *"t3.micro"
	imageId:      string | *"ami-0885b1f6bd170450c" // Amazon Linux 2 (us-east-1)
}

resources: {
	// VPC — created first; downstream resources reference its outputs.
	vpc: {
		apiVersion: "praxis.io/v1"
		kind:       "VPC"
		metadata: {
			name: "\(variables.name)-\(variables.environment)-vpc"
		}
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

	// Security group — depends on vpc via output expression.
	webSG: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: {
			name: "\(variables.name)-\(variables.environment)-sg"
		}
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-sg"
			description: "Security group for \(variables.name) \(variables.environment)"
			vpcId:       "${resources.vpc.outputs.vpcId}"
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

	// EC2 instance — depends on webSG via output expression.
	// The orchestrator builds DAG edges: vpc → webSG → server.
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
			securityGroupIds: ["${resources.webSG.outputs.groupId}"]
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
