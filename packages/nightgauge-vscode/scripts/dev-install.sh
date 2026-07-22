#!/usr/bin/env bash
# Build, package, and install the latest VSIX into VS Code.
#
# Usage:
#   ./scripts/dev-install.sh                # Build locally and install
#   ./scripts/dev-install.sh --from-release # Download latest release from GitHub and install

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PKG_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$PKG_DIR/../.." && pwd)"

# ---------------------------------------------------------------------------
# --from-release: download the latest .vsix from GitHub Releases and install
# ---------------------------------------------------------------------------
if [[ "${1:-}" == "--from-release" ]]; then
  echo "==> Downloading latest release from GitHub..."

  # Require gh CLI
  if ! command -v gh &>/dev/null; then
    echo "ERROR: 'gh' CLI is required for --from-release. Install: https://cli.github.com"
    exit 1
  fi

  TMPDIR_RELEASE=$(mktemp -d)
  trap 'rm -rf "$TMPDIR_RELEASE"' EXIT

  # Download the .vsix asset from the latest release
  gh release download --dir "$TMPDIR_RELEASE" --pattern "*.vsix" --clobber

  VSIX=$(ls -t "$TMPDIR_RELEASE"/*.vsix 2>/dev/null | head -1)
  if [[ -z "$VSIX" ]]; then
    echo "ERROR: No .vsix found in latest GitHub release."
    echo "       Has the Release workflow run? Check: gh release list"
    exit 1
  fi

  echo "==> Installing $VSIX..."
  code --install-extension "$VSIX" --force

  echo "==> Done. Reload VS Code window (Cmd+Shift+P → 'Reload Window')."
  exit 0
fi

# ---------------------------------------------------------------------------
# Default: local build and install
# ---------------------------------------------------------------------------

echo "==> Building Go binary (nightgauge serve — IPC backend)..."
cd "$REPO_ROOT"
make build-cli
echo "    Built: bin/nightgauge ($(./bin/nightgauge version 2>/dev/null || echo 'dev'))"

echo "==> Bundling binary into extension dist/bin/..."
DIST_BIN="$PKG_DIR/dist/bin"
mkdir -p "$DIST_BIN"
cp "$REPO_ROOT/bin/nightgauge" "$DIST_BIN/nightgauge"
chmod +x "$DIST_BIN/nightgauge"
echo "    Copied to dist/bin/nightgauge"

echo "==> Building Nightgauge SDK CLI (required by Codex stage runner)..."
npm run -w @nightgauge/sdk build

cd "$PKG_DIR"

# Remove old .vsix files before building
rm -f *.vsix

# Inject a dev build version so each install is unique in VSCode.
# Uses unix timestamp as the patch so every rebuild bumps the version —
# `code --install-extension` refuses to reinstall the same version while
# VSCode is running, so a stable patch (e.g. git commit count) silently
# breaks reinstalls between commits.
# The original version is restored after packaging — both package.json AND
# package-lock.json are reverted to avoid polluting git state.
ORIG_VERSION=$(node -p "require('./package.json').version")
BUILD_NUM=$(date +%s)
DEV_VERSION="${ORIG_VERSION%.*}.$BUILD_NUM"
echo "==> Dev version: $DEV_VERSION"
npm version "$DEV_VERSION" --no-git-tag-version --allow-same-version --engine-strict=false >/dev/null 2>&1

# Restore original version on exit (even if build fails)
# Restores both package.json and package-lock.json via git checkout
trap 'git -C "$REPO_ROOT" checkout -- "$PKG_DIR/package.json" "$REPO_ROOT/package-lock.json" 2>/dev/null || npm version "$ORIG_VERSION" --no-git-tag-version --allow-same-version >/dev/null 2>&1' EXIT

echo "==> Building..."
# Split the canonical `npm run package` (build + vsce package) so we can stamp
# `dist/build-info.json` AFTER `npm run build` (which writes the bundle and
# assets into dist/) but BEFORE `vsce package` (which freezes dist/ into the
# .vsix). Previously the stamp ran much earlier in this script — any later
# `dist/` rewrite by the build pipeline could omit it from the final VSIX,
# leaving ExtensionStalenessService unable to read provenance after install.
# Issue #3650 (Part B): make the stamp's presence in the .vsix non-negotiable.
npm run build

# Stamp build provenance into dist/build-info.json so ExtensionStalenessService
# (Issue #3300) can compare what's running against the workspace HEAD at runtime
# and refuse autonomous dispatch when the deployed build lags behind
# critical-path commits.
#
# Stamping happens here — AFTER `npm run build` (which runs `build:bundle` +
# `build:assets`, the two paths that write into dist/) and BEFORE `vsce
# package` (which freezes dist/ into the .vsix). This sequencing guarantees
# build-info.json survives into the artifact regardless of what the asset
# bundler does. Issue #3650 (Part B).
#
# SKIP_BUILD_INFO=1 is honored ONLY for CI environments without git history
# (the release workflow stamps from its own GitHub Actions step). For local
# dev installs we fail loud at the end if the file is missing rather than
# silently producing a stale-detection-blind extension.
if [ "${SKIP_BUILD_INFO:-0}" != "1" ]; then
  echo "==> Stamping build provenance into dist/build-info.json (Issue #3300)..."
  BUILD_SHA=$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || echo "unknown")
  BUILD_BRANCH=$(git -C "$REPO_ROOT" branch --show-current 2>/dev/null || echo "unknown")
  COMMIT_TIMESTAMP=$(git -C "$REPO_ROOT" log -1 --format=%cI HEAD 2>/dev/null || echo "unknown")
  BUILD_TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  cat > "$PKG_DIR/dist/build-info.json" <<EOF_BUILD_INFO
{
  "commitSha": "$BUILD_SHA",
  "branch": "$BUILD_BRANCH",
  "commitTimestamp": "$COMMIT_TIMESTAMP",
  "buildTimestamp": "$BUILD_TIMESTAMP",
  "schemaVersion": "1"
}
EOF_BUILD_INFO
  echo "    commitSha: $BUILD_SHA ($BUILD_BRANCH @ $COMMIT_TIMESTAMP)"

  # Fail loud if the file we just wrote isn't actually present + parseable.
  # `cat > ... <<EOF` followed by a successful exit doesn't guarantee a
  # readable JSON file (disk full, permission flip mid-write, etc.) — be
  # explicit so a future dev-install regression can never silently produce
  # an unstamped build without surfacing here.
  if ! "$SCRIPT_DIR/check-build-info.sh" "$PKG_DIR/dist/build-info.json"; then
    echo "ERROR: dist/build-info.json was not written or is not valid JSON."
    echo "       ExtensionStalenessService cannot detect stale installs without it."
    exit 1
  fi
else
  echo "WARNING: SKIP_BUILD_INFO=1 — skipping provenance stamp."
  echo "         ExtensionStalenessService will report 'unknown' state for this install."
fi

# Resolve `vsce` from the repo's hoisted node_modules. Calling bare `vsce`
# works inside `npm run package` (npm prepends node_modules/.bin to PATH for
# script steps) but fails when the script is invoked from a shell directly —
# which is exactly the dev-install path. #3650 (B) split the build/package
# steps to insert the build-info stamp between them, which moved this call
# out of npm-script context. Use the hoisted bin explicitly so the script
# works in both contexts.
VSCE_BIN="$REPO_ROOT/node_modules/.bin/vsce"
if [[ ! -x "$VSCE_BIN" ]]; then
  # Fallback to npx (slow but works) so an unhoisted dev environment still
  # produces a usable artifact rather than a confusing "command not found".
  VSCE_BIN="npx --yes @vscode/vsce"
fi
$VSCE_BIN package --no-dependencies

# Find the most recently created .vsix file (handles any version)
VSIX=$(ls -t *.vsix 2>/dev/null | head -1)

if [[ -z "$VSIX" ]]; then
  echo "ERROR: No .vsix file found after build."
  exit 1
fi

# Clean up old extension versions before installing the new one.
# VSCode keeps every prior version on disk; they accumulate quickly with dev builds.
EXT_DIR="$HOME/.vscode/extensions"
EXT_PREFIX="nightgauge.nightgauge-vscode-"
OLD_VERSIONS=$(ls -d "${EXT_DIR}/${EXT_PREFIX}"* 2>/dev/null | sort -t- -k4 -V | sed '$d')
if [[ -n "$OLD_VERSIONS" ]]; then
  OLD_COUNT=$(echo "$OLD_VERSIONS" | wc -l | tr -d ' ')
  echo "==> Cleaning up $OLD_COUNT old extension version(s)..."
  echo "$OLD_VERSIONS" | xargs rm -rf
fi

echo "==> Installing $VSIX..."
code --install-extension "$VSIX" --force

# Fail-loud assertion: after install, the installed extension directory MUST
# contain dist/build-info.json. Pre-#3650 the script silently allowed the
# stamp to go missing (build pipeline rewrite, vsce ignore rule, etc.) which
# left ExtensionStalenessService stuck in `state: "unknown"` and the user
# unable to tell whether the running build was current. Verify here so the
# install fails visibly instead of producing a stale-blind extension.
if [ "${SKIP_BUILD_INFO:-0}" != "1" ]; then
  # Pick the version we just installed — VSCode lays out
  # ~/.vscode/extensions/<publisher>.<name>-<version>/dist/build-info.json.
  INSTALLED_VERSION="$DEV_VERSION"
  INSTALLED_DIR="${EXT_DIR}/${EXT_PREFIX}${INSTALLED_VERSION}"
  if [ ! -d "$INSTALLED_DIR" ]; then
    echo "ERROR: Installed extension directory not found at:"
    echo "       $INSTALLED_DIR"
    echo "       VSCode may have installed it elsewhere — check ~/.vscode/extensions/"
    exit 1
  fi
  if ! "$SCRIPT_DIR/check-build-info.sh" "$INSTALLED_DIR/dist/build-info.json"; then
    echo "ERROR: dist/build-info.json missing or invalid in installed extension:"
    echo "       $INSTALLED_DIR/dist/build-info.json"
    echo "       The .vsix was packaged but the stamp didn't survive — verify"
    echo "       packages/nightgauge-vscode/.vscodeignore is not excluding it."
    exit 1
  fi
  echo "==> Verified: $INSTALLED_DIR/dist/build-info.json"
fi

# ---------------------------------------------------------------------------
# Refresh the GLOBAL agent skill installs (Claude Code plugins + Codex skills)
# from this local working tree, so the skills you edit here are usable in any
# project you open — not just inside this repo. The .vsix above already bundles
# the pipeline skills for the extension; this covers the two tool-native global
# locations the extension does not own.
#
# Best-effort: the extension is already installed at this point, so a skill-sync
# hiccup (e.g. Claude/Codex not installed) must NOT fail the dev-install.
# Set NIGHTGAUGE_SKIP_SKILL_SYNC=1 to skip entirely.
# ---------------------------------------------------------------------------
if [ "${NIGHTGAUGE_SKIP_SKILL_SYNC:-0}" != "1" ]; then
  echo "==> Syncing agent skills (Claude Code + Codex) from local working tree..."
  if ! bash "$REPO_ROOT/scripts/install-agent-skills.sh"; then
    echo "WARNING: agent skill sync reported an error — the extension installed"
    echo "         fine, but Claude Code/Codex skills may not be fully refreshed."
    echo "         Re-run manually: ./scripts/install-agent-skills.sh"
  fi
fi

echo "==> Done. Reload VS Code window (Cmd+Shift+P → 'Reload Window')."
