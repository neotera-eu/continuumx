#!/usr/bin/env sh
# scripts/sync/rebrand-tier-a.sh
#
# Writes (or overwrites) all Tier A files that ContinuumX owns entirely.
# Run this after every upstream merge to restore files that are either
# absent from upstream or that upstream overwrites with its own version.
#
# Tier A files managed here:
#   README.md                             -- full ContinuumX landing page
#   CODEOWNERS                            -- Neotera review rules
#   .github/workflows/sync-check.yml      -- branding-leak CI gate
#   docs/upstream-sync.md                 -- 8-step sync runbook
#
# Contrast with rebrand.sh (Tier B), which patches upstream-owned files
# in-place using idempotent sed substitutions.
#
# Usage:
#   sh scripts/sync/rebrand-tier-a.sh [--dry-run]
#
# Flags:
#   --dry-run   Print which files would be written without writing them.

set -eu

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DRY_RUN=0

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    *) echo "Unknown flag: $arg"; exit 1 ;;
  esac
done

log() { printf '[rebrand-tier-a] %s\n' "$*"; }

write_file() {
  dest="$1"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "DRY-RUN would write: $dest"
    return
  fi
  mkdir -p "$(dirname "$dest")"
  # Content is piped from stdin by the caller.
  cat > "$dest"
  log "wrote: $dest"
}

# ---------------------------------------------------------------------------
# A0 — README.md
# ---------------------------------------------------------------------------
log "A0: writing README.md"
write_file "$REPO_ROOT/README.md" <<'EOF'
# ContinuumX

