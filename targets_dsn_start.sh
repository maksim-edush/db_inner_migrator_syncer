#!/usr/bin/env bash
set -euo pipefail

# Start two Postgres instances for target DBs.
IMAGE="${IMAGE:-postgres:16}"
USER="${USER_NAME:-migratehub}"
PASS="${PASSWORD:-migratehub}"
DB_PREFIX="${DB_PREFIX:-migratehub_target}"
NAME_PREFIX="${NAME_PREFIX:-migratehub-target}"

PORTS=(40011 40012)

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required but not installed" >&2
  exit 1
fi

start_instance() {
  local idx="$1"
  local port="$2"
  local name="${NAME_PREFIX}-${idx}"
  local db="${DB_PREFIX}_${idx}"
  local volume="${name}-data"

  existing=$(docker ps -a --format '{{.Names}}' | grep -w "${name}" || true)
  if [[ -n "${existing}" ]]; then
    status=$(docker inspect -f '{{.State.Running}}' "${name}")
    if [[ "${status}" == "true" ]]; then
      echo "Container ${name} already running."
    else
      echo "Starting existing container ${name}..."
      docker start "${name}"
    fi
  else
    echo "Creating and starting container ${name} on port ${port}..."
    docker run -d \
      --name "${name}" \
      -p "${port}:5432" \
      -e POSTGRES_USER="${USER}" \
      -e POSTGRES_PASSWORD="${PASS}" \
      -e POSTGRES_DB="${db}" \
      -v "${volume}:/var/lib/postgresql/data" \
      "${IMAGE}"
  fi

  echo "DSN for ${name}:"
  echo "  postgres://${USER}:${PASS}@localhost:${port}/${db}?sslmode=disable"
}

start_instance 1 "${PORTS[0]}"
start_instance 2 "${PORTS[1]}"
