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
# Tests of lib/e2e-common.sh that need no playground: the trace checks on a synthetic event
# stream, option parsing, and the desired-value patch/restore against a stub kubectl.
# Usage: tests/up2date4sdv/dtpublisher/lib/e2e-common-test.sh

set -Eeuo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=e2e-common.sh
source "${HERE}/e2e-common.sh"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
FAILS=0
RUN=0

ok() { RUN=$((RUN + 1)); printf 'ok   %s\n' "$1"; }
bad() { RUN=$((RUN + 1)); FAILS=$((FAILS + 1)); printf 'FAIL %s\n' "$1"; }
expect_true() { local name=$1; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }
expect_false() { local name=$1; shift; if "$@"; then bad "$name"; else ok "$name"; fi; }
expect_eq() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1: got '$2', want '$3'"; fi; }

# ---- a synthetic stream, as `dtpublisher ctl watch -o json` writes it ------------------------

THERMAL_KEY=devices/default/automotive-thermal-ecu-fw-001
TIRE_KEY=devices/default/automotive-tire-pressure-ecu-fw-001
EDGE_KEY=nodes/edge-worker/u2deb-edge
CLOUD_KEY=nodes/cloud-control-plane/u2deb-cloud

ev() { jq -n -c --arg e "$1" --arg id "${3:-}" --argjson d "$2" \
  '{receivedAt: "2026-11-25T10:00:00Z", event: $e, data: $d} + (if $id == "" then {} else {id: $id} end)'; }
node() { jq -n -c --arg k "$1" --argjson r "$2" --arg ready "$3" \
  '{schemaVersion: "v1alpha1", kind: "Node", key: $k, revision: $r, spec: {name: ($k | split("/")[2]), ready: $ready}}'; }
