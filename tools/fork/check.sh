#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
    echo "run this script from a Determined Git checkout" >&2
    exit 1
}
cd "${repo_root}"

mode=${1:-quick}
(($# <= 1)) || {
    echo "usage: tools/fork/check.sh [quick|security]" >&2
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

case ${mode} in
    quick) quick ;;
    security) security ;;
    -h | --help | help)
        cat <<'EOF'
Usage: tools/fork/check.sh [MODE]

  quick     Required archive-safety regression (default)
  security  Focused Python archive-safety regressions

Set PYTHON to select an existing interpreter with pytest and responses.
The script does not install packages or start services.
EOF
        ;;
    *) die "unknown mode '${mode}'; use --help for available modes" ;;
esac
