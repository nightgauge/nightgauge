#!/usr/bin/env bash
# Runs INSIDE the clean-install container as the empty-home `tester` user.
# Mounted by scripts/clean-install-e2e.sh:
#   /vsix/*.vsix                 the packaged extension (read-only)
#   /harness/bin/                this script (read-only)
#   /harness/driver/             the driver extension (read-only)
#   /auth/.credentials.json      OPTIONAL: Claude Code OAuth credentials (read-only)
#   /out/                        logs and the JSON report (read-write)
# Environment: GH_TOKEN (required), ANTHROPIC_API_KEY (optional; wins over the
# mount), E2E_REPO owner/name, E2E_OWNER, E2E_OWNER_TYPE, E2E_PROJECT (board
# number), E2E_WALL_CLOCK_MS, E2E_COST_CAP_USD, E2E_SMOKE ("1" = activation only).
set -euo pipefail

OUT=/out
mkdir -p "$OUT"
exec > >(tee -a "$OUT/container.log") 2>&1

step() { printf '\n=== %s ===\n' "$*"; }
finding() { echo "FINDING: $*"; echo "$*" >> "$OUT/findings.txt"; }

step "clean-machine preconditions"
whoami; echo "HOME=$HOME"
[[ ! -e "$HOME/.nightgauge" ]] || { echo "ERROR: ~/.nightgauge exists in a supposedly clean home"; exit 1; }
[[ ! -e "$HOME/.vscode" ]] || { echo "ERROR: ~/.vscode exists in a supposedly clean home"; exit 1; }
! command -v nightgauge >/dev/null 2>&1 || { echo "ERROR: a nightgauge binary is on PATH"; exit 1; }
[[ -z "${NIGHTGAUGE_GO_BINARY_PATH:-}" && -z "${NIGHTGAUGE_BIN:-}" ]] \
  || { echo "ERROR: NIGHTGAUGE_GO_BINARY_PATH / NIGHTGAUGE_BIN would short-circuit binary resolution"; exit 1; }
[[ "${E2E_SMOKE:-0}" == "1" || -n "${GH_TOKEN:-}" ]] || { echo "ERROR: GH_TOKEN is not set"; exit 1; }
echo "vscode: $(code --version --no-sandbox --user-data-dir=/tmp/probe | head -1)"; rm -rf /tmp/probe
echo "claude: $(claude --version)"; echo "gh: $(gh --version | head -1)"; echo "node: $(node --version)"

step "agent auth (the one thing inherited from the host)"
if [[ "${E2E_SMOKE:-0}" == "1" ]]; then
  echo "smoke: no agent auth needed"
elif [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "using ANTHROPIC_API_KEY"
elif [[ -f /auth/.credentials.json ]]; then
  mkdir -p "$HOME/.claude" && chmod 700 "$HOME/.claude"
  cp /auth/.credentials.json "$HOME/.claude/.credentials.json" && chmod 600 "$HOME/.claude/.credentials.json"
  echo "using a copy of the host's Claude Code credentials"
else
  echo "ERROR: neither ANTHROPIC_API_KEY nor /auth/.credentials.json was provided"; exit 1
fi

step "git + gh as a new user would set them up"
git config --global user.name "Clean Install E2E"
git config --global user.email "clean-install-e2e@users.noreply.github.com"
git config --global init.defaultBranch main
if [[ "${E2E_SMOKE:-0}" != "1" ]]; then gh auth setup-git; fi
gh auth status 2>&1 | sed 's/gho_[A-Za-z0-9_]*/gho_***/g; s/ghp_[A-Za-z0-9_]*/ghp_***/g' || true

EXT_DIR="$HOME/.vscode-e2e/extensions"
USER_DIR="$HOME/.vscode-e2e/user-data"
mkdir -p "$EXT_DIR" "$USER_DIR"
CODE_ARGS=(--no-sandbox --disable-gpu --user-data-dir "$USER_DIR" --extensions-dir "$EXT_DIR")

step "README step 1: install the packaged VSIX into a fresh profile"
VSIX="$(ls /vsix/*.vsix | head -1)"
echo "vsix: $VSIX ($(stat -c %s "$VSIX") bytes)"
code "${CODE_ARGS[@]}" --install-extension "$VSIX"
code "${CODE_ARGS[@]}" --list-extensions --show-versions

step "which bundle VS Code recorded (extensions.json is the authority, #356)"
REL="$(python3 - "$EXT_DIR/extensions.json" <<'PY'
import json, sys
recs = json.load(open(sys.argv[1]))
hits = [r["relativeLocation"] for r in recs if r.get("relativeLocation", "").startswith("nightgauge.nightgauge-vscode-")]
print(hits[0] if len(hits) == 1 else "")
PY
)"
[[ -n "$REL" ]] || { echo "ERROR: extensions.json does not record exactly one nightgauge bundle"; cat "$EXT_DIR/extensions.json"; exit 1; }
BIN="$EXT_DIR/$REL/dist/bin/nightgauge"
[[ -x "$BIN" ]] || { echo "ERROR: recorded bundle has no executable dist/bin/nightgauge: $BIN"; exit 1; }
echo "bundle: $EXT_DIR/$REL"
echo "binary: $BIN"
"$BIN" version

if [[ "${E2E_SMOKE:-0}" == "1" ]]; then
  step "smoke: activation only"
  WORK="$HOME/work/smoke"; mkdir -p "$WORK"; git -C "$WORK" init -q
