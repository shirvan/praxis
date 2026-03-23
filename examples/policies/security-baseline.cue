// security-baseline.cue — Global security baseline policy.
//
// Enforces organization-wide security requirements across all templates:
//   - S3 buckets must have encryption enabled
//   - EC2 root volumes must be encrypted
//   - All resources must carry "environment" and "app" tags
//
// This policy uses CUE's pattern constraint syntax (resources: [_]: ...)
// to apply rules to every resource in a template, regardless of name.
//
// Usage:
//   praxis policy add --name security-baseline --scope global \
//     --source examples/policies/security-baseline.cue \
//     --description "Organization-wide security baseline"

// Require tags on every resource.
resources: [_]: spec: tags: {
	environment: string
	app:         string
}

// S3 buckets must have encryption enabled.
resources: [_]: {
	kind: string
	if kind == "S3Bucket" {
		spec: encryption: enabled: true
	}
}

// EC2 instances must have encrypted root volumes.
resources: [_]: {
	kind: string
	if kind == "EC2Instance" {
		spec: rootVolume: encrypted: true
	}
}
