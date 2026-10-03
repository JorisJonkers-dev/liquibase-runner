# Agent contract

The estate-wide conventions live in one place and are **not duplicated here**:

**https://github.com/JorisJonkers-dev/workspace/blob/main/CLAUDE.md**

Read it before doing anything non-trivial in this repository. It covers the
things that most often go wrong, including:

- **Pull request labels.** The estate uses a prefixed taxonomy - `type:`,
  `area:`, `component:`, `priority:`, `status:`. Plain `bug` / `enhancement` /
  `documentation` do **not** exist, and `gh pr create` fails with
  `'bug' not found`. Run `gh label list --repo <owner>/<repo>` once before
  passing `--label`.
- **Verify the value, not the command.** An exit code, a `Ready` condition or
  an accepted object is not evidence that a consumer sees what you intended.
- Traps around workflow runs, `zsh` word-splitting, and detached submodule
  HEADs.

Duplicating that content into every repository guarantees the copies drift, so
this file stays a pointer. Add repo-specific guidance below.

## This repository

The platform's Liquibase runner: the image every application's migration image is built `FROM`,
and the checks an application's CI runs against the same changelog. Its contract is normative in
`JorisJonkers-dev/deploy-kit`, `spec/v1/55-delivery.md` ("The runner's contract" and "Migration
safety"); change the contract there first, then here.

`task check` is everything CI runs; `task` lists the targets. The toolchain is pinned in
`mise.toml` (`mise install`). `task test:integration` needs Docker: it builds the image and runs it
against a real Postgres.

- **The image is the unit under test.** Behaviour that depends on Liquibase or Postgres is proven
  in `test/integration`, through `docker run`, never against a mock of either.
- **`liquibase-runner.yaml` and the author `liquibase-runner` never change.** Every tag row already
  written is recorded under them.
- **The runner reads only the variables the render sets.** A new one is a spec change.
