# PR Create — Knowledge & What-to-Test Sections (Phases 1.7, 1.8)

Procedural detail for Phase 1.7 (Build Knowledge Section) and Phase 1.8 (Build
What to Test Section). Both produce optional PR-body sections appended in
Phase 3.

## Contents

- [Phase 1.7: build knowledge section](#phase-17-build-knowledge-section)
- [Phase 1.8: build what to test section](#phase-18-build-what-to-test-section)

## Phase 1.7: build knowledge section

**PURPOSE**: Construct the `## Knowledge` section for the PR body from knowledge
base entries created during the pipeline run. This section is **omitted
entirely** when no knowledge entries exist — never include an empty section.

**No-op when**: the issue's knowledge directory does not exist or contains no
qualifying entries — the verb prints nothing and exits 0 in both cases.

```bash
KNOWLEDGE_SECTION=""

if [ -n "$KNOWLEDGE_PATH" ]; then
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
    KNOWLEDGE_SECTION=$("$BINARY" knowledge render-pr-section --issue "$ISSUE_NUMBER" 2>/dev/null || true)
  fi
fi
```

**Link format**: Relative paths from the repository root — e.g.,
`.nightgauge/knowledge/features/1234-slug/PRD.md`. GitHub renders these as
clickable links in PR descriptions.

**Result**: `KNOWLEDGE_SECTION` is either a non-empty string containing the
rendered `## Knowledge` block (terminated with a single trailing newline) or an
empty string. Phase 3 appends it to the PR body when non-empty. The verb
performs the dictionary lookup and Markdown emission deterministically — see
`docs/GO_BINARY.md` for the full reference.

## Phase 1.8: build what to test section

**PURPOSE**: Generate the `## What to Test` section from the dependency graph
and the feature branch diff. Appended to the PR body after `## Validation` and
before `## Knowledge`. **No-op** when the dependency graph file is absent or
when the git diff produces no output — set `WHAT_TO_TEST_SECTION=""` and
continue without error.

**Base branch early resolution** — `BASE_BRANCH` is needed here but formally
determined in Phase 2. Resolve it early using the same priority:

```bash
# Early base branch resolution for diff
BASE_BRANCH=$(jq -r '.base_branch // empty' \
  ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)
if [ -z "$BASE_BRANCH" ]; then
  BASE_BRANCH=$(git config --get nightgauge.branch.base 2>/dev/null || echo "main")
fi
```

```bash
WHAT_TO_TEST_SECTION=""
GRAPH_FILE=".nightgauge/dependency-graph.json"

if [ -f "$GRAPH_FILE" ]; then
  DIFF_OUTPUT=$(git diff "${BASE_BRANCH}...HEAD" --name-status 2>/dev/null || true)
  if [ -n "$DIFF_OUTPUT" ]; then
    WHAT_TO_TEST_SECTION=$(DIFF_OUTPUT="$DIFF_OUTPUT" node --input-type=module << 'EOF'
import { loadGraph, analyzeImpactFromDiff, generateWhatToTestSection } from '@nightgauge/sdk';
const fs = await import('fs');
try {
  const graph = JSON.parse(fs.readFileSync('.nightgauge/dependency-graph.json', 'utf8'));
  const diffOutput = process.env.DIFF_OUTPUT ?? '';
  const result = analyzeImpactFromDiff(diffOutput, graph);
  const section = generateWhatToTestSection(result);
  process.stdout.write(section.markdown);
} catch (err) {
  process.stderr.write('Warning: What to Test section skipped: ' + String(err) + '\n');
  process.stdout.write('');
}
EOF
    ) || true
  fi
fi
```

**No-op conditions** — `WHAT_TO_TEST_SECTION` remains `""`:

- `.nightgauge/dependency-graph.json` does not exist
- `git diff` produces no output (branch has no commits beyond base)
- SDK throws during analysis (error logged to stderr; stage continues)

**Result**: `WHAT_TO_TEST_SECTION` is either a non-empty Markdown string or
`""`. Phase 3 appends it to the PR body after `## Validation` when non-empty.
