# syntax=docker/dockerfile:1.28
# The platform's migration runner. An application's migration image is built FROM this one and
# adds only its changelog:
#
#   FROM ghcr.io/jorisjonkers-dev/liquibase-runner@sha256:...
#   COPY changelog/ /liquibase/changelog/
#
# Cross-compiles on the build platform, so a multi-arch build needs no emulation for the Go stage.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/runner ./cmd/runner

# squawk lints each changeset's SQL for long locks and lost data (`runner ci lint`). Its static
# binary for the target architecture, pinned by checksum: a new version is a new pair of checksums.
FROM --platform=$BUILDPLATFORM alpine:3.22 AS squawk
ARG TARGETARCH
ARG SQUAWK_VERSION=2.66.0
ARG SQUAWK_SHA256_AMD64=e403ec140fbd15d653763ed0e271a76ae512ad6f523f1b3168f134fdfe92c1dd
ARG SQUAWK_SHA256_ARM64=59e83fedf6993db755e1574285620268b799b51232204aff6cb99413ab53e9f2
RUN case "$TARGETARCH" in \
      amd64) asset=squawk-linux-musl-x64; sum="$SQUAWK_SHA256_AMD64" ;; \
      arm64) asset=squawk-linux-musl-arm64; sum="$SQUAWK_SHA256_ARM64" ;; \
      *) echo "no squawk for $TARGETARCH" >&2; exit 1 ;; \
    esac \
    && wget -q -O /squawk "https://github.com/sbdchd/squawk/releases/download/v${SQUAWK_VERSION}/${asset}" \
    && echo "${sum}  /squawk" | sha256sum -c - \
    && chmod 0755 /squawk

FROM liquibase/liquibase:5.0.4@sha256:060f96efff4811ccf03957ff3160f21ea04139b89940640f8ad54a065cdc6906
# Liquibase 5 ships no database driver. This is the one the estate's databases need, pinned by
# checksum because it is fetched at build time: a new version is a new checksum, set together.
ARG POSTGRESQL_JDBC_VERSION=42.7.13
ARG POSTGRESQL_JDBC_SHA256=6e0e4cc2d8cae902084f8a2b18728b073a6fd9d1f87c9d8bff8f298c18185b93
ADD --checksum=sha256:${POSTGRESQL_JDBC_SHA256} --chown=liquibase:liquibase \
    https://repo1.maven.org/maven2/org/postgresql/postgresql/${POSTGRESQL_JDBC_VERSION}/postgresql-${POSTGRESQL_JDBC_VERSION}.jar \
    /liquibase/lib/postgresql.jar
COPY --from=build /out/runner /usr/local/bin/runner
COPY --from=squawk /squawk /usr/local/bin/squawk

# The root changelog, relative to /liquibase/changelog. Every changeset is recorded under this
# name, so a migration image sets it once, to the name its changesets already carry, and never
# changes it.
ENV CHANGELOG_FILE=changelog.yaml
USER liquibase:liquibase
ENTRYPOINT ["/usr/local/bin/runner"]
