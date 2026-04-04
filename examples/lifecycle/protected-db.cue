// protected-db.cue — RDS instance with deletion protection.
//
// Demonstrates preventDestroy to block accidental deletion of
// production databases. The allowDelete variable acts as a safety
// latch: set it to true to remove protection before deleting.
//
// Usage:
//   praxis template register examples/lifecycle/protected-db.cue --description "Protected RDS instance"
//   praxis deploy protected-db --account local -f examples/lifecycle/protected-db.vars.json --key mydb --wait
//
//   # Attempt delete (fails due to preventDestroy: true)
//   praxis delete Deployment/mydb --yes --wait
//
//   # Remove protection, then delete
//   praxis deploy protected-db --account local -f examples/lifecycle/protected-db.vars.json \
//     --var allowDelete=true --key mydb --wait
//   praxis delete Deployment/mydb --yes --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,30}$"
	environment: "dev" | "staging" | "prod"
	allowDelete: bool | *false
}

resources: database: {
	apiVersion: "praxis.io/v1"
	kind:       "RDSInstance"
	metadata: name: "\(variables.name)-\(variables.environment)-db"
	spec: {
		region:           "us-east-1"
		allocatedStorage: 100
		storageType:      "gp3"
		engine:           "postgres"
		engineVersion:    "15.3"
		instanceClass:    "db.t3.small"
		masterUsername:   "admin"
		multiAZ:          variables.environment == "prod"
		tags: {
			app: variables.name
			env: variables.environment
		}
	}
	// Block deletion unless explicitly unlocked via allowDelete.
	lifecycle: {
		preventDestroy: !variables.allowDelete
	}
}
