---
title: Configure an AWS account
description: Understand how Praxis resolves credentials and selects AWS accounts.
sidebar:
  order: 3
---

Praxis names AWS credential configurations so templates and deployments can select an account without embedding credentials in CUE.

For local development, the Docker Compose stack provides a `local` account configured for Moto:

```bash
praxis plan examples/vpc/basic-vpc.cue \
  --account local \
  -f examples/vpc/basic-vpc.vars.json
```

For AWS, Praxis supports runtime account configuration using role assumption or static credentials. Role assumption is preferred for real environments because long-lived access keys do not need to live in Praxis configuration.

## Account selection

Commands that call provider APIs accept `--account`. You can also set:

```bash
export PRAXIS_ACCOUNT=prod
```

An explicit command flag takes precedence over the environment default. Workspaces can supply their own default account for repeated team workflows.

## Credential boundary

Driver services request credentials for the named account through the Auth Service when they need to call AWS. Templates contain resource intent, not AWS secrets.

:::tip[Full reference]
For account resolution, credential sources, workspace defaults, and current runtime configuration payloads, see [Accounts and authentication in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/AUTH.md).
:::
