#!/usr/bin/env bash
# Proves a migration image's changelog against a throwaway Postgres, then writes the proof.
#   1. the serving revision's migration image brings the database to what serves today
#   2. a non-transactional changeset stands alone in the release
#   3. every changeset of the release applies, rolls back and applies again
#   4. the serving revision's own test suite passes against the migrated database
# A first release, with nothing serving, skips 1 and 4 and records no testedAgainst.
set -euo pipefail

: "${IMAGE:?}" "${APPLICATION:?}" "${DATABASE_NAME:?}" "${PROOF_FILE:?}"
SERVING_IMAGE="${SERVING_IMAGE:-}"
TESTED_AGAINST="${TESTED_AGAINST:-}"
TEST_COMMAND="${TEST_COMMAND:-}"
WORKING_DIRECTORY="${WORKING_DIRECTORY:-.}"
POSTGRES_IMAGE="${POSTGRES_IMAGE:-postgres:17-alpine}"

fail() {
  echo "migration-proof: $*" >&2
  exit 1
}

if [ -n "$SERVING_IMAGE" ]; then
  [[ "$TESTED_AGAINST" =~ ^sha256:[a-f0-9]{64}$ ]] || fail "tested-against must be the serving revision, sha256:..., when serving-image is set"
  [ -n "$TEST_COMMAND" ] || fail "test-command must run the serving revision's test suite when serving-image is set"
else
  [ -z "$TESTED_AGAINST" ] || fail "tested-against names a serving revision, but serving-image is empty"
fi

name="migration-proof-$$-${RANDOM}"
password="$(head -c 18 /dev/urandom | base64 | tr -d '+/=')"
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker network rm "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create "$name" >/dev/null
docker run -d --name "$name" --network "$name" -p 127.0.0.1::5432 \
  -e POSTGRES_PASSWORD="$password" -e POSTGRES_DB="$DATABASE_NAME" "$POSTGRES_IMAGE" >/dev/null
# The image starts Postgres twice, so only a query over TCP proves it is the server that stays.
for _ in $(seq 1 120); do
  if docker exec "$name" psql -h 127.0.0.1 -U postgres -d "$DATABASE_NAME" -c 'select 1' >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done
[ "${ready:-}" = 1 ] || fail "postgres did not start"
port="$(docker port "$name" 5432/tcp | head -n 1 | sed 's/.*://')"

runner() {
  local image="$1"
  shift
  docker run --rm -i --network "$name" \
    -e DATABASE_HOST="$name" -e DATABASE_PORT=5432 -e DATABASE_NAME="$DATABASE_NAME" \
    -e DATABASE_USERNAME=postgres -e DATABASE_PASSWORD="$password" \
    "$image" "$@"
}

if [ -n "$SERVING_IMAGE" ]; then
  echo "::group::The serving revision's schema"
  serving_tag="${TESTED_AGAINST#sha256:}"
  runner "$SERVING_IMAGE" ci up "${serving_tag:0:12}" </dev/null
  echo "::endgroup::"
fi

echo "::group::A non-transactional changeset stands alone"
non_transactional="$(runner "$IMAGE" ci non-transactional </dev/null)"
case "$non_transactional" in
  true | false) ;;
  *) fail "the image answered the non-transactional check with something that is neither true nor false" ;;
esac
echo "nonTransactional: $non_transactional"
echo "::endgroup::"

echo "::group::Reversibility"
runner "$IMAGE" ci reversibility </dev/null
echo "::endgroup::"

if [ -n "$SERVING_IMAGE" ]; then
  echo "::group::Serving-version compatibility"
  (
    cd "$WORKING_DIRECTORY"
    DATABASE_HOST=127.0.0.1 DATABASE_PORT="$port" DATABASE_NAME="$DATABASE_NAME" \
      DATABASE_USERNAME=postgres DATABASE_PASSWORD="$password" \
      bash -euo pipefail -c "$TEST_COMMAND"
  )
  echo "::endgroup::"
fi

proof_arguments=(--application "$APPLICATION" "--non-transactional=$non_transactional")
if [ -n "$TESTED_AGAINST" ]; then
  proof_arguments+=(--tested-against "$TESTED_AGAINST")
fi
existing="$(mktemp)"
if [ -f "$PROOF_FILE" ]; then
  cp "$PROOF_FILE" "$existing"
fi
mkdir -p "$(dirname "$PROOF_FILE")"
runner "$IMAGE" ci proof "${proof_arguments[@]}" <"$existing" >"$PROOF_FILE.new"
mv "$PROOF_FILE.new" "$PROOF_FILE"
rm -f "$existing"

echo "wrote $PROOF_FILE"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "non-transactional=$non_transactional" >>"$GITHUB_OUTPUT"
fi