# device KEY REV NODE NAME=FIELD:VALUE...
device() {
  local key=$1 rev=$2 host=$3
  shift 3
  local props='[]' kv name rest field value
  for kv in "$@"; do
    name=${kv%%=*}
    rest=${kv#*=}
    field=${rest%%:*}
    value=${rest#*:}
    props=$(jq -c --arg n "$name" --arg f "$field" --arg v "$value" '
      if any(.[]; .name == $n) then map(if .name == $n then .[$f] = {value: $v} else . end)
      else . + [{name: $n, ($f): {value: $v}}] end' <<<"$props")
  done
  jq -n -c --arg k "$key" --argjson r "$rev" --arg h "$host" --argjson p "$props" \
    '{schemaVersion: "v1alpha1", kind: "Device", key: $k, revision: $r, spec: {node: $h, properties: $p}}'
}

EV="${TMP}/events.jsonl"
{
  ev RESET '{"epoch":"e1","revision":4}'
  ev UPSERT "$(node "$EDGE_KEY" 1 True)"
  ev UPSERT "$(node "$CLOUD_KEY" 2 True)"
  ev UPSERT "$(device "$THERMAL_KEY" 3 u2deb-edge targetCoolantTemperature=desired:70.0 targetCoolantTemperature=reported:70.0 coolantTemperature=reported:71.2)"
  ev UPSERT "$(device "$TIRE_KEY" 4 u2deb-edge frontLeftPressure=reported:233.5 lowPressureWarning=reported:false lowPressureWheels=reported:NONE leakEnabled=desired:false)"
  ev SYNCED '{"epoch":"e1","revision":4}' e1:4
} >"$EV"
MARK0=$(e2e_trace_mark "$EV")
{
  ev UPSERT "$(device "$THERMAL_KEY" 5 u2deb-edge targetCoolantTemperature=desired:63.0 targetCoolantTemperature=reported:70.0 coolantTemperature=reported:71.0)" e1:5
  ev HEARTBEAT '{"epoch":"e1","revision":5}' e1:5
  ev UPSERT "$(device "$THERMAL_KEY" 6 u2deb-edge targetCoolantTemperature=desired:63.0 targetCoolantTemperature=reported:63 coolantTemperature=reported:68.4)" e1:6
  ev UPSERT "$(device "$TIRE_KEY" 7 u2deb-edge frontLeftPressure=reported:231.0 lowPressureWarning=reported:false lowPressureWheels=reported:NONE leakEnabled=desired:true)" e1:7
  ev UPSERT "$(device "$TIRE_KEY" 8 u2deb-edge frontLeftPressure=reported:221.0 lowPressureWarning=reported:True lowPressureWheels=reported:FL leakEnabled=desired:true)" e1:8
  ev UPSERT "$(node "$EDGE_KEY" 9 Unknown)" e1:9
  ev DELETE '{"key":"devices/default/gone","revision":10}' e1:10
} >>"$EV"
# An incomplete last line, as while the watch is still writing it.
printf '{"receivedAt":"2026-11-25T10:00:09Z","event":"UPSERT","data":{"key":"nodes/edge' >>"$EV"

expect_eq "mark counts complete lines only" "$(e2e_trace_mark "$EV")" 13
expect_eq "events skip the incomplete line" "$(e2e_trace_events "$EV" | grep -c .)" 13
expect_eq "events after a mark" "$(e2e_trace_events "$EV" "$MARK0" | grep -c .)" 7
expect_true "synced seen" e2e_trace_synced "$EV"
HEAD3="${TMP}/head3.jsonl"; head -n 3 "$EV" >"$HEAD3"
BEFORE="${TMP}/before.jsonl"; head -n "$MARK0" "$EV" >"$BEFORE"
expect_false "synced not seen in a stream without it" e2e_trace_synced "$HEAD3"

STATE=$(e2e_trace_state "$EV")
expect_eq "state keys" "$(jq -r 'keys | join(" ")' <<<"$STATE")" "$THERMAL_KEY $TIRE_KEY $CLOUD_KEY $EDGE_KEY"
expect_eq "state holds the latest revision" "$(jq -r --arg k "$TIRE_KEY" '.[$k].revision' <<<"$STATE")" 8
RESET_EV="${TMP}/reset.jsonl"
{ cat "$HEAD3"; ev RESET '{"epoch":"e2","revision":1}'; ev UPSERT "$(node "$CLOUD_KEY" 1 True)"; } >"$RESET_EV"
expect_eq "RESET drops the state" "$(e2e_trace_state "$RESET_EV" | jq -r 'keys | join(" ")')" "$CLOUD_KEY"

expect_eq "values in order" "$(e2e_trace_values "$EV" 0 "$THERMAL_KEY" coolantTemperature reported | tr '\n' ' ')" "71.2 71.0 68.4 "
expect_eq "last value" "$(e2e_trace_last "$EV" "$TIRE_KEY" frontLeftPressure reported)" 221.0
expect_eq "missing field yields nothing" "$(e2e_trace_values "$EV" 0 "$TIRE_KEY" frontLeftPressure desired)" ""

expect_true "desired 63.0 after the mark" e2e_trace_number_is "$EV" "$MARK0" "$THERMAL_KEY" targetCoolantTemperature desired 63.0
expect_true "reported 63 equals 63.0" e2e_trace_number_is "$EV" "$MARK0" "$THERMAL_KEY" targetCoolantTemperature reported 63.0
expect_false "reported 63 not before the mark" e2e_trace_number_is "$BEFORE" 0 "$THERMAL_KEY" targetCoolantTemperature reported 63
expect_true "control: reported 70 before the mark" e2e_trace_number_is "$BEFORE" 0 "$THERMAL_KEY" targetCoolantTemperature reported 70
expect_true "control: synced in the first part" e2e_trace_synced "$BEFORE"
expect_false "reported 62 never" e2e_trace_number_is "$EV" 0 "$THERMAL_KEY" targetCoolantTemperature reported 62.0
expect_true "string value" e2e_trace_value_is "$EV" "$MARK0" "$TIRE_KEY" leakEnabled desired true
expect_false "string value not before the mark" e2e_trace_value_is "$BEFORE" 0 "$TIRE_KEY" leakEnabled desired true
expect_true "two distinct telemetry values" e2e_trace_distinct "$EV" "$MARK0" "$THERMAL_KEY" coolantTemperature reported 2
expect_false "not four distinct values" e2e_trace_distinct "$EV" "$MARK0" "$THERMAL_KEY" coolantTemperature reported 4

expect_true "leak seen below 233.5" e2e_trace_leak_seen "$EV" "$MARK0" "$TIRE_KEY" 233.5
expect_false "leak not seen below 220" e2e_trace_leak_seen "$EV" "$MARK0" "$TIRE_KEY" 220
expect_false "leak not seen before the mark" e2e_trace_leak_seen "$BEFORE" 0 "$TIRE_KEY" 240
NOWARN="${TMP}/nowarn.jsonl"
ev UPSERT "$(device "$TIRE_KEY" 11 u2deb-edge frontLeftPressure=reported:200 lowPressureWarning=reported:false lowPressureWheels=reported:FL)" >"$NOWARN"
expect_false "leak needs lowPressureWarning=true" e2e_trace_leak_seen "$NOWARN" 0 "$TIRE_KEY" 233.5
NOFL="${TMP}/nofl.jsonl"
ev UPSERT "$(device "$TIRE_KEY" 12 u2deb-edge frontLeftPressure=reported:200 lowPressureWarning=reported:true lowPressureWheels=reported:RR)" >"$NOFL"
expect_false "leak needs FL in lowPressureWheels" e2e_trace_leak_seen "$NOFL" 0 "$TIRE_KEY" 233.5

expect_true "node Unknown after the mark" e2e_trace_node_ready_is "$EV" "$MARK0" "$EDGE_KEY" Unknown
expect_false "node True not after the mark" e2e_trace_node_ready_is "$EV" "$MARK0" "$EDGE_KEY" True
expect_true "upsert of one of the keys" e2e_trace_upsert_of "$EV" "$MARK0" "$TIRE_KEY" nodes/x
expect_false "no upsert of other keys" e2e_trace_upsert_of "$EV" "$MARK0" nodes/x nodes/y
expect_true "revision 6 carried" e2e_trace_has_revision "$EV" "$THERMAL_KEY" 6
expect_false "revision 7 is not the thermal device" e2e_trace_has_revision "$EV" "$THERMAL_KEY" 7
expect_eq "resource at revision" "$(e2e_trace_resource_at "$EV" "$TIRE_KEY" 7 | jq -r '.spec.properties[0].reported.value')" 231.0

# ---- option parsing ---------------------------------------------------------------------------

e2e_parse_common_arg --ecu thermal
expect_eq "--ecu consumes two" "$E2E_ARGS_USED" 2
expect_eq "--ecu sets the ECU" "$E2E_ECU" thermal
e2e_parse_common_arg --keep
expect_eq "--keep consumes one" "$E2E_ARGS_USED" 1
expect_eq "--keep sets keep" "$E2E_KEEP" 1
e2e_parse_common_arg --cloud x
expect_eq "unknown option consumes none" "$E2E_ARGS_USED" 0
code=0; (e2e_parse_common_arg --ecu nope) 2>/dev/null || code=$?
expect_eq "--ecu nope is a usage error" "$code" "$E2E_EXIT_USAGE"
code=0; (e2e_parse_common_arg --ecu) 2>/dev/null || code=$?
expect_eq "--ecu without value is a usage error" "$code" "$E2E_EXIT_USAGE"
code=0; (E2E_ECU=both; E2E_ALLOW_ECU_STATE_CHANGE=0; e2e_check_state_change_allowed note) 2>/dev/null || code=$?
expect_eq "tire without --allow-ecu-state-change is refused" "$code" "$E2E_EXIT_USAGE"
code=0; (E2E_ECU=thermal; E2E_ALLOW_ECU_STATE_CHANGE=0; e2e_check_state_change_allowed note) 2>/dev/null || code=$?
expect_eq "thermal needs no flag" "$code" 0
code=0; (E2E_ECU=tire; E2E_ALLOW_ECU_STATE_CHANGE=1; e2e_check_state_change_allowed note) 2>/dev/null || code=$?
expect_eq "tire with the flag is allowed" "$code" 0

# ---- patch and restore against a stub kubectl -------------------------------------------------

DEVICE_JSON="${TMP}/device.json"
PATCHES="${TMP}/patches.log"
jq -n '{spec: {properties: [{name: "leakEnabled", desired: {value: "false"}}, {name: "leakRate", desired: {value: "1.0"}}]}}' >"$DEVICE_JSON"
: >"$PATCHES"
# The stub runs the command string the library would send to the cloud node.
e2e_cloud_exec() { eval "$1"; }
kubectl() {
  case " $* " in
    *" get device "*) cat "$DEVICE_JSON" ;;
    *" patch device "*)
      local p
      p=$(sed -n 's/.* -p \(.*\)$/\1/p' <<<"$*")
      printf '%s\n' "$p" >>"$PATCHES"
      jq --argjson p "$p" '.spec.properties[$p[1].path | split("/")[3] | tonumber].desired.value = $p[1].value' \
        "$DEVICE_JSON" >"${DEVICE_JSON}.new" && mv "${DEVICE_JSON}.new" "$DEVICE_JSON"
      ;;
    *) return 1 ;;
  esac
}
E2E_TRACE_DIR=
expect_eq "desired read" "$(e2e_desired dev leakRate)" 1.0
e2e_remember_desired dev leakEnabled
e2e_remember_desired dev leakRate
e2e_patch_desired dev leakRate 10 2>/dev/null
e2e_patch_desired dev leakEnabled true 2>/dev/null
expect_eq "patch tests the property name" "$(tail -n 1 "$PATCHES" | jq -c '.[0]')" '{"op":"test","path":"/spec/properties/0/name","value":"leakEnabled"}'
expect_eq "patched" "$(e2e_desired dev leakEnabled),$(e2e_desired dev leakRate)" "true,10"
expect_false "unknown property is refused" e2e_patch_desired dev nosuch 1 2>/dev/null
e2e_restore_desired 2>/dev/null
expect_eq "restored" "$(e2e_desired dev leakEnabled),$(e2e_desired dev leakRate)" "false,1.0"
expect_eq "restored newest first" "$(tail -n 2 "$PATCHES" | jq -r '.[0].value' | tr '\n' ' ')" "leakRate leakEnabled "
expect_eq "restore list emptied" "${#E2E_RESTORE[@]}" 0