else
  step "README step 3: open the repository (cloned like a new user, over HTTPS)"
  WORK="$HOME/work/$(basename "$E2E_REPO")"
  gh repo clone "$E2E_REPO" "$WORK" -- -q
  cd "$WORK"
  git log --oneline -3

  step "README step 4 stand-in: provision with the VSIX's own binary (the skill's mutations, non-interactively)"
  "$BIN" project ensure-fields --number "$E2E_PROJECT" --owner "$E2E_OWNER" --owner-type "$E2E_OWNER_TYPE" --json
  "$BIN" config init --owner "$E2E_OWNER" --owner-type "$E2E_OWNER_TYPE" \
    --repo "$(basename "$E2E_REPO")" --project "$E2E_PROJECT" --json
  "$BIN" config validate --json
  "$BIN" label ensure --owner "$E2E_OWNER" --repo "$E2E_REPO" --owner-type "$E2E_OWNER_TYPE" --json
  # The skill's type-label taxonomy (skills/nightgauge-repo-init/_includes/labels.md).
  while read -r name color desc; do
    "$BIN" label create --name "$name" --color "$color" --description "$desc" \
      --owner "$E2E_OWNER" --repo "$E2E_REPO" --owner-type "$E2E_OWNER_TYPE" --json >/dev/null
  done <<'LABELS'
type:bug d73a4a Something broken
type:docs 0075ca Documentation only
type:chore cfd3d7 Maintenance task
type:feature 1d76db New functionality
type:refactor fbca04 Code improvement
type:epic 8957e5 Parent issue with sub-issues
type:spike c2e0c6 Research/investigation task
LABELS
  "$BIN" label list --owner "$E2E_OWNER" --repo "$E2E_REPO" --owner-type "$E2E_OWNER_TYPE" --json | jq -r '[.[].name] | join(" ")'
  PROJECT_ID="$(grep -E '^\s+id:' .nightgauge/config.yaml | head -1 | sed -E 's/.*id: *"?([^"]*)"?/\1/')"
  REPO_ID="$(gh api "repos/$E2E_REPO" --jq .node_id)"
  LINK_Q='mutation($projectId:ID!,$repoId:ID!){linkProjectV2ToRepository(input:{projectId:$projectId,repositoryId:$repoId}){repository{id}}}'
  if "$BIN" forge graphql --json -f query="$LINK_Q" -f "projectId=$PROJECT_ID" -f "repoId=$REPO_ID"; then
    echo "board linked via nightgauge forge graphql"
  else
    finding "the repo-init skill's board-link step (\`nightgauge forge graphql\`, skills/nightgauge-repo-init/_includes/board-fields-and-link.md) fails on a fresh install: 'active forge does not expose a GraphQL transport'. Linked the board with \`gh api graphql\` instead."
    gh api graphql -f query="$LINK_Q" -f "projectId=$PROJECT_ID" -f "repoId=$REPO_ID"
  fi
  sed -n '1,40p' .nightgauge/config.yaml
  git add .nightgauge/config.yaml
  git commit -q -m "chore: initialize Nightgauge"
  git push -q origin HEAD:main

  step "file the one issue (what the README's user does next) and put it on the board as Ready"
  ISSUE_URL="$(gh issue create --repo "$E2E_REPO" --title "feat: add truncate(text, maxLength) next to slugify" \
    --label type:feature --body-file /harness/issue.md)"
  echo "issue: $ISSUE_URL"
  ISSUE_NUM="${ISSUE_URL##*/}"
  "$BIN" project add "$ISSUE_NUM" --repo "$E2E_REPO" --project "$E2E_PROJECT" --owner-type "$E2E_OWNER_TYPE"
  "$BIN" project sync-status "$ISSUE_NUM" ready --owner "$E2E_OWNER" --owner-type "$E2E_OWNER_TYPE" \
    --project "$E2E_PROJECT" --repo "$E2E_REPO" || echo "WARN: sync-status did not set Ready (continuing; pickup does not require it)"
  echo "$ISSUE_NUM" > "$OUT/issue-number"
  export CLEAN_INSTALL_ISSUE="$ISSUE_NUM"
fi

step "README steps 5–6: drive VS Code (installed VSIX + driver extension) under Xvfb"
export CLEAN_INSTALL_REPORT="$OUT/report.json"
export CLEAN_INSTALL_EXTENSIONS_DIR="$EXT_DIR"
export CLEAN_INSTALL_WALL_CLOCK_MS="${E2E_WALL_CLOCK_MS:-5400000}"
export CLEAN_INSTALL_COST_CAP_USD="${E2E_COST_CAP_USD:-15}"
export CLEAN_INSTALL_SMOKE="${E2E_SMOKE:-0}"
rm -f "$CLEAN_INSTALL_REPORT"
set +e
xvfb-run -a --server-args="-screen 0 1600x1000x24" \
  code "${CODE_ARGS[@]}" --wait --verbose --disable-updates --skip-welcome --skip-release-notes \
    --disable-workspace-trust \
    --extensionDevelopmentPath=/harness/driver \
    --extensionTestsPath=/harness/driver/driver.js \
    "$WORK" > "$OUT/vscode.log" 2>&1
CODE_EXIT=$?
set -e
echo "code exited $CODE_EXIT"
grep -a "^\[driver " "$OUT/vscode.log" || true

step "collect evidence"
if [[ -d "$WORK/.nightgauge/pipeline" ]]; then
  mkdir -p "$OUT/pipeline"
  cp -r "$WORK/.nightgauge/pipeline/." "$OUT/pipeline/" 2>/dev/null || true
fi
[[ -f "$CLEAN_INSTALL_REPORT" ]] || { echo "ERROR: the driver never wrote its report — VS Code exited without running it"; exit 1; }
python3 -c "import json,sys; r=json.load(open(sys.argv[1])); print('driver status:', r['status']); sys.exit(0 if r['status']=='pass' else 1)" "$CLEAN_INSTALL_REPORT"
