// bastion-host.cue — EC2 bastion (jump box) with a key pair and security group.
//
// Topology:
//   KeyPair → SecurityGroup (SSH only) → EC2Instance
//
// Usage:
//   praxis template register examples/ec2/bastion-host.cue --description "SSH bastion host"
//   praxis deploy bastion-host --account local -f examples/ec2/bastion-host.vars.json --key bastion-dev --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
	vpcId:       string
	subnetId:    string
	allowedCidr: string | *"0.0.0.0/0"
	imageId:     string | *"ami-0885b1f6bd170450c"
}

resources: {
	// ── SSH Key Pair ────────────────────────────────────
	keypair: {
		apiVersion: "praxis.io/v1"
		kind:       "KeyPair"
		metadata: name: "\(variables.name)-\(variables.environment)-bastion-key"
		spec: {
			region:  "us-east-1"
			keyType: "ed25519"
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}

	// ── Bastion Security Group (SSH inbound only) ───────
	bastionSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(variables.name)-\(variables.environment)-bastion-sg"
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-bastion-sg"
			description: "Bastion host - SSH access from \(variables.allowedCidr)"
			vpcId:       variables.vpcId
			ingressRules: [{
				protocol:  "tcp"
				fromPort:  22
				toPort:    22
				cidrBlock: variables.allowedCidr
			}]
			egressRules: [{
				protocol:  "-1"
				fromPort:  0
				toPort:    0
				cidrBlock: "0.0.0.0/0"
			}]
			tags: {
				app:  variables.name
				env:  variables.environment
				role: "bastion"
			}
		}
	}

	// ── Bastion EC2 Instance ────────────────────────────
	bastion: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: name: "\(variables.name)-\(variables.environment)-bastion"
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: "t3.micro"
			subnetId:     variables.subnetId
			keyName:      "${resources.keypair.outputs.keyName}"
			securityGroupIds: ["${resources.bastionSg.outputs.groupId}"]
			monitoring: false
			rootVolume: {
				sizeGiB:    8
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {
				app:  variables.name
				env:  variables.environment
				role: "bastion"
			}
		}
	}
}
