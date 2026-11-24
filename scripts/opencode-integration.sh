#!/usr/bin/env bash
#
# opencode-integration.sh — the opencode_integration-tagged Go suite against
# the real OpenCode binary (#1632): the adversarial config-merge and
# plugin-loading suite, the catalog snapshots, and run isolation (#1616).
#
# Usage: scripts/opencode-integration.sh [prefix]
#
# Installs the manifest's max_tested OpenCode with the vendor's 2.x installer
# (the manifest's install.installer; OpenCode 2.x is not published to npm as
# opencode-ai) into <prefix> (default: a fresh temp directory) and runs the
# suite with it first on PATH. The installer has no destination option and
# always writes $HOME/.opencode/bin, so it runs with HOME=<prefix>. Its sha256
# must match the manifest's install.installer_sha256.
#
# ci.yml and ci-local.sh both call this one script. Before it existed the
# install and the -run list lived only in ci.yml, so the local gate never
# compiled the tag: a moved run root broke two tests that only CI ran.
set -euo pipefail

prefix="${1:-$(mktemp -d "${TMPDIR:-/tmp}/opencode-cli.XXXXXX")}"
manifest=internal/adaptercompat/manifests/opencode.json
version="$(jq -r '.max_tested' "$manifest")"
installer="$(jq -r '.install.installer' "$manifest")"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
  echo "opencode-integration: max_tested '$version' is not MAJOR.MINOR.PATCH" >&2
  exit 1
}
[[ "$installer" =~ ^https://[A-Za-z0-9.-]+(/[A-Za-z0-9._~/-]*)?$ ]] || {
  echo "opencode-integration: install.installer '$installer' is not a plain https URL" >&2
  exit 1
}
want_sha="$(jq -r '.install.installer_sha256 // ""' "$manifest")"
[[ "$want_sha" =~ ^[0-9a-f]{64}$ ]] || {
  echo "opencode-integration: $manifest records no install.installer_sha256" >&2
  exit 1
}

mkdir -p "$prefix"
script="$prefix/opencode-install.sh"
curl -fsSL --proto '=https' -o "$script" "$installer"
if command -v shasum >/dev/null 2>&1; then
  got_sha="$(shasum -a 256 "$script" | awk '{print $1}')"
else
  got_sha="$(sha256sum "$script" | awk '{print $1}')"
fi
[ "$got_sha" = "$want_sha" ] || {
  echo "opencode-integration: $installer sha256 is $got_sha, not the pinned $want_sha ($manifest install.installer_sha256)" >&2
  exit 1
}
HOME="$prefix" bash "$script" --version "$version" --no-modify-path >/dev/null
bin="$prefix/.opencode/bin"
got="$("$bin/opencode" --version)"
case "$got" in
  "$version" | "v$version" | "opencode v$version") ;;
  *)
    echo "opencode-integration: the installer produced opencode '$got', not $version" >&2
    exit 1
    ;;
esac
PATH="$bin:$PATH" go test -tags opencode_integration -count=1 -v -timeout 20m \
  -run '^Test(OpenCode(Integration.*|PurePluginLoading|InlineDenyBeatsProjectAllow|ArrayMerge|ProviderBaseURLPrecedence|ConfigDiscoveryAboveWorktree|DisableProjectConfig|MergeHarnessReapsEveryProcess|MergeHarnessReclaimsOrphanedLocks|CatalogEnvMatchesTheBinary|AnthropicModelsMatchTheBinary)|RealOpenCodePinRelaxedUnderCanary|PluginLoadsOnRealOpenCode|ToolExecuteBeforeFiresInSubagentAgainstRealOpenCode|RealPluginDeniesTaskAgainstRealOpenCode|CompactionAutocontinueSuppressionAgainstRealOpenCode|PermissionAskEventAgainstRealOpenCode)$' \
  ./internal/execution/ ./internal/execution/adapters/ ./internal/execution/opencodeplugin/
