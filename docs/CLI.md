# CLI Reference

The `praxis` binary is the primary human interface for Praxis. It communicates with Praxis Core exclusively through the Restate ingress HTTP endpoint — it never talks to driver services or deployment state directly.

## Quick Reference

| Command              | Audience | Purpose                                         |
|----------------------|----------|-------------------------------------------------|
| `deploy`             | Users    | Deploy from a registered template               |
| `template register`  | Operators| Register a CUE template                         |
| `template list`      | Both     | List registered templates                        |
| `template describe`  | Both     | Show template details and variable schema        |
| `template delete`    | Operators| Remove a registered template                     |
| `apply`              | Operators| Provision resources from inline CUE              |
| `plan`               | Operators| Preview what would change without applying       |
| `get`                | Both     | Show deployment or resource details              |
| `list`               | Both     | List active deployments                          |
| `delete`             | Both     | Tear down a deployment                           |
| `import`             | Operators| Adopt an existing cloud resource                 |
| `workspace create`   | Operators| Create or update a workspace                     |
| `workspace list`     | Both     | List workspaces                                  |
| `workspace select`   | Both     | Set the active workspace                         |
| `workspace show`     | Both     | Show workspace details                           |
| `workspace delete`   | Operators| Remove a workspace                               |
| `observe`            | Both     | Watch deployment progress in real time           |
| `state mv`           | Operators| Rename or move a resource between deployments    |
| `fmt`                | Both     | Format CUE template files                        |
| `version`            | Both     | Print the CLI version                            |

## Global Flags

Every subcommand inherits these flags:

| Flag         | Env Var                   | Default                  | Description                              |
|--------------|---------------------------|--------------------------|------------------------------------------|
| `--endpoint` | `PRAXIS_RESTATE_ENDPOINT` | `http://localhost:8080`  | Restate ingress URL                      |
| `-o, --output` | —                       | `table`                  | Output format: `table` or `json`         |
| `--plain`    | `NO_COLOR`               | `false`                  | Disable colors and styled table borders  |
| `--region`   | `PRAXIS_REGION`           | —                        | Default AWS region for key resolution    |

The `--account` flag is available on commands that touch provider APIs (`apply`, `deploy`, `plan`, `import`). It can also be set via the `PRAXIS_ACCOUNT` environment variable.

### Output Formats

- **table** (default) — Human-friendly terminal output. On a TTY, Praxis renders colored status values, diff markers, and bordered tables. When stdout is piped or redirected, output automatically falls back to plain text.
- **json** — Machine-readable indented JSON, suitable for scripting, piping to `jq`, and AI agents.

Use `--plain` to force plain text even on a TTY. Praxis also respects `NO_COLOR=1` and disables styling automatically for non-interactive output.

### Styled Output Details

When styling is active (TTY, no `--plain`, no `NO_COLOR`), the CLI applies contextual colors and formatting:

| Element | Styling |
|---------|---------|
| Plan diffs | `+` lines green (create), `~` lines yellow (update), `-` lines red (delete) |
| Status values | `Ready` / `Complete` green, `Applying` / `Pending` yellow, `Failed` red |
| Tables | Bordered (Lip Gloss) with bold colored headers |
| Event stream | Timestamps dimmed, status colored, resource names bold |
| Success messages | Green `✓` prefix |
| Error messages | Red with bold formatting |
| Confirmation prompts | Yellow bold |
| Labels / keys | Dimmed secondary text |

When styling is disabled (`--plain`, piped output, or `NO_COLOR=1`), all output falls back to plain `tabwriter` tables and undecorated text — fully compatible with `grep`, `awk`, and other text processing tools.

