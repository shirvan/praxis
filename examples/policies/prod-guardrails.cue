// prod-guardrails.cue — Production environment guardrails.
//
// Restricts production workloads to approved configurations:
//   - EC2 monitoring must be enabled
//   - S3 buckets must be private with versioning enabled
//   - VPCs must have DNS support for service discovery
//
// Apply globally to catch any template deploying to production,
// or scope to specific templates that target prod accounts.
//
// Usage:
//   praxis policy add --name prod-guardrails --scope global \
//     --source examples/policies/prod-guardrails.cue \
//     --description "Production environment guardrails"

// Production-named resources must have monitoring.
resources: [=~"-prod"]: {
	kind: string
	if kind == "EC2Instance" {
		spec: monitoring: true
	}
}

// Production S3 buckets must be private and versioned.
resources: [=~"-prod"]: {
	kind: string
	if kind == "S3Bucket" {
		spec: {
			acl:        "private"
			versioning: true
		}
	}
}

// Production VPCs must have DNS enabled for service discovery.
resources: [=~"-prod"]: {
	kind: string
	if kind == "VPC" {
		spec: {
			enableDnsSupport:   true
			enableDnsHostnames: true
		}
	}
}

// Production resources must be protected from accidental deletion.
resources: [=~"-prod"]: lifecycle: preventDestroy: true
