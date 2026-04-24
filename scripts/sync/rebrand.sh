#!/usr/bin/env sh
# scripts/sync/rebrand.sh
#
# Applies all Tier B ContinuumX rebranding after an upstream KubeEdge sync.
# Safe to re-run: every substitution is idempotent (already-replaced strings
# are not present in the upstream source, so a second run is a no-op).
#
# Usage:
#   sh scripts/sync/rebrand.sh [--dry-run] [--verify-only]
#
# Flags:
#   --dry-run       Print what would change without writing any files.
#   --verify-only   Exit non-zero if any Tier A/B zone still contains a raw
#                   'kubeedge' string that should have been replaced.

set -eu

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DRY_RUN=0
VERIFY_ONLY=0

for arg in "$@"; do
  case "$arg" in
    --dry-run)      DRY_RUN=1 ;;
    --verify-only)  VERIFY_ONLY=1 ;;
    *) echo "Unknown flag: $arg"; exit 1 ;;
  esac
done

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { printf '[rebrand] %s\n' "$*"; }

# sed in-place, portable across GNU and BSD sed.
# Usage: sed_inplace PATTERN FILE...
sed_inplace() {
  pattern="$1"; shift
  if [ "$DRY_RUN" -eq 1 ]; then
    for f in "$@"; do
      if grep -qE -- "$pattern" "$f" 2>/dev/null; then
        log "DRY-RUN would modify: $f"
      fi
    done
    return
  fi
  for f in "$@"; do
    sed -i.rebrnd_bak "$pattern" "$f" && rm -f "${f}.rebrnd_bak"
  done
}

# Find all non-vendor Go files.
go_files() {
  find "$REPO_ROOT" -name '*.go' \
    -not -path '*/vendor/*' \
    -not -path '*/.git/*'
}

# ---------------------------------------------------------------------------
# B1 — Go module path: github.com/kubeedge/kubeedge → github.com/neotera-eu/continuumx
# ---------------------------------------------------------------------------
log "B1: rewriting Go import paths (github.com/kubeedge/kubeedge → github.com/neotera-eu/continuumx)"

# Use xargs in batches to avoid ARG_MAX limits.
# The staging sub-modules (api, beehive, mapper-framework) are intentionally
# kept at github.com/kubeedge/* and are NOT touched here.
go_files | xargs -P4 grep -l 'github\.com/kubeedge/kubeedge' 2>/dev/null | while IFS= read -r f; do
  sed_inplace 's|github\.com/kubeedge/kubeedge|github.com/neotera-eu/continuumx|g' "$f"
done

# ---------------------------------------------------------------------------
# B1b — hack/lib/init.sh: KUBEEDGE_GO_PACKAGE used for go build package paths
#        and version ldflags — must match the Go module name.
# ---------------------------------------------------------------------------
log "B1b: updating KUBEEDGE_GO_PACKAGE in hack/lib/init.sh"
INIT_SH="$REPO_ROOT/hack/lib/init.sh"
if [ -f "$INIT_SH" ]; then
  sed_inplace 's|readonly KUBEEDGE_GO_PACKAGE="github\.com/kubeedge/kubeedge"|readonly KUBEEDGE_GO_PACKAGE="github.com/neotera-eu/continuumx"|' "$INIT_SH"
fi

# ---------------------------------------------------------------------------
# B2 — go.mod module declaration
# ---------------------------------------------------------------------------
log "B2: updating go.mod module declaration"
sed_inplace 's|^module github\.com/kubeedge/kubeedge$|module github.com/neotera-eu/continuumx|' \
  "$REPO_ROOT/go.mod"

# go.work: use directives are relative paths — no path change needed.
# The module name line is not present in go.work; nothing to change.

# ---------------------------------------------------------------------------
# B3 — Dockerfile COPY/build paths
# ---------------------------------------------------------------------------
log "B3: updating Dockerfile COPY and go build paths"

find "$REPO_ROOT/build" \( -name 'Dockerfile*' -o -name '*.dockerfile' \) \
  -not -path '*/vendor/*' | while IFS= read -r f; do
  sed_inplace 's|github\.com/kubeedge/kubeedge|github.com/neotera-eu/continuumx|g' "$f"
