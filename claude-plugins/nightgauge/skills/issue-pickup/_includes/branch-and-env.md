# Issue Pickup — Phase 5: Branch Creation

Procedural detail for **Phase 5** (`branch-creation`, index 7): verify a clean
working tree, then create the feature branch deterministically via the Go binary
(prefix/slug derivation, parent-epic detection, lazy epic-branch creation).

## Contents

- [Step 5.1: Verify Clean Working Directory](#step-51-verify-clean-working-directory)
- [Steps 5.2–5.5: Deterministic Branch Creation](#steps-5255-deterministic-branch-creation)

---

## Step 5.1: Verify Clean Working Directory

```bash
git status --porcelain
```

If uncommitted changes exist, offer options: Stash / Commit first / Discard
(with confirmation) / Cancel.

## Steps 5.2–5.5: Deterministic Branch Creation

Prefix derivation (from issue labels), slug generation (from issue title),
parent epic detection, epic branch lookup/lazy creation, and feature branch
creation are all handled by the Go binary. Pass `--issue $ISSUE_NUMBER` so
the binary fetches the issue once and synthesizes the `<prefix>/<N>-<slug>`
branch name deterministically.

```bash
# Deterministic branch creation with prefix+slug derivation and epic detection
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
# The Go binary's git branch-create subcommand requires GITHUB_TOKEN to interact
# with GitHub (issue fetch, parent epic detection, remote branch lookup). Export
# it explicitly so the binary can authenticate even when the shell environment
# lacks it.
export GITHUB_TOKEN=$(nightgauge forge auth token 2>/dev/null || echo "")

BRANCH_RESULT=$("$BINARY" git branch-create --issue "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"success":false,"error":"binary not found"}')

if [ "$(echo "$BRANCH_RESULT" | jq -r '.success')" != "true" ]; then
  echo "ERROR: Branch creation failed: $(echo "$BRANCH_RESULT" | jq -r '.error')"
  exit 1
fi

BRANCH_NAME=$(echo "$BRANCH_RESULT" | jq -r '.branch')
BASE_BRANCH=$(echo "$BRANCH_RESULT" | jq -r '.base_branch')
PARENT_ISSUE_NUMBER=$(echo "$BRANCH_RESULT" | jq -r '.parent_issue // empty')
EPIC_BRANCH=$(echo "$BRANCH_RESULT" | jq -r '.epic_branch // empty')
ACTION=$(echo "$BRANCH_RESULT" | jq -r '.action')
```

The script handles:

- **Parent epic detection** via GraphQL (`parentIssueNumber` from IPC)
- **Epic branch lookup** via `git ls-remote` (warns on multiple matches, uses
  first)
- **Lazy epic branch creation** from default branch when no epic branch exists
- **Feature branch creation** from the correct base branch
- **Idempotency** — re-running when the branch exists returns without error:
  `action: "reused-remote"` when the branch is on `origin` (the local ref is
  reset to the pushed tip so the re-run continues from already-validated work
  and never diverges), or `action: "already-exists"` for a local-only branch

**CRITICAL — RE-RUN INVARIANT**: Whether the branch was created new or already
existed, you **MUST continue through Phase 6, Phase 7, and Phase 8**. The
pipeline orchestrator depends on `issue-{N}.json` being written after every
successful exit. **Do NOT exit early** when an existing branch is found.

For non-sub-issues, the script branches from the default branch (main). If you
want to offer interactive base branch selection for top-level issues:

```bash
if [ -z "$PARENT_ISSUE_NUMBER" ]; then
  # Not a sub-issue — check for active epic/feature branches and offer selection
  git branch -r | grep -E "origin/(epic|feat)/.*" | head -5
  # If epic/feature branches exist, offer base branch selection.
  # Store the selected base branch for PR creation.
  # BASE_BRANCH is already set to main by the script; override if user selects
  # a different base.
fi
```
