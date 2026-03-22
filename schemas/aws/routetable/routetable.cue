package routetable

#RouteTable: {
	apiVersion: "praxis.io/v1"
	kind:       "RouteTable"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region: string
		vpcId:  string

		routes?: [...#Route]
		associations?: [...#Association]
		tags?: [string]: string
	}

	outputs?: {
		routeTableId: string
		vpcId:        string
		ownerId:      string
		routes: [...{
			destinationCidrBlock: string
			gatewayId?:           string
			natGatewayId?:        string
			vpcPeeringConnectionId?: string
			transitGatewayId?:    string
			networkInterfaceId?:  string
			vpcEndpointId?:       string
			state:                string
			origin:               string
		}]
		associations: [...{
			associationId: string
			subnetId:      string
			main:          bool
		}]
	}
}

#Route: {
	destinationCidrBlock: string & =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$"

	gatewayId?:              string
	natGatewayId?:           string
	vpcPeeringConnectionId?: string
	transitGatewayId?:       string
	networkInterfaceId?:     string
	vpcEndpointId?:          string
}

#Association: {
	subnetId: string
}