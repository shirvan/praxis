package vpcpeering

#VPCPeeringConnection: {
	apiVersion: "praxis.io/v1"
	kind:       "VPCPeeringConnection"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region: string

		requesterVpcId: string
		accepterVpcId:  string

		peerOwnerId?: string
		peerRegion?:  string

		autoAccept: bool | *true

		requesterOptions?: {
			allowDnsResolutionFromRemoteVpc: bool | *false
		}

		accepterOptions?: {
			allowDnsResolutionFromRemoteVpc: bool | *false
		}

		tags: [string]: string
	}

	outputs?: {
		vpcPeeringConnectionId: string
		requesterVpcId:         string
		accepterVpcId:          string
		requesterCidrBlock:     string
		accepterCidrBlock:      string
		status:                 string
		requesterOwnerId:       string
		accepterOwnerId:        string
	}
}