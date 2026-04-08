# CLI Reference

The `praxis` binary is the primary human interface for Praxis. It communicates with Praxis Core exclusively through the Restate ingress HTTP endpoint — it never talks to driver services or deployment state directly.

## Verb-First Grammar

Praxis uses a consistent **verb-first** grammar for all commands:

```
praxis <VERB> [<RESOURCE>] [flags]
```

### Core Verbs

| Verb       | Meaning           | Description                                                                |
|------------|-------------------|----------------------------------------------------------------------------|
| `deploy`   | Provision it      | From a CUE file path or registered template name                          |
| `plan`     | Dry-run it        | Changes nothing — shows the JSON diff that `deploy` would produce          |
| `get`      | Show one thing    | Deployment, resource, workspace, config, concierge status, notifications   |
| `list`     | Show many things  | Deployments, templates, workspaces, sinks, events, concierge history       |
| `delete`   | Tear it down      | Deployment, workspace, template, sink, concierge session                   |
| `create`   | Make a new thing  | Workspace, template, notification sink                                     |
| `set`      | Update a setting  | Active workspace, config field, concierge provider                         |
| `move`     | Relocate it       | Rename resource or move between deployments                                |
| `import`   | Adopt it          | Adopt an existing cloud resource                                           |
| `reconcile`| Drift-check it    | On-demand reconciliation of a resource                                     |
| `observe`  | Watch it          | Real-time event stream for any resource: deployments, individual resources |
| `ask`      | Talk to AI        | Send a natural language prompt to the concierge                            |
| `approve`  | Human-in-the-loop | Approve or reject a pending concierge action                               |
| `test`     | Verify it         | Test delivery of an integration (notification sinks)                       |
| `fmt`      | Format it         | Format CUE template files                                                  |

### Environment Variables

| Variable                  | Purpose                                    | Default                 |
|---------------------------|--------------------------------------------|-------------------------|
| `PRAXIS_RESTATE_ENDPOINT` | Restate ingress URL                        | `http://localhost:8080` |
| `PRAXIS_REGION`           | Default AWS region for key resolution      | —                       |
| `PRAXIS_ACCOUNT`          | Default AWS account for deploy/plan/import | —                       |
| `PRAXIS_WORKSPACE`        | Active workspace override                  | from `~/.praxis/config` |
| `PRAXIS_OUTPUT`           | Default output format (`table` or `json`)  | `table`                 |
| `PRAXIS_SESSION`          | Concierge session ID                       | auto-generated          |

## Quick Reference

| Command              | Audience | Purpose                                         |
|----------------------|----------|-------------------------------------------------|
| **Verb-first (preferred)** | | |
| `deploy`             | Users    | Deploy from a template or CUE file              |
| `plan`               | Operators| Preview what would change without applying       |
| `get <Kind/Key>`     | Both     | Show deployment or resource details              |
| `get workspace`      | Both     | Show workspace details                           |
| `get config`         | Both     | Show workspace-scoped configuration              |
| `get concierge`      | Both     | Show concierge session status                    |
| `get notifications`  | Both     | Show notification sink health                    |
| `list deployments`   | Both     | List active deployments                          |
| `list templates`     | Both     | List registered templates                        |
| `list workspaces`    | Both     | List workspaces                                  |
| `list sinks`         | Both     | List notification sinks                          |
| `list events`        | Both     | List or query events                             |
| `list concierge`     | Both     | Show concierge conversation history              |
| `delete <Kind/Key>`  | Both     | Delete a deployment, workspace, template, sink, or session |
| `create workspace`   | Operators| Create or update a workspace                     |
| `create template`    | Operators| Register or update a CUE template                |
| `create sink`        | Operators| Create or update a notification sink             |
| `set workspace`      | Both     | Set the active workspace                         |
| `set config`         | Operators| Update workspace-scoped configuration            |
| `set concierge`      | Operators| Configure the concierge LLM provider             |
| `move`               | Operators| Rename or move a resource between deployments    |
| `import`             | Operators| Adopt an existing cloud resource                 |
| `reconcile`          | Operators| Trigger on-demand drift detection and correction |
| `observe <Kind/Key>` | Both     | Watch any resource in real time                  |
| `ask`                | Users    | Send a prompt to the AI concierge                |
| `approve`            | Both     | Approve or reject a pending concierge action     |
| `test sink/<name>`   | Operators| Test notification sink delivery                  |
| `fmt`                | Both     | Format CUE template files                        |
| `version`            | Both     | Print the CLI version                            |
| `concierge slack configure` | Operators | Configure the Slack gateway              |
| `concierge slack get-config` | Both   | Show Slack gateway configuration              |
| `concierge slack allowed-users` | Operators | Manage the Slack allowed-user list      |
| `concierge slack watch` | Operators | Manage event watch rules                      |
| `<prompt>` (root)    | Users    | Natural language shorthand — forwards to concierge |

