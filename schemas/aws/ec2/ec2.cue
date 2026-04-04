package ec2

#EC2Instance: {
	apiVersion: "praxis.io/v1"
	kind:       "EC2Instance"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region:       string
		imageId:      string & =~"^ami-[a-f0-9]{8,17}$"
		instanceType: string
		keyName?:     string
		subnetId:     string
		securityGroupIds: [...string] | *[]
		userData?:           string
		iamInstanceProfile?: string
		rootVolume?: {
			sizeGiB:    int & >=1 & <=16384 | *20
			volumeType: "gp2" | "gp3" | "io1" | "io2" | "st1" | "sc1" | *"gp3"
			encrypted:  bool | *true
		}
		monitoring: bool | *false
		tags: [string]: string
	}

	outputs?: {
		instanceId:       string
		privateIpAddress: string
		publicIpAddress?: string
		privateDnsName:   string
		arn:              string
		state:            string
		subnetId:         string
		vpcId:            string
	}
}