done

# installation-package.dockerfile: binary name keadm → cxadm
INSTALL_PKG_DF="$REPO_ROOT/build/docker/installation-package/installation-package.dockerfile"
if [ -f "$INSTALL_PKG_DF" ]; then
  sed_inplace 's|make WHAT=keadm|make WHAT=cxadm|g' "$INSTALL_PKG_DF"
  sed_inplace 's|bin/keadm|bin/cxadm|g' "$INSTALL_PKG_DF"
  sed_inplace 's|/usr/local/bin/keadm|/usr/local/bin/cxadm|g' "$INSTALL_PKG_DF"
fi

# Image build scripts: docker tag org  kubeedge/ → neotera/
log "B3b: updating image org in hack/make-rules build scripts"
for f in "$REPO_ROOT/hack/make-rules/image.sh" \
         "$REPO_ROOT/hack/make-rules/crossbuildimage.sh"; do
  if [ -f "$f" ]; then
    sed_inplace 's|-t kubeedge/|-t neotera/|g' "$f"
    sed_inplace "s|IMAGE_REPO_NAME:-kubeedge|IMAGE_REPO_NAME:-neotera|g" "$f"
  fi
done

# Helm values.yaml: image repository org  kubeedge/ → neotera/
log "B3c: updating Helm values image repositories"
HELM_VALUES="$REPO_ROOT/manifests/charts/cloudcore/values.yaml"
if [ -f "$HELM_VALUES" ]; then
  sed_inplace 's|"kubeedge/|"neotera/|g' "$HELM_VALUES"
fi

# Helm Chart.yaml: source URL and description
CHART_YAML="$REPO_ROOT/manifests/charts/cloudcore/Chart.yaml"
if [ -f "$CHART_YAML" ]; then
  sed_inplace 's|https://github.com/kubeedge/kubeedge|https://github.com/neotera-eu/continuumx|g' "$CHART_YAML"
  sed_inplace 's|The KubeEdge cloudcore component|The ContinuumX cloudcore component|g' "$CHART_YAML"
fi

# ---------------------------------------------------------------------------
# B4 — Binary name constants and user-visible tool strings
# ---------------------------------------------------------------------------
log "B4: updating binary name constants (keadm → cxadm)"

# KeadmBinaryName constant
CONST_FILE="$REPO_ROOT/staging/src/github.com/kubeedge/api/apis/common/constants/default.go"
if [ -f "$CONST_FILE" ]; then
  sed_inplace 's|KeadmBinaryName\s*=\s*"keadm"|KeadmBinaryName    = "cxadm"|' "$CONST_FILE"
fi

# UpgradeTool field in taskmanager executor
EXECUTOR_FILE="$REPO_ROOT/cloud/pkg/taskmanager/v1alpha1/manager/executor.go"
if [ -f "$EXECUTOR_FILE" ]; then
  sed_inplace 's|UpgradeTool: "keadm"|UpgradeTool: "cxadm"|g' "$EXECUTOR_FILE"
fi

# Release tarball path in batch.go (keadm-<ver>-linux-<arch>/keadm/keadm)
BATCH_FILE="$REPO_ROOT/keadm/cmd/keadm/app/cmd/edge/batch.go"
if [ -f "$BATCH_FILE" ]; then
  sed_inplace 's|keadm-%s-linux-%s/keadm/keadm|cxadm-%s-linux-%s/cxadm/cxadm|g' "$BATCH_FILE"
  sed_inplace 's|filepath\.Join(baseDir, "keadm")|filepath.Join(baseDir, "cxadm")|g' "$BATCH_FILE"
fi

# Node version string reported by edgecore to the API server
EDGED_FILE="$REPO_ROOT/edge/pkg/edged/edged.go"
if [ -f "$EDGED_FILE" ]; then
  sed_inplace 's|-kubeedge-|-continuumx-|g' "$EDGED_FILE"
fi

# ---------------------------------------------------------------------------
# B5 — Project name constants, version banner, Prometheus namespace, user messages
# ---------------------------------------------------------------------------
log "B5: updating project name constants and user-visible strings"