## Natural Language Shorthand

When the concierge is running, you can talk to Praxis directly on the root
command — any unrecognised arguments are forwarded as a natural language prompt:

```bash
praxis "why did my deploy fail?"
praxis "convert this terraform to praxis" --file main.tf
praxis "deploy the orders API to staging"
```

This is equivalent to `praxis ask <prompt>`. The following flags
apply only when the root command forwards to the concierge:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--session` | | auto-resolved | Switch to a specific session ID (env: PRAXIS_SESSION) |
| `--new-session` | | `false` | Start a new session (ignores saved session) |
| `--file` | `-f` | | Attach file, directory, or glob to the prompt |
| `--account` | | env | Override AWS account |
| `--workspace` | | | Override workspace |
| `--auto-approve` | | `false` | Skip approval prompts |
| `--json` | | `false` | Output raw AskResponse JSON |

The `--file` flag supports single files, directories (walked recursively), and glob patterns:

```bash
# Single file
praxis "convert this" --file main.tf

# Directory (all files recursively)
praxis "migrate everything" --file ./terraform/

# Glob pattern
praxis "analyze these modules" --file "./modules/*.tf"
```

Each attached file is appended to the prompt with a path marker so the concierge knows which file is which.

### Session Persistence

Session IDs are resolved in this order:

1. `PRAXIS_SESSION` environment variable
2. State file at `~/.praxis/session`
3. Auto-generated random hex ID

After every `ask` invocation, the active session ID is saved to `~/.praxis/session` so subsequent commands in any shell reuse the same session automatically. To start a fresh session, pass `--new-session`. The active session ID is printed to stderr at the start of each invocation.

When the concierge container is not running, unrecognised arguments print a
helpful setup message instead of an error.

## Global Flags

Every subcommand inherits these flags:

| Flag         | Env Var                   | Default                  | Description                              |
|--------------|---------------------------|--------------------------|------------------------------------------|
| `--endpoint` | `PRAXIS_RESTATE_ENDPOINT` | `http://localhost:8080`  | Restate ingress URL                      |
| `-o, --output` | `PRAXIS_OUTPUT`           | `table`                  | Output format: `table` or `json`         |
| `--plain`    | `NO_COLOR`               | `false`                  | Disable colors and styled table borders  |
| `--region`   | `PRAXIS_REGION`           | —                        | Default AWS region for key resolution    |

The `--account` flag is available on commands that touch provider APIs (`deploy`, `plan`, `import`). It can also be set via the `PRAXIS_ACCOUNT` environment variable.

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

Deploy infrastructure from a CUE file path or a pre-registered template name.

```bash
praxis deploy <template-name-or-file.cue> [flags]
```

**Flags:**

| Flag              | Default | Description                                        |
|-------------------|---------|----------------------------------------------------|
| `--var key=value` | —       | Template variable (repeatable)                     |
| `-f, --file`      | —       | JSON file containing template variables            |
| `--key`           | —       | Pin a stable deployment key for idempotent re-deploy|
| `--account`       | env     | AWS account name                                   |
| `-y, --yes`       | false   | Skip confirmation prompt                           |
| `--wait`          | false   | Poll until deployment reaches a terminal state     |
| `--dry-run`       | false   | Preview changes without provisioning (runs PlanDeploy) |
| `--show-rendered` | false   | Display the fully-evaluated template JSON (with `--dry-run`) |
| `--target`        | —       | Limit to named resource and its dependencies (repeatable) |
| `--replace`       | —       | Force delete and re-provision of named resource (repeatable) |
| `--allow-replace` | false   | Automatically replace resources that fail due to immutable field changes (WARNING: destroys and recreates affected resources) |
| `--poll-interval` | 2s      | Polling interval when `--wait` is set              |
| `--timeout`       | 5m      | Maximum wait time (0 for no limit)                 |

