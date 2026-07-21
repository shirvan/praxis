---
title: Deploy your first stack
description: Plan, deploy, inspect, and remove a Praxis stack locally.
sidebar:
  order: 4
---

This walkthrough uses the S3 template included in the downloaded quick-start
bundle. It exercises the same template, planning, orchestration, state, and
generic driver paths used with AWS.

## 1. Start Praxis

```bash
cd praxis-alpha-quickstart
./praxis-up
praxis version
```

## 2. Inspect the template

Open `bucket.cue`. It declares one encrypted, versioned `S3Bucket` using the
current `praxis.io/alpha` API.

Validate the full plan without changing infrastructure:

```bash
praxis plan bucket.cue --account local
```

The plan shows whether each resource will be created, updated, replaced, deleted, or left unchanged. A plan does not provision resources.

## 3. Deploy

```bash
praxis deploy bucket.cue \
  --account local \
  --key quickstart \
  --yes --wait
```

`--key` gives the deployment stable identity. Reusing it applies later versions of the same desired deployment instead of creating unrelated state.

## 4. Inspect the result

```bash
praxis get Deployment/quickstart --all
praxis observe Deployment/quickstart
```

`get` reads the current deployment and resource state. `observe` follows the event stream as provisioning or reconciliation progresses.

Every CLI operation supports machine-readable output:

```bash
praxis get Deployment/quickstart -o json | jq .deployment.status
```

## 5. Remove the deployment

```bash
praxis delete Deployment/quickstart --yes --wait
./praxis-down
```

Praxis deletes managed resources in reverse dependency order. A resource protected by `lifecycle.preventDestroy` blocks deletion until that declared protection is changed.

## Next

- [Build a CUE template](/docs/build/templates/)
- [Understand plans and deployment](/docs/operate/plan-deploy/)
- [Learn how reconciliation works](/docs/operate/reconciliation/)

:::tip[Full reference]
For complete template syntax and examples, see [Templates](https://github.com/shirvan/praxis/blob/main/docs/TEMPLATES.md). For every command and flag, see the [CLI reference](https://github.com/shirvan/praxis/blob/main/docs/CLI.md).
:::
