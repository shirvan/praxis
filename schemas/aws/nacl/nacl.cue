package nacl

#NetworkACL: {
	apiVersion: "praxis.io/v1"
	kind:       "NetworkACL"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region: string
		vpcId:  string

		ingressRules?: [...#NetworkACLRule]
		egressRules?:  [...#NetworkACLRule]
		subnetAssociations?: [...string]
		tags?:               [string]: string
	}

	outputs?: {
		networkAclId: string
		vpcId:        string
		isDefault:    bool
		ingressRules: [...#NetworkACLRuleOutput]
		egressRules:  [...#NetworkACLRuleOutput]
		associations: [...#NetworkACLAssociationOutput]
	}
}

#NetworkACLRule: {
	ruleNumber: int & >=1 & <=32766
	protocol:   string
	ruleAction: "allow" | "deny"
	cidrBlock:  string
	fromPort?:  int
	toPort?:    int
}

#NetworkACLRuleOutput: {
	ruleNumber: int
	protocol:   string
	ruleAction: string
	cidrBlock:  string
	fromPort:   int
	toPort:     int
}

#NetworkACLAssociationOutput: {
	associationId: string
	subnetId:      string
}