**Examples:**

```bash
# Deploy from a registered template
praxis deploy stack1 --var name=orders-api --var environment=prod

# Deploy from a CUE file (inline template)
praxis deploy webapp.cue --var env=production --var region=us-west-2

# With a JSON variables file
praxis deploy stack1 -f variables.json

# Combine file and flags (flags override file values)
praxis deploy stack1 -f base.json --var environment=prod

# Idempotent re-deploy with a stable key
praxis deploy stack1 --var name=orders-api --key orders-prod

# Skip confirmation (CI/scripting)
praxis deploy webapp.cue --yes

# Wait for completion
praxis deploy stack1 --var name=orders-api --key orders-prod --wait

# Preview changes without provisioning
praxis deploy stack1 --var name=orders-api --dry-run

# Deploy only a specific resource (and its dependencies)
praxis deploy stack1 --var name=orders-api --target web-sg

# Force-replace a resource (destroy + recreate)
praxis deploy stack1 --var name=orders-api --replace web-sg

# Auto-replace on immutable field conflicts
praxis deploy stack1 --var name=orders-api --allow-replace

# JSON output for scripting
praxis deploy stack1 --var name=orders-api -o json
```

**Behavior:**

When the argument is a CUE file path (`*.cue`), deploy evaluates it as an inline template. When the argument is a bare name, deploy looks up a registered template (see `praxis create template`). Variables are validated against the template's extracted schema before the CUE pipeline runs — missing required variables, type mismatches, and invalid enum values are rejected immediately with a clear error.

Without `--wait`, the command returns immediately with the deployment key and status. With `--wait`, the CLI polls until a terminal state or `--timeout` is reached.

The `--dry-run` flag runs the full evaluation pipeline but does not submit a workflow — it shows a plan diff of what would change, identical to `praxis plan` output.

When the plan detects immutable field changes (e.g., VPC CIDR block), it shows a note with `--replace` and `--allow-replace` hints. `--replace` targets specific resources for destroy-then-recreate; `--allow-replace` does so automatically for any resource that fails with a 409 immutable-field conflict during provisioning. Resources with `lifecycle.preventDestroy` are still protected.

When a template contains data sources, `plan` and `deploy --dry-run` also print a `Data sources:` section showing each resolved lookup and its outputs. In JSON mode, the same information is returned in the `dataSources` field.

---

## plan

Perform a dry-run evaluation of a CUE template. Runs the full template pipeline (CUE evaluation, SSM resolution, DAG construction) and compares desired state against current driver state to produce a diff.

No resources are provisioned — this is a read-only operation.

```bash
praxis plan <template-name-or-file.cue> [flags]
```

**Flags:**

| Flag              | Default | Description                                     |
|-------------------|---------|-------------------------------------------------|
| `--var key=value` | —       | Template variable (repeatable)                  |
| `-f, --file`      | —       | JSON file containing template variables         |
| `--account`       | env     | AWS account name                                |
| `--target`        | —       | Limit to named resource and its dependencies (repeatable) |
| `--key`           | —       | Deployment key for comparing against prior state |
| `--graph`         | false   | Display the resource dependency graph           |
| `--show-rendered` | false   | Display the fully-evaluated template JSON       |

**Examples:**

```bash
# Preview changes
praxis plan webapp.cue

# With variables
praxis plan webapp.cue --var env=staging

# Compare against a specific deployment
praxis plan webapp.cue --key my-deployment

# Debug template evaluation
praxis plan webapp.cue --show-rendered

# Machine-readable diff
praxis plan webapp.cue -o json
```

**Plan Output:**

The plan displays each resource with a change symbol and field-level diffs:

