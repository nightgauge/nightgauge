# Phase 1: Validate Environment — Procedural Detail

This file holds the step-by-step procedure for Phase 1 (Validate Environment) of
the `nightgauge-pr-merge` skill: verify the feature branch, resolve the PR
number and state, extract the issue number, and run the pre-CI Go build
integrity check.

## Contents

- [Step 1.1: Verify Feature Branch](#step-11-verify-feature-branch)
- [Step 1.2: Get PR Number](#step-12-get-pr-number)
- [Step 1.3: Check PR State](#step-13-check-pr-state)
- [Step 1.4: Extract Issue Number](#step-14-extract-issue-number)
- [Step 1.5: Pre-CI Go Build Integrity Check](#step-15-pre-ci-go-build-integrity-check)

#### Step 1.1: Verify Feature Branch

```bash
BRANCH=$(git branch --show-current)

# Handle detached HEAD state — common when running inside a git worktree that
# checked out a remote branch via `git checkout origin/<branch>`.
if [ -z "$BRANCH" ]; then
  HEAD_BRANCH=$(git name-rev --name-only HEAD 2>/dev/null | sed 's|remotes/origin/||')
  if [ -n "$HEAD_BRANCH" ] && [ "$HEAD_BRANCH" != "HEAD" ] && [ "$HEAD_BRANCH" != "undefined" ]; then
    echo "Detached HEAD detected. Creating local tracking branch: $HEAD_BRANCH"
    git checkout -b "$HEAD_BRANCH" --track "origin/$HEAD_BRANCH" 2>/dev/null || \
      git checkout "$HEAD_BRANCH" 2>/dev/null || true
    BRANCH=$(git branch --show-current)
  fi
fi

if [ -z "$BRANCH" ] || [ "$BRANCH" = "main" ] || [ "$BRANCH" = "master" ]; then
  echo "Error: Must be on a feature branch with an open PR"
  exit 1
fi
```

#### Step 1.2: Get PR Number

```bash
# Read PR number from context file (written by pr-create) or use --pr argument
PR_NUMBER=$(jq -r '.pr_number // empty' "$CONTEXT_FILE" 2>/dev/null || echo "")
```

If `--pr` argument provided, use that instead.

#### Step 1.3: Check PR State

```bash
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
PR_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
```

Handle: `MERGED` → exit 0, `CLOSED` → exit 1.

#### Step 1.4: Extract Issue Number

```bash
ISSUE_NUMBER=$(echo $BRANCH | grep -oE '[0-9]+' | head -1)
```

#### Step 1.5: Pre-CI Go Build Integrity Check

**PURPOSE**: Catch duplicate type/function declarations and other build errors
locally before waiting for CI. This is especially important for concurrent epic
sub-issues that may define the same types in the same Go package independently.

**Skip condition**: If the pre-push merge validation gate already passed `vet`,
skip this step (the merged-state `go vet` in pre-push is strictly more thorough
than this local check).

```bash
# Check if pre-push validation already passed vet
PRE_PUSH_FILE=".nightgauge/pipeline/pre-push-${ISSUE_NUMBER}.json"
SKIP_VET=false
if [ -f "$PRE_PUSH_FILE" ]; then
  PREPUSH_VET=$(jq -r '.validation_phases.vet // "skipped"' "$PRE_PUSH_FILE")
  if [ "$PREPUSH_VET" = "passed" ]; then
    echo "go vet already passed by pre-push validation — skipping"
    SKIP_VET=true
  fi
fi

# Only run for Go repositories (and only if not skipped by pre-push)
if [ "$SKIP_VET" = "false" ] && [ -f "go.mod" ] && command -v go >/dev/null 2>&1; then
  echo "Running Go build integrity check (go vet ./...)..."
  VET_OUTPUT=$(go vet ./... 2>&1 || true)

  # Detect duplicate declaration errors (common in concurrent epic development)
  DUPLICATE_DECLS=$(echo "$VET_OUTPUT" | grep -E "redeclared in this block|declared and not used.*redeclared" || true)

  if [ -n "$DUPLICATE_DECLS" ]; then
    echo ""
    echo "ERROR: Go build integrity check failed — duplicate declarations detected:"
    echo "$DUPLICATE_DECLS"
    echo ""
    echo "RESOLUTION: Merge conflicting declarations into a single file."
    echo "  1. Identify which files define the same type/function:"
    echo "     grep -rn '<TypeName>' --include='*.go' <package_dir>/"
    echo "  2. Move all methods from the duplicate file into the canonical file"
    echo "  3. Delete the duplicate file"
    echo "  4. Commit the consolidation: git add -A && git commit -m 'fix: consolidate duplicate <TypeName> declarations'"
    echo ""
    echo "This usually happens when two concurrent issues created separate files"
    echo "for the same struct in the same package. One file should be canonical."
    exit 1
  fi

  BUILD_OUTPUT=$(go build ./... 2>&1 || true)
  if [ -n "$BUILD_OUTPUT" ]; then
    echo "WARNING: go build ./... reported issues:"
    echo "$BUILD_OUTPUT"
    echo "Proceeding to CI gate — CI will provide full details."
  else
    echo "Go build integrity check: passed"
  fi
fi
```
