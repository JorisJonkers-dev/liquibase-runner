# syntax=docker/dockerfile:1.10
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

FROM liquibase/liquibase:5.0.4@sha256:060f96efff4811ccf03957ff3160f21ea04139b89940640f8ad54a065cdc6906
# Liquibase 5 ships no database driver. This is the one the estate's databases need, pinned by
# checksum because it is fetched at build time: a new version is a new checksum, set together.
ARG POSTGRESQL_JDBC_VERSION=42.7.13
ARG POSTGRESQL_JDBC_SHA256=6e0e4cc2d8cae902084f8a2b18728b073a6fd9d1f87c9d8bff8f298c18185b93
ADD --checksum=sha256:${POSTGRESQL_JDBC_SHA256} --chown=liquibase:liquibase \
    https://repo1.maven.org/maven2/org/postgresql/postgresql/${POSTGRESQL_JDBC_VERSION}/postgresql-${POSTGRESQL_JDBC_VERSION}.jar \
    /liquibase/lib/postgresql.jar
COPY --from=build /out/runner /usr/local/bin/runner

# The root changelog, relative to /liquibase/changelog. Every changeset is recorded under this
# name, so a migration image sets it once, to the name its changesets already carry, and never
# changes it.
ENV CHANGELOG_FILE=changelog.yaml
USER liquibase:liquibase
ENTRYPOINT ["/usr/local/bin/runner"]