```text
Praxis will perform the following actions:

  # S3Bucket "my-bucket" will be created
  + resource "S3Bucket" "my-bucket" {
      + bucketName = "my-bucket"
      + tags {
          + env = "staging"
        }
    }

  # SecurityGroup "vpc-0abc123~web-sg" will be updated in-place
  ~ resource "SecurityGroup" "vpc-0abc123~web-sg" {
      ~ description = "old desc" => "new desc"
      - sslPolicy   = "ELBSecurityPolicy-2016-08"
    }

  # S3Bucket "old-resource" will be destroyed
  - resource "S3Bucket" "old-resource" {
      - bucketName = "old-resource"
    }

Plan: 1 to create, 1 to update, 1 to delete, 0 unchanged.
```

Symbols: `+` create, `~` update, `-` delete. Within an update block, fields whose value changes to empty (empty string, zero, false, empty list, or empty map) are displayed as deletions with the `-` prefix. A summary line follows with the total counts.

Resources with `lifecycle.ignoreChanges` have matching diffs filtered from the plan. If all diffs are ignored, the resource shows as unchanged. Resources with `lifecycle.preventDestroy: true` that would be deleted are flagged as protected in the summary.

**Expression-bearing resources:**

Resources that reference other resources via `${resources.X.outputs.Y}` expressions are resolved at plan time using **live output collection**. As each resource is planned in topological order, its outputs are read from the driver's virtual-object state and used to hydrate downstream expression-bearing resources. This produces accurate create/update/noop diffs for expression-bearing resources, just like non-expression resources — without depending on a prior deployment record.

The plan also resolves expression-bearing resource keys for display. For example, a security group keyed by `${resources.vpc.outputs.vpcId}~web-sg` is displayed with the resolved VPC ID (e.g., `vpc-0abc123~web-sg`).

When a referenced resource has not been provisioned yet (first deploy), expression-bearing resources are shown as `create`. The `--key` flag can optionally specify a deployment whose prior outputs are used as a fallback seed; when omitted, the deployment key is auto-derived from the template.

**Field-level deletions:**

Within an update, fields whose value changes to empty (empty string, zero, false, empty list, or empty map) are displayed as deletions with a `-` prefix showing only the old value, rather than as an update to an empty value.

---

## get

Retrieve the current state of a deployment, individual resource, or meta-resource.

```bash
praxis get <Kind/Key>
praxis get workspace [name]
praxis get config <path>
praxis get concierge [--session <id>]
praxis get notifications
praxis get template/<name>
praxis get sink/<name>
```

The argument uses `Kind/Key` format for deployments and cloud resources. Meta-resources (workspace, config, concierge, notifications, template, sink) use subcommands.

### get (deployment / cloud resource)

Supported kinds:

- `Deployment/<key>` — Full deployment status with per-resource breakdown
- `S3Bucket/<key>` — Single S3 bucket resource status
- `SecurityGroup/<key>` — Single security group status
- `EC2Instance/<key>` — Single EC2 instance status
- `VPC/<key>` — Single VPC status
- `ElasticIP/<key>` — Single Elastic IP resource status
- `AMI/<key>` — Single AMI resource status
- `EBSVolume/<key>` — Single EBS volume status
- `InternetGateway/<key>` — Single Internet Gateway status

**Deployment Flags:**

| Flag         | Default | Description                                        |
|--------------|---------|----------------------------------------------------|
| `--deps`     | false   | Show resource dependency graph                     |
| `--inputs`   | false   | Show resource input specs (fetched from drivers)   |
| `--outputs`  | false   | Show resource outputs                              |
| `--errors`   | false   | Show full resource error details                   |
| `--all`      | false   | Show all optional sections (deps, inputs, outputs, errors) |

By default only metadata and the resource table are shown. Use flags to include additional sections.

**Examples:**

```bash
# Deployment overview (metadata + resource table only)
praxis get Deployment/my-webapp

# Include dependency graph and outputs
praxis get Deployment/my-webapp --deps --outputs

# Show everything
praxis get Deployment/my-webapp --all

# Individual resource
praxis get S3Bucket/my-bucket
praxis get SecurityGroup/vpc-123~web-sg
praxis get EC2Instance/us-east-1~web-server

# JSON for scripting (always includes all sections)
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
```

With `--all` (or `--outputs`), the outputs section is appended:

