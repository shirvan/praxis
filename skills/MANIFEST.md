# Praxis Skills Manifest

> Actionable agent skills for working with the Praxis codebase. Each skill is a step-by-step procedure.

## Skills

### Core Development

| Skill | Path | Description | When to Use |
|-------|------|-------------|-------------|
| **implement-driver** | [implement-driver/SKILL.md](implement-driver/SKILL.md) | Implement a new AWS resource driver from scratch | Adding a new AWS resource type to Praxis |
| **add-adapter** | [add-adapter/SKILL.md](add-adapter/SKILL.md) | Create a provider adapter for a driver | Connecting a driver to the orchestrator |
| **write-tests** | [write-tests/SKILL.md](write-tests/SKILL.md) | Write unit, integration, and E2E tests | Testing driver logic, adapters, or templates |
| **extend-cli** | [extend-cli/SKILL.md](extend-cli/SKILL.md) | Add new CLI commands or subcommands | Adding new user-facing commands |

### Template & Config

| Skill | Path | Description | When to Use |
|-------|------|-------------|-------------|
| **create-template** | [create-template/SKILL.md](create-template/SKILL.md) | Author CUE templates for infrastructure | Writing or modifying CUE templates |
| **migrate-template** | [migrate-template/SKILL.md](migrate-template/SKILL.md) | Convert Terraform/CloudFormation/Crossplane to Praxis CUE | Migrating existing IaC to Praxis |

### Operations

| Skill | Path | Description | When to Use |
|-------|------|-------------|-------------|
| **run-project** | [run-project/SKILL.md](run-project/SKILL.md) | Build, run, and test the project | Setting up dev environment, running tests |
| **debug-deployment** | [debug-deployment/SKILL.md](debug-deployment/SKILL.md) | Debug deployment failures and errors | Investigating failed deployments |

### Quality

| Skill | Path | Description | When to Use |
|-------|------|-------------|-------------|
| **review-code** | [review-code/SKILL.md](review-code/SKILL.md) | Review Praxis code changes | Pull request review, code quality checks |

## Skill Format

Each skill follows the VS Code SKILL.md convention:
- **Description**: What the skill does
- **When to Use**: Trigger conditions
- **Prerequisites**: What you need before starting
- **Steps**: Numbered procedure with code examples
- **Verification**: How to confirm success
- **Common Pitfalls**: Known gotchas

## Related

- [../docs/INDEX.md](../docs/INDEX.md) — Knowledge base for background understanding
- [../docs/CODEBASE.md](../docs/CODEBASE.md) — Codebase navigation
- [../docs/GLOSSARY.md](../docs/GLOSSARY.md) — Term definitions
