#!/usr/bin/env bash
# Clean-machine install gate (docs/RELEASE_CHECKLIST.md, gate G1) — automated.
#
# One command: package the .vsix from THIS tree, build a container that has
# nothing but VS Code + git + gh + Node + the claude CLI, install the extension
# into a fresh profile there, create a throwaway GitHub repository seeded from
# tests/clean-install/fixture, and drive one issue to a merged pull request
# with a real agent and a real forge. Logs land in .clean-install-e2e/<ts>/.
#
# Usage:
#   bash scripts/clean-install-e2e.sh            # the full gate (spends tokens, creates a repo)
#   bash scripts/clean-install-e2e.sh --smoke    # package + install + activate only; no forge, no agent
#
# Environment:
#   GH_TOKEN / gh login            must create AND delete private repos and projects under $E2E_OWNER
#   ANTHROPIC_API_KEY              agent auth; if unset, the host's Claude Code OAuth credentials are
#                                  exported (macOS Keychain or ~/.claude/.credentials.json) — the ONE
#                                  thing the container inherits from this machine
#   E2E_OWNER (nightgauge)         org or user that owns the throwaway repo and board
#   E2E_OWNER_TYPE (org)           org | user
#   CLEAN_INSTALL_WALL_CLOCK_MINUTES (90), CLEAN_INSTALL_COST_CAP_USD (15)
#   CLEAN_INSTALL_KEEP_REPO=1      do not delete the throwaway repo/board on exit (debugging)
#   CLEAN_INSTALL_VSIX=<path>      skip packaging and use this VSIX (must match the docker target)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SMOKE=0
for arg in "$@"; do
  case "$arg" in
    --smoke) SMOKE=1 ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

OWNER="${E2E_OWNER:-nightgauge}"
OWNER_TYPE="${E2E_OWNER_TYPE:-org}"
WALL_MIN="${CLEAN_INSTALL_WALL_CLOCK_MINUTES:-90}"
COST_CAP="${CLEAN_INSTALL_COST_CAP_USD:-15}"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
RUN_DIR="$ROOT/.clean-install-e2e/$TS"
OUT_DIR="$RUN_DIR/out"
mkdir -p "$RUN_DIR/vsix" "$OUT_DIR" "$RUN_DIR/auth"
chmod 700 "$RUN_DIR/auth"
# The container user is uid 1000; on a Linux host the bind mount keeps the
# host's uid, so the output directory must be world-writable to receive logs.
chmod 777 "$OUT_DIR"
exec > >(tee -a "$RUN_DIR/host.log") 2>&1

step() { printf '\n=== %s ===\n' "$*"; }
die() { echo "ERROR: $*" >&2; exit 1; }

REPO_NAME="e2e-clean-install-$(echo "$TS" | tr -d 'TZ' | tr '[:upper:]' '[:lower:]')"
REPO="$OWNER/$REPO_NAME"
PROJECT_NUMBER=""
CONTAINER="ng-clean-install-$TS"
REPO_CREATED=0
AUTH_MOUNT=()

cleanup() {
  local rc=$?
  set +e
  step "cleanup (exit $rc)"
  if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 && echo "removed container $CONTAINER"
  fi
  ! docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER" || echo "WARN: container $CONTAINER still present"
  rm -rf "$RUN_DIR/auth"
  if [[ "${CLEAN_INSTALL_KEEP_REPO:-0}" == "1" ]]; then
    echo "CLEAN_INSTALL_KEEP_REPO=1: keeping $REPO and project $PROJECT_NUMBER"
  else
    if [[ "$REPO_CREATED" == "1" ]]; then
      gh repo delete "$REPO" --yes && echo "deleted repository $REPO" || echo "WARN: failed to delete $REPO — delete it by hand"
    fi
    if [[ -n "$PROJECT_NUMBER" ]]; then
      gh project delete "$PROJECT_NUMBER" --owner "$OWNER" >/dev/null && echo "deleted project $OWNER/$PROJECT_NUMBER" \
        || echo "WARN: failed to delete project $OWNER/$PROJECT_NUMBER — delete it by hand"
    fi
  fi
  # Scrub the HOST side of the evidence too (#1335). The container scrubs
  # /out, but the workflow uploads the whole `.clean-install-e2e/` tree — and
  # `host.log` is this script's own tee'd stdout, which includes everything the
  # container printed. So the identical bytes were being published in a second
  # file that the container's scrub could not reach, alongside
  # `docker-build.log` and anything else written here.
  #
  # In the cleanup trap so it covers every exit path, including `die` and a
  # ^C — the failure path is where a token is most likely to have been printed.
  #
  # Everything after this point is a path, not a credential. Output written from
  # here on reaches host.log through the `tee` above and is therefore NOT
  # covered; that is why this sits as late as it does.
  if [[ -x "$ROOT/docker/clean-install/scrub-evidence.sh" ]]; then
    "$ROOT/docker/clean-install/scrub-evidence.sh" "$RUN_DIR" \
      || echo "ERROR: host evidence scrub FAILED — $RUN_DIR may still contain credentials" >&2
  fi
  echo "logs: $RUN_DIR"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

step "preconditions"
for tool in docker gh node go jq python3 npx; do command -v "$tool" >/dev/null || die "$tool is required"; done
docker info >/dev/null 2>&1 || die "docker daemon is not reachable"
DOCKER_ARCH="$(docker version --format '{{.Server.Arch}}')"
case "$DOCKER_ARCH" in
  arm64) VSCE_TARGET=linux-arm64; GOARCH=arm64 ;;
  amd64) VSCE_TARGET=linux-x64;   GOARCH=amd64 ;;
  *) die "unsupported docker architecture: $DOCKER_ARCH" ;;