```text
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

### get workspace

Show workspace details. Uses the active workspace if no name is given.

```bash
praxis get workspace [name]
```

### get config

Read a configuration value for the active workspace.

```bash
praxis get config <path> [flags]
```

**Flags:**

| Flag             | Default          | Description                              |
|------------------|------------------|------------------------------------------|
| `-w, --workspace`| active workspace | Workspace name                           |

**Supported paths:**

| Path                | Description                      |
|---------------------|----------------------------------|
| `events.retention`  | Event retention policy           |

**Examples:**

```bash
praxis get config events.retention
praxis get config events.retention -w staging
praxis get config events.retention -o json
```

### get concierge

Show the current status of a concierge session, including any pending approvals.

```bash
praxis get concierge [--session <id>]
```

**Flags:**

| Flag        | Default         | Description    |
|-------------|-----------------|----------------|
| `--session` | auto-resolved   | Session ID     |

### get notifications

Show aggregate health across all notification sinks.

```bash
praxis get notifications
```

### get template/\<name\>

Show template details including the extracted variable schema.

```bash
praxis get template/<name>
```

### get sink/\<name\>

Show the full configuration of a single notification sink.

```bash
praxis get sink/<name>
```

---

## list

List known resources of a given type.

```bash
praxis list <resource-type> [flags]
```

Accepted values: `deployments` (aliases: `deployment`, `deploy`), `templates`, `workspaces`, `sinks`, `events`, `concierge`, or any cloud resource Kind (e.g. `S3Bucket`, `EC2Instance`, `VPC`).

**Flags:**

| Flag             | Default | Description                                         |
|------------------|---------|-----------------------------------------------------|
| `-w, --workspace`| —       | Filter by workspace name (deployments, events)      |
| `--since`        | —       | Show events from the last duration (e.g. `1h`, `7d`)|
| `--type`         | —       | Filter events by type prefix                        |
| `--severity`     | —       | Filter events by severity (info, warn, error)       |
| `--resource`     | —       | Filter events by resource name                      |
| `--limit`        | 100     | Maximum events to return                            |
| `--session`      | —       | Concierge session ID (default: auto-resolved)       |

**Examples:**

```bash
# Deployments
praxis list deployments
praxis list deployments -w staging

# Templates
praxis list templates

# Workspaces
praxis list workspaces

# Notification sinks
praxis list sinks

# Events for a single deployment
praxis list events Deployment/my-webapp
praxis list events Deployment/my-webapp --since 1h --severity error

# Cross-deployment event search
praxis list events --severity error
praxis list events -w staging --since 1d --limit 50

# Concierge conversation history
praxis list concierge
praxis list concierge --session my-session

# Cloud resources by Kind (walks all deployments)
praxis list S3Bucket
praxis list S3Bucket -w staging
praxis list EC2Instance -o json
```

The `--since` flag accepts Go-style durations (`1h`, `30m`, `2h30m`) plus a `d` suffix for days (`7d`).

**Output:**

```text
KEY          STATUS     RESOURCES  CREATED                   UPDATED
---          ------     ---------  -------                   -------
my-webapp    Complete   3          2025-01-15 10:30:00 UTC   2025-01-15 10:31:45 UTC
staging-app  Applying   2          2025-01-15 11:00:00 UTC   2025-01-15 11:00:05 UTC
```

---

## delete

Tear down a deployment and all its resources, or delete a meta-resource.

```bash
praxis delete <Kind/Key> [flags]
```

Supported kinds: `Deployment`, `workspace`, `template`, `sink`, `concierge`, or any cloud resource Kind (e.g. `S3Bucket/my-bucket`, `EC2Instance/us-east-1~web-server`).

**Flags:**

| Flag           | Default | Description                               |
|----------------|---------|-------------------------------------------|
| `-y, --yes`    | false   | Skip confirmation prompt                  |
| `--wait`       | false   | Block until deletion completes            |
| `--timeout`    | 5m      | Maximum wait time (0 for no limit)        |
| `--rollback`   | false   | Delete only resources for a failed/cancelled deployment |
| `--force`      | false   | Override `lifecycle.preventDestroy` protection on resources |

**Examples:**

```bash
# Delete a deployment (interactive confirmation)
praxis delete Deployment/my-webapp

# Skip prompt (CI/scripting)
praxis delete Deployment/my-webapp --yes

# Wait for completion
praxis delete Deployment/my-webapp --yes --wait

