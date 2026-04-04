package elb

#ListenerRule: {
	apiVersion: "praxis.io/v1"
	kind:       "ListenerRule"

	metadata: {
		name: string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"
		labels: [string]: string
	}

	spec: {
		account?:    string
		region?:     string
		listenerArn: string
		priority:    int & >=1 & <=50000
		conditions: [...#RuleCondition] & [_, ...]
		actions: [...#RuleAction] & [_, ...]
		tags: [string]: string
	}

	outputs?: {
		ruleArn:  string
		priority: int
	}
}

#RuleCondition: {
	field: "path-pattern" | "host-header" | "http-header" | "query-string" | "source-ip" | "http-request-method"
	values?: [...string]
	httpHeaderConfig?: {
		name: string
		values: [...string] & [_, ...]
	}
	queryStringConfig?: values: [...#QueryStringKV] & [_, ...]
}

#QueryStringKV: {
	key:   string
	value: string
}

#RuleAction: {
	type:            "forward" | "redirect" | "fixed-response"
	order?:          int & >=1
	targetGroupArn?: string
	forwardConfig?: {
		targetGroups: [...#WeightedTargetGroup] & [_, ...]
		stickiness?: {
			enabled:  bool | *false
			duration: int & >=1 & <=604800
		}
	}
	redirectConfig?: {
		protocol:   string
		host:       string
		port:       string
		path:       string
		query:      string
		statusCode: "HTTP_301" | "HTTP_302"
	}
	fixedResponseConfig?: {
		statusCode:  string
		contentType: string
		messageBody: string
	}
}

#WeightedTargetGroup: {
	targetGroupArn: string
	weight:         int & >=0 & <=999 | *1
}