esac
echo "docker: linux/$DOCKER_ARCH → vsce target $VSCE_TARGET"
if [[ "$SMOKE" == "0" ]]; then
  GH_TOKEN="${GH_TOKEN:-$(gh auth token 2>/dev/null || true)}"
  [[ -n "$GH_TOKEN" ]] || die "no GH_TOKEN and no gh login"
  export GH_TOKEN
  gh auth status >/dev/null 2>&1 || die "gh is not authenticated with GH_TOKEN"
  if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
    echo "agent auth: ANTHROPIC_API_KEY"
  else
    CRED="$RUN_DIR/auth/.credentials.json"
    if [[ "$(uname -s)" == "Darwin" ]] && security find-generic-password -s "Claude Code-credentials" -w >"$CRED" 2>/dev/null; then
      echo "agent auth: Claude Code credentials exported from the macOS Keychain (copy; deleted on exit)"
    elif [[ -f "$HOME/.claude/.credentials.json" ]] && grep -q claudeAiOauth "$HOME/.claude/.credentials.json"; then
      cp "$HOME/.claude/.credentials.json" "$CRED"
      echo "agent auth: copy of ~/.claude/.credentials.json (deleted on exit)"
    else
      die "no ANTHROPIC_API_KEY and no Claude Code OAuth credentials to inherit"
    fi
    chmod 600 "$CRED"
    grep -q claudeAiOauth "$CRED" || die "exported credentials carry no claudeAiOauth block"
    AUTH_MOUNT=(-v "$CRED:/auth/.credentials.json:ro")
  fi
fi

PKG="$ROOT/packages/nightgauge-vscode"
if [[ -n "${CLEAN_INSTALL_VSIX:-}" ]]; then
  step "using the supplied VSIX (CLEAN_INSTALL_VSIX)"
  VSIX="$CLEAN_INSTALL_VSIX"
  [[ -f "$VSIX" ]] || die "CLEAN_INSTALL_VSIX does not exist: $VSIX"
  [[ "$(basename "$VSIX")" == *"$VSCE_TARGET"* ]] || die "$(basename "$VSIX") is not a $VSCE_TARGET build"
else
step "package the VSIX for $VSCE_TARGET from $(git rev-parse --short HEAD)"
BIN_VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
npm run -w @nightgauge/sdk build >/dev/null
mkdir -p "$PKG/dist/bin"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -ldflags "-s -w -X main.version=$BIN_VERSION" \
  -o "$PKG/dist/bin/nightgauge" ./cmd/nightgauge