# ProjectName / SystemName constants
COMMON_CONST="$REPO_ROOT/common/constants/default.go"
if [ -f "$COMMON_CONST" ]; then
  sed_inplace 's|ProjectName\s*=\s*"KubeEdge"|ProjectName                  = "ContinuumX"|' "$COMMON_CONST"
  sed_inplace 's|SystemName\s*=\s*"kubeedge"|SystemName                   = "continuumx"|' "$COMMON_CONST"
fi

# Version banner: "KubeEdge <version>"
VERFLAG="$REPO_ROOT/pkg/version/verflag/verflag.go"
if [ -f "$VERFLAG" ]; then
  sed_inplace 's|"KubeEdge %s\\n"|"ContinuumX %s\\n"|g' "$VERFLAG"
fi

# Prometheus metric namespace
MONITOR="$REPO_ROOT/cloud/pkg/common/monitor/monitor.go"
if [ -f "$MONITOR" ]; then
  sed_inplace 's|metricNamespace = "KubeEdge"|metricNamespace = "ContinuumX"|' "$MONITOR"
fi

# cxadm CLI user-facing status messages (join/init output)
for f in \
  "$REPO_ROOT/keadm/cmd/keadm/app/cmd/util/common_others.go" \
  "$REPO_ROOT/keadm/cmd/keadm/app/cmd/util/cloudinstaller.go" \
  "$REPO_ROOT/keadm/cmd/keadm/app/cmd/edge/join_others.go" \
  "$REPO_ROOT/keadm/cmd/keadm/app/cmd/edge/join_windows.go"; do
  if [ -f "$f" ]; then
    sed_inplace 's|KubeEdge edgecore is running|ContinuumX edgecore is running|g' "$f"
    sed_inplace 's|KubeEdge cloudcore is running|ContinuumX cloudcore is running|g' "$f"
    sed_inplace 's|KubeEdge Node unique identification|ContinuumX Node unique identification|g' "$f"
    sed_inplace 's|KubeEdge Edge Node RemoteRuntimeEndpoint|ContinuumX Edge Node RemoteRuntimeEndpoint|g' "$f"
  fi
done

# cloudcore informer log message
INFORMER_MGR="$REPO_ROOT/cloud/pkg/common/informers/informer_manager.go"
if [ -f "$INFORMER_MGR" ]; then
  sed_inplace 's|"KubeEdge CRD resource|"ContinuumX CRD resource|g' "$INFORMER_MGR"
fi

# YAML manifest comments
for f in \
  "$REPO_ROOT/manifests/profiles/version.yaml" \
  "$REPO_ROOT/manifests/charts/cloudcore/values.yaml"; do
  if [ -f "$f" ]; then
    sed_inplace 's|once KubeEdge is enabled|once ContinuumX is enabled|g' "$f"
  fi
done

# e2e test suite name
E2E_TEST="$REPO_ROOT/tests/e2e/e2e_test.go"
if [ -f "$E2E_TEST" ]; then
  sed_inplace 's|"KubeEdge e2e suite"|"ContinuumX e2e suite"|g' "$E2E_TEST"
fi

# Cilium integration scripts
for f in \
  "$REPO_ROOT/hack/cilium_e2e_test.sh" \
  "$REPO_ROOT/hack/configure_cilium.sh"; do
  if [ -f "$f" ]; then
    sed_inplace 's|for KubeEdge compatibility|for ContinuumX compatibility|g' "$f"
    sed_inplace 's|with KubeEdge permissions|with ContinuumX permissions|g' "$f"
    sed_inplace 's|with KubeEdge by making|with ContinuumX by making|g' "$f"
    sed_inplace 's|KubeEdge Cilium Integration Script|ContinuumX Cilium Integration Script|g' "$f"
    sed_inplace 's|installing KubeEdge with keadm|install ContinuumX with cxadm|g' "$f"
    sed_inplace 's|cilium-kubeedge DaemonSet|cilium-continuumx DaemonSet|g' "$f"
  fi
done

