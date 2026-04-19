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
| Tier A | Cosmetic/owned files (README, docs, Helm metadata) -- applied via direct edits |
| Tier B | Go module paths, Dockerfiles, binary constants -- applied via `scripts/sync/rebrand.sh` |
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

## Step 3 -- Re-apply Tier B branding

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

## Step 4 -- Update the Go vendor directory

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

## Step 5 -- Verify the build

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

## Step 6 -- Validate branding (idempotent)

```sh
sh scripts/sync/rebrand.sh --verify-only
```

This is the same check run by the `sync-check` CI workflow on every PR.

---

## Step 7 -- Update version references (if syncing a tagged release)

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

## Step 8 -- Commit and push

```sh
git add -p   # stage changes selectively; review Tier B files
git commit -m "chore: sync with KubeEdge <VERSION>

Pulls in upstream changes from kubeedge/kubeedge@<COMMIT_SHA>.
Re-applies ContinuumX Tier A/B branding via scripts/sync/rebrand.sh."

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
