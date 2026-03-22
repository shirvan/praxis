// web-fleet.cue — Multiple EC2 instances behind a shared security group.
//
// Two web servers in different AZs with HTTP/HTTPS ingress,
// plus a shared EBS volume for persistent data.
//
// Usage:
//   praxis template register examples/ec2/web-fleet.cue --description "Web server fleet with EBS"
//   praxis deploy web-fleet --account local -f examples/ec2/web-fleet.vars.json --key web-prod --wait

variables: {
	name:         string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:  "dev" | "staging" | "prod"
	vpcId:        string
	subnetIdA:    string
	subnetIdB:    string
	instanceType: string | *"t3.small"
	imageId:      string | *"ami-0885b1f6bd170450c"
}

resources: {
	// ── Web Security Group ──────────────────────────────
	webSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(variables.name)-\(variables.environment)-web-sg"
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-web-sg"
			description: "Web tier - HTTP/HTTPS from internet"
			vpcId:       variables.vpcId
			ingressRules: [
				{protocol: "tcp", fromPort: 80, toPort: 80, cidrBlock: "0.0.0.0/0"},
				{protocol: "tcp", fromPort: 443, toPort: 443, cidrBlock: "0.0.0.0/0"},
			]
			egressRules: [{
				protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"
			}]
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "web"
			}
		}
	}

	// ── Web Server A (AZ-a) ─────────────────────────────
	webA: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: name: "\(variables.name)-\(variables.environment)-web-a"
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: variables.instanceType
			subnetId:     variables.subnetIdA
			securityGroupIds: ["${resources.webSg.outputs.groupId}"]
			monitoring: variables.environment == "prod"
			rootVolume: {
				sizeGiB:    20
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "web"
				az:   "a"
			}
		}
	}

	// ── Web Server B (AZ-b) ─────────────────────────────
	webB: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: name: "\(variables.name)-\(variables.environment)-web-b"
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: variables.instanceType
			subnetId:     variables.subnetIdB
			securityGroupIds: ["${resources.webSg.outputs.groupId}"]
			monitoring: variables.environment == "prod"
			rootVolume: {
				sizeGiB:    20
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "web"
				az:   "b"
			}
		}
	}

	// ── Shared Data Volume (AZ-a) ───────────────────────
	dataVolume: {
		apiVersion: "praxis.io/v1"
		kind:       "EBSVolume"
		metadata: name: "\(variables.name)-\(variables.environment)-data"
		spec: {
			region:           "us-east-1"
			availabilityZone: "us-east-1a"
			volumeType:       "gp3"
			sizeGiB:          100
			encrypted:        true
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "data"
			}
		}
	}
}
