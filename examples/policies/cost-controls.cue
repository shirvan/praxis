// cost-controls.cue — Cost control policy.
//
// Prevents expensive resource configurations:
//   - EC2 instances restricted to approved types
//   - EBS volumes capped at 500 GiB
//   - EBS volume types restricted (no io1/io2 provisioned IOPS)
//
// Usage:
//   praxis policy add --name cost-controls --scope global \
//     --source examples/policies/cost-controls.cue \
//     --description "Cost control guardrails"

// Restrict EC2 instance types to approved sizes.
resources: [_]: {
	kind: string
	if kind == "EC2Instance" {
		spec: instanceType: "t3.micro" | "t3.small" | "t3.medium" | "t3.large" | "t3.xlarge"
	}
}

// Cap root volume size to prevent runaway storage costs.
resources: [_]: {
	kind: string
	if kind == "EC2Instance" {
		spec: rootVolume: sizeGiB: <=500
	}
}

// Restrict EBS volume types — no provisioned IOPS.
resources: [_]: {
	kind: string
	if kind == "EC2Instance" {
		spec: rootVolume: volumeType: "gp2" | "gp3"
	}
}
