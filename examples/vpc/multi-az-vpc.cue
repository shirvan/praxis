// multi-az-vpc.cue — Production-ready VPC with public/private subnets across two AZs,
// internet gateway, NAT gateway, and route tables.
//
// Topology:
//   VPC (10.0.0.0/16)
//   ├── public-a  (10.0.1.0/24, us-east-1a)  → IGW route table
//   ├── public-b  (10.0.2.0/24, us-east-1b)  → IGW route table
//   ├── private-a (10.0.10.0/24, us-east-1a) → NAT route table
//   └── private-b (10.0.11.0/24, us-east-1b) → NAT route table
//
// Usage:
//   praxis template register examples/vpc/multi-az-vpc.cue --description "Multi-AZ VPC with public/private subnets"
//   praxis deploy multi-az-vpc --account local -f examples/vpc/multi-az-vpc.vars.json --key prod-vpc --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
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
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}

	// ── Internet Gateway ────────────────────────────────
	igw: {
		apiVersion: "praxis.io/v1"
		kind:       "InternetGateway"
		metadata: name: "\(variables.name)-\(variables.environment)-igw"
		spec: {
			region: "us-east-1"
			vpcId:  "${resources.vpc.outputs.vpcId}"
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}

	// ── Public Subnets ──────────────────────────────────
	publicA: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-\(variables.environment)-public-a"
		spec: {
			region:              "us-east-1"
			vpcId:               "${resources.vpc.outputs.vpcId}"
			cidrBlock:           "10.0.1.0/24"
			availabilityZone:    "us-east-1a"
			mapPublicIpOnLaunch: true
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "public"
			}
		}
	}

	publicB: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-\(variables.environment)-public-b"
		spec: {
			region:              "us-east-1"
			vpcId:               "${resources.vpc.outputs.vpcId}"
			cidrBlock:           "10.0.2.0/24"
			availabilityZone:    "us-east-1b"
			mapPublicIpOnLaunch: true
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "public"
			}
		}
	}

	// ── Private Subnets ─────────────────────────────────
	privateA: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-\(variables.environment)-private-a"
		spec: {
			region:           "us-east-1"
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.10.0/24"
			availabilityZone: "us-east-1a"
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "private"
			}
		}
	}

	privateB: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-\(variables.environment)-private-b"
		spec: {
			region:           "us-east-1"
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.11.0/24"
			availabilityZone: "us-east-1b"
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "private"
			}
		}
	}

	// ── Elastic IP for NAT Gateway ──────────────────────
	natEip: {
		apiVersion: "praxis.io/v1"
		kind:       "ElasticIP"
		metadata: name: "\(variables.name)-\(variables.environment)-nat-eip"
		spec: {
			region: "us-east-1"
			domain: "vpc"
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}

	// ── NAT Gateway (in public-a) ───────────────────────
	natgw: {
		apiVersion: "praxis.io/v1"
		kind:       "NATGateway"
		metadata: name: "\(variables.name)-\(variables.environment)-natgw"
		spec: {
			region:           "us-east-1"
			subnetId:         "${resources.publicA.outputs.subnetId}"
			connectivityType: "public"
			allocationId:     "${resources.natEip.outputs.allocationId}"
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}

	// ── Public Route Table (→ IGW) ──────────────────────
	publicRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(variables.name)-\(variables.environment)-public-rt"
		spec: {
			region: "us-east-1"
			vpcId:  "${resources.vpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock: "0.0.0.0/0"
				gatewayId:            "${resources.igw.outputs.internetGatewayId}"
			}]
			associations: [
				{subnetId: "${resources.publicA.outputs.subnetId}"},
				{subnetId: "${resources.publicB.outputs.subnetId}"},
			]
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "public"
			}
		}
	}

	// ── Private Route Table (→ NAT) ─────────────────────
	privateRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(variables.name)-\(variables.environment)-private-rt"
		spec: {
			region: "us-east-1"
			vpcId:  "${resources.vpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock: "0.0.0.0/0"
				natGatewayId:         "${resources.natgw.outputs.natGatewayId}"
			}]
			associations: [
				{subnetId: "${resources.privateA.outputs.subnetId}"},
				{subnetId: "${resources.privateB.outputs.subnetId}"},
			]
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "private"
			}
		}
	}
}
