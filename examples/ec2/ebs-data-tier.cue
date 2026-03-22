// ebs-data-tier.cue — EBS volumes for a data-heavy workload.
//
// Provisions high-performance EBS volumes: a fast io2 volume for
// database storage and a large gp3 volume for application data.
//
// Usage:
//   praxis template register examples/ec2/ebs-data-tier.cue --description "EBS volumes for data tier"
//   praxis deploy ebs-data-tier --account local -f examples/ec2/ebs-data-tier.vars.json --key data-prod --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
	az:          string | *"us-east-1a"
}

resources: {
	// ── Database Volume (high IOPS) ─────────────────────
	dbVolume: {
		apiVersion: "praxis.io/v1"
		kind:       "EBSVolume"
		metadata: name: "\(variables.name)-\(variables.environment)-db"
		spec: {
			region:           "us-east-1"
			availabilityZone: variables.az
			volumeType:       "io2"
			sizeGiB:          50
			iops:             3000
			encrypted:        true
			tags: {
				app:     variables.name
				env:     variables.environment
				purpose: "database"
			}
		}
	}

	// ── Application Data Volume ─────────────────────────
	appVolume: {
		apiVersion: "praxis.io/v1"
		kind:       "EBSVolume"
		metadata: name: "\(variables.name)-\(variables.environment)-appdata"
		spec: {
			region:           "us-east-1"
			availabilityZone: variables.az
			volumeType:       "gp3"
			sizeGiB:          200
			throughput:       250
			encrypted:        true
			tags: {
				app:     variables.name
				env:     variables.environment
				purpose: "appdata"
			}
		}
	}
}