# Force-delete protected resources
praxis delete Deployment/my-webapp --force --yes

# Delete a workspace
praxis delete workspace/old-env

# Delete a template
praxis delete template/legacy-stack

# Delete a notification sink
praxis delete sink/old-webhook

# Delete an individual cloud resource
praxis delete S3Bucket/my-bucket --yes
praxis delete EC2Instance/us-east-1~web-server -y

# Clear a concierge session
praxis delete concierge
praxis delete concierge/my-session
```

Without `--yes`, the command prompts for confirmation before proceeding. Deletion is asynchronous — use `--wait` to block until all resources have been removed!

Resources with `lifecycle.preventDestroy: true` cannot be deleted by default. The delete workflow fails with a terminal error identifying the protected resource. To proceed, either update the template to remove or disable `preventDestroy`, re-deploy, then retry the delete — or use `--force` to override the protection (an audit event is emitted for each overridden resource).

When deleting a deployment that has a stuck apply workflow (e.g., hard-killed via the Restate admin API), the delete workflow waits up to 60 seconds for the apply to drain, then force-transitions the deployment to `Cancelled` and proceeds with teardown.

Before prompting for confirmation, `praxis delete` now shows a **destroy plan** listing all resources that will be removed (in reverse topological order) and any that will be skipped.

The same timeout behavior as `deploy --wait` applies: exit code **2** on timeout, with recovery commands printed to stderr.

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

## reconcile

Trigger on-demand drift detection and correction for a single resource. Normally, reconciliation runs automatically every 5 minutes via Restate durable timers — this command lets you check immediately without waiting.

```bash
praxis reconcile <Kind>/<Key>
```

The argument uses `Kind/Key` format, identical to `praxis get`.

**Examples:**

```bash
# Check drift on an S3 bucket
praxis reconcile S3Bucket/my-bucket

# Reconcile after a manual change in AWS console
praxis reconcile EC2Instance/us-east-1~web-server

# Check a security group
praxis reconcile SecurityGroup/vpc-123~web-sg

# JSON output for scripting
praxis reconcile S3Bucket/my-bucket -o json
```

**Table Output (no drift):**

```text
Resource:   S3Bucket/my-bucket
Drift:      Ready — no drift
Correcting: false
✓ Resource is in sync — no drift detected.
```

**Table Output (drift detected, Managed mode):**

```text
Resource:   S3Bucket/my-bucket
Drift:      Failed — resource has drifted
Correcting: Applying
```

**JSON Output:**

```json
{
  "drift": true,
  "correcting": true
}
```

**Behavior:**

- **Managed mode**: If drift is detected, the driver automatically re-applies the desired configuration to correct it. The `correcting` field is `true`.
- **Observed mode**: Drift is reported but not corrected. The `correcting` field is always `false`.
- If the reconciliation check itself fails (e.g., AWS API error), the `error` field contains the failure message.

This command is useful for:

- Verifying a resource is in sync after a manual AWS console change
- Diagnosing why a resource is in `Error` status
- Forcing immediate drift correction without waiting for the 5-minute timer
- CI/CD pipelines that need to confirm resource state before proceeding

---

## observe

Watch a resource's status changes in real time.

For Deployments, observe polls the event stream and displays incremental progress
updates. For individual cloud resources, it polls the resource status and displays
changes until the resource reaches a terminal state.

```bash
praxis observe <Kind/Key> [flags]
```

**Flags:**

| Flag              | Default | Description                           |
|-------------------|---------|---------------------------------------|
| `--poll-interval` | 1s      | How frequently to poll for new events |
| `--timeout`       | 5m      | Maximum time to observe (0 = no limit)|
| `--severity`      | —       | Filter by severity (info, warn, error)|
| `--resource`      | —       | Filter by resource name               |
| `--type`          | —       | Filter by event type prefix           |

**Examples:**

```bash
# Watch a deployment
praxis observe Deployment/my-webapp

# Faster polling
praxis observe Deployment/my-webapp --poll-interval 500ms

# Watch an individual resource
praxis observe S3Bucket/my-bucket
praxis observe EC2Instance/web-1 --timeout 2m