# ---------------------------------------------------------------------------
# A1 — Makefile: rename keadm binary in BINARIES list
# ---------------------------------------------------------------------------
log "A1: Makefile BINARIES list (keadm → cxadm)"
MAKEFILE="$REPO_ROOT/Makefile"
if [ -f "$MAKEFILE" ]; then
  sed_inplace 's|^\tkeadm \\$|\tcxadm \\|' "$MAKEFILE"
fi

# ---------------------------------------------------------------------------
# A2 — hack/lib/golang.sh: binary-to-source mapping + output name fix
# ---------------------------------------------------------------------------
log "A2: hack/lib/golang.sh binary mapping and build function"
GOLANG_SH="$REPO_ROOT/hack/lib/golang.sh"
if [ -f "$GOLANG_SH" ]; then
  # Map cxadm output name to the keadm source tree
  sed_inplace 's|  keadm:keadm/cmd/keadm|  cxadm:keadm/cmd/keadm|' "$GOLANG_SH"

  # The build loop derives the output name from the last component of the package
  # path (e.g. "keadm" from ".../keadm/cmd/keadm"). Inject a one-line shim that
  # remaps "keadm" → "cxadm" immediately after the name is assigned.
  # Idempotent: the shim line contains "cxadm" so the grep guard prevents double-injection.
  if ! grep -q 'name.*=.*cxadm' "$GOLANG_SH" 2>/dev/null; then
    if [ "$DRY_RUN" -eq 0 ]; then
      sed_inplace 's|    local name="${bin##\*/}"|    local name="${bin##*/}"; [ "$name" = "keadm" ] \&\& name="cxadm"|' "$GOLANG_SH"
    else
      log "DRY-RUN would inject cxadm name shim in hack/lib/golang.sh"
    fi
  fi
fi

# ---------------------------------------------------------------------------
# A3 — keadm CLI: cobra Use:/Short: strings, FlagSet name, help text
# ---------------------------------------------------------------------------
log "A3: keadm CLI user-visible strings (keadm → cxadm)"

# cmd_others.go and cmd_windows.go: banner, Use:, Short:
for f in "$REPO_ROOT/keadm/cmd/keadm/app/cmd/cmd_others.go" \
         "$REPO_ROOT/keadm/cmd/keadm/app/cmd/cmd_windows.go"; do
  if [ -f "$f" ]; then
    sed_inplace 's|KEADM |CXADM |g' "$f"
    sed_inplace 's|bootstrap a KubeEdge cluster|bootstrap a ContinuumX cluster|g' "$f"
    sed_inplace 's|Use:.*"keadm"|Use:     "cxadm"|g' "$f"
    sed_inplace 's|Short:.*"keadm:.*"|Short:   "cxadm: Bootstrap ContinuumX cluster"|g' "$f"
    sed_inplace 's|sudo keadm |sudo cxadm |g' "$f"
    sed_inplace 's|# sudo keadm |# sudo cxadm |g' "$f"
    sed_inplace 's|https://github.com/neotera-eu/continuumx/issues              |https://github.com/neotera-eu/continuumx/issues          |g' "$f"
  fi
done

# keadm.go: FlagSet name
KEADM_MAIN="$REPO_ROOT/keadm/cmd/keadm/app/keadm.go"
if [ -f "$KEADM_MAIN" ]; then
  sed_inplace 's|flag\.NewFlagSet("keadm"|flag.NewFlagSet("cxadm"|g' "$KEADM_MAIN"
fi

# All keadm command source files: user-visible "keadm <cmd>" strings
# (multi-pattern sed called directly to avoid the single-pattern sed_inplace limit)
_keadm_cmd_seds='-e s|"keadm gettoken"|"cxadm gettoken"|g
-e s|"keadm init"|"cxadm init"|g
-e s|"keadm manifest"|"cxadm manifest"|g
-e s|"keadm reset"|"cxadm reset"|g
-e s|"keadm join"|"cxadm join"|g
-e s|"keadm debug"|"cxadm debug"|g
-e s|"keadm deprecated|"cxadm deprecated|g
-e s|keadm gettoken |cxadm gettoken |g
-e s|keadm init |cxadm init |g
-e s|keadm manifest |cxadm manifest |g
-e s|keadm reset |cxadm reset |g
-e s|keadm join |cxadm join |g
-e s|keadm upgrade |cxadm upgrade |g
-e s|keadm debug |cxadm debug |g
-e s|Short:.*"keadm |Short:   "cxadm |g
-e s|keadm version |cxadm version |g
-e s|keadm will |cxadm will |g
-e s|keadm version error|cxadm version error|g'

