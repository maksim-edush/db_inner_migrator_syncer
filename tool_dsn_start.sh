#!/usr/bin/env bash
set -euo pipefail

# Start a local Postgres instance for migrate-hub tool DB.
NAME="${NAME:-migratehub-postgres}"
PORT="${PORT:-5432}"
USER="${USER_NAME:-migratehub}"
PASS="${PASSWORD:-migratehub}"
DB="${DB_NAME:-migratehub}"
IMAGE="${IMAGE:-postgres:16}"
VOLUME="${VOLUME:-${NAME}-data}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required but not installed" >&2
  exit 1
fi

existing=$(docker ps -a --format '{{.Names}}' | grep -w "${NAME}" || true)
if [[ -n "${existing}" ]]; then
  status=$(docker inspect -f '{{.State.Running}}' "${NAME}")
  if [[ "${status}" == "true" ]]; then
    echo "Container ${NAME} already running."
  else
    echo "Starting existing container ${NAME}..."
    docker start "${NAME}"
  fi
else
  echo "Creating and starting container ${NAME} on port ${PORT}..."
  docker run -d \
    --name "${NAME}" \
    -p "${PORT}:5432" \
    -e POSTGRES_USER="${USER}" \
    -e POSTGRES_PASSWORD="${PASS}" \
    -e POSTGRES_DB="${DB}" \
    -v "${VOLUME}:/var/lib/postgresql/data" \
    "${IMAGE}"
fi

DSN="postgres://${USER}:${PASS}@localhost:${PORT}/${DB}?sslmode=disable"
echo "Postgres ready. Use this DSN:"
echo "  ${DSN}"
echo "Export for migrate-hub:"
echo "  export MIGRATEHUB_DB_DSN=\"${DSN}\""
