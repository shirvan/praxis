import "encoding/json"

// saas-platform.cue — A realistic multi-tier SaaS platform deployment.
//
// This template provisions the core infrastructure for a typical SaaS
// application: VPC networking, an internet-facing ALB, EC2 application
// servers behind an auto-scaling-friendly instance profile, an RDS
// PostgreSQL database, S3 storage, IAM roles, TLS via ACM, DNS via
// Route 53, and CloudWatch logging.
//
// ┌──────────────────────────────────────────────────────────────────┐
// │                        ARCHITECTURE                              │
// │                                                                  │
// │  VPC (10.0.0.0/16) — Multi-AZ, DNS-enabled                       │
// │  ├── Internet Gateway                                            │
// │  ├── Public Subnets (AZ-a, AZ-b)                                 │
// │  │   ├── Route Table → IGW                                       │
// │  │   ├── NAT Gateway + Elastic IP                                │
// │  │   └── ALB (HTTPS) → Target Group                              │
// │  │       └── HTTP→HTTPS redirect listener                        │
// │  ├── App Subnets (AZ-a, AZ-b)                                    │
// │  │   ├── Route Table → NAT                                       │
// │  │   ├── Security Group (8080 from VPC CIDR)                     │
// │  │   ├── SSH Key Pair                                            │
// │  │   └── App Servers (per AZ, for-loop + Instance Profile)       │
// │  ├── Data Subnets (AZ-a, AZ-b)                                   │
// │  │   ├── DB Subnet Group + Parameter Group                       │
// │  │   ├── Security Group (5432 from app subnets)                  │
// │  │   └── RDS PostgreSQL (Multi-AZ in prod)                       │
// │  ├── ACM Certificate + DNS Validation Record                     │
// │  ├── Route 53 Hosted Zone + A-record → ALB                       │
// │  ├── CloudWatch Log Group                                        │
// │  ├── IAM Role + Policy + Instance Profile                        │
// │  └── S3 Buckets (dynamic from list)                              │
// └──────────────────────────────────────────────────────────────────┘
//
// Usage:
//   praxis template register examples/stacks/saas-platform.cue \
//     --description "Multi-tier SaaS platform — VPC, ALB, EC2, RDS, S3"
//
//   praxis deploy saas-platform --account local \
//     -f examples/stacks/saas-platform.vars.json --key acme-prod --wait

// ═══════════════════════════════════════════════════════════════════════
// VARIABLES
// ═══════════════════════════════════════════════════════════════════════

variables: {
	name:         string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:  "dev" | "staging" | "prod"
	instanceType: string | *"t3.small"
	imageId:      string | *"ami-0885b1f6bd170450c"
	domainName:   string & =~"^(([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\\.)+[a-zA-Z]{2,})$"

	// Database
	dbInstanceClass: string | *"db.t3.small"
	dbEngineVersion: string | *"15.3"
	dbFamily:        string | *"postgres15"

	// Dynamic bucket generation
	storageBuckets: [...string] | *["assets", "uploads"]

	// Multi-AZ configuration
	availabilityZones: [...string] | *["us-east-1a", "us-east-1b"]
}

// ── Hidden helpers ──────────────────────────────────────────────────
_naming: {
	prefix: "\(variables.name)-\(variables.environment)"
	region: "us-east-1"
}

// ── Reusable definitions ────────────────────────────────────────────
#StandardTags: {
	app:       variables.name
	env:       variables.environment
	managedBy: "praxis"
	stack:     "saas-platform"
	...
}

#DataTags: #StandardTags & {tier: "data"}
#AppTags:  #StandardTags & {tier: "app"}
#WebTags:  #StandardTags & {tier: "web"}

// ═══════════════════════════════════════════════════════════════════════
// RESOURCES
// ═══════════════════════════════════════════════════════════════════════

