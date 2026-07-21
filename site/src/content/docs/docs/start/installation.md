---
title: Installation
description: Prepare a local Praxis environment.
sidebar:
  order: 2
---

The supported evaluation path downloads the Praxis alpha artifacts. It starts
Moto as a local AWS-compatible API, Restate, Praxis Core, and all five driver
packs without compiling Praxis or cloning the repository.

## Prerequisites

- Docker with Docker Compose

## Start the stack

Open the [Praxis alpha release](https://github.com/shirvan/praxis/releases/tag/alpha)
and download the CLI archive for your platform, `checksums.txt`, and
`praxis-alpha-quickstart.tar.gz`. Verify the archives against the checksum
file, place `praxis` on your `PATH`, then run:

```bash
tar -xzf praxis-alpha-quickstart.tar.gz
cd praxis-alpha-quickstart
./praxis-up
praxis version
```

The bundle pulls all six Praxis images at the single supported `:alpha` tag,
waits for the infrastructure services, and registers Core and every driver
pack with Restate.

## Confirm the environment

```bash
praxis list schemas
```

`praxis list schemas` runs offline from the schemas embedded in the CLI. It is a quick way to confirm the binary and discover current resource kinds.

:::note
The local environment uses Moto and the `local` account. It is intended for learning and development. Configure a real account only after you are comfortable with plans, lifecycle policy, and deletion behavior.
:::

:::caution[One mutable alpha]
Update the CLI, all service images, chart, schemas, and templates together.
Alpha revisions may break existing state and templates.
:::

:::tip[Full reference]
For production deployment, configuration, health checks, upgrades, and troubleshooting, see the [operator guide on GitHub](https://github.com/shirvan/praxis/blob/main/docs/OPERATORS.md). For repository builds and contributor setup, see the [developer guide](https://github.com/shirvan/praxis/blob/main/docs/DEVELOPERS.md).
:::
