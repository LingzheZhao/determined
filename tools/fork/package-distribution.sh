#!/usr/bin/env bash
set -euo pipefail

: "${FORK_OUTPUT_DIR:?set FORK_OUTPUT_DIR}"
: "${FORK_STAGE_DIR:?set FORK_STAGE_DIR}"
: "${FORK_VERSION:?set FORK_VERSION}"
: "${FORK_COMMIT:?set FORK_COMMIT}"
: "${FORK_ARCH:?set FORK_ARCH}"
: "${FORK_MASTER_IMAGE:?set FORK_MASTER_IMAGE}"
: "${FORK_AGENT_IMAGE:?set FORK_AGENT_IMAGE}"
: "${FORK_BASE_IMAGE:?set FORK_BASE_IMAGE}"

source_date_epoch=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct "${FORK_COMMIT}")}
output_dir=${FORK_OUTPUT_DIR}
stage_dir=${FORK_STAGE_DIR}
artifact_prefix="determined-fork-${FORK_VERSION}-${FORK_ARCH}"

for path in \
  "${stage_dir}/bin/determined-master" \
  "${stage_dir}/bin/determined-agent" \
  "${stage_dir}/bin/determined-gotmpl" \
  "${stage_dir}/master/webui/react" \
  "${stage_dir}/master/webui/docs" \
  "${stage_dir}/master/wheels"; do
  if [[ ! -e "${path}" ]]; then
    echo "missing staged distribution input: ${path}" >&2
    exit 1
  fi
done

mkdir -p "${output_dir}"
chmod 0755 "${stage_dir}/bin/determined-master" \
  "${stage_dir}/bin/determined-agent" \
  "${stage_dir}/bin/determined-gotmpl"

tar_reproducible() {
  tar --sort=name \
    --mtime="@${source_date_epoch}" \
    --owner=0 --group=0 --numeric-owner \
    "$@"
}

tar_reproducible -C "${stage_dir}" -cf - bin \
  | gzip -n >"${output_dir}/${artifact_prefix}-binaries.tar.gz"
tar_reproducible -C "${stage_dir}/master/webui" -cf - react \
  | gzip -n >"${output_dir}/${artifact_prefix}-webui.tar.gz"
tar_reproducible -C "${stage_dir}/master/webui" -cf - docs \
  | gzip -n >"${output_dir}/${artifact_prefix}-docs.tar.gz"
cp "${stage_dir}"/master/wheels/*.whl "${output_dir}/"
cp docs/maintenance/distribution.md "${output_dir}/DEPLOYMENT.md"

docker save "${FORK_MASTER_IMAGE}" | gzip -n \
  >"${output_dir}/${artifact_prefix}-master-image.tar.gz"
docker save "${FORK_AGENT_IMAGE}" | gzip -n \
  >"${output_dir}/${artifact_prefix}-agent-image.tar.gz"

master_image_id=$(docker image inspect --format '{{.Id}}' "${FORK_MASTER_IMAGE}")
agent_image_id=$(docker image inspect --format '{{.Id}}' "${FORK_AGENT_IMAGE}")
mapfile -t payloads < <(find "${output_dir}" -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort)
payload_json=$(printf '%s\n' "${payloads[@]}" | jq -R . | jq -s .)

jq -n \
  --arg version "${FORK_VERSION}" \
  --arg commit "${FORK_COMMIT}" \
  --arg arch "${FORK_ARCH}" \
  --arg source_date_epoch "${source_date_epoch}" \
  --arg go "$(go version)" \
  --arg node "$(node --version)" \
  --arg npm "$(npm --version)" \
  --arg python "$(python --version 2>&1)" \
  --arg helm "$(helm version --short)" \
  --arg protoc "$(protoc --version)" \
  --arg base_image "${FORK_BASE_IMAGE}" \
  --arg go_sum_sha256 "$(sha256sum go.sum | cut -d' ' -f1)" \
  --arg npm_lock_sha256 "$(sha256sum webui/react/package-lock.json | cut -d' ' -f1)" \
  --arg docs_requirements_sha256 "$(sha256sum docs/requirements.txt | cut -d' ' -f1)" \
  --arg harness_pyproject_sha256 "$(sha256sum harness/pyproject.toml | cut -d' ' -f1)" \
  --arg master_image "${FORK_MASTER_IMAGE}" \
  --arg master_image_id "${master_image_id}" \
  --arg agent_image "${FORK_AGENT_IMAGE}" \
  --arg agent_image_id "${agent_image_id}" \
  --argjson payloads "${payload_json}" \
  '{
    schema_version: 1,
    version: $version,
    source: {commit: $commit, source_date_epoch: ($source_date_epoch | tonumber)},
    platform: {os: "linux", architecture: $arch},
    toolchain: {
      go: $go, node: $node, npm: $npm, python: $python, helm: $helm, protoc: $protoc
    },
    build_inputs: {
      base_image: $base_image,
      dependency_files: {
        "go.sum": $go_sum_sha256,
        "webui/react/package-lock.json": $npm_lock_sha256,
        "docs/requirements.txt": $docs_requirements_sha256,
        "harness/pyproject.toml": $harness_pyproject_sha256
      }
    },
    images: {
      master: {name: $master_image, id: $master_image_id},
      agent: {name: $agent_image, id: $agent_image_id}
    },
    payloads: $payloads,
    publication: "candidate artifact only; no registry or package index publication performed"
  }' >"${output_dir}/MANIFEST.json"

(cd "${output_dir}" && find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%f\n' \
  | LC_ALL=C sort | xargs sha256sum >SHA256SUMS)
