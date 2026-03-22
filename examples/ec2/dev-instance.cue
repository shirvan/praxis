// dev-instance.cue — Minimal development EC2 instance.
//
// A single instance with sensible dev defaults: small size,
// encrypted root volume, no monitoring overhead.
//
// Usage:
//   praxis template register examples/ec2/dev-instance.cue --description "Dev EC2 instance"
//   praxis deploy dev-instance --account local -f examples/ec2/dev-instance.vars.json --key myapp-dev --wait

variables: {
	name:     string & =~"^[a-z][a-z0-9-]{2,40}$"
	subnetId: string
	imageId:  string | *"ami-0885b1f6bd170450c"
}

resources: {
	instance: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: name: "\(variables.name)-dev"
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: "t3.micro"
			subnetId:     variables.subnetId
			monitoring:   false
			rootVolume: {
				sizeGiB:    20
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {
				app: variables.name
				env: "dev"
			}
		}
	}
}
