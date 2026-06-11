# Future Directions

> This document describes planned capabilities that are not yet implemented. Each section outlines the intended technical approach so design discussions have a common reference point; nothing here is a commitment to a timeline.
>
> Implemented capabilities — the Restate command service, DAG-driven deployment orchestrator, built-in CLI, deployment state and index objects, the deployment event stream, the observe flow, the AWS SSM resolver, the Auth Service (credential management), the Workspace Service (environment isolation), resource import (`praxis import`), CUE policy enforcement, and targeted rollback of failed deployments (`praxis delete --rollback`) — are intentionally omitted. Praxis also ships no built-in AI assistant by design: it is agent-friendly at the boundary, driven by external agent harnesses through the CLI (`-o json`, stable exit codes) or the HTTP API.

---

## Cross-Stack References

Allow one deployment to consume the outputs of another. A networking stack publishes a VPC ID; an application stack references it.

**Technical approach:** Introduce a cross-stack reference syntax, e.g. `${ stacks["networking"].outputs["vpc"].vpcId }`. Core resolves these at deploy time by querying the referenced deployment's stored outputs via `GetOutputs` on the Deployment Workflow. Resolution requires the referenced stack to be deployed and in a `Complete` state, and each reference creates an implicit dependency edge between stacks so coordinated applies order correctly.

---

## Point-in-Time Rollback

Praxis already ships targeted rollback for failed deployments — `praxis delete Deployment/<key> --rollback` deletes confirmed-provisioned resources in reverse dependency order. What remains is reverting a deployment to a previous known-good state.

**Technical approach:** `praxis rollback <stack> --to <deployment-id>`. Core diffs the current deployment state against the target deployment record and applies the inverse changes — reverting specs that changed, deleting resources that were added, re-provisioning resources that were removed. Deployment History provides the state snapshots this requires.

---

## Approval Gates

Require explicit human approval before changes are applied to protected stacks.

**Technical approach:** A stack or workspace is marked as protected; a deploy against it computes its plan, then suspends awaiting approval. Restate's durable promises make this nearly free to build — the workflow can remain suspended for days at no cost, then resume the instant `praxis approve <deployment-id>` (or the equivalent HTTP API call) is invoked. Approval and rejection decisions land in the deployment event stream, giving an audit trail of who approved what and when.

---

## Additional Secret Backends

Extend secret resolution beyond AWS SSM Parameter Store.

**Technical approach:** A pluggable resolver interface — `Resolve(path) → value` — selected by URI scheme. AWS Secrets Manager is first (e.g. `secretsmanager://`), since it fills a genuine gap for an AWS-focused tool rather than extending into new territory. HashiCorp Vault (e.g. `vault:///secret/data/db-password`) follows as the most common cloud-agnostic store.
