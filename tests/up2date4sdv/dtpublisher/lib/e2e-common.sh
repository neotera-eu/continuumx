# shellcheck shell=bash
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
# End-to-end check of the Device Twins Resources Publisher (UP2DATE4SDV CID 93) on a playground.
# Shared by docker-e2e.sh (Docker playground) and vbox-e2e.sh (VirtualBox playground); sourced,
# not run.
#
# The calling script defines how commands reach the two nodes. Each function runs one shell
# command string as root on that node:
#   e2e_cloud_exec CMD      stdin is /dev/null
#   e2e_cloud_exec_in CMD   stdin is passed through
#   e2e_edge_exec CMD       stdin is /dev/null
#   e2e_target_preconditions   target-specific checks; returns non-zero if not met
#   e2e_target_trace           target-specific trace collection (may be empty)
# and then calls e2e_main. Everything else is here.
#
# What the check does, in order: build the dtpublisher image and side-load it into the cloud
# node's containerd; deploy build/dtpublisher/ (TLS with a test certificate, query opt-in on);
# subscribe with `dtpublisher ctl watch` on the cloud node; drive the selected ECU scenarios
# through the Device desired values; check what the subscription received; optionally stop and
# start edgecore; compare `ctl list`/`ctl get` with the stream; collect a trace; clean up.
#
# The publisher only publishes. The scenarios change desired values the way an operator would;
# nothing here decides anything on behalf of an orchestrator.

# ---- settings (the calling script may override them before e2e_main) --------------------------

: "${E2E_TARGET:=unknown}"
: "${E2E_NAMESPACE:=continuumx}"
: "${E2E_DEVICE_NAMESPACE:=default}"
: "${E2E_CLOUD_NODE:=}"            # empty: the single control-plane node, detected
: "${E2E_EDGE_NODE:=u2deb-edge}"
: "${E2E_REMOTE_DIR:=/tmp/cx-dtp-e2e}"
: "${E2E_THERMAL_DEVICE:=automotive-thermal-ecu-fw-001}"
: "${E2E_TIRE_DEVICE:=automotive-tire-pressure-ecu-fw-001}"
: "${E2E_MAPPER_UNIT:=continuumx-virtual-ethernet-mapper}"

# Timeouts in seconds.
: "${E2E_TIMEOUT_ROLLOUT:=180}"
: "${E2E_TIMEOUT_SYNC:=60}"
: "${E2E_TIMEOUT_DESIRED:=60}"
: "${E2E_TIMEOUT_REPORTED:=180}"
: "${E2E_TIMEOUT_TELEMETRY:=90}"
: "${E2E_TIMEOUT_NODE:=300}"
: "${E2E_TIMEOUT_RESUME:=120}"

readonly E2E_EXIT_FAIL=1
readonly E2E_EXIT_USAGE=2
readonly E2E_EXIT_PRECONDITION=3

E2E_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
E2E_REPO_ROOT="$(cd "${E2E_LIB_DIR}/../../../.." && pwd -P)"

# Options shared by both scripts (set by e2e_parse_common_arg).
E2E_ECU=both
E2E_ALLOW_ECU_STATE_CHANGE=0
E2E_SKIP_BUILD=0
E2E_SKIP_NODE_RESTART=0
E2E_KEEP=0
E2E_TRACE_DIR=
E2E_PLAYGROUND=

# Run state.
E2E_RESULTS=()
E2E_RESTORE=()
E2E_WATCH_PID=
E2E_EDGECORE_STOPPED=0
E2E_DEPLOYED=0
E2E_CLOUD_HOSTNAME=
E2E_IMAGE=
E2E_SERVER=
E2E_EVENTS=
E2E_START_EPOCH=
E2E_WORK_DIR=

# ---- logging and results ----------------------------------------------------------------------

e2e_log() {
  local line
  line="[$(date -u +%H:%M:%S)] $*"
  printf '%s\n' "$line" >&2
  if [[ -n "${E2E_TRACE_DIR}" && -d "${E2E_TRACE_DIR}" ]]; then
    printf '%s\n' "$line" >>"${E2E_TRACE_DIR}/steps.log"
  fi
}

e2e_pass() { E2E_RESULTS+=("PASS  $1"); e2e_log "PASS  $1"; }
e2e_fail() { E2E_RESULTS+=("FAIL  $1${2:+ ($2)}"); e2e_log "FAIL  $1${2:+ ($2)}"; }
e2e_warn() { E2E_RESULTS+=("WARN  $1${2:+ ($2)}"); e2e_log "WARN  $1${2:+ ($2)}"; }

# e2e_die CODE MESSAGE: stop the run with exit code CODE (clean-up runs from the EXIT trap).
e2e_die() {
  local code=$1
  shift
  e2e_log "error: $*"
  exit "$code"
}

e2e_failed() {
  local r
  for r in "${E2E_RESULTS[@]}"; do
    [[ "$r" == FAIL* ]] && return 0
  done
  return 1
}

# ---- option parsing ---------------------------------------------------------------------------

e2e_common_usage() {
  cat <<'EOF'
Common options:
  --ecu thermal|tire|both     ECU scenarios to run (default: both)
  --allow-ecu-state-change    allow the tire scenario: the simulated leak lowers the ECU's
                              tire pressure and the ECU keeps that state (see below)
  --playground DIR            playground checkout; its commit is recorded in the trace
  --cloud-node NAME           Kubernetes name of the cloud node (default: the single node with
                              the control-plane role)
  --edge-node NAME            Kubernetes name of the edge node (default: u2deb-edge)
  --skip-build                reuse the image built earlier for the same source commit
  --skip-node-restart         do not stop and start edgecore (skips the node Ready check)
  --keep                      leave the publisher deployed at the end
  --trace-dir DIR             where to write the trace
                              (default: _output/up2date4sdv/dtpublisher-e2e/<time>-<target>)
  -h, --help                  this help

Exit codes: 0 all checks passed, 1 a check failed, 2 usage error, 3 precondition not met.
EOF
}

