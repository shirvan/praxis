package ec2

#SecurityGroup: {
	apiVersion: "praxis.io/v1"
	kind:       "SecurityGroup"

	metadata: {
		name: string & =~"^[a-zA-Z0-9 _\\-]{1,255}$"
		labels: [string]: string
	}

	spec: {
		groupName:   string
		description: string
		vpcId:       string
		ingressRules: [...#Rule] | *[]
		egressRules: [...#Rule] | *[{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
		tags: [string]: string
	}

	// Outputs are populated by the driver after provisioning.
	// Optional at template time — the driver fills them after Provision.
	outputs?: {
		groupId:  string
		groupArn: string
		vpcId:    string
	}
}

#Rule: {
	protocol:  "tcp" | "udp" | "icmp" | "-1"
	fromPort:  int & >=0 & <=65535
	toPort:    int & >=fromPort & <=65535
	cidrBlock: string & =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/[0-9]{1,2}$"
}
