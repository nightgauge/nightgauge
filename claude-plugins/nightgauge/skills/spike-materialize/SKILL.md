---
name: spike-materialize
description: Parse a spike artifact's YAML recommendations block and create
  follow-up GitHub issues idempotently. Runs as the final stage of the spike
  pipeline path, after pr-merge.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Bash Read Write
context: fork
disable-model-invocation: true
---

# Spike Materialize

> Convert spike artifact recommendations into actionable follow-up issues.

## Description

This skill is a thin wrapper around `nightgauge spike materialize <N>`. It
runs after `pr-merge` for `type:spike` issues only and:

1. Locates the spike artifact at `docs/spikes/<N>-*.md`.
2. Parses the `yaml recommendations` fenced block per
   [docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md).
3. Validates the schema (action/type/priority/size enums, kebab-case ids,
   no cycles, no duplicate ids).
4. Creates one issue per `adopt` (Status=Ready) and `defer` (Status=Backlog)
   recommendation, filed as a sub-issue of the spike with `blockedBy` chains.
5. Writes a structured JSON context file
   `.nightgauge/pipeline/spike-materialize-{N}.json`.
6. Updates the spike PR description with a `## Created Follow-up Issues`
   section listing the materialized issue numbers.

The Go binary owns the deterministic logic — the skill only orchestrates.

## Invocation

| Tool        | Command                                                     |
| ----------- | ----------------------------------------------------------- |
| Claude Code | `/nightgauge-spike-materialize <issue-number>` (via plugin) |
| Pipeline    | Automatic — appended after pr-merge for type:spike issues   |

## Prerequisites

- Issue must have `type:spike` label.
- Spike PR must be merged to `main`.
- Spike artifact must exist at `docs/spikes/<N>-*.md` and contain a parseable
  `yaml recommendations` block per docs/SPIKE_CONTRACT.md.
- `nightgauge` binary must be on PATH or at `bin/nightgauge`.

## Workflow

### Phase 0: Validate Environment

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
if [ -z "$ISSUE_NUMBER" ]; then
  echo "ERROR: cannot infer issue number from branch '$BRANCH'"
  exit 1
fi

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
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found"
  exit 1
fi
```

### Phase 1: Run Materialize

Run the Go subcommand with `--json` output so the skill can parse the result.

```bash
mkdir -p .nightgauge/pipeline
OUTPUT_FILE=".nightgauge/pipeline/spike-materialize-${ISSUE_NUMBER}.json"

if ! "$BINARY" spike materialize "$ISSUE_NUMBER" --json > "$OUTPUT_FILE"; then
  echo "ERROR: spike materialize failed — see output above"
  cat "$OUTPUT_FILE" 2>/dev/null
  exit 1
fi

echo "Wrote: $OUTPUT_FILE"
```

### Phase 2: Update PR Description

If the spike PR is still discoverable (typically merged but not deleted),
append a `## Created Follow-up Issues` section listing the materialized
numbers. Use `gh` because it handles both open and merged PRs.

```bash
PR_NUMBER=$(gh pr list --state merged --search "head:${BRANCH}" --json number --jq '.[0].number' 2>/dev/null)
if [ -n "$PR_NUMBER" ] && [ "$PR_NUMBER" != "null" ]; then
  ISSUES_LIST=$(jq -r '.issues[] | select(.skipped != true and .issue_number > 0) | "- #\(.issue_number) \(.title)"' "$OUTPUT_FILE")
  if [ -n "$ISSUES_LIST" ]; then
    CURRENT_BODY=$(gh pr view "$PR_NUMBER" --json body --jq .body 2>/dev/null)
    if ! echo "$CURRENT_BODY" | grep -q "## Created Follow-up Issues"; then
      NEW_BODY=$(printf "%s\n\n## Created Follow-up Issues\n\n%s\n" "$CURRENT_BODY" "$ISSUES_LIST")
      gh pr edit "$PR_NUMBER" --body "$NEW_BODY" 2>/dev/null || true
    fi
  fi
fi
```

### Phase 3: Comment on the Spike Issue

Post a comment summarizing the materialized issues so the spike has a single
visible record of its follow-ups.

```bash
COMMENT=$(jq -r '
  "Spike materialize complete:\n\n" +
  ([.issues[] |
    if .skipped then "- · `\(.id)` skipped (\(.title))"
    elif .already_exists then "- ✓ `\(.id)` already materialized as #\(.issue_number)"
    elif .issue_number > 0 then "- ✓ `\(.id)` → #\(.issue_number) \(.title)"
    else "- ? `\(.id)` (no issue number recorded)"
    end
  ] | join("\n"))
' "$OUTPUT_FILE")

if [ -n "$COMMENT" ]; then
  gh issue comment "$ISSUE_NUMBER" --body "$COMMENT" 2>/dev/null || true
fi
```

### Phase 4: Move Spike to Done

```bash
"$BINARY" project move-status "$ISSUE_NUMBER" "Done" 2>/dev/null || true
```

## Output Contract

This skill writes:

1. **`.nightgauge/pipeline/spike-materialize-{N}.json`** — output of
   `nightgauge spike materialize --json`. Schema:

   ```json
   {
     "spike": 4042,
     "repo": "nightgauge/nightgauge",
     "dry_run": false,
     "issues": [
       {
         "id": "alpha",
         "action": "adopt",
         "title": "...",
         "issue_number": 4101,
         "url": "https://github.com/.../issues/4101"
       }
     ],
     "blocked_by": [{ "blocked_id": "beta", "blocker_id": "alpha" }]
   }
   ```

2. A comment on the spike issue listing the materialized issues.
3. A `## Created Follow-up Issues` section appended to the spike PR body.

## Failure Modes

| Failure                      | Behavior                                                        |
| ---------------------------- | --------------------------------------------------------------- |
| Artifact missing             | Stage fails. Re-run after the artifact lands on main.           |
| YAML block missing/malformed | Stage fails. feature-validate's dry-run gate should catch this. |
| Schema validation error      | Stage fails with the offending entry name.                      |
| Partial run (network hiccup) | Re-run is safe — idempotency marker prevents duplicates.        |

## Author

nightgauge