The styling layer uses [Lip Gloss v2](https://github.com/charmbracelet/lipgloss) for declarative style rendering with automatic terminal color profile detection (TrueColor → 256-color → 16-color → none) and adaptive light/dark background support.

---

## deploy

Deploy infrastructure from a pre-registered CUE template. This is the primary user-facing command — no CUE knowledge required.

```bash
praxis deploy <template-name> [flags]
```

**Flags:**

| Flag              | Default | Description                                        |
|-------------------|---------|----------------------------------------------------|
| `--var key=value` | —       | Template variable (repeatable)                     |
| `-f, --file`      | —       | JSON file containing template variables            |
| `--key`           | —       | Pin a stable deployment key for idempotent re-deploy|
| `--account`       | env     | AWS account name                                   |
| `--wait`          | false   | Poll until deployment reaches a terminal state     |
| `--dry-run`       | false   | Preview changes without provisioning (runs PlanDeploy) |
| `--show-rendered` | false   | Display the fully-evaluated template JSON (with `--dry-run`) |
| `--poll-interval` | 2s      | Polling interval when `--wait` is set              |
| `--timeout`       | 5m      | Maximum wait time (0 for no limit)                 |

**Examples:**

```bash
# Deploy from a registered template
praxis deploy stack1 --var name=orders-api --var environment=prod

# With a JSON variables file
praxis deploy stack1 -f variables.json

# Combine file and flags (flags override file values)
praxis deploy stack1 -f base.json --var environment=prod

# Idempotent re-deploy with a stable key
praxis deploy stack1 --var name=orders-api --key orders-prod

# Wait for completion
praxis deploy stack1 --var name=orders-api --key orders-prod --wait

# Preview changes without provisioning
praxis deploy stack1 --var name=orders-api --dry-run

# JSON output for scripting
praxis deploy stack1 --var name=orders-api -o json
```

**Behavior:**

The template must have been registered by an operator using `praxis template register`. Variables are validated against the template's extracted schema before the CUE pipeline runs — missing required variables, type mismatches, and invalid enum values are rejected immediately with a clear error.

Without `--wait`, the command returns immediately with the deployment key and status. With `--wait`, the CLI polls until a terminal state or `--timeout` is reached.

The `--dry-run` flag runs the full evaluation pipeline but does not submit a workflow — it shows a plan diff of what would change, identical to `praxis plan` output.

When a template contains data sources, `plan`, `apply --dry-run`, and `deploy --dry-run` also print a `Data sources:` section showing each resolved lookup and its outputs. In JSON mode, the same information is returned in the `dataSources` field.

---

## template

Manage CUE templates in the Praxis registry. Templates must be registered before they can be used with `praxis deploy`.

### template register

Register or update a CUE template from a file.

```bash
praxis template register <file.cue> [flags]
```

**Flags:**

| Flag            | Default        | Description                                |
|-----------------|----------------|--------------------------------------------|
| `--name`        | filename       | Template name (defaults to filename without extension) |
| `--description` | —              | Human-readable description                 |

**Examples:**

```bash
# Register with auto-name from filename
praxis template register stack1.cue

# Custom name
praxis template register stack1.cue --name my-stack

# With description
praxis template register stack1.cue --description "Service stack with S3 and SG"
```

On registration, Praxis extracts the variable schema from the CUE `variables:` block. Re-registering the same name updates the template and shifts the previous version to a one-level rollback buffer.

### template list

List all registered templates.

```bash
praxis template list
```

**Output:**

```text
NAME          DESCRIPTION                        UPDATED
----          -----------                        -------
stack1        Service stack with S3 and SG       2026-03-15 10:30:00 UTC
vpc-baseline  VPC baseline with public subnets   2026-03-14 09:00:00 UTC
```

### template describe

Show template details including the extracted variable schema.

```bash
praxis template describe <name>
```

**Output:**

```text
Template:    stack1
Description: Service stack with S3 and SG
Digest:      a1b2c3d4...
Created:     2026-03-15 10:30:00 UTC
Updated:     2026-03-15 10:30:00 UTC

Variables:
  NAME          TYPE    REQUIRED  DEFAULT  CONSTRAINT
  name          string  yes       -        ^[a-z][a-z0-9-]{2,40}$
  environment   string  yes       -        dev | staging | prod
  vpcId         string  yes       -        -
```

### template delete

Remove a registered template.

```bash
praxis template delete <name>
```

---

## apply

Evaluate a CUE template and submit it to the Praxis orchestrator for provisioning. This is the operator/developer path — for user-facing deployments, see `deploy`.

```bash
praxis apply <template.cue> [flags]
```

**Flags:**

| Flag              | Default | Description                                        |
|-------------------|---------|----------------------------------------------------|
| `--var key=value` | —       | Template variable (repeatable)                     |
| `--key`           | —       | Pin a stable deployment key for idempotent re-apply|
| `--account`       | env     | AWS account name                                   |
| `--wait`          | false   | Poll until deployment reaches a terminal state     |
| `--poll-interval` | 2s      | Polling interval when `--wait` is set              |
| `--timeout`       | 5m      | Maximum wait time (0 for no limit)                 |

**Examples:**

```bash
# Basic apply
praxis apply webapp.cue

# With template variables
praxis apply webapp.cue --var env=production --var region=us-west-2

# Idempotent re-apply with a stable key
praxis apply webapp.cue --key my-webapp

# Wait for completion (blocks until terminal state)
praxis apply webapp.cue --key my-webapp --wait

# JSON output for scripting
praxis apply webapp.cue -o json
```

**Behavior:**

Without `--wait`, the command returns immediately with the deployment key and status. The deployment continues asynchronously in the background.

With `--wait`, the CLI polls the deployment state at the configured interval. If the deployment does not reach a terminal state before `--timeout`, the CLI prints an error message with recovery commands and exits with code **2**.

The `--key` flag enables idempotent re-apply: submitting the same template with the same key updates the existing deployment rather than creating a new one.

---

## plan

Perform a dry-run evaluation of a CUE template. Runs the full template pipeline (CUE evaluation, SSM resolution, DAG construction) and compares desired state against current driver state to produce a diff.

No resources are provisioned — this is a read-only operation.

```bash
praxis plan <template.cue> [flags]
```

**Flags:**

| Flag              | Default | Description                                     |
|-------------------|---------|-------------------------------------------------|
| `--var key=value` | —       | Template variable (repeatable)                  |
| `--account`       | env     | AWS account name                                |
| `--show-rendered` | false   | Display the fully-evaluated template JSON       |

**Examples:**

```bash
# Preview changes
praxis plan webapp.cue

# With variables
praxis plan webapp.cue --var env=staging

# Debug template evaluation
praxis plan webapp.cue --show-rendered

# Machine-readable diff
praxis plan webapp.cue -o json
```

**Plan Output:**

The plan displays each resource with a change symbol and field-level diffs:

```text
+ my-bucket (S3Bucket)
    + bucket_name = "my-bucket"
    + tags = {"env": "staging"}

~ web-sg (SecurityGroup)
    ~ description: "old desc" => "new desc"

- old-resource (S3Bucket)
    - bucket_name = "old-resource"
```

Symbols: `+` create, `~` update, `-` delete. A summary line follows with the total counts.

Resources with `lifecycle.ignoreChanges` have matching diffs filtered from the plan. If all diffs are ignored, the resource shows as unchanged. Resources with `lifecycle.preventDestroy: true` that would be deleted are flagged as protected in the summary.

---

## get

Retrieve the current state of a deployment or individual resource.

```bash
praxis get <Kind>/<Key>
```

The argument uses `Kind/Key` format. Supported kinds:

- `Deployment/<key>` — Full deployment status with per-resource breakdown and outputs
- `S3Bucket/<key>` — Single S3 bucket resource status
- `SecurityGroup/<key>` — Single security group status
- `EC2Instance/<key>` — Single EC2 instance status
- `VPC/<key>` — Single VPC status
- `ElasticIP/<key>` — Single Elastic IP resource status
- `AMI/<key>` — Single AMI resource status
- `EBSVolume/<key>` — Single EBS volume status
- `InternetGateway/<key>` — Single Internet Gateway status

**Examples:**

```bash
# Deployment overview
praxis get Deployment/my-webapp

# Individual resource
praxis get S3Bucket/my-bucket
praxis get SecurityGroup/vpc-123~web-sg
praxis get EC2Instance/us-east-1~web-server

# JSON for scripting
praxis get Deployment/my-webapp -o json
```

**Deployment Output:**

```text
Deployment: my-webapp
Status:     Complete
Template:   webapp.cue
Created:    2025-01-15 10:30:00 UTC
Updated:    2025-01-15 10:31:45 UTC

RESOURCE      KIND            STATUS    ERROR
--------      ----            ------    -----
my-bucket     S3Bucket        Ready     -
web-sg        SecurityGroup   Ready     -

Outputs:
  my-bucket.arn = arn:aws:s3:::my-bucket
  web-sg.group_id = sg-0abc123
```

**Resource Output:**

```text
Resource:   S3Bucket/my-bucket
Status:     Ready
Mode:       managed
Generation: 3
```

For resources with errors, the full error text is displayed below the summary table so you can diagnose failures without digging into logs.

---

## list

List known resources of a given type. Currently supports deployments.

```bash
praxis list <resource-type>
```

Accepted values: `deployments`, `deployment`, `deploy`.

**Flags:**

| Flag           | Default | Description                   |
|----------------|---------|-------------------------------|
| `-w, --workspace` | —    | Filter by workspace name      |

**Examples:**

```bash
praxis list deployments
praxis list deployments -o json
praxis list deployments -w staging
```

**Output:**

```text
KEY          STATUS     RESOURCES  CREATED                   UPDATED
---          ------     ---------  -------                   -------
my-webapp    Complete   3          2025-01-15 10:30:00 UTC   2025-01-15 10:31:45 UTC
staging-app  Applying   2          2025-01-15 11:00:00 UTC   2025-01-15 11:00:05 UTC
```

---

## delete

Tear down a deployment and all its resources in reverse dependency order.

```bash
praxis delete Deployment/<key> [flags]
```

**Flags:**

| Flag        | Default | Description                               |
|-------------|---------|-------------------------------------------|
| `--yes`     | false   | Skip confirmation prompt                  |
| `--wait`    | false   | Block until deletion completes            |
| `--timeout` | 5m      | Maximum wait time (0 for no limit)        |

**Examples:**

```bash
# Interactive confirmation
praxis delete Deployment/my-webapp

# Skip prompt (CI/scripting)
praxis delete Deployment/my-webapp --yes

# Wait for completion
praxis delete Deployment/my-webapp --yes --wait
```

Without `--yes`, the command prompts for confirmation before proceeding. Deletion is asynchronous — use `--wait` to block until all resources have been removed!

Resources with `lifecycle.preventDestroy: true` cannot be deleted. The delete workflow fails with a terminal error identifying the protected resource. To proceed, update the template to remove or disable `preventDestroy`, re-apply, then retry the delete.

The same timeout behavior as `apply --wait` applies: exit code **2** on timeout, with recovery commands printed to stderr.

---

## import

Adopt an existing cloud resource under Praxis management without recreating it.

```bash
praxis import <Kind> [flags]
```

**Flags:**

| Flag        | Default   | Description                                        |
|-------------|-----------|----------------------------------------------------|
| `--id`      | —         | Cloud-provider-native resource identifier (required)|
| `--region`  | env       | AWS region where the resource lives (required)     |
| `--account` | env       | AWS account name                                   |
| `--observe` | false     | Import in observed mode (track but never modify)   |

**Examples:**

```bash
# Import an S3 bucket
praxis import S3Bucket --id my-existing-bucket --region us-east-1

# Import a security group
praxis import SecurityGroup --id sg-0abc123 --region us-east-1

# Import an EBS volume
praxis import EBSVolume --id vol-0abc123 --region us-east-1

# Import an Elastic IP
praxis import ElasticIP --id eipalloc-0abc123 --region us-east-1

# Import an existing Internet Gateway
praxis import InternetGateway --id igw-0abc123 --region us-east-1

# Import in observed mode (read-only tracking)
praxis import S3Bucket --id my-bucket --region us-west-2 --observe
```

The `--observe` flag imports the resource in **observed mode** — Praxis tracks it and reports drift, but never modifies it. This is useful for monitoring resources managed by another system.

**Output:**

```text
Key:    my-existing-bucket
Status: Ready
Outputs:
  arn = arn:aws:s3:::my-existing-bucket
  region = us-east-1
```

---

## observe

Stream deployment progress events in real time by polling the event timeline.

```bash
praxis observe Deployment/<key> [flags]
```

**Flags:**

| Flag              | Default | Description                           |
|-------------------|---------|---------------------------------------|
| `--poll-interval` | 1s      | How frequently to poll for new events |
| `--timeout`       | 5m      | Maximum time to observe (0 = no limit)|

**Examples:**

```bash
# Watch a deployment
praxis observe Deployment/my-webapp

# Faster polling
praxis observe Deployment/my-webapp --poll-interval 500ms

# JSON event stream
praxis observe Deployment/my-webapp -o json
```

**Output:**

```text
Observing deployment my-webapp...

[2025-01-15 10:30:05 UTC] Applying my-bucket/S3Bucket: Provisioning started
[2025-01-15 10:30:12 UTC] Applying web-sg/SecurityGroup: Provisioning started
[2025-01-15 10:30:18 UTC] Complete my-bucket/S3Bucket: Resource ready
[2025-01-15 10:30:25 UTC] Complete web-sg/SecurityGroup: Resource ready
[2025-01-15 10:30:25 UTC] Complete Deployment complete
```

The command exits automatically when the deployment reaches a terminal state (Complete, Failed, Deleted, or Cancelled). If the event stream is unavailable, it falls back to status polling.

---

## state mv

Rename a resource within a deployment or move it to another deployment. Only the deployment state mapping is updated — no cloud resources are created, modified, or deleted. The deployment must be in a terminal state (Complete, Failed, or Cancelled).

```bash
praxis state mv <source> <destination>
```

Source format: `Deployment/<key>/<resource-name>`

Destination can be:

- A bare name — renames within the same deployment
- `Deployment/<key>/<resource-name>` — moves to another deployment

**Examples:**

```bash
# Rename a resource within the same deployment
praxis state mv Deployment/web-app/myBucket newBucketName

# Move a resource to another deployment, keeping its name
praxis state mv Deployment/web-app/myBucket Deployment/data-stack/myBucket

# Move and rename in one step
praxis state mv Deployment/web-app/myBucket Deployment/data-stack/dataBucket
```

**Table output:**

```text
Renamed myBucket → newBucketName in deployment web-app
```

```text
Moved myBucket from web-app to data-stack as dataBucket
```

The underlying driver Virtual Object key does not change. This enables template refactoring (renaming a resource in CUE) without reprovisioning.

---

## fmt

Format CUE template files using the canonical CUE style. Files are formatted in place by default.

```bash
praxis fmt [files or directories...]
```

**Flags:**

| Flag      | Default | Description                                              |
|-----------|---------|----------------------------------------------------------|
| `--check` | `false` | Check formatting without modifying files (exit 1 if any file would change) |

**Examples:**

```bash
# Format all .cue files in the current directory (recursive)
praxis fmt

# Format specific files
praxis fmt templates/vpc.cue templates/s3.cue

# Format all .cue files under a directory
praxis fmt schemas/

# CI check — exits non-zero if any file needs formatting
praxis fmt --check .
```

When no arguments are given, `fmt` defaults to the current directory and walks it recursively for `.cue` files. The `--check` flag is useful for CI gating — it lists unformatted files and exits with code 1 without modifying them.

---

## version

Print the CLI binary version and build date.

```bash
praxis version
```

```text
praxis v0.1.0 (built 2025-01-15T10:00:00Z)
```

---

## workspace

Manage workspaces — named environment contexts that bind deployments with shared defaults.

See [Auth & Workspaces](AUTH.md) for the full design.

### workspace create

Create or update a workspace.

```bash
praxis workspace create <name> --account <alias> --region <region> [flags]
```

**Flags:**

| Flag        | Required | Description                                        |
|-------------|----------|----------------------------------------------------|
| `--account` | Yes      | AWS account alias (must be registered in Auth)     |
| `--region`  | Yes      | Default AWS region                                 |
| `--var`     | No       | Default variable `key=value` (repeatable)          |
| `--select`  | No       | Set as active workspace after creation             |

```bash
praxis workspace create prod --account prod-us --region us-east-1
praxis workspace create staging --account staging --region us-west-2 --var env=staging --select
```

### workspace list

List all workspaces with account, region, and active marker.

```bash
praxis workspace list
praxis workspace list -o json
```

### workspace select

Set the active workspace. Persisted in `~/.praxis/config.json`.

```bash
praxis workspace select <name>
```

### workspace show

Show workspace details. Uses the active workspace if no name is given.

```bash
praxis workspace show [name]
```

### workspace delete

Delete a workspace and deregister it from the index.

```bash
praxis workspace delete <name>
```

---

## Resource Key Resolution

Resources are identified by `Kind/Key` pairs. The CLI automatically resolves keys based on the resource kind's scope:

| Scope    | Kinds           | Key Format        | Example                   |
|----------|-----------------|-------------------|---------------------------|
| Global   | `S3Bucket`      | Name as-is        | `my-bucket`               |
| Custom   | `SecurityGroup`, `NetworkACL`, `RouteTable` | User-supplied key  | `vpc-123~web-sg`          |
| Region   | `EC2Instance`, `VPC`, `AMI`, `EBSVolume` | `region~name` | `us-east-1~web-server` |

When `PRAXIS_REGION` (or `--region`) is set and the key doesn't already contain a `~` separator, the CLI prepends the region for region-scoped resources. Global and custom-scoped resources are passed through unchanged.

## Exit Codes

| Code | Meaning                                          |
|------|--------------------------------------------------|
| 0    | Success                                          |
| 1    | Error (invalid arguments, API failure, etc.)     |
| 2    | Timeout waiting for deployment completion        |

## Environment Variables

| Variable                   | Purpose                                 |
|----------------------------|-----------------------------------------|
| `PRAXIS_RESTATE_ENDPOINT`  | Restate ingress URL                    |
| `PRAXIS_REGION`            | Default AWS region for key resolution  |
| `PRAXIS_ACCOUNT`           | Default AWS account for provider calls |
