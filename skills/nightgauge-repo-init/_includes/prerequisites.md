# Prerequisites (Phase 0)

Procedural detail for Phase 0: tool checks, active-account pinning, token-scope
validation, argument parsing, repo identity, and existing-config detection.

## Contents

- [Step 0.1: Check Tools](#step-01-check-tools)
- [Step 0.1.2: Detect and Pin Active Account](#step-012-detect-and-pin-active-account)
- [Step 0.1.3: Validate Required Token Scopes](#step-013-validate-required-token-scopes)
- [Step 0.1.5: Parse Arguments](#step-015-parse-arguments)
- [Step 0.2: Get Repo Identity](#step-02-get-repo-identity)
- [Step 0.3: Check Existing Config](#step-03-check-existing-config)

---

## Step 0.1: Check Tools

```bash
# nightgauge binary installed and configured
if ! command -v nightgauge &>/dev/null; then
  echo "ERROR: nightgauge binary not installed — see docs/GO_BINARY.md"
  exit 1
fi
if ! nightgauge forge auth status &>/dev/null; then
  echo "ERROR: forge auth not configured — run: nightgauge forge auth login (or set GITHUB_TOKEN env var)"
  exit 1
fi
# jq required for JSON parsing
if ! command -v jq &>/dev/null; then
  echo "ERROR: jq not installed — brew install jq"
  exit 1
fi
```

## Step 0.1.2: Detect and Pin Active Account

Capture the active forge account once and pin it. **Never switch to another account
during this skill — fail with a clear error instead.**

```bash
ACTIVE_USER=$(nightgauge forge auth whoami --json --jq .login 2>/dev/null)
if [ -z "$ACTIVE_USER" ]; then
  echo "ERROR: Could not detect active forge account — run: nightgauge forge auth login"
  exit 1
fi
echo "Active forge account: $ACTIVE_USER"
```

## Step 0.1.3: Validate Required Token Scopes

The `project` scope is required for all project board operations. Check upfront
so the skill fails fast with an actionable error rather than silently falling
back to a wrong account.

```bash
if ! nightgauge forge auth status 2>&1 | grep -q "project"; then
  echo ""
  echo "ERROR: Active token for '$ACTIVE_USER' is missing the 'project' scope."
  echo ""
  echo "Fix by running:"
  echo "  nightgauge forge auth login --scopes project"
  echo ""
  echo "Then re-run this skill."
  exit 1
fi
echo "Token scopes: ✓ project scope confirmed (via nightgauge forge auth status)"

# Optional: enhanced scope validation via Go binary (provides structured output)
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
if [ -n "$BINARY" ]; then
  if ! "$BINARY" auth check 2>/dev/null; then
    echo ""
    echo "WARNING: nightgauge auth check reported missing scopes."
    echo "Required scopes: repo, project, read:org"
    echo "Fix by running: nightgauge forge auth login --scopes project,repo,read:org"
    echo ""
    echo "Continuing — nightgauge forge auth status already confirmed 'project' scope above."
  fi
fi
```

## Step 0.1.5: Parse Arguments

Parse all arguments in a single pass:

```bash
SEED_FROM=""
SKIP_KNOWLEDGE=false
PROJECT_ARG=""        # Raw --project value (resolved in Phase 1 Step 1.3)
DRY_RUN=false

while [ $# -gt 0 ]; do
  case "$1" in
    --seed-from=*)    SEED_FROM="${1#*=}" ;;
    --seed-from)      shift; SEED_FROM="$1" ;;
    --skip-knowledge) SKIP_KNOWLEDGE=true ;;
    --project=*)      PROJECT_ARG="${1#*=}" ;;
    --project)        shift; PROJECT_ARG="$1" ;;
    --dry-run)        DRY_RUN=true ;;
  esac
  shift
done

if [ -n "$SEED_FROM" ] && [ ! -f "$SEED_FROM" ]; then
  echo "ERROR: --seed-from path not found: $SEED_FROM"
  exit 1
fi
```

> **Note:** `--project N` is captured as `PROJECT_ARG` and resolved against both
> org and user ownership in Phase 1, Step 1.3. It is NOT assumed to refer to a
> user project.

## Step 0.2: Get Repo Identity

```bash
REPO=$(nightgauge forge repo view --json nameWithOwner -q .nameWithOwner)
OWNER=$(nightgauge forge repo view --json owner -q .owner.login)
REPO_NAME=$(nightgauge forge repo view --json name -q .name)
echo "Repository: $REPO"
```

If not in a git repo or not authenticated to it, exit with a clear error.

## Step 0.3: Check Existing Config

```bash
CONFIG=".nightgauge/config.yaml"
if [ -f "$CONFIG" ]; then
  echo "Found existing config at $CONFIG"
  PROJECT_NUMBER=$(grep "number:" "$CONFIG" | head -1 | awk '{print $2}')
  echo "Project: #$PROJECT_NUMBER"
fi
```

If config exists and has a project number, use it. Otherwise ask in Phase 1.