[![Go Report Card](https://goreportcard.com/badge/github.com/neotera-eu/continuumx)](https://goreportcard.com/report/github.com/neotera-eu/continuumx)
[![LICENSE](https://img.shields.io/github/license/neotera-eu/continuumx.svg?style=flat-square)](/LICENSE)
[![Releases](https://img.shields.io/github/release/neotera-eu/continuumx/all.svg?style=flat-square)](https://github.com/neotera-eu/continuumx/releases)

> **ContinuumX** is a branded distribution of [KubeEdge](https://github.com/kubeedge/kubeedge), maintained by [Neotera](https://neotera.eu).
> It tracks KubeEdge upstream and layers Neotera-specific integrations on top.
> See [docs/upstream-sync.md](./docs/upstream-sync.md) for the upstream sync runbook.

ContinuumX extends Kubernetes with native edge computing capabilities — bringing containerised application orchestration and device management to edge nodes.
It consists of a cloud component and an edge component, providing core infrastructure support for networking, application deployment, and metadata synchronisation between cloud and edge.
It also supports **MQTT**, enabling edge devices to communicate through edge nodes.

## Advantages

- **Kubernetes-native support**: Manage edge applications and edge devices from the cloud using fully compatible Kubernetes APIs.
- **Cloud-Edge Reliable Collaboration**: Reliable message delivery over unstable cloud-edge networks.
- **Edge Autonomy**: Edge nodes run autonomously even when the cloud-edge network is unstable or the edge is offline.
- **Edge Device Management**: Manage edge devices through Kubernetes native APIs implemented by CRD.
- **Extremely Lightweight Edge Agent**: Minimal footprint EdgeCore for resource-constrained edge hardware.

## How It Works

ContinuumX consists of a cloud part and an edge part.

### Architecture

<div align="center">
<img src="./docs/images/kubeedge_arch.png" width="85%" align="center">
</div>

### In the Cloud
- **CloudHub**: WebSocket server responsible for watching changes at the cloud side, caching and sending messages to EdgeHub.
- **EdgeController**: Extended Kubernetes controller managing edge nodes and pod metadata.
- **DeviceController**: Extended Kubernetes controller managing device metadata and status sync between edge and cloud.

### On the Edge
- **EdgeHub**: WebSocket client interacting with Cloud Service, syncing resource updates and reporting edge status.
- **Edged**: Agent running on edge nodes, managing containerised applications.
- **EventBus**: MQTT client offering publish/subscribe capabilities to other components.
- **ServiceBus**: HTTP client providing REST capabilities to reach HTTP servers running at edge.
- **DeviceTwin**: Stores device status and syncs it to the cloud.
- **MetaManager**: Message processor between Edged and EdgeHub; stores/retrieves metadata in a lightweight SQLite database.

## Kubernetes Compatibility

|                           | Kubernetes 1.27 | Kubernetes 1.28 | Kubernetes 1.29 | Kubernetes 1.30 | Kubernetes 1.31 | Kubernetes 1.32 |
|---------------------------|-----------------|-----------------|-----------------|-----------------|-----------------|-----------------|
| ContinuumX 1.19 (KE 1.19) | ✓               | ✓               | ✓               | -               | -               | -               |
| ContinuumX 1.20 (KE 1.20) | +               | ✓               | ✓               | ✓               | -               | -               |
| ContinuumX 1.21 (KE 1.21) | +               | ✓               | ✓               | ✓               | -               | -               |
| ContinuumX 1.22 (KE 1.22) | +               | +               | ✓               | ✓               | ✓               | -               |
| ContinuumX 1.23 (KE 1.23) | +               | +               | +               | ✓               | ✓               | ✓               |

Key:
* `✓` ContinuumX and the Kubernetes version are exactly compatible.
* `+` ContinuumX has features or API objects that may not be present in the Kubernetes version.
* `-` The Kubernetes version has features or API objects that ContinuumX can't use.

## Getting Started

```bash
# Install the cloud component
cxadm init --advertise-address=<CLOUD_IP>

# Join an edge node
cxadm join --cloudcore-ipport=<CLOUD_IP>:10000 --edgenode-name=<NODE_NAME>
```

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for details on submitting patches, the contribution workflow, and how this project tracks KubeEdge upstream.

## Security

Report suspected vulnerabilities to the Neotera security team.
See [SECURITY.md](.github/SECURITY.md) for details.

## Upstream

ContinuumX tracks the KubeEdge `main` branch. See [docs/upstream-sync.md](./docs/upstream-sync.md) for the sync runbook.

## License

ContinuumX is under the Apache 2.0 license. See the [LICENSE](LICENSE) file for details.
EOF

# ---------------------------------------------------------------------------
# A0b — CODEOWNERS
# ---------------------------------------------------------------------------
log "A0b: writing CODEOWNERS"
write_file "$REPO_ROOT/CODEOWNERS" <<'EOF'
# CODEOWNERS — ContinuumX
# Each pattern maps to one or more GitHub users/teams who must review changes.
# The last matching pattern wins.

# Default: Neotera platform team reviews everything
*                                       @neotera-eu/platform

# Sync scripts and branding — platform team only
/scripts/sync/                          @neotera-eu/platform
/docs/upstream-sync.md                  @neotera-eu/platform

# CI/CD workflows
/.github/workflows/                     @neotera-eu/platform

# Cloud core
/cloud/                                 @neotera-eu/platform
/manifests/                             @neotera-eu/platform

# Edge
/edge/                                  @neotera-eu/platform

# CLI (cxadm)
/keadm/                                 @neotera-eu/platform

# Staging sub-modules (upstream-tracked, changes need extra care)
/staging/                               @neotera-eu/platform
EOF

# ---------------------------------------------------------------------------
# A0c — .github/workflows/sync-check.yml
# ---------------------------------------------------------------------------
log "A0c: writing .github/workflows/sync-check.yml"
write_file "$REPO_ROOT/.github/workflows/sync-check.yml" <<'EOF'
name: ContinuumX Sync Check

on:
  pull_request:
    branches: [ main ]

jobs:
  branding-leak-check:
    name: Verify no kubeedge strings in Tier A/B zones
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Run rebrand verify
        run: sh scripts/sync/rebrand.sh --verify-only

      - name: Check go.mod module path
        run: |
          if grep -q 'module github.com/kubeedge/kubeedge' go.mod; then
            echo "FAIL: go.mod still declares kubeedge module path"
            exit 1
          fi
          echo "OK: go.mod module path is correct"

      - name: Check binary name in Makefile
        run: |
          if grep -q '^\s*keadm\\' Makefile || grep -qP '^\tkeadm $' Makefile; then
            echo "FAIL: Makefile BINARIES still contains 'keadm'"
            exit 1
          fi
          echo "OK: binary name in Makefile is correct"

      - name: Check Docker image org in build scripts
        run: |
          if grep -q '\-t kubeedge/' hack/make-rules/image.sh; then
            echo "FAIL: image.sh still uses kubeedge/ org prefix"
            exit 1
          fi
          echo "OK: image.sh uses correct org prefix"

      - name: Check Helm values image repositories
        run: |
          if grep -q '"kubeedge/' manifests/charts/cloudcore/values.yaml; then
            echo "FAIL: Helm values.yaml still references kubeedge/ image repos"
            exit 1
          fi
          echo "OK: Helm values.yaml image repos are correct"

      - name: Scan Go source for raw kubeedge imports (excluding vendor)
        run: |
          leaks=$(find . -name '*.go' -not -path '*/vendor/*' -not -path '*/.git/*' \
            | xargs grep -l 'github\.com/kubeedge/kubeedge' 2>/dev/null || true)
          if [ -n "$leaks" ]; then
            echo "FAIL: Go files still importing github.com/kubeedge/kubeedge:"
            echo "$leaks"
            exit 1
          fi
          echo "OK: no raw kubeedge imports in Go source"
EOF

# ---------------------------------------------------------------------------
# A0d — docs/upstream-sync.md
# ---------------------------------------------------------------------------
log "A0d: writing docs/upstream-sync.md"
write_file "$REPO_ROOT/docs/upstream-sync.md" <<'EOF'
# Upstream Sync Runbook

ContinuumX tracks the KubeEdge `main` branch. This document describes the exact
steps to pull in a new KubeEdge release or `main` snapshot and re-apply all
ContinuumX branding. Every step is either fully scripted or explicitly verified.

---

## Terminology

| Term | Meaning |
|------|---------|
| `upstream` | The KubeEdge remote (`https://github.com/kubeedge/kubeedge.git`) |
| `origin` | The ContinuumX remote (`https://github.com/neotera-eu/continuumx.git`) |
| Tier A | Owned files (README, CODEOWNERS, docs/, CI workflows) -- restored via `scripts/sync/rebrand-tier-a.sh` |
| Tier B | Go module paths, Dockerfiles, binary constants -- patched via `scripts/sync/rebrand.sh` |
| Tier C | Internal Go identifiers, Kubernetes API groups -- intentionally unchanged |

---

## Prerequisites

```sh
# Verify remotes are configured correctly
git remote -v
# Expected:
#   upstream  https://github.com/kubeedge/kubeedge.git (fetch)
#   upstream  https://github.com/kubeedge/kubeedge.git (push)
#   origin    https://github.com/neotera-eu/continuumx.git (fetch)
#   origin    https://github.com/neotera-eu/continuumx.git (push)

# If upstream is missing, add it:
git remote add upstream https://github.com/kubeedge/kubeedge.git
```

Required tools: `git`, `go` (1.23+), `sed` (GNU or BSD), POSIX `sh`.

---

## Step 1 -- Create a sync branch

```sh
git fetch upstream
git checkout main
git pull origin main
git checkout -b sync/ke-<VERSION>
# e.g.: git checkout -b sync/ke-v1.24.0
```

---

## Step 2 -- Merge upstream

```sh
# Option A: merge a tagged release
git merge upstream/v<VERSION> --no-edit

# Option B: rebase onto latest upstream main
git rebase upstream/main
```

Resolve any conflicts. Conflicts will almost always be in files that ContinuumX
owns (README.md, CONTRIBUTING.md, docs/, scripts/). **Never resolve conflicts by
accepting upstream's version of Tier A files** -- always keep the ContinuumX version
and re-apply the upstream content where relevant.

---

## Step 3 -- Re-apply Tier A owned files

After a merge, upstream may overwrite README.md or delete CODEOWNERS and CI workflows.
Restore all ContinuumX-owned files:

```sh
sh scripts/sync/rebrand-tier-a.sh
```

Expected output ends with:

```
[rebrand-tier-a] wrote: README.md
[rebrand-tier-a] wrote: CODEOWNERS
[rebrand-tier-a] wrote: .github/workflows/sync-check.yml
[rebrand-tier-a] wrote: docs/upstream-sync.md
[rebrand-tier-a] Tier A restore complete.
```

---

## Step 4 -- Re-apply Tier B branding

After every merge or rebase, upstream reintroduces `github.com/kubeedge/kubeedge`
import paths, Dockerfile COPY paths, and binary constants. The rebrand script
replaces them all idempotently:

```sh
sh scripts/sync/rebrand.sh
```

Expected output ends with:

```
[rebrand] OK: all Tier B zones clean.
[rebrand] Rebrand complete.
```

If the script exits non-zero, fix the reported leaks before continuing.

---

## Step 5 -- Update the Go vendor directory

```sh
# Regenerate vendor from go.mod (uses the renamed module path)
GOWORK=off go mod vendor
```

If upstream added new dependencies, `go mod tidy` first:

```sh
GOWORK=off go mod tidy
GOWORK=off go mod vendor
git add vendor/ go.sum
```

---

## Step 6 -- Verify the build

```sh
# Full build (requires Docker)
make all BUILD_WITH_CONTAINER=true

# Or locally with Go 1.23+
make all BUILD_WITH_CONTAINER=false

# Unit tests
make test BUILD_WITH_CONTAINER=false
```

All binaries must compile and all tests must pass before proceeding.

---

## Step 7 -- Validate branding (idempotent)

```sh
sh scripts/sync/rebrand.sh --verify-only
```

This is the same check run by the `sync-check` CI workflow on every PR.

---

## Step 8 -- Update version references (if syncing a tagged release)

If syncing to a specific KubeEdge release (e.g., `v1.24.0`), update the default
version constant in the CLI:

```sh
# File: keadm/cmd/keadm/app/cmd/common/constant.go
# Update: DefaultKubeEdgeVersion = "1.24.0"
```

Also update the Helm chart version:

```sh
# File: manifests/charts/cloudcore/Chart.yaml
# Update: version: 1.24.0 and appVersion: 1.24.0
```

---

## Step 9 -- Commit and push

```sh
git add -p   # stage changes selectively; review Tier A and B files
git commit -m "chore: sync with KubeEdge <VERSION>

Pulls in upstream changes from kubeedge/kubeedge@<COMMIT_SHA>.
Re-applies ContinuumX Tier A files via scripts/sync/rebrand-tier-a.sh.
Re-applies ContinuumX Tier B branding via scripts/sync/rebrand.sh."

git push origin sync/ke-<VERSION>
```

Open a PR against `main`. The `sync-check` CI workflow will run automatically.

---

## Troubleshooting

### `rebrand.sh` reports a Go import leak

A new Go file was added upstream that imports `github.com/kubeedge/kubeedge`. The
script should have already fixed it. If not:

```sh
grep -rn 'github.com/kubeedge/kubeedge' --include='*.go' . | grep -v vendor
```

Check whether the file was added during the merge and was not picked up because it's
in an unusual location. Re-run the rebrand script; it uses `find` recursively so
it should catch all files.

### Merge conflict in `go.mod`

Accept **your** version of the `module` line (`module github.com/neotera-eu/continuumx`),
then merge upstream's `require` block manually. Run `go mod tidy` afterwards.

### Merge conflict in `hack/lib/golang.sh`

The `ALL_BINARIES_AND_TARGETS` array must keep `cxadm:keadm/cmd/keadm` (not
`keadm:keadm/cmd/keadm`). Accept upstream's other changes but preserve this mapping.

### New Dockerfile added upstream

Run `sh scripts/sync/rebrand.sh` -- it scans all Dockerfiles under `build/`
automatically. If the new Dockerfile is outside `build/`, add it to the B3 section
of the script.

---

## Tier C decisions (unchanged from upstream)

The following are intentionally NOT rebranded. Do not change these during sync:

| Item | Reason |
|------|--------|
| Kubernetes API groups (`devices.kubeedge.io`, `apps.kubeedge.io`, etc.) | Protocol identifiers baked into CRD schemas and etcd objects |
| `/etc/kubeedge/` filesystem paths | Operational paths on deployed edge nodes; changing breaks existing installs |
| Internal Go identifiers (`KubeEdgeCustomInformer`, `InitKubeEdgeClient`, etc.) | Renaming causes conflicts on every upstream PR touching those packages |
| Copyright headers in upstream Go files | Legal attribution must remain accurate; Apache 2.0 preserves original notices |
| `vendor/` directory | Auto-generated; do not edit manually |
| Source directory `keadm/` | Renaming the directory causes path-level merge conflicts on every keadm PR |
EOF

log "Tier A restore complete."