chmod +x "$PKG/dist/bin/nightgauge"
(cd "$PKG" && npm run build >/dev/null)
# Provenance stamp, as release.yml does, so ExtensionStalenessService sees a real build.
cat > "$PKG/dist/build-info.json" <<JSON
{
  "commitSha": "$(git rev-parse HEAD)",
  "branch": "$(git rev-parse --abbrev-ref HEAD)",
  "commitTimestamp": "$(git log -1 --format=%cI HEAD)",
  "buildTimestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "schemaVersion": "1"
}
JSON
bash "$PKG/scripts/check-runtime-assets.sh" "$PKG/dist"
rm -f "$PKG"/nightgauge-vscode-*"$VSCE_TARGET"*.vsix
(cd "$PKG" && npx @vscode/vsce package --no-dependencies --target "$VSCE_TARGET" >/dev/null)
VSIX="$(ls -t "$PKG"/nightgauge-vscode-*"$VSCE_TARGET"*.vsix | head -1)"
fi
bash "$PKG/scripts/check-runtime-assets.sh" "$VSIX"
cp "$VSIX" "$RUN_DIR/vsix/"
echo "vsix: $(basename "$VSIX") ($(du -h "$VSIX" | cut -f1))"

step "build the clean-machine image"
IMAGE="nightgauge-clean-install:$TS"
# Not -q: a quiet build that fails prints only the Dockerfile snippet, not the
# command's output, and the first CI run failed that way with the cause
# invisible. The full log lands in the run directory; its tail is printed on
# failure so the workflow log carries the reason.
if ! docker build -t "$IMAGE" docker/clean-install >"$RUN_DIR/docker-build.log" 2>&1; then
  echo "ERROR: image build failed — last 40 lines of $RUN_DIR/docker-build.log:" >&2
  tail -n 40 "$RUN_DIR/docker-build.log" >&2
  exit 1
fi
echo "image: $IMAGE (build log: $RUN_DIR/docker-build.log)"

if [[ "$SMOKE" == "0" ]]; then
  step "throwaway repository $REPO (private) seeded from tests/clean-install/fixture"
  gh repo create "$REPO" --private --description "Nightgauge clean-install gate — throwaway, auto-deleted" >/dev/null
  REPO_CREATED=1
  SEED="$RUN_DIR/seed"; mkdir -p "$SEED"
  cp -R tests/clean-install/fixture/. "$SEED/"
  git -C "$SEED" init -q -b main
  git -C "$SEED" -c user.name="Clean Install E2E" -c user.email="clean-install-e2e@users.noreply.github.com" add -A
  git -C "$SEED" -c user.name="Clean Install E2E" -c user.email="clean-install-e2e@users.noreply.github.com" \
    commit -q -m "chore: seed fixture project"
  git -C "$SEED" remote add origin "https://x-access-token:${GH_TOKEN}@github.com/$REPO.git"
  git -C "$SEED" push -q origin main
  git -C "$SEED" remote set-url origin "https://github.com/$REPO.git"
  echo "pushed $(git -C "$SEED" rev-parse --short HEAD) to $REPO"

  step "throwaway project board (what the README's user brings to Initialize Repository)"
  PROJECT_NUMBER="$(gh project create --owner "$OWNER" --title "$REPO_NAME" --format json --jq .number)"
  [[ -n "$PROJECT_NUMBER" ]] || die "gh project create returned no number"
  echo "project: $OWNER/$PROJECT_NUMBER"
fi

step "run the container"
docker run --name "$CONTAINER" --shm-size=1g \
  -v "$RUN_DIR/vsix:/vsix:ro" \
  -v "$ROOT/docker/clean-install/run-in-container.sh:/harness/bin/run-in-container.sh:ro" \
  -v "$ROOT/docker/clean-install/scrub-evidence.sh:/harness/bin/scrub-evidence.sh:ro" \
  -v "$ROOT/tests/clean-install/driver:/harness/driver:ro" \
  -v "$ROOT/tests/clean-install/issue.md:/harness/issue.md:ro" \
  -v "$OUT_DIR:/out" \
  ${AUTH_MOUNT[@]+"${AUTH_MOUNT[@]}"} \
  -e GH_TOKEN -e ANTHROPIC_API_KEY \
  -e E2E_REPO="$REPO" -e E2E_OWNER="$OWNER" -e E2E_OWNER_TYPE="$OWNER_TYPE" \
  -e E2E_PROJECT="$PROJECT_NUMBER" -e E2E_SMOKE="$SMOKE" \
  -e E2E_WALL_CLOCK_MS="$((WALL_MIN * 60000))" -e E2E_COST_CAP_USD="$COST_CAP" \
  "$IMAGE" && CONTAINER_RC=0 || CONTAINER_RC=$?
echo "container exited $CONTAINER_RC"