# JSON event stream
praxis observe Deployment/my-webapp -o json
```

The command exits automatically when a terminal state is reached. For deployments, that means Complete, Failed, Deleted, or Cancelled. For resources, that means Ready, Error, or Deleted.

---

## create

Create a new resource in Praxis.

### create workspace

Create or update a workspace.

```bash
praxis create workspace <name> --account <acct> --region <region> [flags]
```

**Flags:**

| Flag       | Default | Description                                     |
|------------|---------|-------------------------------------------------|
| `--account`| —       | AWS account alias (required)                    |
| `--region` | —       | Default AWS region (required)                   |
| `--var`    | —       | Default variable key=value (repeatable)         |
| `--select` | false   | Set as active workspace after creation          |

### create template

Register or update a CUE template from a file.

```bash
praxis create template <file.cue> [flags]
```

**Flags:**

| Flag           | Default        | Description                        |
|----------------|----------------|------------------------------------|
| `--name`       | filename       | Template name                      |
| `--description`| —              | Human-readable description         |

### create sink

Create or update a notification sink.

```bash
praxis create sink [flags]
```

**Flags:**

| Flag                  | Default      | Description                              |
|-----------------------|-------------|------------------------------------------|
| `--name`              | —            | Sink name                                |
| `--type`              | —            | Sink type (webhook, structured_log, etc) |
| `--url`               | —            | Endpoint URL                             |
| `-f, --file`          | —            | Read config from JSON file or stdin (-)  |
| `--filter-types`      | —            | Comma-separated event type prefixes      |
| `--filter-severities` | —            | Comma-separated severities               |
| `--max-retries`       | 3            | Max delivery retry attempts              |

---

## set

Update a setting or select a resource.

### set workspace

Set the active workspace.

```bash
praxis set workspace <name>
```

### set config

Update workspace-scoped configuration.

```bash
praxis set config <path> <value> [flags]
```

**Flags:**

| Flag             | Default          | Description                              |
|------------------|------------------|------------------------------------------|
| `-w, --workspace`| active workspace | Workspace name                           |
| `-f, --file`     | —                | Load full policy from JSON file          |

Supported paths: `events.retention.max-age`, `events.retention.max-events-per-deployment`, `events.retention.max-index-entries`, `events.retention.sweep-interval`, `events.retention.ship-before-delete`, `events.retention.drain-sink`.

**Examples:**

```bash
praxis set config events.retention.max-age 180d
praxis set config events.retention.max-events-per-deployment 500
praxis set config events.retention.drain-sink ops-drain
praxis set config events.retention -f retention.json
praxis set config events.retention.max-age 90d -w staging
```

### set concierge

Configure the concierge LLM provider.

```bash
praxis set concierge --provider <provider> [flags]
```

**Flags:**

| Flag         | Default | Description                    |
|--------------|---------|--------------------------------|
| `--provider` | —       | LLM provider: openai or claude |
| `--model`    | —       | Model name                     |
| `--api-key`  | —       | API key for the provider       |
| `--base-url` | —       | Custom API base URL            |

---

## move

Rename a resource within a deployment or move it to another deployment. Only the deployment state mapping is updated — no cloud resources are created, modified, or deleted.

```bash
praxis move <source> <destination>
```

Source format: `Deployment/<key>/<resource-name>`

Destination can be:

- A bare name — renames within the same deployment
- `Deployment/<key>/<resource-name>` — moves to another deployment

**Examples:**

```bash
praxis move Deployment/web-app/myBucket newBucketName
praxis move Deployment/web-app/myBucket Deployment/data-stack/dataBucket
```

---

## ask

Send a natural language prompt to the Concierge AI assistant.

```bash
praxis ask <prompt> [flags]
```

**Flags:**

| Flag          | Default | Description                    |
|---------------|---------|--------------------------------|
| `--session`   | —       | Session ID for continuity      |
| `--account`   | env     | AWS account name               |
| `-w, --workspace` | — | Workspace name                 |

**Examples:**

```bash
praxis ask how do I deploy a VPC
praxis ask "what's the status of my-app"
```

---

## approve

Approve or reject a pending action that the Concierge AI is waiting on.

```bash
praxis approve --awakeable-id <id> [flags]
```

**Flags:**

| Flag              | Default | Description                     |
|-------------------|---------|---------------------------------|
| `--awakeable-id`  | —       | Awakeable ID (required)         |
| `--reject`        | false   | Reject instead of approve       |
| `--reason`        | —       | Reason for approval/rejection   |

---

## test

Test an integration.

```bash
praxis test sink/<name>
```

Sends a synthetic CloudEvent to the named notification sink.

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
praxis <version> (built <timestamp>)
```

