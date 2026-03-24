// app-buckets.cue — S3 buckets for a typical application: assets, logs, and backups.
//
// Three buckets with different configurations:
//   - Assets: versioned, private
//   - Logs: unversioned, private
//   - Backups: versioned, KMS-encrypted
//
// Usage:
//   praxis template register examples/s3/app-buckets.cue --description "Application S3 buckets"
//   praxis deploy app-buckets --account local -f examples/s3/app-buckets.vars.json --key myapp --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
}

resources: {
	// ── Static Assets Bucket ────────────────────────────
	assets: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: name: "\(variables.name)-\(variables.environment)-assets"
		spec: {
			region:     "us-east-1"
			versioning: true
			acl:        "private"
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				app:     variables.name
				env:     variables.environment
				purpose: "assets"
			}
		}
	}

	// ── Application Logs Bucket ─────────────────────────
	logs: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: name: "\(variables.name)-\(variables.environment)-logs"
		spec: {
			region:     "us-east-1"
			versioning: false
			acl:        "private"
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				app:     variables.name
				env:     variables.environment
				purpose: "logs"
			}
		}
	}

	// ── Backups Bucket (KMS encrypted) ──────────────────
	backups: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: name: "\(variables.name)-\(variables.environment)-backups"
		spec: {
			region:     "us-east-1"
			versioning: true
			acl:        "private"
			encryption: {
				enabled:   true
				algorithm: "aws:kms"
			}
			tags: {
				app:     variables.name
				env:     variables.environment
				purpose: "backups"
			}
		}
		// Production backups must not be accidentally deleted.
		lifecycle: {
			preventDestroy: variables.environment == "prod"
		}
	}
}
