#!/usr/bin/env bash
# Copyright 2026 Neotera.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# End-to-end check of the Device Twins Resources Publisher (UP2DATE4SDV CID 93) on the Docker
# (Podman Compose) playground, with the thermal ECU, the tire-pressure ECU or both. Commands
# reach the playground nodes with `podman exec`. The playground repository is not modified.
# See lib/e2e-common.sh for what is checked.

set -Eeuo pipefail

# shellcheck source=lib/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/e2e-common.sh"

E2E_TARGET=docker
PROJECT=continuumx-playground

usage() {
  cat <<'EOF'
Usage: docker-e2e.sh [options]

Runs the dtpublisher end-to-end check on a running Docker playground (podman compose, started
with `make run` in the playground's docker/ directory). Builds the image with Podman, loads it
into the cloud container's containerd, deploys build/dtpublisher/, checks what a subscriber
receives, and removes the publisher again.

Docker playground options:
  --project NAME              compose project name (default: continuumx-playground); the
                              containers are NAME_cloud_1 and NAME_edge_1

EOF
  e2e_common_usage
  cat <<'EOF'

Tire ECU state on the Docker playground: the ECU keeps its pressure in
/var/lib/continuumx-virtual-tire-ecu/state.json inside the edge container (not on a volume).
It survives container restarts and is reset only when the container is recreated
(`make delete && make run` in the playground's docker/ directory).
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --project)
      [[ $# -ge 2 && -n "$2" ]] || e2e_die "$E2E_EXIT_USAGE" "--project needs a name"
      PROJECT=$2
      shift 2
      ;;
    *)
      e2e_parse_common_arg "$@"
      [[ "$E2E_ARGS_USED" -gt 0 ]] || e2e_die "$E2E_EXIT_USAGE" "unknown argument: $1 (see --help)"
      shift "$E2E_ARGS_USED"
      ;;
  esac
done

CLOUD_CTR="${PROJECT}_cloud_1"
EDGE_CTR="${PROJECT}_edge_1"

e2e_check_state_change_allowed "reset only when the edge container is recreated: make delete && make run"

e2e_cloud_exec() { podman exec "$CLOUD_CTR" bash -c "$1" </dev/null; }
e2e_cloud_exec_in() { podman exec -i "$CLOUD_CTR" bash -c "$1"; }
e2e_edge_exec() { podman exec "$EDGE_CTR" bash -c "$1" </dev/null; }

e2e_target_preconditions() {
  local unit
  local -a units=(edgecore "$E2E_MAPPER_UNIT")
  e2e_wants_thermal && units+=(continuumx-virtual-thermal-ecu)
  e2e_wants_tire && units+=(continuumx-virtual-tire-ecu)
  for unit in "${units[@]}"; do
    if ! e2e_edge_exec "systemctl is-active --quiet ${unit}"; then
      e2e_log "error: ${unit} is not active in ${EDGE_CTR}"
      if [[ "$unit" == continuumx-virtual-tire-ecu ]]; then
        e2e_log "the tire ECU is in the Docker playground from commit 79c9fc0 (2026-11-24) on; rebuild the playground images (make build) from that version or later"
      fi
      return 1
    fi
  done
}

e2e_target_trace() {
  local since="@${E2E_START_EPOCH}" unit
  for unit in continuumx-virtual-thermal-ecu continuumx-virtual-tire-ecu edgecore; do
    e2e_edge_exec "journalctl -u ${unit} --since $(e2e_quote "$since") --no-pager -o short-iso" \
      >"${E2E_TRACE_DIR}/${unit}.log" 2>&1 || true
  done
}

e2e_require_tools podman
for ctr in "$CLOUD_CTR" "$EDGE_CTR"; do
  state=$(podman inspect -f '{{.State.Status}}' "$ctr" 2>/dev/null || echo missing)
  [[ "$state" == running ]] \
    || e2e_die "$E2E_EXIT_PRECONDITION" "container ${ctr} is ${state}; start the playground (make run in its docker/ directory)"
done

e2e_main
