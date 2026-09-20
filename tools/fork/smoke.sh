#!/usr/bin/env bash
set -euo pipefail

compose_file=${FORK_SMOKE_COMPOSE_FILE:-tools/fork/docker-compose.smoke.yml}
master_url=${FORK_SMOKE_MASTER_URL:-http://127.0.0.1:8080}
dynamic_pool_smoke=${FORK_SMOKE_DYNAMIC_POOLS:-0}

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
    "${compose[@]}" --profile dynamic-pool down --volumes --remove-orphans || true
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

runtime_container_id_for() {
    local determined_container_id=$1
    local -a runtime_container_ids
    mapfile -t runtime_container_ids < <(
        docker ps --no-trunc \
            --filter "label=ai.determined.container.id=${determined_container_id}" \
            --format '{{.ID}}'
    )
    if ((${#runtime_container_ids[@]} != 1)); then
        echo "expected one running Docker container for Determined container ${determined_container_id}" >&2
        return 1
    fi
    printf '%s\n' "${runtime_container_ids[0]}"
}

command_state_is() {
    command_id=$1
    expected_state=$2
    det command describe "${command_id}" --json 2>/dev/null \
        | jq -e --arg expected_state "${expected_state}" \
            '.state == $expected_state' >/dev/null
}

command_logs_contain() {
    command_id=$1
    expected_text=$2
    det command logs "${command_id}" 2>/dev/null | grep -q "${expected_text}"
}

expect_http_status() {
    expected_status=$1
    shift
    actual_status=$(curl --silent --show-error --output /dev/null \
        --write-out '%{http_code}' "$@")
    if [[ ${actual_status} != "${expected_status}" ]]; then
        echo "expected HTTP ${expected_status}, received ${actual_status}" >&2
        return 1
    fi
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

if [[ ${dynamic_pool_smoke} != 1 ]]; then
    echo "CPU image smoke passed; dynamic-pool extension was not requested."
    exit 0
fi

dynamic_url="${master_url}/api/v1/resource-pools/dynamic"
dynamic_body='{"idempotency_key":"fork-distribution-smoke","config":{"pool_name":"fork-smoke-dynamic"}}'
phase "Verify dynamic-pool authentication and authorization"
expect_http_status 401 "${dynamic_url}"
expect_http_status 401 \
    -H 'Content-Type: application/json' --data "${dynamic_body}" "${dynamic_url}"

det user create fork-smoke-user --password 'ForkSmokeUser123!' >/dev/null
non_admin_login=$(curl --fail --silent --show-error \
    -H 'Content-Type: application/json' \
    --data '{"username":"fork-smoke-user","password":"ForkSmokeUser123!","isHashed":false}' \
    "${master_url}/api/v1/auth/login")
non_admin_token=$(jq -er '.token' <<<"${non_admin_login}")
non_admin_header="Authorization: Bearer ${non_admin_token}"
expect_http_status 403 -H "${non_admin_header}" "${dynamic_url}"
expect_http_status 403 \
    -H "${non_admin_header}" -H 'Content-Type: application/json' \
    --data "${dynamic_body}" "${dynamic_url}"
expect_http_status 403 \
    -H "${non_admin_header}" -H 'Content-Type: application/json' --data '{}' \
    "${dynamic_url}/fork-smoke-dynamic/retry"

login_json=$(curl --fail --silent --show-error \
    -H 'Content-Type: application/json' \
    --data '{"username":"admin","password":"fork-smoke-password","isHashed":false}' \
    "${master_url}/api/v1/auth/login")
token=$(jq -er '.token' <<<"${login_json}")
auth_header="Authorization: Bearer ${token}"

# Keep real work active in the original pool while the new pool is published.
phase "Create a dynamic pool while an original-pool command stays active"
active_id=$(det command run --detach \
    --config "environment.image=${FORK_TASK_IMAGE}" \
    --config resources.slots=1 \
    sh -c 'printf "fork-before-pool-create\n"; sleep 30; printf "fork-after-pool-create\n"')
wait_for "original-pool task running" command_state_is "${active_id}" RUNNING
wait_for "original-pool task initial progress" command_logs_contain \
    "${active_id}" fork-before-pool-create
active_allocation_before=$(det task list --json \
    | jq -er --arg task_id "${active_id}" \
        'to_entries[] | select(.value.taskId == $task_id)')
active_allocation_id=$(jq -er '.key' <<<"${active_allocation_before}")
active_determined_container_id=$(jq -er '.value.resources[0].containerId' \
    <<<"${active_allocation_before}")
active_runtime_container_id=$(runtime_container_id_for "${active_determined_container_id}")
[[ $(docker inspect --format '{{.State.Running}}' "${active_runtime_container_id}") == true ]]

create_json=$(curl --fail --silent --show-error \
    -H "${auth_header}" -H 'Content-Type: application/json' \
    --data "${dynamic_body}" \
    "${dynamic_url}")
jq -e '.pool_name == "fork-smoke-dynamic" and .state == "Ready"' \
    <<<"${create_json}" >/dev/null

# An exact replay must return the existing operation without duplicating the pool.
expect_http_status 200 \
    -H "${auth_header}" -H 'Content-Type: application/json' \
    --data "${dynamic_body}" "${dynamic_url}"
dynamic_list_json=$(det resource-pool list-dynamic --json)
jq -e \
    '[.resource_pools[] | select(.pool_name == "fork-smoke-dynamic" and .state == "Ready")] |
    length == 1' <<<"${dynamic_list_json}" >/dev/null

# Pool creation must not replace, move, or interrupt the active original-pool allocation.
active_after=$(det command describe "${active_id}" --json)
active_allocation_after=$(det task list --json \
    | jq -er --arg task_id "${active_id}" \
        'to_entries[] | select(.value.taskId == $task_id)')
jq -e \
    --arg id "${active_id}" \
    '.id == $id and .resourcePool == "default" and .state == "RUNNING"' \
    <<<"${active_after}" >/dev/null
jq -e \
    --arg task_id "${active_id}" \
    --arg allocation_id "${active_allocation_id}" \
    --arg container_id "${active_determined_container_id}" \
    '.key == $allocation_id and .value.taskId == $task_id and
    .value.resourcePool == "default" and .value.resources[0].containerId == $container_id' \
    <<<"${active_allocation_after}" >/dev/null
active_runtime_container_after=$(runtime_container_id_for "${active_determined_container_id}")
[[ ${active_runtime_container_after} == "${active_runtime_container_id}" ]]
[[ $(docker inspect --format '{{.State.Running}}' "${active_runtime_container_after}") == true ]]
curl --fail --silent --show-error -H "${auth_header}" \
    "${dynamic_url}" \
    | jq -e '.resource_pools[] | select(.pool_name == "fork-smoke-dynamic" and .state == "Ready")' \
        >/dev/null
wait_for "original-pool task completion" command_state_is "${active_id}" TERMINATED
wait_for "original-pool task continued progress" command_logs_contain \
    "${active_id}" fork-after-pool-create

phase "Join the dynamic-pool CPU agent and run a command"
"${compose[@]}" --profile dynamic-pool up --detach dynamic-agent
wait_for "enabled dynamic agent in fork-smoke-dynamic pool" \
    agent_ready_in_pool fork-dynamic-agent fork-smoke-dynamic

before_restart_output=$(det command run \
    --config "environment.image=${FORK_TASK_IMAGE}" \
    --config resources.resource_pool=fork-smoke-dynamic \
    --config resources.slots=1 \
    sh -c 'printf "fork-dynamic-before-restart-ok\n"')
grep -q 'fork-dynamic-before-restart-ok' <<<"${before_restart_output}"

# Restart recovery must reconstruct the durable pool before its agent reconnects.
phase "Restart master and verify recovered dynamic-pool work"
"${compose[@]}" restart determined-master
wait_for "master health after restart" health_ready
wait_for "enabled static agent after master restart" \
    agent_ready_in_pool fork-static-agent default
wait_for "enabled dynamic agent after master restart" \
    agent_ready_in_pool fork-dynamic-agent fork-smoke-dynamic
login_json=$(curl --fail --silent --show-error \
    -H 'Content-Type: application/json' \
    --data '{"username":"admin","password":"fork-smoke-password","isHashed":false}' \
    "${master_url}/api/v1/auth/login")
token=$(jq -er '.token' <<<"${login_json}")
auth_header="Authorization: Bearer ${token}"
curl --fail --silent --show-error -H "${auth_header}" \
    "${dynamic_url}" \
    | jq -e '.resource_pools[] | select(.pool_name == "fork-smoke-dynamic" and .state == "Ready")' \
        >/dev/null

dynamic_output=$(det command run \
    --config "environment.image=${FORK_TASK_IMAGE}" \
    --config resources.resource_pool=fork-smoke-dynamic \
    --config resources.slots=1 \
    sh -c 'printf "fork-dynamic-task-ok\n"')
grep -q 'fork-dynamic-task-ok' <<<"${dynamic_output}"
echo "CPU image and dynamic-pool recovery smoke passed."