# ---- manifests --------------------------------------------------------------------------------

CUSTOM=$(jq -n -c '{kind: "List", items: [
  {kind: "ServiceAccount", metadata: {name: "dtpublisher", namespace: "continuumx"}},
  {kind: "Deployment", metadata: {name: "dtpublisher"}, spec: {template: {spec: {containers: [
    {name: "dtpublisher", image: "neotera/dtpublisher:v1.23.0", args: ["serve", "--v=2"]}]}}}},
  {kind: "ClusterRoleBinding", metadata: {name: "dtpublisher"}, subjects: [{kind: "ServiceAccount", name: "dtpublisher", namespace: "continuumx"}]}]}' \
  | e2e_customize docker.io/neotera/dtpublisher:e2e-x u2deb-cloud kubeedge)
expect_eq "customize sets the image" "$(jq -r '.items[1].spec.template.spec.containers[0].image' <<<"$CUSTOM")" docker.io/neotera/dtpublisher:e2e-x
expect_eq "customize adds --enable-query" "$(jq -r '.items[1].spec.template.spec.containers[0].args | join(" ")' <<<"$CUSTOM")" "serve --v=2 --enable-query"
expect_eq "customize pins the node" "$(jq -r '.items[1].spec.template.spec.nodeSelector["kubernetes.io/hostname"]' <<<"$CUSTOM")" u2deb-cloud
expect_eq "customize moves namespaced objects" "$(jq -c '.items[0]' <<<"$CUSTOM")" '{"kind":"ServiceAccount","metadata":{"name":"dtpublisher","namespace":"kubeedge"}}'
expect_eq "customize moves the binding subject" "$(jq -r '.items[2].subjects[0].namespace' <<<"$CUSTOM")" kubeedge
expect_eq "customize leaves cluster-scoped metadata alone" "$(jq -c '.items[2].metadata' <<<"$CUSTOM")" '{"name":"dtpublisher"}'
expect_eq "customize accepts a single object" "$(jq -n -c '{kind: "Service"}' | e2e_customize i n ns | jq -r '.items[0].kind')" Service
expect_eq "manifests are six documents" "$(e2e_manifests | grep -c '^kind:')" 6

printf '\n%d run, %d failed\n' "$RUN" "$FAILS"
[[ "$FAILS" -eq 0 ]]