---

## concierge

AI-powered infrastructure assistant. The concierge is an optional service — these commands fail gracefully with setup instructions when the service is not running.

The main concierge operations are now verb-first:
- `praxis ask <prompt>` — Send a prompt
- `praxis set concierge` — Configure the LLM provider
- `praxis get concierge` — Show session status
- `praxis list concierge` — Show conversation history
- `praxis delete concierge` — Clear a session
- `praxis approve` — Approve or reject a pending action

### concierge slack

Manage the Slack gateway integration. The Slack gateway is an optional component that allows users to interact with the concierge from Slack channels.

#### concierge slack configure

Configure the Slack gateway with bot and app tokens.

```bash
praxis concierge slack configure [flags]
```

**Flags:**

| Flag               | Default | Description                                |
|--------------------|---------|--------------------------------------------|
| `--bot-token`      | —       | Slack bot token (`xoxb-...`)               |
| `--bot-token-ref`  | —       | SSM parameter name for bot token           |
| `--app-token`      | —       | Slack app-level token (`xapp-...`)         |
| `--app-token-ref`  | —       | SSM parameter name for app token           |
| `--event-channel`  | —       | Default channel for event notifications    |
| `--allowed-users`  | —       | Comma-separated Slack user IDs             |

**Examples:**

```bash
# Direct tokens (development)
praxis concierge slack configure \
  --bot-token xoxb-... \
  --app-token xapp-... \
  --event-channel C01ABC123

# SSM-backed tokens (production)
praxis concierge slack configure \
  --bot-token-ref ssm:///praxis/slack/bot-token \
  --app-token-ref ssm:///praxis/slack/app-token \
  --event-channel C01ABC123 \
  --allowed-users U04XYZ,U05ABC
```

#### concierge slack get-config

Show the current Slack gateway configuration (tokens are redacted server-side).

```bash
praxis concierge slack get-config
```

#### concierge slack allowed-users

Manage the Slack allowed-user list. When the list is empty, all users are permitted.

```bash
# Replace the entire list
praxis concierge slack allowed-users set "U04XYZ,U05ABC"

# Clear the list (permit all users)
praxis concierge slack allowed-users set ""

# Add a user
praxis concierge slack allowed-users add U06DEF

# Remove a user
praxis concierge slack allowed-users remove U04XYZ

# List current allowed users
praxis concierge slack allowed-users list
```

#### concierge slack watch

Manage event watch rules that route deployment events to specific Slack channels.

```bash
# Add a watch rule
praxis concierge slack watch add --name prod-errors \
  --channel C01ABC123 \
  --severities error \
  --workspaces prod

# List all watch rules
praxis concierge slack watch list

# Update a rule
praxis concierge slack watch update --id <rule-id> --enabled false

# Remove a rule
praxis concierge slack watch remove --id <rule-id>
```

**Watch add flags:**

| Flag             | Default | Description                                      |
|------------------|---------|--------------------------------------------------|
| `--name`         | —       | Watch rule name (required)                       |
| `--channel`      | —       | Slack channel for notifications                  |
| `--types`        | —       | Comma-separated event types (supports trailing `*`) |
| `--categories`   | —       | Comma-separated categories                       |
| `--severities`   | —       | Comma-separated severities                       |
| `--workspaces`   | —       | Comma-separated workspaces                       |
| `--deployments`  | —       | Comma-separated deployments                      |

**Watch update flags:**

| Flag        | Default | Description                    |
|-------------|---------|--------------------------------|
| `--id`      | —       | Watch rule ID (required)       |
| `--name`    | —       | New name                       |
| `--enabled` | —       | Enable or disable (`true`/`false`) |

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
| `PRAXIS_WORKSPACE`         | Active workspace override              |
| `PRAXIS_OUTPUT`            | Default output format (`table`/`json`) |
| `PRAXIS_SESSION`           | Override concierge session ID          |
