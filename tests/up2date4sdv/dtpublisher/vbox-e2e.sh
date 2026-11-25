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
# End-to-end check of the Device Twins Resources Publisher (UP2DATE4SDV CID 93) on the VirtualBox
# playground (VM0 cloud, VM1 edge), with the thermal ECU, the tire-pressure ECU or both. Runs on
# the VirtualBox host; commands reach the VMs over SSH and run there with `sudo -n`. No password
# is stored or asked for. See lib/e2e-common.sh for what is checked.

set -Eeuo pipefail

# shellcheck source=lib/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/e2e-common.sh"

E2E_TARGET=vbox
CLOUD_SSH=user@192.168.56.10
EDGE_SSH=user@192.168.56.11
SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=4)
# The playground's VM procedure says not to restart EdgeCore during its tests, so the node
# Ready check (edgecore stop/start) is opt-in here.
E2E_SKIP_NODE_RESTART=1

usage() {
  cat <<'EOF'
Usage: vbox-e2e.sh [options]

Runs the dtpublisher end-to-end check on the VirtualBox playground from the VirtualBox host.
Builds the image with Podman on the host, loads it into containerd on the cloud VM, deploys
build/dtpublisher/, checks what a subscriber receives, and removes the publisher again.

Needs: key-based SSH from this host to both VMs (no password prompt), passwordless sudo on
both VMs, kubectl working as root on the cloud VM (/root/.kube/config), bash as the login shell.
If passwordless sudo is missing, the check stops and prints how to set it up, with a warning
about what it allows.

VirtualBox playground options:
  --cloud USER@HOST           SSH target of the cloud VM (default: user@192.168.56.10)
  --edge USER@HOST            SSH target of the edge VM (default: user@192.168.56.11)
  --ssh-opt OPTION            extra ssh option, repeatable (for example --ssh-opt -i
                              --ssh-opt ~/.ssh/playground, or --ssh-opt -oPort=2222)
  --node-restart              also run the node check: stop and start edgecore on the edge VM
                              (off by default: the playground's VM procedure says not to restart
                              EdgeCore during its tests)

EOF
  e2e_common_usage
  cat <<'EOF'

Tire ECU state on the VirtualBox playground: the ECU keeps its pressure in
/var/lib/continuumx-virtual-tire-ecu/state.json on the edge VM. Switching the leak off does not
restore the lost pressure (playground README). The check sets the threshold to the current
pressure, so it also works after earlier runs, as long as the pressure is above the model's
pressureThreshold minimum.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --cloud | --edge)
      [[ $# -ge 2 && -n "$2" ]] || e2e_die "$E2E_EXIT_USAGE" "$1 needs USER@HOST"
      [[ "$2" != -* ]] || e2e_die "$E2E_EXIT_USAGE" "$1 $2: not an SSH target"
      if [[ "$1" == --cloud ]]; then CLOUD_SSH=$2; else EDGE_SSH=$2; fi
      shift 2
      ;;
    --ssh-opt)
      [[ $# -ge 2 && -n "$2" ]] || e2e_die "$E2E_EXIT_USAGE" "--ssh-opt needs a value"
      SSH_OPTS+=("$2")
      shift 2
      ;;
    --node-restart)
      E2E_SKIP_NODE_RESTART=0
      shift
      ;;
    *)
      e2e_parse_common_arg "$@"
      [[ "$E2E_ARGS_USED" -gt 0 ]] || e2e_die "$E2E_EXIT_USAGE" "unknown argument: $1 (see --help)"
      shift "$E2E_ARGS_USED"
      ;;
  esac
done

e2e_check_state_change_allowed "kept in /var/lib/continuumx-virtual-tire-ecu/state.json on the edge VM; switching the leak off does not restore it"

# vm_exec TARGET CMD: run the shell command string CMD as root on TARGET. The remote login shell
# runs `sudo -n bash -c <CMD quoted>`; -n makes sudo fail instead of asking for a password.
vm_exec() {
  ssh "${SSH_OPTS[@]}" "$1" "sudo -n bash -c $(printf '%q' "$2")"
}

e2e_cloud_exec() { vm_exec "$CLOUD_SSH" "$1" </dev/null; }
e2e_cloud_exec_in() { vm_exec "$CLOUD_SSH" "$1"; }
e2e_edge_exec() { vm_exec "$EDGE_SSH" "$1" </dev/null; }

e2e_target_preconditions() {
  local unit
  local -a units=(edgecore "$E2E_MAPPER_UNIT")
  e2e_wants_thermal && units+=(continuumx-virtual-thermal-ecu)
  e2e_wants_tire && units+=(continuumx-virtual-tire-ecu)
  for unit in "${units[@]}"; do
    if ! e2e_edge_exec "systemctl is-active --quiet ${unit}"; then
      e2e_log "error: ${unit} is not active on ${EDGE_SSH}"
      return 1
    fi
  done
  e2e_cloud_exec "command -v ctr >/dev/null" \
    || { e2e_log "error: ctr not found on ${CLOUD_SSH} (needed to load the image into containerd)"; return 1; }
}

e2e_target_trace() {
  local since="@$((E2E_START_EPOCH - 60))" unit
  for unit in continuumx-virtual-thermal-ecu continuumx-virtual-tire-ecu edgecore; do
    e2e_edge_exec "journalctl -u ${unit} --since $(e2e_quote "$since") --no-pager -o short-iso" \
      >"${E2E_TRACE_DIR}/${unit}.log" 2>&1 || true
  done
  {
    echo "cloud VM: ${CLOUD_SSH}"
    echo "edge VM: ${EDGE_SSH}"
  } >>"${E2E_TRACE_DIR}/versions.txt"
}

# sudo_help TARGET: how to allow passwordless sudo on TARGET, and what that means.
sudo_help() {
  local user=${1%@*}
  [[ "$user" != "$1" ]] || user='<the VM user you log in as>'
  cat >&2 <<EOF

Passwordless sudo is not set up for ${user} on ${1#*@}.

Why it is needed: the check runs its commands on the VMs as root (kubectl, ctr, systemctl,
journalctl, and files under ${E2E_REMOTE_DIR}) with 'sudo -n bash -c ...'. It never asks for,
reads or stores a password, so sudo must not need one.

To allow it, log in to that VM (ssh ${1}) and run:

  echo '${user} ALL=(ALL) NOPASSWD: ALL' | sudo tee /etc/sudoers.d/90-cx-dtp-e2e
  sudo chmod 0440 /etc/sudoers.d/90-cx-dtp-e2e
  sudo visudo -cf /etc/sudoers.d/90-cx-dtp-e2e

and remove it again when you no longer need it:

  sudo rm /etc/sudoers.d/90-cx-dtp-e2e

WARNING: this lets anyone who can log in as ${user} on that VM, or who holds its SSH key, run any
command as root without a password. Do it only on disposable playground VMs, never on a shared
or production machine. A rule limited to single commands does not help here: the check runs
'bash -c' as root, which is equivalent to full root access.

EOF
}

# Access checks, before anything else: SSH without a prompt and sudo without a password.
e2e_require_tools ssh
for target in "$CLOUD_SSH" "$EDGE_SSH"; do
  if ! out=$(ssh "${SSH_OPTS[@]}" "$target" true </dev/null 2>&1); then
    e2e_die "$E2E_EXIT_PRECONDITION" "cannot SSH to ${target} without a prompt (key-based SSH needed; see --ssh-opt): ${out}"
  fi
  if ! out=$(vm_exec "$target" true </dev/null 2>&1); then
    if [[ "$out" == *password* || "$out" == *"terminal is required"* ]]; then
      e2e_log "warning: sudo on ${target} asks for a password: ${out}"
      sudo_help "$target"
      e2e_die "$E2E_EXIT_PRECONDITION" "passwordless sudo is not set up on ${target}; see above"
    fi
    e2e_die "$E2E_EXIT_PRECONDITION" "sudo -n bash -c true failed on ${target}: ${out}"
  fi
done
e2e_cloud_exec "kubectl version --request-timeout=10s >/dev/null 2>&1" \
  || e2e_die "$E2E_EXIT_PRECONDITION" "kubectl does not work as root on ${CLOUD_SSH} (the VM0 guide copies the kubeconfig to /root/.kube/config)"

e2e_main
