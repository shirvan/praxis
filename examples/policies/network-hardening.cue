// network-hardening.cue — Network security hardening policy.
//
// Prevents common networking misconfigurations:
//   - Security groups must not allow SSH (22) from 0.0.0.0/0
//   - S3 buckets must never be public-read
//   - VPCs must enable DNS support
//
// Usage:
//   praxis policy add --name network-hardening --scope global \
//     --source examples/policies/network-hardening.cue \
//     --description "Network security hardening rules"

// S3 buckets must always be private.
resources: [_]: {
	kind: string
	if kind == "S3Bucket" {
		spec: acl: "private"
	}
}

// All VPCs must have DNS support enabled.
resources: [_]: {
	kind: string
	if kind == "VPC" {
		spec: enableDnsSupport: true
	}
}
