#!/usr/bin/env bash
set -euo pipefail

compose_file=${FORK_SMOKE_COMPOSE_FILE:-tools/fork/docker-compose.smoke.yml}
master_url=${FORK_SMOKE_MASTER_URL:-http://127.0.0.1:8080}

: "${FORK_MASTER_IMAGE:?set FORK_MASTER_IMAGE to the locally built master image}"
: "${FORK_AGENT_IMAGE:?set FORK_AGENT_IMAGE to the locally built agent image}"
: "${FORK_TASK_IMAGE:?set FORK_TASK_IMAGE to the locally built CPU task image}"

export DET_MASTER=${master_url}
export DET_USER=admin
export DET_PASS=fork-smoke-password

compose=(docker compose -f "${compose_file}")

cleanup() {
    status=$?
    if ((status != 0)); then
        "${compose[@]}" ps || true
        "${compose[@]}" logs --no-color || true
    fi
    "${compose[@]}" down --volumes --remove-orphans || true
    exit "${status}"
}
trap cleanup EXIT

wait_for() {
    description=$1
    shift
    for _ in $(seq 1 60); do
        if "$@"; then
            return 0
        fi
        sleep 2
    done
    echo "timed out waiting for ${description}" >&2
    return 1
}

phase() {
    printf '\n==> %s\n' "$1"
}

health_ready() {
    curl --fail --silent --show-error \
        -H 'Content-Type: application/json' \
        --data '{"username":"admin","password":"fork-smoke-password","isHashed":false}' \
        "${master_url}/api/v1/auth/login" >/dev/null
}

agent_ready_in_pool() {
    agent_id=$1
    pool_name=$2
    det agent list --json 2>/dev/null \
        | jq -e --arg agent_id "${agent_id}" --arg pool_name "${pool_name}" \
            'any(.[]; .id == $agent_id and .enabled == true and .resource_pools == $pool_name)' \
            >/dev/null
}

phase "Start PostgreSQL and master; verify authenticated readiness"
"${compose[@]}" up --detach postgres determined-master
wait_for "master health" health_ready

# This exercises password authentication and proves the built wheel's CLI can use the image API.
det user whoami >/dev/null

phase "Join the static CPU agent and run a command"
"${compose[@]}" up --detach determined-agent
wait_for "enabled static agent in default pool" \
    agent_ready_in_pool fork-static-agent default

static_output=$(det command run \
    --config "environment.image=${FORK_TASK_IMAGE}" \
    --config resources.slots=1 \
    sh -c 'printf "fork-static-task-ok\n"')
grep -q 'fork-static-task-ok' <<<"${static_output}"

static_id=$(det command list --json | jq -er '.[0].id')
det command describe "${static_id}" --json >/dev/null
echo "CPU image smoke passed."