# e2e_parse_common_arg ARG [VALUE]: handle one common option. Sets E2E_ARGS_USED to the number
# of arguments consumed (1 or 2), or to 0 if ARG is not a common option. Call it directly, not in
# $(...): it sets the options in this shell.
E2E_ARGS_USED=0
e2e_parse_common_arg() {
  E2E_ARGS_USED=1
  case "$1" in
    --ecu)
      [[ $# -ge 2 ]] || e2e_die "$E2E_EXIT_USAGE" "--ecu needs a value: thermal, tire or both"
      case "$2" in
        thermal | tire | both) E2E_ECU=$2 ;;
        *) e2e_die "$E2E_EXIT_USAGE" "--ecu $2: want thermal, tire or both" ;;
      esac
      E2E_ARGS_USED=2
      ;;
    --allow-ecu-state-change) E2E_ALLOW_ECU_STATE_CHANGE=1 ;;
    --playground)
      [[ $# -ge 2 && -n "$2" ]] || e2e_die "$E2E_EXIT_USAGE" "--playground needs a directory"
      [[ -d "$2" ]] || e2e_die "$E2E_EXIT_USAGE" "--playground $2: not a directory"
      E2E_PLAYGROUND=$(cd "$2" && pwd -P)
      E2E_ARGS_USED=2
      ;;
    --cloud-node | --edge-node)
      [[ $# -ge 2 && -n "$2" ]] || e2e_die "$E2E_EXIT_USAGE" "$1 needs a node name"
      if [[ "$1" == --cloud-node ]]; then E2E_CLOUD_NODE=$2; else E2E_EDGE_NODE=$2; fi
      E2E_ARGS_USED=2
      ;;
    --skip-build) E2E_SKIP_BUILD=1 ;;
    --skip-node-restart) E2E_SKIP_NODE_RESTART=1 ;;
    --keep) E2E_KEEP=1 ;;
    --trace-dir)
      [[ $# -ge 2 && -n "$2" ]] || e2e_die "$E2E_EXIT_USAGE" "--trace-dir needs a directory"
      E2E_TRACE_DIR=$2
      E2E_ARGS_USED=2
      ;;
    *) E2E_ARGS_USED=0 ;;
  esac
}

e2e_wants_thermal() { [[ "$E2E_ECU" == thermal || "$E2E_ECU" == both ]]; }
e2e_wants_tire() { [[ "$E2E_ECU" == tire || "$E2E_ECU" == both ]]; }

# e2e_check_state_change_allowed NOTE: refuse the tire scenario without --allow-ecu-state-change.
# NOTE says where the target keeps the ECU state and how it is reset.
e2e_check_state_change_allowed() {
  if e2e_wants_tire && [[ "$E2E_ALLOW_ECU_STATE_CHANGE" != 1 ]]; then
    e2e_die "$E2E_EXIT_USAGE" "the tire scenario simulates a leak: the tire ECU loses pressure and keeps that state ($1). Run again with --allow-ecu-state-change, or with --ecu thermal."
  fi
}

# ---- helpers ----------------------------------------------------------------------------------

