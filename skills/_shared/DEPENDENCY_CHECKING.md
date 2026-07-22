### Dependency Checking (Enforcement)

**PURPOSE**: Check if an issue has open dependencies (blockers) before
proceeding. This prevents wasted effort on issues that can't be completed until
prerequisite work lands.

#### Check Dependency Configuration

```bash
# Check if dependency enforcement is enabled (default: true)
DEP_ENABLED=$(yq -r '.enforcement.dependencies.enabled // "true"' .nightgauge/config.yaml 2>/dev/null || echo "true")

# Get enforcement mode: warn | block | ignore (default: warn)
DEP_MODE=$(yq -r '.enforcement.dependencies.mode // "warn"' .nightgauge/config.yaml 2>/dev/null || echo "warn")

# Check if transitive checking is enabled (default: false)
DEP_TRANSITIVE=$(yq -r '.enforcement.dependencies.check_transitive // "false"' .nightgauge/config.yaml 2>/dev/null || echo "false")
```

#### Fetch Dependencies

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

if [ "$DEP_ENABLED" = "true" ] && [ "$DEP_MODE" != "ignore" ]; then
  if [ -z "$BINARY" ]; then
    echo "ERROR: nightgauge binary not found in PATH or bin/nightgauge" >&2
    echo "This binary is required for dependency enforcement." >&2
    exit 1
  fi
  DEP_RESULT=$("$BINARY" hook check-deps "$ISSUE_NUMBER" 2>/dev/null || echo '{"has_open_dependencies":false}')
  HAS_OPEN_DEPS=$(echo "$DEP_RESULT" | jq -r '.has_open_dependencies')
  SHOULD_BLOCK=$(echo "$DEP_RESULT" | jq -r '.should_block')
  OPEN_DEPS=$(echo "$DEP_RESULT" | jq -r '.open_dependencies')
  OPEN_COUNT=$(echo "$DEP_RESULT" | jq -r '.open_count')
fi
```

#### Handle Open Dependencies

If the issue has open dependencies, display the blocking issues and their count.

##### Block Mode → Deferral (Issue #231)

If `enforcement.dependencies.mode: block`, **defer** the issue rather than
reporting a pipeline failure. An open native `blockedBy` dependency is a
controlled hold, not an organic failure — mirror the baseline-CI gate
(Phase 2.8): pause the queue item, post a deferral comment naming the blockers,
print `signal=deferred`, and exit **0** so the orchestrator records a deferral
(not a failure) and auto-requeues when the blockers close.

```bash
# $BINARY was resolved in the "Fetch Dependencies" block above.
GATE_OUTPUT=$("$BINARY" deps-gate check --issue "$ISSUE_NUMBER" --json 2>&1)
GATE_EXIT=$?

if [ $GATE_EXIT -eq 1 ]; then
  # Deferred: the gate paused the queue item (kind=blocked_dependency) and
  # emitted the open blockers as JSON. Post a deferral comment naming each.
  BLOCKERS=$(echo "$GATE_OUTPUT" | jq -r '.open_dependencies[]? | "- #\(.number) \(.title)"' 2>/dev/null)
  REASON=$(echo "$GATE_OUTPUT" | jq -r '.reason // "blocked by open dependency"' 2>/dev/null)

  COMMENT_BODY="## Dependency Gate — Deferred

This issue has an open \`blockedBy\` dependency (the blocker's PR is not merged),
so pickup is deferred ([\`blocked-dependency\`](../../docs/FAILURE_TAXONOMY.md#infrastructure)):

$BLOCKERS

The pipeline has paused this item and will automatically resume dispatch when the
blockers close (\`deps-gate promote\` sweep, or the autonomous cascade). No
operator action required."

  nightgauge forge issue comment --subject-id "$ISSUE_NUMBER" -b "$COMMENT_BODY" 2>/dev/null || \
    echo "warning: failed to post deferral comment to #$ISSUE_NUMBER"

  "$BINARY" outcome record \
    --issue "$ISSUE_NUMBER" \
    --stage "issue-pickup" \
    --outcome "deferred" \
    --reason "[blocked-dependency] $REASON" 2>/dev/null || true

  echo "Dependency gate: DEFERRED"
  echo "signal=deferred"
  exit 0
fi
```

Exit the skill without creating a branch. Do **not** emit a failure report —
this is a deferral, and the run must exit 0.

##### Warn Mode (Default)

If `enforcement.dependencies.mode: warn`, display open blockers and offer
options: Pick up blocker #X instead (recommended) / Continue anyway / View
dependencies / Cancel.

If multiple blockers exist, recommend the least-blocked one (fewest open
dependencies).

- **Pick up blocker #X instead** (recommended): Restart with the blocker's issue
  number. If multiple blockers exist, present the list sorted by blocker count
  (least-blocked first) and let the user choose.
- **Continue anyway**: Record override in context file and proceed
- **View dependencies**: Display full details of each blocker
- **Cancel**: Exit skill

##### Record Override Decision

If user chooses "Continue anyway":

```bash
DEPENDENCY_OVERRIDE=true
DEPENDENCY_OVERRIDE_REASON="User acknowledged open dependencies"
```

This will be stored in the context file for downstream skills.

##### Handle Blocker Redirect

If user chooses "Pick up blocker #X instead":

1. Set `ISSUE_NUMBER` to the selected blocker's issue number
2. Restart issue analysis with the new issue number (re-fetch issue details,
   re-run epic check, re-run dependency check)
3. The original issue is NOT recorded in the context — the skill now operates
   entirely on the blocker issue

If the selected blocker also has open dependencies, the recursive restart will
naturally present the same options for the blocker's dependencies.