find "$REPO_ROOT/keadm/cmd/keadm/app/cmd" -name "*.go" \
  -not -name "*_test.go" | while IFS= read -r f; do
  if [ "$DRY_RUN" -eq 1 ]; then
    if grep -qE 'keadm (gettoken|init|manifest|reset|join|upgrade|debug|will|version)' "$f" 2>/dev/null; then
      log "DRY-RUN would update keadm strings in: $f"
    fi
  else
    sed -i.rebrnd_bak \
      -e 's|"keadm gettoken"|"cxadm gettoken"|g' \
      -e 's|"keadm init"|"cxadm init"|g' \
      -e 's|"keadm manifest"|"cxadm manifest"|g' \
      -e 's|"keadm reset"|"cxadm reset"|g' \
      -e 's|"keadm join"|"cxadm join"|g' \
      -e 's|"keadm debug"|"cxadm debug"|g' \
      -e 's|"keadm deprecated|"cxadm deprecated|g' \
      -e 's|keadm gettoken |cxadm gettoken |g' \
      -e 's|keadm init |cxadm init |g' \
      -e 's|keadm manifest |cxadm manifest |g' \
      -e 's|keadm reset |cxadm reset |g' \
      -e 's|keadm join |cxadm join |g' \
      -e 's|keadm upgrade |cxadm upgrade |g' \
      -e 's|keadm debug |cxadm debug |g' \
      -e 's|Short: "keadm |Short: "cxadm |g' \
      -e 's|keadm version |cxadm version |g' \
      -e 's|keadm will |cxadm will |g' \
      -e 's|keadm version error|cxadm version error|g' \
      "$f" && rm -f "${f}.rebrnd_bak"
  fi
done

# ---------------------------------------------------------------------------
# A4 — .github issue templates and SECURITY.md
# ---------------------------------------------------------------------------
log "A4: .github issue templates and SECURITY.md"

find "$REPO_ROOT/.github/ISSUE_TEMPLATE" -name "*.md" 2>/dev/null | while IFS= read -r f; do
  sed_inplace 's/KubeEdge/ContinuumX/g; s/kubeedge/continuumx/g' "$f"
done

if [ -f "$REPO_ROOT/.github/SECURITY.md" ]; then
  sed_inplace 's/KubeEdge/ContinuumX/g' "$REPO_ROOT/.github/SECURITY.md"
  sed_inplace 's|cncf-kubeedge-security@lists.cncf.io|security@neotera.eu|g' "$REPO_ROOT/.github/SECURITY.md"
fi

if [ -f "$REPO_ROOT/.github/PULL_REQUEST_TEMPLATE.md" ]; then
  sed_inplace 's/KubeEdge/ContinuumX/g' "$REPO_ROOT/.github/PULL_REQUEST_TEMPLATE.md"
fi

# ---------------------------------------------------------------------------
# A5 — CONTRIBUTING.md: prepend ContinuumX header
# ---------------------------------------------------------------------------
log "A5: CONTRIBUTING.md upstream-tracking header"
CONTRIB="$REPO_ROOT/CONTRIBUTING.md"
if [ -f "$CONTRIB" ] && ! grep -q 'ContinuumX' "$CONTRIB" 2>/dev/null; then
  if [ "$DRY_RUN" -eq 0 ]; then
    TMP=$(mktemp)
    cat > "$TMP" <<'HEADER'
# Contributing to ContinuumX

