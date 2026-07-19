// foundation-services.cue — Minimal examples for the six foundation and
// control-plane resource kinds that are not used by the application stacks.

variables: {
	name:   string & =~"^[a-z][a-z0-9-]{2,24}$"
	region: string | *"us-east-1"

	// EKS requires an existing cluster role and network. These inputs keep the
	// example focused on the EKS resource contract rather than recreating a VPC.
	eksRoleArn:         string
	eksSubnetIds:      [...string] & [string, string, ...string]
	eksSecurityGroupId: string

	// Supply this through a protected variables file in real deployments.
	bootstrapSecret: string
}

_tags: {
	example:   "foundation-services"
	managedBy: "praxis"
}

resources: {
	metadata: {
		apiVersion: "praxis.io/alpha"
		kind:       "DynamoDBTable"
		metadata: name: "\(variables.name)-metadata"
		spec: {
			region:   variables.region
			hashKey:  "resourceId"
			tags:     _tags
		}
	}

	workers: {
		apiVersion: "praxis.io/alpha"
		kind:       "ECSCluster"
		metadata: name: "\(variables.name)-workers"
		spec: {
			region:               variables.region
			containerInsights:    "enabled"
			capacityProviders:    ["FARGATE", "FARGATE_SPOT"]
			tags:                 _tags
		}
	}

	kubernetes: {
		apiVersion: "praxis.io/alpha"
		kind:       "EKSCluster"
		metadata: name: "\(variables.name)-eks"
		spec: {
			region:                variables.region
			roleArn:               variables.eksRoleArn
			subnetIds:             variables.eksSubnetIds
			securityGroupIds:      [variables.eksSecurityGroupId]
			endpointPublicAccess:  true
			endpointPrivateAccess: true
			publicAccessCidrs:     ["203.0.113.0/24"]
			enabledLoggingTypes:   ["api", "audit", "authenticator"]
			tags:                  _tags
		}
	}

	encryption: {
		apiVersion: "praxis.io/alpha"
		kind:       "KMSKey"
		metadata: name: "\(variables.name)/application"
		spec: {
			region:            variables.region
			description:       "Application data key"
			enableKeyRotation: true
			tags:              _tags
		}
	}

	configuration: {
		apiVersion: "praxis.io/alpha"
		kind:       "SSMParameter"
		metadata: name: "/\(variables.name)/environment"
		spec: {
			region:      variables.region
			value:       "production"
			description: "Application environment"
			tags:        _tags
		}
	}

	credentials: {
		apiVersion: "praxis.io/alpha"
		kind:       "SecretsManagerSecret"
		metadata: name: "\(variables.name)/bootstrap"
		spec: {
			region:       variables.region
			description:  "Bootstrap credential managed by Praxis"
			secretString: variables.bootstrapSecret
			tags:         _tags
		}
	}
}
