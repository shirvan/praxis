// three-tier-app.cue — Complete three-tier web application infrastructure.
//
// Topology:
//   VPC → IGW → Public Subnet + Private Subnet
//   ├── Public: EIP → NAT GW, Route Table (→ IGW)
//   ├── Private: Route Table (→ NAT)
//   ├── Web SG (HTTP/HTTPS) → Web Server (public subnet)
//   ├── App SG (8080 from web SG CIDR) → App Server (private subnet)
//   └── S3 Bucket (artifacts)
//
// DAG:
//   vpc → igw, publicSubnet, privateSubnet
//   publicSubnet → natEip → natgw → privateRT
//   igw → publicRT
//   webSg → webServer
//   appSg → appServer
//
// Usage:
//   praxis template register examples/stacks/three-tier-app.cue --description "Three-tier VPC + EC2 + S3 stack"
//   praxis deploy three-tier-app --account local -f examples/stacks/three-tier-app.vars.json --key webapp-prod --wait

variables: {
	name:         string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:  "dev" | "staging" | "prod"
	instanceType: string | *"t3.small"
	imageId:      string | *"ami-0885b1f6bd170450c"
}

resources: {
	// ═══════════════════════════════════════════════════
	// NETWORKING
	// ═══════════════════════════════════════════════════

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
				app:   variables.name
				env:   variables.environment
				stack: "three-tier"
			}
		}
	}

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

	publicSubnet: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-\(variables.environment)-public"
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

	privateSubnet: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(variables.name)-\(variables.environment)-private"
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

	natEip: {
		apiVersion: "praxis.io/v1"
		kind:       "ElasticIP"
		metadata: name: "\(variables.name)-\(variables.environment)-nat-eip"
		spec: {
			region: "us-east-1"
			domain: "vpc"
			tags: {app: variables.name, env: variables.environment}
		}
	}

	natgw: {
		apiVersion: "praxis.io/v1"
		kind:       "NATGateway"
		metadata: name: "\(variables.name)-\(variables.environment)-natgw"
		spec: {
			region:           "us-east-1"
			subnetId:         "${resources.publicSubnet.outputs.subnetId}"
			connectivityType: "public"
			allocationId:     "${resources.natEip.outputs.allocationId}"
			tags: {app: variables.name, env: variables.environment}
		}
	}

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
			associations: [{subnetId: "${resources.publicSubnet.outputs.subnetId}"}]
			tags: {app: variables.name, env: variables.environment, tier: "public"}
		}
	}

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
			associations: [{subnetId: "${resources.privateSubnet.outputs.subnetId}"}]
			tags: {app: variables.name, env: variables.environment, tier: "private"}
		}
	}

	// ═══════════════════════════════════════════════════
	// SECURITY
	// ═══════════════════════════════════════════════════

	webSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(variables.name)-\(variables.environment)-web-sg"
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-web-sg"
			description: "Web tier - HTTP/HTTPS from internet"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [
				{protocol: "tcp", fromPort: 80, toPort: 80, cidrBlock: "0.0.0.0/0"},
				{protocol: "tcp", fromPort: 443, toPort: 443, cidrBlock: "0.0.0.0/0"},
			]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: {app: variables.name, env: variables.environment, tier: "web"}
		}
	}

	appSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(variables.name)-\(variables.environment)-app-sg"
		spec: {
			groupName:   "\(variables.name)-\(variables.environment)-app-sg"
			description: "App tier - port 8080 from VPC CIDR"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [{
				protocol:  "tcp"
				fromPort:  8080
				toPort:    8080
				cidrBlock: "10.0.0.0/16"
			}]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: {app: variables.name, env: variables.environment, tier: "app"}
		}
	}

	// ═══════════════════════════════════════════════════
	// COMPUTE
	// ═══════════════════════════════════════════════════

	webServer: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: name: "\(variables.name)-\(variables.environment)-web"
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: variables.instanceType
			subnetId:     "${resources.publicSubnet.outputs.subnetId}"
			securityGroupIds: ["${resources.webSg.outputs.groupId}"]
			monitoring: variables.environment == "prod"
			rootVolume: {
				sizeGiB:    20
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {app: variables.name, env: variables.environment, tier: "web"}
		}
	}

	appServer: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: name: "\(variables.name)-\(variables.environment)-app"
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: variables.instanceType
			subnetId:     "${resources.privateSubnet.outputs.subnetId}"
			securityGroupIds: ["${resources.appSg.outputs.groupId}"]
			monitoring: variables.environment == "prod"
			rootVolume: {
				sizeGiB:    30
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {app: variables.name, env: variables.environment, tier: "app"}
		}
	}

	// ═══════════════════════════════════════════════════
	// STORAGE
	// ═══════════════════════════════════════════════════

	artifacts: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: name: "\(variables.name)-\(variables.environment)-artifacts"
		spec: {
			region:     "us-east-1"
			versioning: true
			acl:        "private"
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				app:     variables.name
				env:     variables.environment
				purpose: "artifacts"
			}
		}
		// Protect production artifact storage from accidental deletion.
		lifecycle: {
			preventDestroy: variables.environment == "prod"
			ignoreChanges: ["tags.CostCenter"]
		}
	}
}
