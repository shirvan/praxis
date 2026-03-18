// ec2-instance.cue — A standalone EC2 instance template.
//
// Usage:
//   # Register the template
//   praxis template register examples/ec2-instance.cue --description "Single EC2 instance"
//
//   # Preview the deployment
//   praxis deploy ec2-instance --account local -f examples/ec2-instance.vars.json --dry-run
//
//   # Deploy
//   praxis deploy ec2-instance --account local -f examples/ec2-instance.vars.json --key my-app-dev --wait

variables: {
	name:         string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:  "dev" | "staging" | "prod"
	subnetId:     string
	instanceType: string | *"t3.micro"
	imageId:      string | *"ami-0885b1f6bd170450c" // Amazon Linux 2 (us-east-1)
}

resources: {
	instance: {
		apiVersion: "praxis.io/v1"
		kind:       "EC2Instance"
		metadata: {
			name: "\(variables.name)-\(variables.environment)"
		}
		spec: {
			region:       "us-east-1"
			imageId:      variables.imageId
			instanceType: variables.instanceType
			subnetId:     variables.subnetId
			monitoring:   variables.environment == "prod"
			rootVolume: {
				sizeGiB:    20
				volumeType: "gp3"
				encrypted:  true
			}
			tags: {
				app: variables.name
				env: variables.environment
			}
		}
	}
}
