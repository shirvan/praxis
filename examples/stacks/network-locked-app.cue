// network-locked-app.cue — EC2 instance in a VPC with strict NACLs.
//
// Demonstrates Network ACL rules layered on top of security groups
// for defense-in-depth. Allows only HTTP/HTTPS inbound and
// ephemeral port return traffic.
//
// Usage:
//   praxis template register examples/stacks/network-locked-app.cue --description "VPC + NACL + SG + EC2"
//   praxis deploy network-locked-app --account local -f examples/stacks/network-locked-app.vars.json --key secure-dev --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
	imageId:     string | *"ami-0885b1f6bd170450c"
}

resources: {
	// ── VPC ─────────────────────────────────────────────
	vpc: {
		apiVersion: "praxis.io/v1"
		kind:       "VPC"
		metadata: name: "\(variables.name)-\(variables.environment)-vpc"
		spec: {
			region:             "us-east-1"
			cidrBlock:          "10.0.0.0/16"
			enableDnsHostnames: true
			enableDnsSupport:   true
			tags: {app: variables.name, env: variables.environment}
		}
	}

	// ── Subnet ──────────────────────────────────────────
	subnet: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-\(variables.environment)-web"
		spec: {
			region:              "us-east-1"
			vpcId:               "${resources.vpc.outputs.vpcId}"
			cidrBlock:           "10.0.1.0/24"
			availabilityZone:    "us-east-1a"
			mapPublicIpOnLaunch: true
			tags: {app: variables.name, env: variables.environment}
		}
	}

	// ── Network ACL (strict allow-list) ─────────────────
	nacl: {
		apiVersion: "praxis.io/v1"
		kind:       "NetworkACL"
		metadata: name: "\(variables.name)-\(variables.environment)-nacl"
		spec: {
			region: "us-east-1"
			vpcId:  "${resources.vpc.outputs.vpcId}"
			ingressRules: [
				// Allow HTTP
				{ruleNumber: 100, protocol: "tcp", ruleAction: "allow", cidrBlock: "0.0.0.0/0", fromPort: 80, toPort: 80},
				// Allow HTTPS
				{ruleNumber: 110, protocol: "tcp", ruleAction: "allow", cidrBlock: "0.0.0.0/0", fromPort: 443, toPort: 443},
				// Allow ephemeral return traffic
				{ruleNumber: 200, protocol: "tcp", ruleAction: "allow", cidrBlock: "0.0.0.0/0", fromPort: 1024, toPort: 65535},
			]
			egressRules: [
				// Allow all outbound
				{ruleNumber: 100, protocol: "-1", ruleAction: "allow", cidrBlock: "0.0.0.0/0"},
			]
			subnetAssociations: ["${resources.subnet.outputs.subnetId}"]
			tags: {app: variables.name, env: variables.environment}
		}
	}

	// ── Security Group ──────────────────────────────────
	sg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(variables.name)-\(variables.environment)-web-sg"
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-web-sg"
			description: "Web server SG - HTTP/HTTPS"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [
				{protocol: "tcp", fromPort: 80, toPort: 80, cidrBlock: "0.0.0.0/0"},
				{protocol: "tcp", fromPort: 443, toPort: 443, cidrBlock: "0.0.0.0/0"},
			]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: {app: variables.name, env: variables.environment}
		}
	}

	// ── EC2 Instance ────────────────────────────────────
	server: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: name: "\(variables.name)-\(variables.environment)-web"
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: "t3.micro"
			subnetId:     "${resources.subnet.outputs.subnetId}"
			securityGroupIds: ["${resources.sg.outputs.groupId}"]
			monitoring: variables.environment == "prod"
			rootVolume: {
				sizeGiB:    20
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {app: variables.name, env: variables.environment}
		}
	}
}