> **ContinuumX** is a branded distribution of [KubeEdge](https://github.com/kubeedge/kubeedge)
> maintained by [Neotera](https://neotera.eu). It tracks KubeEdge upstream.
> See [docs/upstream-sync.md](docs/upstream-sync.md) for the sync runbook.
>
> When submitting changes, run `sh scripts/sync/rebrand.sh --verify-only` to confirm
> no raw `kubeedge` strings have leaked into Tier A/B zones.

---

HEADER
    cat "$CONTRIB" >> "$TMP"
    mv "$TMP" "$CONTRIB"
  else
    log "DRY-RUN would prepend ContinuumX header to CONTRIBUTING.md"
  fi
fi

# ---------------------------------------------------------------------------
# Verify step (always runs; also triggered by --verify-only)
#
# We make targeted checks rather than scanning all of docs/ wholesale:
#   - docs/ contains historical design proposals with Kubernetes API group
#     strings (devices.kubeedge.io, rules.kubeedge.io, etc.) and upstream
#     GitHub links — these are intentionally Tier C and are NOT checked here.
#   - We only verify the critical Tier B zones that must be clean.
# ---------------------------------------------------------------------------
log "Verify: targeted Tier B leak checks..."

LEAKS=0

# 1. go.mod module declaration
if grep -q 'module github\.com/kubeedge/kubeedge' "$REPO_ROOT/go.mod" 2>/dev/null; then
  log "LEAK: go.mod still declares module github.com/kubeedge/kubeedge"
  LEAKS=$((LEAKS + 1))
fi

# 1b. hack/lib/init.sh must declare neotera-eu package path
if grep -q 'KUBEEDGE_GO_PACKAGE="github\.com/kubeedge/kubeedge"' "$REPO_ROOT/hack/lib/init.sh" 2>/dev/null; then
  log "LEAK: hack/lib/init.sh still declares KUBEEDGE_GO_PACKAGE as github.com/kubeedge/kubeedge"
  LEAKS=$((LEAKS + 1))
fi

# 2. No Go file outside vendor/ should import github.com/kubeedge/kubeedge
found=$(go_files | xargs grep -l 'github\.com/kubeedge/kubeedge' 2>/dev/null || true)
if [ -n "$found" ]; then
  log "LEAK: Go files still importing github.com/kubeedge/kubeedge:"
  echo "$found"
  LEAKS=$((LEAKS + 1))
fi

# 3. Image build scripts must not push kubeedge/ org
for f in "$REPO_ROOT/hack/make-rules/image.sh" \
         "$REPO_ROOT/hack/make-rules/crossbuildimage.sh"; do
  if [ -f "$f" ] && grep -q 'kubeedge/' "$f" 2>/dev/null; then
    log "LEAK: $f still references kubeedge/ image org"
    LEAKS=$((LEAKS + 1))
  fi
done

# 4. Helm values.yaml must not have kubeedge/ image repository
HELM_VALUES="$REPO_ROOT/manifests/charts/cloudcore/values.yaml"
if [ -f "$HELM_VALUES" ] && grep -q 'repository:.*"kubeedge/' "$HELM_VALUES" 2>/dev/null; then
  log "LEAK: Helm values.yaml still references kubeedge/ image repositories"
  LEAKS=$((LEAKS + 1))
fi

# 5. Dockerfiles under build/ must not COPY to github.com/kubeedge/kubeedge path
found=$(find "$REPO_ROOT/build" -name 'Dockerfile*' \
        | xargs grep -l 'github\.com/kubeedge/kubeedge' 2>/dev/null || true)
if [ -n "$found" ]; then
  log "LEAK: Dockerfiles still reference github.com/kubeedge/kubeedge path:"
  echo "$found"
  LEAKS=$((LEAKS + 1))
fi

# 6. README must lead with "ContinuumX", not "KubeEdge" (Tier A check)
if [ -f "$REPO_ROOT/README.md" ] && head -1 "$REPO_ROOT/README.md" | grep -q 'KubeEdge' 2>/dev/null; then
  log "FAIL: README.md still has KubeEdge as the top heading — apply Tier A changes"
  LEAKS=$((LEAKS + 1))
fi

if [ "$LEAKS" -gt 0 ]; then
  log "FAIL: $LEAKS Tier B leak(s) found. Fix before committing."
  exit 1
fi

log "OK: all Tier B zones clean."
log "Rebrand complete."
