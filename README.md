# liquibase-runner

The platform's migration runner: the image every application's migration image is built `FROM`,
and the checks an application's CI runs against the same changelog before it publishes a
fragment.

Its contract is normative in `JorisJonkers-dev/deploy-kit`:
[`spec/v1/55-delivery.md`](https://github.com/JorisJonkers-dev/deploy-kit/blob/main/spec/v1/55-delivery.md),
"The runner's contract" and "Migration safety". This repository implements it; where the two
disagree, the spec wins.

## A migration image

```dockerfile
FROM ghcr.io/jorisjonkers-dev/liquibase-runner@sha256:...
COPY changelog/ /liquibase/changelog/
```

The root changelog is `/liquibase/changelog/changelog.yaml`. An application whose changesets are
already recorded under another name sets it once and never changes it, because Liquibase
identifies a changeset by its id, its author and this name:

```dockerfile
ENV CHANGELOG_FILE=db/changelog/db.changelog-master.yaml
```

Changelogs are YAML, the estate's one migration format. The image carries Liquibase and the
PostgreSQL driver and nothing else an application needs to add.

## In the cluster

| command | what it does |
|---|---|
| `up <tag>` | applies the changelog, then a changeset of the runner's own that tags the database `<tag>` |
| `down <tag>` | rolls the database back to `<tag>` |

A tag is an Application revision's first 12 hex digits. Both commands read only the variables
the render sets:

| variable | value |
|---|---|
| `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME` | the project database |
| `VAULT_ADDR` | the Secret Store |
| `VAULT_ROLE` | the migration identity's Vault role |
| `VAULT_CREDENTIALS_PATH` | the owner credential, `database/creds/<project>-owner` |

The runner signs in to Vault's Kubernetes auth method with the pod's own ServiceAccount token,
reads the owner credential at that path, and hands it to Liquibase in the environment, never in
an argument list.

**Every revision gets its own row and its own tag.** `up` runs the application's changelog and
then one changeset, authored `liquibase-runner` and recorded under `liquibase-runner.yaml`, whose
id is the tag. A revision that changes no schema is still a row, so `down` always has a tag to
return to.

**`down` takes the later tags with it.** It names every tag changeset written after its target
in the changelog it hands Liquibase, so their rows are rolled back with the changes they
followed. Without that, a revision applied again after a rollback would find its tag row
already there, and the tag would mark the state *before* its changes.

**A stale lock is released.** A migration Job runs once and is bounded by the platform's
deadline, so a `DATABASECHANGELOGLOCK` granted more than 30 minutes ago belongs to a run that was
killed. The runner clears it, says so, and carries on. A younger lock is left alone and
Liquibase waits for it as usual. An image may set `LOCK_STALE_AFTER` (a Go duration) when the
platform's deadline is longer.

**The root filesystem can be read-only.** The runner writes one file, its own changelog, under
`/tmp`, which must be writable.

## In CI

The Migration Proof action runs the three migration-safety obligations against a throwaway
Postgres and writes `migration-proof.yml`. Run it after building the migration image and before
publishing the fragment:

```yaml
- uses: JorisJonkers-dev/liquibase-runner/actions/migration-proof@<sha> # vX.Y.Z
  with:
    image: ghcr.io/jorisjonkers-dev/auth-api/auth-migration:${{ github.sha }}
    application: auth
    database-name: auth_db
    proof-file: deploy/migration-proof.yml
    # Everything below is left out on a first release, when nothing serves yet.
    serving-image: ghcr.io/jorisjonkers-dev/auth-api/auth-migration@sha256:...
    tested-against: sha256:...          # the serving revision, from the projection published back
    working-directory: serving          # a checkout of the serving revision
    test-command: ./gradlew test
```

| obligation | how it is run |
|---|---|
| a non-transactional changeset stands alone | `ci non-transactional`: the changesets the serving schema does not hold are the release; one marked `runInTransaction: false` must be the only one |
| reversibility | `ci reversibility`: Liquibase `update-testing-rollback` over the release |
| serving-version compatibility | `test-command`, the serving revision's own suite, against the database the new changelog has just migrated; it finds the database in `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME`, `DATABASE_USERNAME` and `DATABASE_PASSWORD` |

The proof is written only when all three hold. `ci proof` replaces the Application's entry and
keeps the others, ordered by id, so a proof that says the same thing is the same bytes.

The `ci` commands sign in with `DATABASE_USERNAME` and `DATABASE_PASSWORD`. `up` and `down`
never do.

## Working on it

```bash
mise install            # the pinned toolchain
task check              # everything CI runs; the last step needs Docker
task test               # unit tests and the coverage gate
task test:integration   # builds the image and drives it against a real Postgres
```

Liquibase and the PostgreSQL driver are pinned in the `Dockerfile`, by digest and by checksum.
