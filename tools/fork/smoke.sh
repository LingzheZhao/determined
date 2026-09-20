#!/usr/bin/env bash
set -euo pipefail

compose_file=${FORK_SMOKE_COMPOSE_FILE:-tools/fork/docker-compose.smoke.yml}
master_url=${FORK_SMOKE_MASTER_URL:-http://127.0.0.1:8080}
dynamic_pool_smoke=${FORK_SMOKE_DYNAMIC_POOLS:-0}

: "${FORK_MASTER_IMAGE:?set FORK_MASTER_IMAGE to the locally built master image}"
: "${FORK_AGENT_IMAGE:?set FORK_AGENT_IMAGE to the locally built agent image}"

export DET_MASTER=${master_url}
export DET_USER=admin
export DET_PASS=fork-smoke-password

compose=(docker compose -f "${compose_file}")

cleanup() {
  status=$?
  if (( status != 0 )); then
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

health_ready() {
  curl --fail --silent --show-error "${master_url}/health" >/dev/null
}

agent_count_at_least() {
  expected=$1
  det agent list --json 2>/dev/null | jq -e --argjson expected "${expected}" \
    'length >= $expected' >/dev/null
}

"${compose[@]}" up --detach postgres determined-master
wait_for "master health" health_ready

# This exercises password authentication and proves the built wheel's CLI can use the image API.
det user whoami >/dev/null

"${compose[@]}" up --detach determined-agent
wait_for "static agent join" agent_count_at_least 1

static_output=$(det command run \
  --config environment.image=ubuntu:22.04 \
  --config resources.slots=1 \
  sh -c 'printf "fork-static-task-ok\n"')
grep -q 'fork-static-task-ok' <<<"${static_output}"
static_id=$(det command list --json | jq -er '.[0].id')
det command describe "${static_id}" --json >/dev/null

if [[ "${dynamic_pool_smoke}" != 1 ]]; then
  echo "CPU image smoke passed; dynamic-pool extension was not requested."
  exit 0
fi

login_json=$(curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  --data '{"username":"admin","password":"fork-smoke-password","isHashed":false}' \
  "${master_url}/api/v1/auth/login")
token=$(jq -er '.token' <<<"${login_json}")
auth_header="Authorization: Bearer ${token}"
dynamic_body='{"idempotency_key":"fork-distribution-smoke","config":{"pool_name":"fork-smoke-dynamic"}}'

create_json=$(curl --fail --silent --show-error \
  -H "${auth_header}" -H 'Content-Type: application/json' \
  --data "${dynamic_body}" \
  "${master_url}/api/v1/resource-pools/dynamic")
jq -e '.pool_name == "fork-smoke-dynamic" and .state == "Ready"' \
  <<<"${create_json}" >/dev/null

# A completed task from the original pool must remain visible after pool creation.
det command describe "${static_id}" --json >/dev/null
curl --fail --silent --show-error -H "${auth_header}" \
  "${master_url}/api/v1/resource-pools/dynamic" \
  | jq -e '.resource_pools[] | select(.pool_name == "fork-smoke-dynamic" and .state == "Ready")' \
    >/dev/null

"${compose[@]}" --profile dynamic-pool up --detach dynamic-agent
wait_for "dynamic-pool agent join" agent_count_at_least 2

# Restart recovery must reconstruct the durable pool before its agent reconnects.
"${compose[@]}" restart determined-master
wait_for "master health after restart" health_ready
wait_for "agents after master restart" agent_count_at_least 2
login_json=$(curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  --data '{"username":"admin","password":"fork-smoke-password","isHashed":false}' \
  "${master_url}/api/v1/auth/login")
token=$(jq -er '.token' <<<"${login_json}")
auth_header="Authorization: Bearer ${token}"
curl --fail --silent --show-error -H "${auth_header}" \
  "${master_url}/api/v1/resource-pools/dynamic" \
  | jq -e '.resource_pools[] | select(.pool_name == "fork-smoke-dynamic" and .state == "Ready")' \
    >/dev/null

dynamic_output=$(det command run \
  --config environment.image=ubuntu:22.04 \
  --config resources.resource_pool=fork-smoke-dynamic \
  --config resources.slots=1 \
  sh -c 'printf "fork-dynamic-task-ok\n"')
grep -q 'fork-dynamic-task-ok' <<<"${dynamic_output}"
echo "CPU image and dynamic-pool recovery smoke passed."
