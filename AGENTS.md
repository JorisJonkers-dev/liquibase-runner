# Agents

Guidance for any coding agent working in this repository.

The repository's agent contract is [CLAUDE.md](CLAUDE.md). The estate-wide conventions (labels, traps) are in the [estate contract](https://github.com/JorisJonkers-dev/workspace/blob/main/CLAUDE.md); read both before non-trivial changes.

This is the platform's Liquibase runner image. Its contract is normative in the deploy-kit repository (spec/v1/55-delivery.md there); change the contract there first.

## Build and test

- `task` lists the targets, defined in `Taskfile.yml`.
- `task check` runs everything CI runs: lint, test, build, secrets scan and integration tests.
- `task lint` runs formatting, golangci-lint and actionlint.
- `task test` runs `go test -race` over all packages.
- `task fix` applies golangci-lint fixes.
