# syntax=docker/dockerfile:1.28
# The platform's migration runner. An application's migration image is built FROM this one and
# adds only its changelog:
#
#   FROM ghcr.io/jorisjonkers-dev/liquibase-runner@sha256:...
#   COPY changelog/ /liquibase/changelog/
#
# Cross-compiles on the build platform, so a multi-arch build needs no emulation for the Go stage.

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=secret,id=packages-netrc,target=/run/secrets/packages-netrc,required=false \
    NETRC=/run/secrets/packages-netrc go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=secret,id=packages-netrc,target=/run/secrets/packages-netrc,required=false \
    NETRC=/run/secrets/packages-netrc CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/runner ./cmd/runner

# squawk lints each changeset's SQL for long locks and lost data (`runner ci lint`). Its static
# binary for the target architecture, pinned by checksum: a new version is a new pair of checksums.
FROM --platform=$BUILDPLATFORM alpine:3.24 AS squawk
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

# The JDBC driver for the estate's databases, fetched through the package proxy when the build
# has the packages-netrc secret, and straight from Maven Central otherwise (a plain local build).
# Liquibase 5 ships no driver. It is pinned by checksum either way: a new version is a new
# checksum, set together.
FROM --platform=$BUILDPLATFORM alpine:3.24 AS jdbc
ARG POSTGRESQL_JDBC_VERSION=42.7.13
ARG POSTGRESQL_JDBC_SHA256=6e0e4cc2d8cae902084f8a2b18728b073a6fd9d1f87c9d8bff8f298c18185b93
# apk installs through the package proxy when the build has its packages-netrc secret (CI); without it, from Alpine's own mirrors.
RUN --mount=type=secret,id=packages-netrc,target=/root/.netrc,required=false \
    set -e; \
    repos=""; \
    if [ -s /root/.netrc ]; then \
      v=$(cut -d. -f1,2 /etc/alpine-release); \
      repos="--repositories-file /dev/null --repository https://packages.jorisjonkers.dev/apk/alpine/v$v/main --repository https://packages.jorisjonkers.dev/apk/alpine/v$v/community"; \
    fi; \
    apk add --no-cache $repos curl
RUN --mount=type=secret,id=packages-netrc,target=/run/secrets/packages-netrc,required=false \
    set -eu; \
    path="org/postgresql/postgresql/${POSTGRESQL_JDBC_VERSION}/postgresql-${POSTGRESQL_JDBC_VERSION}.jar"; \
    if [ -s /run/secrets/packages-netrc ]; then \
      curl -fsSL --retry 3 --netrc-file /run/secrets/packages-netrc -o /postgresql.jar "https://packages.jorisjonkers.dev/maven/${path}"; \
    else \
      curl -fsSL --retry 3 -o /postgresql.jar "https://repo1.maven.org/maven2/${path}"; \
    fi; \
    echo "${POSTGRESQL_JDBC_SHA256}  /postgresql.jar" | sha256sum -c -

FROM liquibase/liquibase:5.0.4@sha256:060f96efff4811ccf03957ff3160f21ea04139b89940640f8ad54a065cdc6906
COPY --from=jdbc --chown=liquibase:liquibase /postgresql.jar /liquibase/lib/postgresql.jar
COPY --from=build /out/runner /usr/local/bin/runner
COPY --from=squawk /squawk /usr/local/bin/squawk

# The root changelog, relative to /liquibase/changelog. Every changeset is recorded under this
# name, so a migration image sets it once, to the name its changesets already carry, and never
# changes it.
ENV CHANGELOG_FILE=changelog.yaml
USER liquibase:liquibase
ENTRYPOINT ["/usr/local/bin/runner"]
