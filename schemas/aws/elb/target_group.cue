package elb

#TargetGroup: {
	apiVersion: "praxis.io/v1"
	kind:       "TargetGroup"

	metadata: {
		name: string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"
		labels: [string]: string
	}

	spec: {
		region:           string
		account?:         string
		protocol:         "HTTP" | "HTTPS" | "TCP" | "UDP" | "TLS" | "TCP_UDP"
		port:             int & >=1 & <=65535
		vpcId:            string
		targetType:       "instance" | "ip" | "lambda" | *"instance"
		protocolVersion?: "HTTP1" | "HTTP2" | "gRPC"
		healthCheck?: {
			protocol:           "HTTP" | "HTTPS" | "TCP" | "TLS" | *"HTTP"
			path?:              string
			port:               string | *"traffic-port"
			healthyThreshold:   int & >=2 & <=10 | *5
			unhealthyThreshold: int & >=2 & <=10 | *2
			interval:           int & >=5 & <=300 | *30
			timeout:            int & >=2 & <=120 | *5
			matcher?:           string
		}
		deregistrationDelay: int & >=0 & <=3600 | *300
		stickiness?: {
			enabled:  bool | *false
			type:     "lb_cookie" | "app_cookie" | "source_ip" | *"lb_cookie"
			duration: int & >=1 & <=604800 | *86400
		}
		targets: [...#Target] | *[]
		tags: [string]: string
	}

	outputs?: {
		targetGroupArn:  string
		targetGroupName: string
	}
}

#Target: {
	id:                string
	port?:             int & >=1 & <=65535
	availabilityZone?: string
}
