package elb

#Listener: {
	apiVersion: "praxis.io/v1"
	kind:       "Listener"

	metadata: {
		name: string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"
		labels: [string]: string
	}

	spec: {
		account?:        string
		region?:         string
		loadBalancerArn: string
		port:            int & >=1 & <=65535
		protocol:        "HTTP" | "HTTPS" | "TCP" | "UDP" | "TLS" | "TCP_UDP"
		sslPolicy?:      string
		certificateArn?: string
		alpnPolicy?:     string
		defaultActions: [...#ListenerAction] & [_, ...]
		tags: [string]: string
	}

	outputs?: {
		listenerArn: string
		port:        int
		protocol:    string
	}
}

#ListenerAction: {
	type:            "forward" | "redirect" | "fixed-response"
	targetGroupArn?: string
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
