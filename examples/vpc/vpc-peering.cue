// vpc-peering.cue — Two VPCs peered together with route tables for cross-VPC traffic.
//
// Topology:
//   App VPC (10.1.0.0/16) ←→ Data VPC (10.2.0.0/16)
//   Each VPC gets a subnet and a route table with a peering route.
//
// Usage:
//   praxis template register examples/vpc/vpc-peering.cue --description "VPC peering between app and data tiers"
//   praxis deploy vpc-peering --account local -f examples/vpc/vpc-peering.vars.json --key peering-dev --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
}

resources: {
	// ── App VPC ─────────────────────────────────────────
	appVpc: {
		apiVersion: "praxis.io/v1"
		kind:       "VPC"
		metadata: name: "\(variables.name)-app-\(variables.environment)"
		spec: {
			region:             "us-east-1"
			cidrBlock:          "10.1.0.0/16"
			enableDnsHostnames: true
			enableDnsSupport:   true
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "app"
			}
		}
	}

	// ── Data VPC ────────────────────────────────────────
	dataVpc: {
		apiVersion: "praxis.io/v1"
		kind:       "VPC"
		metadata: name: "\(variables.name)-data-\(variables.environment)"
		spec: {
			region:             "us-east-1"
			cidrBlock:          "10.2.0.0/16"
			enableDnsHostnames: true
			enableDnsSupport:   true
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "data"
			}
		}
	}

	// ── VPC Peering ─────────────────────────────────────
	peering: {
		apiVersion: "praxis.io/v1"
		kind:       "VPCPeeringConnection"
		metadata: name: "\(variables.name)-app-data-\(variables.environment)"
		spec: {
			region:          "us-east-1"
			requesterVpcId:  "${resources.appVpc.outputs.vpcId}"
			accepterVpcId:   "${resources.dataVpc.outputs.vpcId}"
			autoAccept:      true
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}

	// ── App Subnet ──────────────────────────────────────
	appSubnet: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-app-\(variables.environment)-subnet"
		spec: {
			region:           "us-east-1"
			vpcId:            "${resources.appVpc.outputs.vpcId}"
			cidrBlock:        "10.1.1.0/24"
			availabilityZone: "us-east-1a"
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "app"
			}
		}
	}

	// ── Data Subnet ─────────────────────────────────────
	dataSubnet: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-data-\(variables.environment)-subnet"
		spec: {
			region:           "us-east-1"
			vpcId:            "${resources.dataVpc.outputs.vpcId}"
			cidrBlock:        "10.2.1.0/24"
			availabilityZone: "us-east-1a"
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "data"
			}
		}
	}

	// ── App Route Table (cross-VPC route to data) ───────
	appRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(variables.name)-app-\(variables.environment)-rt"
		spec: {
			region: "us-east-1"
			vpcId:  "${resources.appVpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock:    "10.2.0.0/16"
				vpcPeeringConnectionId: "${resources.peering.outputs.vpcPeeringConnectionId}"
			}]
			associations: [
				{subnetId: "${resources.appSubnet.outputs.subnetId}"},
			]
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "app"
			}
		}
	}

	// ── Data Route Table (cross-VPC route to app) ───────
	dataRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(variables.name)-data-\(variables.environment)-rt"
		spec: {
			region: "us-east-1"
			vpcId:  "${resources.dataVpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock:    "10.1.0.0/16"
				vpcPeeringConnectionId: "${resources.peering.outputs.vpcPeeringConnectionId}"
			}]
			associations: [
				{subnetId: "${resources.dataSubnet.outputs.subnetId}"},
			]
			tags: {
				app:  variables.name
				env:  variables.environment
				tier: "data"
			}
		}
	}
}
