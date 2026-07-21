---
title: Workspaces and accounts
description: Group configuration and protect deployment environments.
sidebar:
  order: 4
---

A workspace supplies a named operational context for deployments. It can hold defaults such as the account, region, and change-control settings used by a team or environment.

```bash
praxis create workspace production \
  --account prod \
  --region us-west-2 \
  --select
praxis set workspace production
praxis get workspace production
```

## Account selection

Provider operations resolve an account in this order:

1. An explicit command or request account.
2. The selected workspace default.
3. The process environment default.

Keeping account identity outside the template lets one validated composition target multiple environments without embedding credentials.

## Protected workspaces

A protected workspace places new deployments into an approval state before resource dispatch. Restate keeps the workflow suspended durably until an operator approves or rejects it.

```bash
praxis approve payments-prod --comment "CAB-1402"
praxis reject payments-prod --comment "change window closed"
```

The decision is recorded in the deployment event stream.

:::tip[Full reference]
For account resolution, credential sources, workspace defaults, and authentication boundaries, see [Accounts and authentication in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/AUTH.md).
:::