resources: {
	// ─── Networking ─────────────────────────────────────

	vpc: {
		apiVersion: "praxis.io/v1"
		kind:       "VPC"
		metadata: name: "\(_naming.prefix)-vpc"
		spec: {
			region:             _naming.region
			cidrBlock:          "10.0.0.0/16"
			enableDnsHostnames: true
			enableDnsSupport:   true
			tags:               #StandardTags
		}
	}

	igw: {
		apiVersion: "praxis.io/v1"
		kind:       "InternetGateway"
		metadata: name: "\(_naming.prefix)-igw"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			tags:   #StandardTags
		}
	}

	// ── Public Subnets ──────────────────────────────────

	publicSubnetA: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-public-a"
		spec: {
			region:              _naming.region
			vpcId:               "${resources.vpc.outputs.vpcId}"
			cidrBlock:           "10.0.1.0/24"
			availabilityZone:    variables.availabilityZones[0]
			mapPublicIpOnLaunch: true
			tags:                #WebTags
		}
	}

	publicSubnetB: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-public-b"
		spec: {
			region:              _naming.region
			vpcId:               "${resources.vpc.outputs.vpcId}"
			cidrBlock:           "10.0.2.0/24"
			availabilityZone:    variables.availabilityZones[1]
			mapPublicIpOnLaunch: true
			tags:                #WebTags
		}
	}

	// ── App Subnets ─────────────────────────────────────

	appSubnetA: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-app-a"
		spec: {
			region:           _naming.region
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.10.0/24"
			availabilityZone: variables.availabilityZones[0]
			tags:             #AppTags
		}
	}

	appSubnetB: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-app-b"
		spec: {
			region:           _naming.region
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.11.0/24"
			availabilityZone: variables.availabilityZones[1]
			tags:             #AppTags
		}
	}

	// ── Data Subnets ────────────────────────────────────

	dataSubnetA: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-data-a"
		spec: {
			region:           _naming.region
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.20.0/24"
			availabilityZone: variables.availabilityZones[0]
			tags:             #DataTags
		}
	}

	dataSubnetB: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-data-b"
		spec: {
			region:           _naming.region
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.21.0/24"
			availabilityZone: variables.availabilityZones[1]
			tags:             #DataTags
		}
	}

	// ─── Gateways & Routing ─────────────────────────────

	natEip: {
		apiVersion: "praxis.io/v1"
		kind:       "ElasticIP"
		metadata: name: "\(_naming.prefix)-nat-eip"
		spec: {
			region: _naming.region
			domain: "vpc"
			tags:   #StandardTags
		}
	}

	natgw: {
		apiVersion: "praxis.io/v1"
		kind:       "NATGateway"
		metadata: name: "\(_naming.prefix)-natgw"
		spec: {
			region:           _naming.region
			subnetId:         "${resources.publicSubnetA.outputs.subnetId}"
			connectivityType: "public"
			allocationId:     "${resources.natEip.outputs.allocationId}"
			tags:             #StandardTags
		}
	}

	publicRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(_naming.prefix)-public-rt"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock: "0.0.0.0/0"
				gatewayId:            "${resources.igw.outputs.internetGatewayId}"
			}]
			associations: [
				{subnetId: "${resources.publicSubnetA.outputs.subnetId}"},
				{subnetId: "${resources.publicSubnetB.outputs.subnetId}"},
			]
			tags: #WebTags
		}
	}

	appRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(_naming.prefix)-app-rt"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock: "0.0.0.0/0"
				natGatewayId:         "${resources.natgw.outputs.natGatewayId}"
			}]
			associations: [
				{subnetId: "${resources.appSubnetA.outputs.subnetId}"},
				{subnetId: "${resources.appSubnetB.outputs.subnetId}"},
			]
			tags: #AppTags
		}
	}

	dataRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(_naming.prefix)-data-rt"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock: "0.0.0.0/0"
				natGatewayId:         "${resources.natgw.outputs.natGatewayId}"
			}]
			associations: [
				{subnetId: "${resources.dataSubnetA.outputs.subnetId}"},
				{subnetId: "${resources.dataSubnetB.outputs.subnetId}"},
			]
			tags: #DataTags
		}
	}

	// ─── Security Groups ────────────────────────────────

	albSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(_naming.prefix)-alb-sg"
		spec: {
			groupName:   "\(_naming.prefix)-alb-sg"
			description: "ALB — HTTP/HTTPS from internet"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [
				{protocol: "tcp", fromPort: 80, toPort: 80, cidrBlock: "0.0.0.0/0"},
				{protocol: "tcp", fromPort: 443, toPort: 443, cidrBlock: "0.0.0.0/0"},
			]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: #WebTags
		}
	}

	appSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(_naming.prefix)-app-sg"
		spec: {
			groupName:   "\(_naming.prefix)-app-sg"
			description: "App tier — port 8080 from VPC CIDR only"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [{
				protocol:  "tcp"
				fromPort:  8080
				toPort:    8080
				cidrBlock: "10.0.0.0/16"
			}]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: #AppTags
		}
	}

	dataSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(_naming.prefix)-data-sg"
		spec: {
			groupName:   "\(_naming.prefix)-data-sg"
			description: "Data tier — PostgreSQL from app subnets only"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [
				{protocol: "tcp", fromPort: 5432, toPort: 5432, cidrBlock: "10.0.10.0/24"},
				{protocol: "tcp", fromPort: 5432, toPort: 5432, cidrBlock: "10.0.11.0/24"},
			]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: #DataTags
		}
	}

	// ─── TLS Certificate + DNS ──────────────────────────

	hostedZone: {
		apiVersion: "praxis.io/v1"
		kind:       "Route53HostedZone"
		metadata: name: variables.domainName
		spec: {
			comment: "\(_naming.prefix) platform zone"
			tags:    #StandardTags
		}
	}

	cert: {
		apiVersion: "praxis.io/v1"
		kind:       "ACMCertificate"
		metadata: name: "\(_naming.prefix)-cert"
		spec: {
			region:           _naming.region
			domainName:       variables.domainName
			validationMethod: "DNS"
			keyAlgorithm:     "RSA_2048"
			subjectAlternativeNames: ["*.\(variables.domainName)"]
			options: certificateTransparencyLoggingPreference: "ENABLED"
			tags: #StandardTags
		}
	}

	validationRecord: {
		apiVersion: "praxis.io/v1"
		kind:       "Route53Record"
		metadata: name: "\(_naming.prefix)-cert-validation"
		spec: {
			hostedZoneId: "${resources.hostedZone.outputs.hostedZoneId}"
			name:         "${resources.cert.outputs.dnsValidationRecords[0].resourceRecordName}"
			type:         "CNAME"
			ttl:          300
			resourceRecords: [
				"${resources.cert.outputs.dnsValidationRecords[0].resourceRecordValue}",
			]
		}
	}

	appDnsRecord: {
		apiVersion: "praxis.io/v1"
		kind:       "Route53Record"
		metadata: name: "\(_naming.prefix)-app-dns"
		spec: {
			hostedZoneId: "${resources.hostedZone.outputs.hostedZoneId}"
			name:         variables.domainName
			type:         "A"
			aliasTarget: {
				dnsName:              "${resources.alb.outputs.dnsName}"
				hostedZoneId:         "${resources.alb.outputs.canonicalHostedZoneId}"
				evaluateTargetHealth: true
			}
		}
	}

	// ─── Load Balancer ──────────────────────────────────

	alb: {
		apiVersion: "praxis.io/v1"
		kind:       "ALB"
		metadata: name: "\(_naming.prefix)-alb"
		spec: {
			region: _naming.region
			name:   "\(_naming.prefix)-alb"
			scheme: "internet-facing"
			subnets: [
				"${resources.publicSubnetA.outputs.subnetId}",
				"${resources.publicSubnetB.outputs.subnetId}",
			]
			securityGroups: ["${resources.albSg.outputs.groupId}"]
			tags: #WebTags
		}
	}

	targetGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "TargetGroup"
		metadata: name: "\(_naming.prefix)-app-tg"
		spec: {
			region:     _naming.region
			port:       8080
			protocol:   "HTTP"
			vpcId:      "${resources.vpc.outputs.vpcId}"
			targetType: "instance"
			healthCheck: {
				path:               "/healthz"
				protocol:           "HTTP"
				port:               "8080"
				healthyThreshold:   3
				unhealthyThreshold: 3
				interval:           30
				timeout:            5
			}
			tags: #AppTags
		}
	}

	httpsListener: {
		apiVersion: "praxis.io/v1"
		kind:       "Listener"
		metadata: name: "\(_naming.prefix)-https"
		spec: {
			region:          _naming.region
			loadBalancerArn: "${resources.alb.outputs.loadBalancerArn}"
			port:            443
			protocol:        "HTTPS"
			certificateArn:  "${resources.cert.outputs.certificateArn}"
			defaultActions: [{
				type:           "forward"
				targetGroupArn: "${resources.targetGroup.outputs.targetGroupArn}"
			}]
			tags: #WebTags
		}
	}

	httpRedirectListener: {
		apiVersion: "praxis.io/v1"
		kind:       "Listener"
		metadata: name: "\(_naming.prefix)-http-redirect"
		spec: {
			region:          _naming.region
			loadBalancerArn: "${resources.alb.outputs.loadBalancerArn}"
			port:            80
			protocol:        "HTTP"
			defaultActions: [{
				type: "redirect"
				redirectConfig: {
					protocol:   "HTTPS"
					host:       "#{host}"
					port:       "443"
					path:       "/#{path}"
					query:      "#{query}"
					statusCode: "HTTP_301"
				}
			}]
			tags: #WebTags
		}
	}

	// ─── Compute ────────────────────────────────────────

	sshKeyPair: {
		apiVersion: "praxis.io/v1"
		kind:       "KeyPair"
		metadata: name: "\(_naming.prefix)-ssh"
		spec: {
			region:  _naming.region
			keyType: "ed25519"
			tags:    #AppTags
		}
	}

	for idx, az in variables.availabilityZones {
		let _az = az
		"appServer-\(idx)": {
			apiVersion: "praxis.io/v1"
			kind:       "EC2Instance"
			metadata: name: "\(_naming.prefix)-app-\(idx)"
			spec: {
				region:       _naming.region
				imageId:      variables.imageId
				instanceType: variables.instanceType
				subnetId:     "${resources.appSubnet\(["A", "B"][idx]).outputs.subnetId}"
				securityGroupIds: ["${resources.appSg.outputs.groupId}"]
				keyName:            "${resources.sshKeyPair.outputs.keyName}"
				iamInstanceProfile: "${resources.appInstanceProfile.outputs.instanceProfileName}"
				monitoring: variables.environment == "prod"
				rootVolume: {
					sizeGiB:    30
					volumeType: "gp3"
					encrypted:  true
				}
				tags: #AppTags & {
					az:    _az
					index: "\(idx)"
				}
			}
		}
	}

	// ─── IAM ────────────────────────────────────────────

	appRole: {
		apiVersion: "praxis.io/v1"
		kind:       "IAMRole"
		metadata: name: "\(_naming.prefix)-app-role"
		spec: {
			path: "/app/"
			assumeRolePolicyDocument: json.Marshal({
				Version: "2012-10-17"
				Statement: [{
					Effect:    "Allow"
					Principal: Service: "ec2.amazonaws.com"
					Action: "sts:AssumeRole"
				}]
			})
			tags: #AppTags
		}
	}

	appPolicy: {
		apiVersion: "praxis.io/v1"
		kind:       "IAMPolicy"
		metadata: name: "\(_naming.prefix)-app-policy"
		spec: {
			path: "/app/"
			policyDocument: json.Marshal({
				Version: "2012-10-17"
				Statement: [
					{
						Sid:    "S3Access"
						Effect: "Allow"
						Action: ["s3:GetObject", "s3:PutObject", "s3:ListBucket"]
						Resource: "*"
					},
					{
						Sid:    "CloudWatchLogs"
						Effect: "Allow"
						Action: ["logs:CreateLogStream", "logs:PutLogEvents"]
						Resource: "arn:aws:logs:\(_naming.region):*:log-group:/praxis/\(_naming.prefix)/app:*"
					},
				]
			})
			tags: #AppTags
		}
	}

	appInstanceProfile: {
		apiVersion: "praxis.io/v1"
		kind:       "IAMInstanceProfile"
		metadata: name: "\(_naming.prefix)-app-profile"
		spec: {
			path:     "/app/"
			roleName: "${resources.appRole.outputs.roleName}"
			tags:     #AppTags
		}
	}

	// ─── Database ───────────────────────────────────────

	dbSubnetGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "DBSubnetGroup"
		metadata: name: "\(_naming.prefix)-db-subnets"
		spec: {
			region:      _naming.region
			description: "Database subnet group for \(_naming.prefix)"
			subnetIds: [
				"${resources.dataSubnetA.outputs.subnetId}",
				"${resources.dataSubnetB.outputs.subnetId}",
			]
			tags: #DataTags
		}
	}

	dbParamGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "DBParameterGroup"
		metadata: name: "\(_naming.prefix)-db-params"
		spec: {
			region:      _naming.region
			family:      variables.dbFamily
			description: "PostgreSQL parameters for \(_naming.prefix)"
			parameters: {
				"shared_buffers":             "256MB"
				"max_connections":            "200"
				"log_statement":              "all"
				"log_min_duration_statement": "500"
			}
			tags: #DataTags
		}
	}

	database: {
		apiVersion: "praxis.io/v1"
		kind:       "RDSInstance"
		metadata: name: "\(_naming.prefix)-db"
		spec: {
			region:              _naming.region
			allocatedStorage:    100
			storageType:         "gp3"
			engine:              "postgres"
			engineVersion:       variables.dbEngineVersion
			instanceClass:       variables.dbInstanceClass
			masterUsername:      "admin"
			masterUserPassword:  "ssm:///praxis/\(variables.environment)/db-password?sensitive=true"
			multiAZ:             variables.environment == "prod"
			dbSubnetGroupName:   "${resources.dbSubnetGroup.outputs.groupName}"
			parameterGroupName:  "${resources.dbParamGroup.outputs.groupName}"
			vpcSecurityGroupIds: ["${resources.dataSg.outputs.groupId}"]
			tags: #DataTags
		}
		lifecycle: {
			preventDestroy: variables.environment == "prod"
			ignoreChanges: ["tags.CostCenter"]
		}
	}

	// ─── Observability ──────────────────────────────────

	appLogGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "LogGroup"
		metadata: name: "/praxis/\(_naming.prefix)/app"
		spec: {
			region:          _naming.region
			retentionInDays: 90
			tags: #AppTags & {purpose: "application-logs"}
		}
	}

	// ─── Storage ────────────────────────────────────────

	for _, suffix in variables.storageBuckets {
		"bucket-\(suffix)": {
			apiVersion: "praxis.io/v1"
			kind:       "S3Bucket"
			metadata: name: "\(_naming.prefix)-\(suffix)"
			spec: {
				region:     _naming.region
				versioning: true
				acl:        "private"
				encryption: {
					enabled:   true
					algorithm: "AES256"
				}
				tags: #StandardTags & {purpose: suffix}
			}
			lifecycle: preventDestroy: variables.environment == "prod"
		}
	}
}
