#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
    echo "run this script from a Determined Git checkout" >&2
    exit 1
}
cd "${repo_root}"

mode=${1:-quick}
(($# <= 1)) || {
    echo "usage: tools/fork/check.sh [quick|security|progress|pools|integration-pools]" >&2
    exit 2
}

die() {
    echo "error: $*" >&2
    exit 1
}

find_python() {
    if [[ -n ${PYTHON:-} ]]; then
        python_bin=${PYTHON}
    elif [[ -x ${repo_root}/.venv/bin/python ]]; then
        python_bin=${repo_root}/.venv/bin/python
    elif command -v python3 >/dev/null 2>&1; then
        python_bin=$(command -v python3)
    elif command -v python >/dev/null 2>&1; then
        python_bin=$(command -v python)
    else
        die "Python was not found; set PYTHON to an existing interpreter"
    fi
    command -v "${python_bin}" >/dev/null 2>&1 || die \
        "PYTHON does not name an executable: ${python_bin}"
}

run_python_tests() {
    find_python
    "${python_bin}" -c 'import pytest, responses' >/dev/null 2>&1 || die \
        "${python_bin} needs pytest and responses; set PYTHON to a prepared environment"
    printf '\n==> Python regression tests (%s)\n' "${python_bin}"
    PYTHONDONTWRITEBYTECODE=1 \
        PYTHONPATH="${repo_root}/harness${PYTHONPATH:+:${PYTHONPATH}}" \
        "${python_bin}" -m pytest -q -p no:cacheprovider "$@"
}

quick() {
    local test_file=harness/tests/checkpoints/test_checkpoint.py
    local safety_test=test_checkpoint_download_via_master_rejects_unsafe_archive
    local -a tests=("${test_file}::${safety_test}")

    [[ -f ${test_file} ]] && grep -q "^def ${safety_test}" "${test_file}" || die \
        "missing required safety regression: ${test_file}::${safety_test}"
    if [[ -f harness/tests/cli/test_resource_pool.py ]]; then
        tests+=(harness/tests/cli/test_resource_pool.py)
    fi
    run_python_tests "${tests[@]}"
}

security() {
    local -a tests=(
        harness/tests/common/test_tarfile_utils.py
        harness/tests/checkpoints/test_checkpoint.py
        harness/tests/exec/test_prep_container.py
    )
    local test_file
    for test_file in "${tests[@]}"; do
        [[ -f ${test_file} ]] || die "missing required security test file: ${test_file}"
    done
    run_python_tests "${tests[@]}"
}

progress() {
    run_python_tests harness/tests/core/test_progress.py harness/tests/core/test_metrics.py
}

find_go() {
    go_bin=${GO:-go}
    command -v "${go_bin}" >/dev/null 2>&1 || die \
        "Go was not found; set GO to an existing Go 1.22-compatible executable"
    [[ $("${go_bin}" env CGO_ENABLED) == 1 ]] || die \
        "race tests require CGO; select a CGO-enabled Go toolchain"
}

require_dynamic_pool_sources() {
    local required
    for required in master/internal/rm/agentrm/{pool_registry,dynamic_pools}.go; do
        [[ -f ${required} ]] || die \
            "this checkout has no dynamic-pool implementation (${required} is missing); use quick or security"
    done
}

pools() {
    require_dynamic_pool_sources
    find_go
    printf '\n==> Focused resource-pool race tests (%s)\n' "$("${go_bin}" version)"
    "${go_bin}" test -race ./master/internal/rm ./master/internal/rm/multirm \
        -run '^(TestLivePoolSchedulerDefaults|TestLivePoolPreservesStaticManagerPriorityFallback|TestMissingLivePoolUsesStaticPriorityFallback|TestResourcePoolSchedulerConfigRouting)$' \
        -count=1
    "${go_bin}" test -race ./master/internal/rm/agentrm \
        -run '^(TestPoolRegistry.*|TestResourcePoolSchedulerConfigUsesReadyEffectiveConfig|TestTaskContainerDefaultsKeepsDynamicEffectiveValues|TestResourceManagerForwardMessage|TestNormalizeDynamicResourcePoolConfig.*|TestValidateDynamicPoolIdempotencyKey|TestDecodeStoredDynamicResourcePool|TestDynamicPoolReadyWriteFailureDoesNotPublish)$' \
        -count=1
}

integration_pools() {
    require_dynamic_pool_sources
    find_go
    [[ -n ${DET_INTEGRATION_POSTGRES_URL:-} ]] || die \
        "set DET_INTEGRATION_POSTGRES_URL to an existing test database; this script does not start Docker"
    printf '\n==> Dynamic resource-pool PostgreSQL and restart race tests\n'
    (
        cd master
        "${go_bin}" test -race -tags=integration ./internal/rm/agentrm \
            -run '^(TestAgentRMRoutingTaskRelatedMessages|TestGetResourcePools|TestGetJobQueueStatsRequest|TestDynamicPoolPersistenceRestart|TestDynamicPoolStartupRejects.*)$' \
            -count=1
        "${go_bin}" test -race -tags=integration ./internal/db \
            -run '^TestDynamicResourcePool.*$' -count=1
    )
}

case ${mode} in
    quick) quick ;;
    security) security ;;
    progress) progress ;;
    pools) pools ;;
    integration | integration-pools) integration_pools ;;
    -h | --help | help)
        cat <<'EOF'
Usage: tools/fork/check.sh [MODE]

  quick              Required archive regression and pool CLI tests if present (default)
  security           Focused Python archive-safety regressions
  progress           Focused Python progress/metrics reporting regressions
  pools              Dynamic resource-pool Go tests with the race detector
  integration-pools  Pool persistence/restart race tests using an existing PostgreSQL database

Set PYTHON or GO to select existing toolchains. integration-pools also requires
DET_INTEGRATION_POSTGRES_URL. The script never installs dependencies or starts services.
EOF
        ;;
    *) die "unknown mode '${mode}'; use --help for available modes" ;;
esac