step "assertions"
REPORT="$OUT_DIR/report.json"
FAILED=0
check() { # name ok evidence
  if [[ "$2" == "1" ]]; then printf 'PASS  %-52s %s\n' "$1" "$3"; else printf 'FAIL  %-52s %s\n' "$1" "$3"; FAILED=1; fi
}
[[ -f "$REPORT" ]] || { check "driver wrote a report" 0 "missing $REPORT"; exit 1; }
DRIVER_STATUS="$(jq -r .status "$REPORT")"
check "driver status" "$([[ "$DRIVER_STATUS" == "pass" ]] && echo 1 || echo 0)" "$DRIVER_STATUS"
jq -r '.assertions[] | "\(if .ok then "PASS" else "FAIL" end)  \(.name) — \(.evidence)"' "$REPORT" | sed 's/^/      /'

EXT_VERSION="$(jq -r '.extension.version // "?"' "$REPORT")"
BIN_PATH="$(jq -r '.binary.path // "?"' "$REPORT")"
BIN_VERSION_SEEN="$(jq -r '.binary.version // "?"' "$REPORT")"
OUTCOME="$(jq -r '.run.history.outcome // .run.terminalOutcome // "?"' "$REPORT")"
PR_URL="$(jq -r '.run.prUrl // ""' "$REPORT")"
COST="$(jq -r '.run.history.estimatedCostUsd // .run.totalCostUsd // 0' "$REPORT")"
DURATION_MS="$(jq -r '.run.history.totalDurationMs // 0' "$REPORT")"
PR_STATE="n/a"; PR_MERGE_SHA="n/a"; ISSUE_STATE="n/a"
if [[ "$SMOKE" == "0" ]]; then
  if [[ -n "$PR_URL" ]]; then
    PR_JSON="$(gh pr view "$PR_URL" --json state,mergeCommit,url 2>/dev/null || echo '{}')"
    PR_STATE="$(jq -r '.state // "UNKNOWN"' <<<"$PR_JSON")"
    PR_MERGE_SHA="$(jq -r '.mergeCommit.oid // "none"' <<<"$PR_JSON")"
  fi
  ISSUE_NUM="$(cat "$OUT_DIR/issue-number" 2>/dev/null || echo 1)"
  ISSUE_STATE="$(gh issue view "$ISSUE_NUM" --repo "$REPO" --json state --jq .state 2>/dev/null || echo UNKNOWN)"
  check "PR exists on the throwaway repo" "$([[ -n "$PR_URL" ]] && echo 1 || echo 0)" "${PR_URL:-none}"
  check "PR is MERGED (forge says so)" "$([[ "$PR_STATE" == "MERGED" ]] && echo 1 || echo 0)" "$PR_STATE"
  check "issue is CLOSED" "$([[ "$ISSUE_STATE" == "CLOSED" ]] && echo 1 || echo 0)" "#$ISSUE_NUM $ISSUE_STATE"
fi
[[ "$CONTAINER_RC" == "0" ]] || FAILED=1

step "findings (product gaps the walk had to route around)"
jq -r '.findings[]' "$REPORT" | sed 's/^/- driver: /'
[[ -f "$OUT_DIR/findings.txt" ]] && sed 's/^/- container: /' "$OUT_DIR/findings.txt" || true

step "summary"
cat <<TABLE
| Field                     | Value |
| ------------------------- | ----- |
| Tree                      | $(git rev-parse --short HEAD) |
| VSIX                      | $(basename "$VSIX") ($VSCE_TARGET) |
| Extension activated       | $EXT_VERSION |
| Binary (resolved in-host) | $BIN_PATH |
| Binary version            | $BIN_VERSION_SEEN |
| Throwaway repo            | $REPO |
| Run outcome               | $OUTCOME |
| PR                        | ${PR_URL:-none} ($PR_STATE, merge $PR_MERGE_SHA) |
| Issue                     | $ISSUE_STATE |
| Cost / duration           | ${COST} USD / $((DURATION_MS / 1000)) s |
| Findings                  | $(( $(jq -r '.findings | length' "$REPORT") + $([[ -f "$OUT_DIR/findings.txt" ]] && wc -l < "$OUT_DIR/findings.txt" || echo 0) )) (report.json + findings.txt) |
| Result                    | $([[ "$FAILED" == "0" ]] && echo PASS || echo FAIL) |
TABLE
cp "$REPORT" "$RUN_DIR/report.json" 2>/dev/null || true
[[ "$FAILED" == "0" ]]