e2e_require_tools() {
  local t missing=()
  for t in "$@"; do
    command -v "$t" >/dev/null 2>&1 || missing+=("$t")
  done
  [[ ${#missing[@]} -eq 0 ]] || e2e_die "$E2E_EXIT_PRECONDITION" "missing tools on this host: ${missing[*]}"
}

# e2e_poll TIMEOUT INTERVAL CMD...: run CMD until it succeeds or TIMEOUT seconds have passed.
e2e_poll() {
  local timeout=$1 interval=$2
  shift 2
  local deadline=$((SECONDS + timeout))
  while true; do
    if "$@"; then
      return 0
    fi
    if ((SECONDS >= deadline)); then
      return 1
    fi
    sleep "$interval"
  done
}

# e2e_quote ARGS...: the arguments as one shell-quoted string, for a remote command line.
e2e_quote() {
  local out=() a
  for a in "$@"; do
    out+=("$(printf '%q' "$a")")
  done
  printf '%s' "${out[*]}"
}

e2e_kubectl() { e2e_cloud_exec "kubectl $(e2e_quote "$@")"; }
e2e_kubectl_in() { e2e_cloud_exec_in "kubectl $(e2e_quote "$@")"; }

e2e_ctl() {
  e2e_cloud_exec "${E2E_REMOTE_DIR}/dtpublisher ctl $(e2e_quote --server "$E2E_SERVER" \
    --ca-file "${E2E_REMOTE_DIR}/ca.crt" --server-name "dtpublisher.${E2E_NAMESPACE}.svc" "$@")"
}

e2e_device_key() { printf 'devices/%s/%s' "$E2E_DEVICE_NAMESPACE" "$1"; }
e2e_edge_node_key() { printf 'nodes/edge-worker/%s' "$E2E_EDGE_NODE"; }
e2e_cloud_node_key() { printf 'nodes/cloud-control-plane/%s' "$E2E_CLOUD_NODE"; }

e2e_selected_devices() {
  if e2e_wants_thermal; then echo "$E2E_THERMAL_DEVICE"; fi
  if e2e_wants_tire; then echo "$E2E_TIRE_DEVICE"; fi
}

# ---- trace evaluation (pure: these only read the events file) ---------------------------------
#
# The events file holds the output of `dtpublisher ctl watch -o json`: one JSON object per line,
# {"receivedAt":..., "event":..., "id":..., "data":...}. While the watch runs, the last line can
# be incomplete; it is skipped until it is complete.

# e2e_trace_mark FILE: the number of complete lines; checks "after the mark" look past them.
e2e_trace_mark() {
  local n
  n=$(wc -l <"$1")
  printf '%s' "${n//[^0-9]/}"
}

# e2e_trace_events FILE [FROM]: the complete events after line FROM (default 0).
e2e_trace_events() {
  local from=${2:-0}
  head -n "$(e2e_trace_mark "$1")" "$1" | tail -n "+$((from + 1))" | jq -c -R 'fromjson? // empty'
}

e2e_trace_synced() {
  e2e_trace_events "$1" | jq -e -s 'any(.[]; .event == "SYNCED")' >/dev/null
}

# e2e_trace_state FILE: the state a subscriber holds, as an object from key to resource.
e2e_trace_state() {
  e2e_trace_events "$1" | jq -c -s 'reduce .[] as $e ({};
      if $e.event == "RESET" then {}
      elif $e.event == "UPSERT" then .[$e.data.key] = $e.data
      elif $e.event == "DELETE" then del(.[$e.data.key])
      else . end)'
}

# e2e_trace_values FILE FROM KEY PROPERTY FIELD: the FIELD (desired, observedDesired or reported)
# values of a device PROPERTY in the UPSERTs of KEY after line FROM, in order.
e2e_trace_values() {
  e2e_trace_events "$1" "$2" | jq -r --arg k "$3" --arg p "$4" --arg f "$5" '
    select(.event == "UPSERT" and .data.key == $k)
    | .data.spec.properties[]? | select(.name == $p) | (.[$f] // {}) | .value // empty'
}

# e2e_trace_value_is FILE FROM KEY PROPERTY FIELD VALUE: an UPSERT after FROM has FIELD = VALUE.
e2e_trace_value_is() {
  e2e_trace_values "$1" "$2" "$3" "$4" "$5" | grep -Fxq -- "$6"
}

# e2e_trace_number_is FILE FROM KEY PROPERTY FIELD NUMBER: as e2e_trace_value_is, compared as
# numbers ("63" and "63.0" are equal).
e2e_trace_number_is() {
  e2e_trace_values "$1" "$2" "$3" "$4" "$5" \
    | jq -e -R -s --argjson n "$6" 'split("\n") | map(tonumber? // empty) | any(. == $n)' >/dev/null
}

# e2e_trace_distinct FILE FROM KEY PROPERTY FIELD COUNT: at least COUNT distinct values after FROM.
e2e_trace_distinct() {
  local n
  n=$(e2e_trace_values "$1" "$2" "$3" "$4" "$5" | sort -u | grep -c . || true)
  [[ "$n" -ge "$6" ]]
}

# e2e_trace_last FILE KEY PROPERTY FIELD: the last published FIELD value of a device PROPERTY.
e2e_trace_last() {
  e2e_trace_values "$1" 0 "$2" "$3" "$4" | tail -n 1
}

# e2e_trace_leak_seen FILE FROM KEY START: one UPSERT of KEY after FROM has frontLeftPressure
# reported below START, lowPressureWarning reported true and FL in lowPressureWheels.
e2e_trace_leak_seen() {
  e2e_trace_events "$1" "$2" | jq -e -s --arg k "$3" --argjson start "$4" '
    [ .[] | select(.event == "UPSERT" and .data.key == $k)
      | [ .data.spec.properties[]? | {(.name): ((.reported // {}).value // "")} ] | add // {}
      | select(((.frontLeftPressure // "") | (tonumber? // empty)) < $start)
      | select(((.lowPressureWarning // "") | ascii_downcase) == "true")
      | select((.lowPressureWheels // "") | test("FL")) ]
    | length > 0' >/dev/null
}

# e2e_trace_node_ready_is FILE FROM KEY STATUS: an UPSERT of node KEY after FROM has ready=STATUS.
e2e_trace_node_ready_is() {
  e2e_trace_events "$1" "$2" | jq -e -s --arg k "$3" --arg s "$4" '
    any(.[]; .event == "UPSERT" and .data.key == $k and .data.spec.ready == $s)' >/dev/null
}

# e2e_trace_upsert_of FILE FROM KEY...: an UPSERT of one of the KEYs after FROM.
e2e_trace_upsert_of() {
  local file=$1 from=$2
  shift 2
  e2e_trace_events "$file" "$from" | jq -e -s --args '$ARGS.positional as $keys
    | any(.[]; .event == "UPSERT" and (.data.key as $k | $keys | index($k)))' "$@" >/dev/null
}

# e2e_trace_has_revision FILE KEY REVISION: the stream carried KEY at exactly REVISION.
e2e_trace_has_revision() {
  e2e_trace_events "$1" | jq -e -s --arg k "$2" --argjson r "$3" '
    any(.[]; .event == "UPSERT" and .data.key == $k and .data.revision == $r)' >/dev/null
}

# e2e_trace_resource_at FILE KEY REVISION: the resource the stream carried at that revision.
e2e_trace_resource_at() {
  e2e_trace_events "$1" | jq -c -s --arg k "$2" --argjson r "$3" '
    [ .[] | select(.event == "UPSERT" and .data.key == $k and .data.revision == $r) | .data ][0]'
}

# ---- cluster operations -----------------------------------------------------------------------

# e2e_desired DEVICE PROPERTY: the desired value in the Device spec.
e2e_desired() {
  e2e_kubectl -n "$E2E_DEVICE_NAMESPACE" get device "$1" -o json \
    | jq -r --arg p "$2" '.spec.properties[]? | select(.name == $p) | (.desired // {}) | .value // empty'
}

# e2e_patch_desired DEVICE PROPERTY VALUE: set a desired value, by property name (a JSON patch
# with a test on the name, so a reordered model cannot patch the wrong property).
e2e_patch_desired() {
  local dev=$1 prop=$2 value=$3 idx patch
  idx=$(e2e_kubectl -n "$E2E_DEVICE_NAMESPACE" get device "$dev" -o json \
    | jq --arg p "$prop" '[.spec.properties[]?.name] | index($p) // empty')
  if [[ ! "$idx" =~ ^[0-9]+$ ]]; then
    e2e_log "error: device ${dev} has no property ${prop}"
    return 1
  fi
  patch=$(jq -n -c --arg p "$prop" --arg v "$value" --argjson i "$idx" \
    '[{op: "test", path: "/spec/properties/\($i)/name", value: $p},
      {op: "replace", path: "/spec/properties/\($i)/desired/value", value: $v}]')
  e2e_log "patch ${dev}: ${prop} desired = ${value}"
  e2e_kubectl -n "$E2E_DEVICE_NAMESPACE" patch device "$dev" --type=json -p "$patch" >/dev/null
}

# e2e_remember_desired DEVICE PROPERTY: record the current desired value, restored at the end.
e2e_remember_desired() {
  local value
  value=$(e2e_desired "$1" "$2")
  E2E_RESTORE+=("$1|$2|$value")
}

# e2e_restore_desired: put back every remembered desired value, newest first.
e2e_restore_desired() {
  local i entry dev prop value
  for ((i = ${#E2E_RESTORE[@]} - 1; i >= 0; i--)); do
    entry=${E2E_RESTORE[$i]}
    IFS='|' read -r dev prop value <<<"$entry"
    e2e_patch_desired "$dev" "$prop" "$value" || e2e_warn "restore ${dev} ${prop}" "set it back to '${value}' by hand"
  done
  E2E_RESTORE=()
}

# e2e_record_status DEVICE LABEL: write the DeviceStatus resourceVersion to the trace.
e2e_record_status() {
  local rv
  rv=$(e2e_kubectl -n "$E2E_DEVICE_NAMESPACE" get devicestatus "$1" \
    -o 'jsonpath={.metadata.resourceVersion}' 2>/dev/null || true)
  printf '%s %s devicestatus/%s resourceVersion=%s\n' "$(date -u +%FT%TZ)" "$2" "$1" "${rv:-?}" \
    >>"${E2E_TRACE_DIR}/kube.log"
}

# e2e_reported_timestamp DEVICE PROPERTY: metadata.timestamp of the reported value.
e2e_reported_timestamp() {
  e2e_kubectl -n "$E2E_DEVICE_NAMESPACE" get devicestatus "$1" -o json 2>/dev/null \
    | jq -r --arg p "$2" '.status.twins[]? | select(.propertyName == $p)
        | ((.reported // {}).metadata // {}).timestamp // empty'
}

e2e_node_ready() {
  e2e_kubectl get node "$1" -o 'jsonpath={.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null
}

# ---- preconditions ----------------------------------------------------------------------------

# e2e_timestamp_changed DEVICE PROPERTY OLD: the reported timestamp is no longer OLD.
e2e_timestamp_changed() {
  [[ "$(e2e_reported_timestamp "$1" "$2")" != "$3" ]]
}

# e2e_timestamp_advances DEVICE PROPERTY: the reported value is refreshed within 60 s.
e2e_timestamp_advances() {
  local first
  first=$(e2e_reported_timestamp "$1" "$2")
  [[ -n "$first" ]] || return 1
  e2e_poll 60 5 e2e_timestamp_changed "$1" "$2" "$first"
}

e2e_preconditions() {
  local node dev prop host
  e2e_log "checking preconditions"
  if [[ -z "$E2E_CLOUD_NODE" ]]; then
    local cps
    cps=$(e2e_kubectl get nodes -l node-role.kubernetes.io/control-plane -o 'jsonpath={.items[*].metadata.name}' 2>/dev/null || true)
    [[ -n "$cps" && "$cps" != *" "* ]] \
      || e2e_die "$E2E_EXIT_PRECONDITION" "cannot tell the cloud node (control-plane nodes: '${cps}'); give --cloud-node"
    E2E_CLOUD_NODE=$cps
    e2e_log "cloud node: ${E2E_CLOUD_NODE} (control-plane role)"
  fi
  E2E_CLOUD_HOSTNAME=$(e2e_kubectl get node "$E2E_CLOUD_NODE" -o 'jsonpath={.metadata.labels.kubernetes\.io/hostname}' 2>/dev/null || true)
  [[ -n "$E2E_CLOUD_HOSTNAME" ]] || E2E_CLOUD_HOSTNAME=$E2E_CLOUD_NODE
  for node in "$E2E_CLOUD_NODE" "$E2E_EDGE_NODE"; do
    [[ "$(e2e_node_ready "$node")" == True ]] \
      || e2e_die "$E2E_EXIT_PRECONDITION" "node ${node} is not Ready"
  done
  # The publisher goes into the namespace cloudcore runs in: continuumx (build/dtpublisher/ as
  # it is), or kubeedge, as some cxadm releases install (the playground handles both).
  local ns found=
  for ns in continuumx kubeedge; do
    if e2e_kubectl -n "$ns" get deployment cloudcore >/dev/null 2>&1; then
      found=$ns
      break
    fi
  done
  [[ -n "$found" ]] || e2e_die "$E2E_EXIT_PRECONDITION" "no cloudcore Deployment in namespace continuumx or kubeedge"
  E2E_NAMESPACE=$found
  e2e_log "cloudcore runs in namespace ${E2E_NAMESPACE}; the publisher is deployed there"
  while read -r dev; do
    host=$(e2e_kubectl -n "$E2E_DEVICE_NAMESPACE" get device "$dev" -o 'jsonpath={.spec.nodeName}' 2>/dev/null || true)
    [[ "$host" == "$E2E_EDGE_NODE" ]] \
      || e2e_die "$E2E_EXIT_PRECONDITION" "device ${dev} not found on node ${E2E_EDGE_NODE} (got '${host}')"
    prop=coolantTemperature
    [[ "$dev" == "$E2E_TIRE_DEVICE" ]] && prop=frontLeftPressure
    if ! e2e_timestamp_advances "$dev" "$prop"; then
      e2e_die "$E2E_EXIT_PRECONDITION" "DeviceStatus ${dev}: reported ${prop} did not advance in 60 s. The playground notes that the mapper may need a restart after Devices are created (systemctl restart ${E2E_MAPPER_UNIT} on ${E2E_EDGE_NODE}); this script does not restart it."
    fi
  done < <(e2e_selected_devices)
  e2e_target_preconditions || e2e_die "$E2E_EXIT_PRECONDITION" "target preconditions not met"
  e2e_log "preconditions met"
}

# ---- build, side-load, deploy -----------------------------------------------------------------

e2e_image_tag() {
  local tag
  tag="e2e-$(git -C "$E2E_REPO_ROOT" rev-parse --short=9 HEAD)"
  if [[ -n "$(git -C "$E2E_REPO_ROOT" status --porcelain)" ]]; then
    tag+="-dirty"
  fi
  printf '%s' "$tag"
}

# e2e_build: build the image with Podman from the files git would see (tracked plus untracked,
# not ignored), save it as an archive and extract the binary for ctl.
e2e_build() {
  E2E_IMAGE="docker.io/neotera/dtpublisher:$(e2e_image_tag)"
  local archive="${E2E_WORK_DIR}/dtpublisher-image.tar" bin="${E2E_WORK_DIR}/dtpublisher"
  if [[ "$E2E_SKIP_BUILD" == 1 && "$E2E_IMAGE" != *-dirty ]] \
     && podman image exists "$E2E_IMAGE" && [[ -s "$archive" && -x "$bin" ]]; then
    e2e_log "reusing image ${E2E_IMAGE}"
    return 0
  fi
  [[ "$E2E_SKIP_BUILD" == 1 ]] && e2e_log "--skip-build: no reusable image for ${E2E_IMAGE} (or the tree is dirty); building"
  local ctx="${E2E_WORK_DIR}/context" ldflags cid
  e2e_log "building ${E2E_IMAGE}"
  rm -rf "$ctx"
  mkdir -p "$ctx"
  (cd "$E2E_REPO_ROOT" && git ls-files -z -co --exclude-standard \
    | tar --null --ignore-failed-read -T - -cf -) | tar -xf - -C "$ctx"
  ldflags=$("${E2E_REPO_ROOT}/hack/make-rules/version.sh")
  podman build --build-arg "GO_LDFLAGS=${ldflags}" -f "${ctx}/build/dtpublisher/Dockerfile" \
    -t "$E2E_IMAGE" "$ctx" >"${E2E_TRACE_DIR}/build.log" 2>&1 \
    || e2e_die "$E2E_EXIT_FAIL" "image build failed, see ${E2E_TRACE_DIR}/build.log"
  rm -rf "$ctx"
  rm -f "$archive"
  podman save --format docker-archive -o "$archive" "$E2E_IMAGE" >>"${E2E_TRACE_DIR}/build.log" 2>&1
  cid=$(podman create "$E2E_IMAGE")
  podman cp "${cid}:/usr/local/bin/dtpublisher" "$bin"
  podman rm "$cid" >/dev/null
  chmod 755 "$bin"
}

e2e_side_load() {
  e2e_log "side-loading ${E2E_IMAGE} into containerd on ${E2E_CLOUD_NODE}"
  e2e_cloud_exec "rm -rf ${E2E_REMOTE_DIR} && mkdir -m 700 ${E2E_REMOTE_DIR}"
  e2e_cloud_exec_in "cat > ${E2E_REMOTE_DIR}/image.tar" <"${E2E_WORK_DIR}/dtpublisher-image.tar"
  e2e_cloud_exec "ctr -n k8s.io images import ${E2E_REMOTE_DIR}/image.tar >/dev/null && rm -f ${E2E_REMOTE_DIR}/image.tar" \
    || e2e_die "$E2E_EXIT_FAIL" "ctr import failed on ${E2E_CLOUD_NODE}"
  e2e_cloud_exec_in "cat > ${E2E_REMOTE_DIR}/dtpublisher && chmod 755 ${E2E_REMOTE_DIR}/dtpublisher" \
    <"${E2E_WORK_DIR}/dtpublisher"
}

# e2e_make_tls: a CA and a server certificate for the Service names, valid two days, test only.
e2e_make_tls() {
  local dir="${E2E_WORK_DIR}/tls" ns=$E2E_NAMESPACE
  rm -rf "$dir"
  mkdir -m 700 "$dir"
  {
    openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=dtpublisher-e2e-ca" \
      -keyout "${dir}/ca.key" -out "${dir}/ca.crt"
    openssl req -newkey rsa:2048 -nodes -subj "/CN=dtpublisher" \
      -keyout "${dir}/tls.key" -out "${dir}/tls.csr"
    printf '%s\n' \
      "subjectAltName=DNS:dtpublisher,DNS:dtpublisher.${ns},DNS:dtpublisher.${ns}.svc,DNS:dtpublisher.${ns}.svc.cluster.local" \
      "basicConstraints=CA:FALSE" "keyUsage=digitalSignature,keyEncipherment" "extendedKeyUsage=serverAuth" \
      >"${dir}/ext"
    openssl x509 -req -days 2 -in "${dir}/tls.csr" -CA "${dir}/ca.crt" -CAkey "${dir}/ca.key" \
      -CAcreateserial -extfile "${dir}/ext" -out "${dir}/tls.crt"
  } >"${E2E_TRACE_DIR}/openssl.log" 2>&1 || e2e_die "$E2E_EXIT_FAIL" "certificate generation failed, see openssl.log"
}

e2e_tls_secret_json() {
  local dir="${E2E_WORK_DIR}/tls"
  jq -n -c --arg ns "$E2E_NAMESPACE" \
    --arg crt "$(base64 -w0 <"${dir}/tls.crt")" --arg key "$(base64 -w0 <"${dir}/tls.key")" \
    --arg ca "$(base64 -w0 <"${dir}/ca.crt")" \
    '{apiVersion: "v1", kind: "Secret", type: "kubernetes.io/tls",
      metadata: {name: "dtpublisher-tls", namespace: $ns,
                 labels: {"k8s-app": "dtpublisher", "kubeedge": "dtpublisher"}},
      data: {"tls.crt": $crt, "tls.key": $key, "ca.crt": $ca}}'
}

# e2e_manifests: build/dtpublisher/*.yaml as one multi-document stream.
e2e_manifests() {
  local f
  for f in "${E2E_REPO_ROOT}"/build/dtpublisher/0*.yaml; do
    cat "$f"
    printf '\n---\n'
  done
}

# e2e_customize IMAGE NODE NAMESPACE: on stdin the manifests as JSON (one object or a List); on
# stdout the same with the e2e image, --enable-query, a node selector (kubernetes.io/hostname =
# NODE) for the node holding the image, and NAMESPACE for the namespaced objects and the
# ClusterRoleBinding subject.
e2e_customize() {
  jq -c --arg img "$1" --arg node "$2" --arg ns "$3" '
    (if .kind == "List" then .items else [.] end)
    | map(if .metadata.namespace? then .metadata.namespace = $ns else . end
          | if .kind == "ClusterRoleBinding" then .subjects |= map(.namespace = $ns) else . end)
    | map(if .kind == "Deployment" and .metadata.name == "dtpublisher" then
            .spec.template.spec.containers |= map(if .name == "dtpublisher" then
                .image = $img | .args += ["--enable-query"] else . end)
            | .spec.template.spec.nodeSelector = {"kubernetes.io/hostname": $node}
          else . end)
    | {apiVersion: "v1", kind: "List", items: .}'
}

# e2e_manifests_json: build/dtpublisher/ as a JSON List, customized for this run.
e2e_manifests_json() {
  e2e_manifests | e2e_kubectl_in create --dry-run=client -o json -f - \
    | e2e_customize "$E2E_IMAGE" "$E2E_CLOUD_HOSTNAME" "$E2E_NAMESPACE"
}

e2e_undeploy() {
  e2e_manifests_json | e2e_kubectl_in delete --ignore-not-found --wait=true -f - >/dev/null 2>&1 || true
  e2e_kubectl -n "$E2E_NAMESPACE" delete secret dtpublisher-tls --ignore-not-found >/dev/null 2>&1 || true
}

e2e_deploy() {
  local json ip pod_ip
  e2e_log "deploying build/dtpublisher/ (TLS with a test certificate, --enable-query)"
  e2e_undeploy
  e2e_make_tls
  e2e_cloud_exec_in "cat > ${E2E_REMOTE_DIR}/ca.crt" <"${E2E_WORK_DIR}/tls/ca.crt"
  e2e_tls_secret_json | e2e_kubectl_in apply -f - >/dev/null \
    || e2e_die "$E2E_EXIT_FAIL" "could not create Secret dtpublisher-tls"
  json=$(e2e_manifests_json) \
    || e2e_die "$E2E_EXIT_FAIL" "could not read build/dtpublisher/ manifests"
  jq -e --arg img "$E2E_IMAGE" 'any(.items[]; .kind == "Deployment" and .spec.template.spec.containers[0].image == $img)' \
    <<<"$json" >/dev/null || e2e_die "$E2E_EXIT_FAIL" "Deployment dtpublisher not found in build/dtpublisher/"
  printf '%s\n' "$json" >"${E2E_TRACE_DIR}/applied-manifests.json"
  E2E_DEPLOYED=1
  e2e_kubectl_in apply -f - <<<"$json" >>"${E2E_TRACE_DIR}/kube.log" \
    || e2e_die "$E2E_EXIT_FAIL" "kubectl apply failed"
  if ! e2e_kubectl -n "$E2E_NAMESPACE" rollout status deployment/dtpublisher --timeout="${E2E_TIMEOUT_ROLLOUT}s" >/dev/null; then
    e2e_kubectl -n "$E2E_NAMESPACE" describe pod -l k8s-app=dtpublisher >"${E2E_TRACE_DIR}/pod-describe.txt" 2>&1 || true
    e2e_die "$E2E_EXIT_FAIL" "Deployment dtpublisher did not become ready, see pod-describe.txt"
  fi
  e2e_pass "deploy: build/dtpublisher/ applied, pod ready"

  ip=$(e2e_kubectl -n "$E2E_NAMESPACE" get service dtpublisher -o 'jsonpath={.spec.clusterIP}')
  E2E_SERVER="https://${ip}:8443"
  if e2e_poll 20 2 e2e_ctl capabilities >"${E2E_TRACE_DIR}/capabilities.yaml" 2>&1; then
    e2e_pass "I-4 reachable through Service dtpublisher (${E2E_SERVER}), TLS verified against the test CA"
  else
    pod_ip=$(e2e_kubectl -n "$E2E_NAMESPACE" get pod -l k8s-app=dtpublisher -o 'jsonpath={.items[0].status.podIP}')
    E2E_SERVER="https://${pod_ip}:8443"
    e2e_ctl capabilities >"${E2E_TRACE_DIR}/capabilities.yaml" 2>&1 \
      || e2e_die "$E2E_EXIT_FAIL" "I-4 not reachable from ${E2E_CLOUD_NODE}, see capabilities.yaml"
    e2e_warn "Service ClusterIP ${ip} not reachable from ${E2E_CLOUD_NODE}; using the pod IP ${pod_ip}"
  fi
}

# ---- subscription -----------------------------------------------------------------------------

e2e_watch_start() {
  E2E_EVENTS="${E2E_TRACE_DIR}/events.jsonl"
  : >"$E2E_EVENTS"
  e2e_log "subscribing: dtpublisher ctl watch (all keys) on ${E2E_CLOUD_NODE}"
  # The watch ends when e2e_watch_stop kills it, so its exit status is not an error.
  {
    e2e_cloud_exec "echo \$\$ > ${E2E_REMOTE_DIR}/watch.pid; exec ${E2E_REMOTE_DIR}/dtpublisher ctl $(e2e_quote \
      --server "$E2E_SERVER" --ca-file "${E2E_REMOTE_DIR}/ca.crt" --server-name "dtpublisher.${E2E_NAMESPACE}.svc" watch)" || true
  } >"$E2E_EVENTS" 2>"${E2E_TRACE_DIR}/watch.err" &
  E2E_WATCH_PID=$!
  if e2e_poll "$E2E_TIMEOUT_SYNC" 1 e2e_trace_synced "$E2E_EVENTS"; then
    e2e_pass "watch: initial state received (RESET, UPSERTs, SYNCED)"
  else
    e2e_die "$E2E_EXIT_FAIL" "watch: no SYNCED within ${E2E_TIMEOUT_SYNC} s, see watch.err"
  fi
}

e2e_watch_stop() {
  [[ -n "$E2E_WATCH_PID" ]] || return 0
  e2e_cloud_exec "kill \$(cat ${E2E_REMOTE_DIR}/watch.pid 2>/dev/null) 2>/dev/null" || true
  local i
  for i in 1 2 3 4 5; do
    kill -0 "$E2E_WATCH_PID" 2>/dev/null || break
    sleep 1
  done
  kill "$E2E_WATCH_PID" 2>/dev/null || true
  wait "$E2E_WATCH_PID" 2>/dev/null || true
  E2E_WATCH_PID=
}

# ---- scenarios --------------------------------------------------------------------------------

e2e_check_initial_state() {
  local state key dev
  state=$(e2e_trace_state "$E2E_EVENTS")
  for key in "$(e2e_edge_node_key)" "$(e2e_cloud_node_key)"; do
    if jq -e --arg k "$key" 'has($k)' <<<"$state" >/dev/null; then
      e2e_pass "nodes: ${key} published"
    else
      e2e_fail "nodes: ${key} published" "not in the initial state"
    fi
  done
  while read -r dev; do
    key=$(e2e_device_key "$dev")
    if jq -e --arg k "$key" --arg n "$E2E_EDGE_NODE" '.[$k].spec.node == $n' <<<"$state" >/dev/null; then
      e2e_pass "devices: ${key} published, node=${E2E_EDGE_NODE}"
    else
      e2e_fail "devices: ${key} published, node=${E2E_EDGE_NODE}" "missing or wrong node"
    fi
  done < <(e2e_selected_devices)
}

# Thermal: set targetCoolantTemperature desired (63.0, as the playground's own test does; 62.0
# if the ECU is already at 63) and expect the publication of the desired value, then of the
# reported value, plus changing coolantTemperature telemetry. Restored at the end.
e2e_scenario_thermal() {
  local dev=$E2E_THERMAL_DEVICE key target current from
  key=$(e2e_device_key "$dev")
  e2e_log "=== scenario thermal (${dev}) ==="
  current=$(e2e_trace_last "$E2E_EVENTS" "$key" targetCoolantTemperature reported)
  target=63.0
  if jq -e -n --arg c "$current" '($c | tonumber? // 0) == 63' >/dev/null; then
    target=62.0
  fi
  e2e_remember_desired "$dev" targetCoolantTemperature
  from=$(e2e_trace_mark "$E2E_EVENTS")
  e2e_record_status "$dev" "before targetCoolantTemperature=${target}"
  e2e_patch_desired "$dev" targetCoolantTemperature "$target" \
    || { e2e_fail "thermal: patch targetCoolantTemperature" "kubectl patch failed"; return 0; }

  if e2e_poll "$E2E_TIMEOUT_DESIRED" 2 e2e_trace_number_is "$E2E_EVENTS" "$from" "$key" targetCoolantTemperature desired "$target"; then
    e2e_pass "thermal: desired targetCoolantTemperature=${target} published"
  else
    e2e_fail "thermal: desired targetCoolantTemperature=${target} published" "not within ${E2E_TIMEOUT_DESIRED} s"
  fi
  if e2e_poll "$E2E_TIMEOUT_REPORTED" 3 e2e_trace_number_is "$E2E_EVENTS" "$from" "$key" targetCoolantTemperature reported "$target"; then
    e2e_pass "thermal: reported targetCoolantTemperature=${target} published"
  else
    e2e_fail "thermal: reported targetCoolantTemperature=${target} published" "not within ${E2E_TIMEOUT_REPORTED} s"
  fi
  if e2e_poll "$E2E_TIMEOUT_TELEMETRY" 3 e2e_trace_distinct "$E2E_EVENTS" "$from" "$key" coolantTemperature reported 2; then
    e2e_pass "thermal: coolantTemperature telemetry published (changing values)"
  else
    e2e_fail "thermal: coolantTemperature telemetry published" "fewer than 2 distinct values in ${E2E_TIMEOUT_TELEMETRY} s"
  fi
  e2e_record_status "$dev" "after targetCoolantTemperature=${target}"
}

# Tire: following the playground's own test, set leakWheel=FL, leakRate=10, pressureThreshold
# to the current front-left pressure, then leakEnabled=true; expect the publication of the
# desired leakEnabled, and of falling front-left pressure with lowPressureWarning=true and FL in
# lowPressureWheels. Desired values are restored at the end; the lost pressure is not (ECU state).
e2e_scenario_tire() {
  local dev=$E2E_TIRE_DEVICE key start from prop
  key=$(e2e_device_key "$dev")
  e2e_log "=== scenario tire (${dev}) ==="
  start=$(e2e_trace_last "$E2E_EVENTS" "$key" frontLeftPressure reported)
  if ! jq -e -n --arg s "$start" '$s | tonumber? // empty' >/dev/null 2>&1; then
    e2e_fail "tire: frontLeftPressure published before the leak" "no numeric value (got '${start}')"
    return 0
  fi
  # The threshold must be a valid value for the model; the published model carries the range.
  local min
  min=$(e2e_trace_events "$E2E_EVENTS" | jq -r -s --arg k "$key" '
    [ .[] | select(.event == "UPSERT" and .data.key == $k) | .data.spec.properties[]?
      | select(.name == "pressureThreshold") | .minimum // empty ] | last // empty')
  if [[ -n "$min" ]] && jq -e -n --argjson s "$start" --arg m "$min" '$s < ($m | tonumber? // 0)' >/dev/null; then
    e2e_fail "tire: leak scenario" "front-left pressure ${start} is below the pressureThreshold minimum ${min}, so no valid threshold can be set; reset the tire ECU state first"
    return 0
  fi
  # Remembered in this order so that the restore (newest first) switches the leak off first,
  # then restores the threshold and the rest, as the playground procedure says.
  for prop in pressureThreshold leakRate leakWheel leakEnabled; do
    e2e_remember_desired "$dev" "$prop"
  done
  from=$(e2e_trace_mark "$E2E_EVENTS")
  e2e_record_status "$dev" "before leak (frontLeftPressure=${start})"
  if ! { e2e_patch_desired "$dev" leakWheel FL && e2e_patch_desired "$dev" leakRate 10 \
         && e2e_patch_desired "$dev" pressureThreshold "$start" && e2e_patch_desired "$dev" leakEnabled true; }; then
    e2e_fail "tire: patch leak settings" "kubectl patch failed"
    return 0
  fi

  if e2e_poll "$E2E_TIMEOUT_DESIRED" 2 e2e_trace_value_is "$E2E_EVENTS" "$from" "$key" leakEnabled desired true; then
    e2e_pass "tire: desired leakEnabled=true published"
  else
    e2e_fail "tire: desired leakEnabled=true published" "not within ${E2E_TIMEOUT_DESIRED} s"
  fi
  if e2e_poll "$E2E_TIMEOUT_REPORTED" 3 e2e_trace_leak_seen "$E2E_EVENTS" "$from" "$key" "$start"; then
    e2e_pass "tire: frontLeftPressure below ${start}, lowPressureWarning=true, FL in lowPressureWheels published"
  else
    e2e_fail "tire: leak published" "no event with frontLeftPressure < ${start}, lowPressureWarning=true and FL within ${E2E_TIMEOUT_REPORTED} s"
  fi
  e2e_record_status "$dev" "after leak"
}

# Both: the two devices are on one subscription and both hosted by the edge node.
e2e_check_both() {
  local state ok=1 dev
  state=$(e2e_trace_state "$E2E_EVENTS")
  for dev in "$E2E_THERMAL_DEVICE" "$E2E_TIRE_DEVICE"; do
    jq -e --arg k "$(e2e_device_key "$dev")" --arg n "$E2E_EDGE_NODE" '.[$k].spec.node == $n' <<<"$state" >/dev/null || ok=0
  done
  if [[ "$ok" == 1 ]]; then
    e2e_pass "both: thermal and tire devices on one subscription under devices/${E2E_DEVICE_NAMESPACE}/, node=${E2E_EDGE_NODE}"
  else
    e2e_fail "both: thermal and tire devices on one subscription" "one is missing or not on ${E2E_EDGE_NODE}"
  fi
}

# Query (opt-in): list and get return the same state as the stream.
e2e_scenario_query() {
  local state list_keys stream_keys kind out key rev dev name
  e2e_log "=== query: list and get against the stream ==="
  state=$(e2e_trace_state "$E2E_EVENTS")
  for kind in nodes devices; do
    if ! out=$(e2e_ctl list "$kind" -o json 2>&1); then
      e2e_fail "query: ctl list ${kind}" "$out"
      continue
    fi
    printf '%s\n' "$out" >"${E2E_TRACE_DIR}/list-${kind}.json"
    list_keys=$(jq -r '.items[].key' <<<"$out" | sort)
    stream_keys=$(jq -r --arg p "${kind}/" 'keys[] | select(startswith($p))' <<<"$state" | sort)
    if [[ "$list_keys" == "$stream_keys" ]]; then
      e2e_pass "query: ctl list ${kind} has the same keys as the stream ($(grep -c . <<<"$list_keys"))"
    else
      e2e_fail "query: ctl list ${kind} has the same keys as the stream" "list: ${list_keys//$'\n'/ }; stream: ${stream_keys//$'\n'/ }"
    fi
  done

  # get returns one revision; the stream must have carried exactly that resource.
  local -a gets=("node|${E2E_EDGE_NODE}|$(e2e_edge_node_key)")
  while read -r dev; do
    gets+=("device|${E2E_DEVICE_NAMESPACE}/${dev}|$(e2e_device_key "$dev")")
  done < <(e2e_selected_devices)
  local g what
  for g in "${gets[@]}"; do
    IFS='|' read -r what name key <<<"$g"
    if ! out=$(e2e_ctl get "$what" "$name" -o json 2>&1); then
      e2e_fail "query: ctl get ${what} ${name}" "$out"
      continue
    fi
    rev=$(jq -r '.revision' <<<"$out")
    if ! e2e_poll 30 1 e2e_trace_has_revision "$E2E_EVENTS" "$key" "$rev"; then
      e2e_warn "query: ctl get ${what} ${name} revision ${rev} not seen on the stream" "a later revision may have replaced it"
      continue
    fi
    if jq -e -n --argjson a "$out" --argjson b "$(e2e_trace_resource_at "$E2E_EVENTS" "$key" "$rev")" '$a == $b' >/dev/null; then
      e2e_pass "query: ctl get ${what} ${name} equals the stream at revision ${rev}"
    else
      e2e_fail "query: ctl get ${what} ${name} equals the stream at revision ${rev}" "resources differ"
    fi
  done
}

# Nodes: stopping edgecore makes the edge node Ready=Unknown, starting it Ready=True; then the
# device twins are expected to be published again.
e2e_scenario_node_restart() {
  local key from keys=()
  key=$(e2e_edge_node_key)
  e2e_log "=== node: stop and start edgecore on ${E2E_EDGE_NODE} ==="
  from=$(e2e_trace_mark "$E2E_EVENTS")
  E2E_EDGECORE_STOPPED=1
  e2e_edge_exec "systemctl stop edgecore" || e2e_warn "node: systemctl stop edgecore failed"
  if e2e_poll "$E2E_TIMEOUT_NODE" 5 e2e_trace_node_ready_is "$E2E_EVENTS" "$from" "$key" Unknown; then
    e2e_pass "node: ${key} ready=Unknown published after edgecore stopped"
  else
    e2e_fail "node: ${key} ready=Unknown published" "not within ${E2E_TIMEOUT_NODE} s"
  fi
  from=$(e2e_trace_mark "$E2E_EVENTS")
  e2e_edge_exec "systemctl start edgecore" || e2e_warn "node: systemctl start edgecore failed"
  E2E_EDGECORE_STOPPED=0
  if e2e_poll "$E2E_TIMEOUT_NODE" 5 e2e_trace_node_ready_is "$E2E_EVENTS" "$from" "$key" True; then
    e2e_pass "node: ${key} ready=True published after edgecore started"
  else
    e2e_fail "node: ${key} ready=True published" "not within ${E2E_TIMEOUT_NODE} s"
  fi
  from=$(e2e_trace_mark "$E2E_EVENTS")
  while read -r dev; do
    keys+=("$(e2e_device_key "$dev")")
  done < <(e2e_selected_devices)
  if e2e_poll "$E2E_TIMEOUT_RESUME" 5 e2e_trace_upsert_of "$E2E_EVENTS" "$from" "${keys[@]}"; then
    e2e_pass "node: device twins published again after edgecore restart"
  else
    e2e_warn "node: no device twin update within ${E2E_TIMEOUT_RESUME} s after edgecore restart" \
      "the playground notes the mapper may need a restart (systemctl restart ${E2E_MAPPER_UNIT}); not done by this script"
  fi
}

# ---- trace and clean-up -----------------------------------------------------------------------

e2e_collect_trace() {
  local t=$E2E_TRACE_DIR since
  since="@$((E2E_START_EPOCH - 60))"
  {
    echo "target: ${E2E_TARGET}"
    echo "ecu: ${E2E_ECU}"
    echo "continuumx: $(git -C "$E2E_REPO_ROOT" rev-parse HEAD)$([[ -n "$(git -C "$E2E_REPO_ROOT" status --porcelain)" ]] && echo ' (dirty)')"
    echo "image: ${E2E_IMAGE}"
    [[ -x "${E2E_WORK_DIR}/dtpublisher" ]] && "${E2E_WORK_DIR}/dtpublisher" version 2>&1 | sed 's/^/  /'
    if [[ -n "$E2E_PLAYGROUND" ]]; then
      echo "playground: $(git -C "$E2E_PLAYGROUND" rev-parse HEAD 2>/dev/null || echo unknown)$([[ -n "$(git -C "$E2E_PLAYGROUND" status --porcelain 2>/dev/null)" ]] && echo ' (dirty)') (${E2E_PLAYGROUND})"
    else
      echo "playground: not recorded (no --playground)"
    fi
    echo "server: ${E2E_SERVER}"
  } >"${t}/versions.txt"
  e2e_kubectl get nodes -o wide >"${t}/nodes.txt" 2>&1 || true
  e2e_kubectl -n "$E2E_NAMESPACE" logs deployment/dtpublisher --timestamps >"${t}/publisher.log" 2>&1 || true
  e2e_kubectl -n "$E2E_DEVICE_NAMESPACE" get device,devicestatus -o yaml >"${t}/devices.yaml" 2>&1 || true
  e2e_edge_exec "journalctl -u ${E2E_MAPPER_UNIT} --since $(e2e_quote "$since") --no-pager -o short-iso" \
    >"${t}/mapper.log" 2>&1 || true
  e2e_target_trace || true
}

e2e_summary() {
  local r
  {
    echo "dtpublisher E2E (${E2E_TARGET}, --ecu ${E2E_ECU})"
    for r in "${E2E_RESULTS[@]}"; do
      echo "  $r"
    done
  } | tee "${E2E_TRACE_DIR}/summary.txt" >&2
  e2e_log "trace: ${E2E_TRACE_DIR}"
}

e2e_cleanup() {
  local code=$?
  trap - EXIT INT TERM
  set +e
  if [[ -n "$E2E_START_EPOCH" ]]; then
    e2e_watch_stop
    if [[ "$E2E_EDGECORE_STOPPED" == 1 ]]; then
      e2e_log "starting edgecore again on ${E2E_EDGE_NODE}"
      e2e_edge_exec "systemctl start edgecore"
    fi
    if [[ ${#E2E_RESTORE[@]} -gt 0 ]]; then
      e2e_log "restoring desired values"
      e2e_restore_desired
    fi
    if [[ "$E2E_DEPLOYED" == 1 ]]; then
      e2e_collect_trace
      if [[ "$E2E_KEEP" == 1 ]]; then
        e2e_log "--keep: publisher left deployed in namespace ${E2E_NAMESPACE} (${E2E_SERVER})"
      else
        e2e_log "removing the publisher"
        e2e_undeploy
        e2e_cloud_exec "rm -rf ${E2E_REMOTE_DIR}"
      fi
    fi
    case "$code" in
      0 | "$E2E_EXIT_FAIL" | "$E2E_EXIT_USAGE" | "$E2E_EXIT_PRECONDITION" | 130) ;;
      *)
        e2e_fail "run stopped by an unexpected error" "exit ${code}, see steps.log"
        code=$E2E_EXIT_FAIL
        ;;
    esac
    e2e_summary
    if [[ "$code" == 0 ]] && e2e_failed; then
      code=$E2E_EXIT_FAIL
    fi
  fi
  exit "$code"
}

# ---- main -------------------------------------------------------------------------------------

e2e_main() {
  e2e_require_tools jq git podman openssl base64 tar
  if [[ -z "$E2E_TRACE_DIR" ]]; then
    E2E_TRACE_DIR="${E2E_REPO_ROOT}/_output/up2date4sdv/dtpublisher-e2e/$(date -u +%Y%m%dT%H%M%SZ)-${E2E_TARGET}"
  fi
  mkdir -p "$E2E_TRACE_DIR"
  E2E_TRACE_DIR=$(cd "$E2E_TRACE_DIR" && pwd -P)
  E2E_WORK_DIR="${E2E_REPO_ROOT}/_output/up2date4sdv/dtpublisher-e2e/work"
  mkdir -p "$E2E_WORK_DIR"
  : >"${E2E_TRACE_DIR}/kube.log"

  e2e_log "dtpublisher E2E on the ${E2E_TARGET} playground, --ecu ${E2E_ECU}"
  e2e_preconditions
  E2E_START_EPOCH=$(date +%s)
  trap e2e_cleanup EXIT
  trap 'exit 130' INT TERM
  trap 'e2e_log "error: command failed (exit $?): ${BASH_COMMAND}"' ERR

  e2e_build
  e2e_side_load
  e2e_deploy
  e2e_watch_start
  e2e_check_initial_state
  if e2e_wants_thermal; then e2e_scenario_thermal; fi
  if e2e_wants_tire; then e2e_scenario_tire; fi
  e2e_restore_desired
  if [[ "$E2E_ECU" == both ]]; then e2e_check_both; fi
  e2e_scenario_query
  if [[ "$E2E_SKIP_NODE_RESTART" == 1 ]]; then
    e2e_warn "node: edgecore stop/start not run, so the node Ready check was skipped"
  else
    e2e_scenario_node_restart
  fi
}
