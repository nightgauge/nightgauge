#!/usr/bin/env bash
#
# opencode-integration.sh — the opencode_integration-tagged Go suite against
# the real OpenCode binary (#1632): the adversarial config-merge and
# plugin-loading suite, the catalog snapshots, and run isolation (#1616).
#
# Usage: scripts/opencode-integration.sh [prefix]
#
# Installs the manifest's max_tested OpenCode (scripts/adapter-cli-pin.sh, an
# exact pin, the version the tests assert) into <prefix> (default: a fresh
# temp directory) and runs the suite with it first on PATH.
#
# ci.yml and ci-local.sh both call this one script. Before it existed the
# install and the -run list lived only in ci.yml, so the local gate never
# compiled the tag: a moved run root broke two tests that only CI ran.
set -euo pipefail

prefix="${1:-$(mktemp -d "${TMPDIR:-/tmp}/opencode-cli.XXXXXX")}"
pin="$(bash scripts/adapter-cli-pin.sh opencode)"

npm install --prefix "$prefix" --no-fund --no-audit "$pin"
PATH="$prefix/node_modules/.bin:$PATH" go test -tags opencode_integration -count=1 -v -timeout 10m \
  -run '^Test(OpenCode(Integration.*|PurePluginLoading|InlineDenyBeatsProjectAllow|ArrayMerge|ProviderBaseURLPrecedence|ConfigDiscoveryAboveWorktree|DisableProjectConfig|MergeHarnessReapsEveryProcess|CatalogEnvMatchesTheBinary|AnthropicModelsMatchTheBinary)|RealOpenCodePinRelaxedUnderCanary|PluginLoadsOnRealOpenCode|CompactionAutocontinueSuppressionAgainstRealOpenCode|PermissionAskEventAgainstRealOpenCode)$' \
  ./internal/execution/ ./internal/execution/adapters/ ./internal/execution/opencodeplugin/
