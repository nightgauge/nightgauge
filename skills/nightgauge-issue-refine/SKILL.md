---
name: nightgauge-issue-refine
description: Analyze a raw GitHub issue and rewrite it with structured sections,
  acceptance criteria, and implementation guidance — making it immediately
  pipeline-ready. Use before /nightgauge-issue-pickup on sparse or unstructured
  issues.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Glob Grep Bash
---

# Issue Refine

> Enrich a raw GitHub issue with structured sections and codebase context

## Description

This skill transforms a sparse or unstructured GitHub issue into a
pipeline-ready issue by:

1. Fetching the raw issue from GitHub
2. Detecting the issue type (bug or feature) from labels and body content
3. Performing codebase-aware analysis using `Glob`/`Grep` to find relevant files
4. Constructing a structured body with all required pipeline sections
5. Updating the issue on GitHub with the enriched content
6. Optionally calling `nightgauge issue mark-refined` (depends on #2533)

## Invocation

| Tool           | Command                                     |
| -------------- | ------------------------------------------- |
| Claude Code    | `/nightgauge:issue-refine <N>` (via plugin) |
| OpenAI Codex   | `$nightgauge-issue-refine <N>`              |
| GitHub Copilot | Invoke via Agent Skills                     |
| Cursor         | Invoke via Agent Skills                     |

## Arguments

```bash
# Refine a specific issue
/nightgauge:issue-refine 42

# Auto-detect from current branch
/nightgauge:issue-refine
```

The `$ARGUMENTS` variable contains everything after the skill name (the issue
number). If omitted, the issue number is inferred from the current branch.

## Philosophy

- **Preserve original content** — the original body is always saved in a
  `<details>` block at the bottom
- **Codebase-grounded** — all file references come from actual `Glob`/`Grep`
  results, never hallucinated
- **Non-destructive** — if required sections already exist, they are not
  duplicated
- **Title-preserving** — the issue title is never modified

---

## Workflow

### Phase Marker Protocol

<!-- phase-registry: standalone-skill -->

This skill is standalone (not a pipeline execution stage), so its `stage="issue-refine"`
emits intentionally do not appear in `PHASE_REGISTRY`. The annotation above opts
the skill out of `scripts/validate-phase-markers.ts`.

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="issue-refine" -->`

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Validate Prerequisites and Resolve Issue Number

```bash
printf '<!-- phase:start name="validate-prerequisites" index=1 total=6 stage="issue-refine" -->\n'
```

#### Step 1.1: Verify GitHub Authentication

```bash
nightgauge forge auth status || { echo "ERROR: Forge auth not configured. Run: nightgauge forge auth login"; exit 1; }
```

#### Step 1.2: Resolve Issue Number

```bash
# Use argument if provided, otherwise infer from current branch
ISSUE_NUMBER="${ARGUMENTS:-$(git branch --show-current 2>/dev/null | grep -oE '[0-9]+' | head -1)}"

if [ -z "$ISSUE_NUMBER" ]; then
  echo "ERROR: No issue number provided and could not infer from branch."
  echo "Usage: /nightgauge:issue-refine <issue-number>"
  echo "Example: /nightgauge:issue-refine 42"
  exit 1
fi

echo "Refining issue #${ISSUE_NUMBER}..."
```

#### Step 1.3: Fetch Issue Details

```bash
ISSUE_JSON=$(nightgauge forge issue view "$ISSUE_NUMBER" --repo $REPO \
  --json number,title,body,labels,comments,state 2>/dev/null)

if [ -z "$ISSUE_JSON" ] || [ "$(echo "$ISSUE_JSON" | jq -r '.number // empty')" = "" ]; then
  echo "ERROR: Issue #${ISSUE_NUMBER} not found or not accessible."
  exit 1
fi

ISSUE_TITLE=$(echo "$ISSUE_JSON" | jq -r '.title')
ISSUE_BODY=$(echo "$ISSUE_JSON" | jq -r '.body // ""')
ISSUE_STATE=$(echo "$ISSUE_JSON" | jq -r '.state')
ISSUE_LABELS=$(echo "$ISSUE_JSON" | jq -r '[.labels[].name] | join(",")')

echo "Issue: #${ISSUE_NUMBER} — ${ISSUE_TITLE}"
echo "State: ${ISSUE_STATE}"
echo "Labels: ${ISSUE_LABELS:-none}"
```

---

### Phase 2: Detect Issue Type and Analyze Existing Content

```bash
printf '<!-- phase:start name="detect-issue-type" index=2 total=6 stage="issue-refine" -->\n'
```

#### Step 2.1: Determine Issue Type

```bash
# Prefer explicit type label
if echo "$ISSUE_LABELS" | grep -q "type:bug"; then
  ISSUE_TYPE="bug"
elif echo "$ISSUE_LABELS" | grep -q "type:feature"; then
  ISSUE_TYPE="feature"
elif echo "$ISSUE_LABELS" | grep -q "type:chore"; then
  ISSUE_TYPE="chore"
elif echo "$ISSUE_LABELS" | grep -q "type:docs"; then
  ISSUE_TYPE="docs"
else
  # Infer from body keywords
  BODY_LOWER=$(echo "$ISSUE_BODY" | tr '[:upper:]' '[:lower:]')
  if echo "$BODY_LOWER" | grep -qE '\b(bug|error|exception|crash|broken|fail|wrong|incorrect|regression|stack trace)\b'; then
    ISSUE_TYPE="bug"
  elif echo "$BODY_LOWER" | grep -qE '\b(add|implement|create|build|feature|enhancement|support|allow)\b'; then
    ISSUE_TYPE="feature"
  else
    ISSUE_TYPE="feature"  # safe default
  fi
  echo "No type label found — inferred type: ${ISSUE_TYPE}"
fi

echo "Issue type: ${ISSUE_TYPE}"
```

#### Step 2.2: Detect Existing Structured Sections

```bash
# Check which pipeline sections already exist to avoid duplication
HAS_SUMMARY=false
HAS_USER_STORY=false
HAS_ACCEPTANCE_CRITERIA=false
HAS_TECHNICAL_NOTES=false
HAS_ROOT_CAUSE=false
HAS_COMPLEXITY=false

echo "$ISSUE_BODY" | grep -q "^## Summary" && HAS_SUMMARY=true
echo "$ISSUE_BODY" | grep -q "^## User Story" && HAS_USER_STORY=true
echo "$ISSUE_BODY" | grep -qE "^## Acceptance Criteria" && HAS_ACCEPTANCE_CRITERIA=true
echo "$ISSUE_BODY" | grep -q "^## Technical Notes" && HAS_TECHNICAL_NOTES=true
echo "$ISSUE_BODY" | grep -q "^## Root Cause" && HAS_ROOT_CAUSE=true
echo "$ISSUE_BODY" | grep -q "^## Complexity" && HAS_COMPLEXITY=true

echo "Existing sections — Summary:${HAS_SUMMARY} AC:${HAS_ACCEPTANCE_CRITERIA} Technical:${HAS_TECHNICAL_NOTES}"
```

---

### Phase 3: Codebase-Aware Analysis

```bash
printf '<!-- phase:start name="codebase-analysis" index=3 total=6 stage="issue-refine" -->\n'
```

**PURPOSE**: Ground the refined issue in actual codebase files. All file
references in the output MUST come from this phase — never from model
generation.

#### Step 3.1: Extract Keywords from Issue

Extract component names, class names, file paths, and error terms mentioned in
the issue title and body:

```bash
# Extract PascalCase component/class names (likely TypeScript/Go identifiers)
COMPONENT_KEYWORDS=$(echo "${ISSUE_TITLE} ${ISSUE_BODY}" | \
  grep -oE '[A-Z][a-zA-Z]{3,}(Service|Manager|Controller|Handler|Provider|View|Panel|Tree|Dashboard|Orchestrator|Selector|Config|Schema|Parser|Client|Router)' | \
  sort -u | head -10)

# Extract file paths or module names explicitly mentioned
EXPLICIT_PATHS=$(echo "${ISSUE_TITLE} ${ISSUE_BODY}" | \
  grep -oE '[a-zA-Z0-9_/.-]+\.(ts|go|tsx|json|yaml|md)' | \
  sort -u | head -10)

# For bugs: extract error message keywords
if [ "$ISSUE_TYPE" = "bug" ]; then
  ERROR_KEYWORDS=$(echo "$ISSUE_BODY" | \
    grep -oE '"[^"]{5,50}"|\`[^`]{5,50}\`|Error: [^\n.]{5,50}' | \
    head -5)
fi

echo "Component keywords: ${COMPONENT_KEYWORDS:-none}"
echo "Explicit paths: ${EXPLICIT_PATHS:-none}"
```

#### Step 3.2: Search Codebase for Related Files

```bash
RELATED_FILES=""

# Use the Grep tool (or rg if available) to find files containing each keyword.
# When executing in a bash block, rg provides the same capability as the Grep tool.
for KW in $COMPONENT_KEYWORDS; do
  MATCHES=$(rg "$KW" \
    --glob "*.ts" --glob "*.tsx" --glob "*.go" \
    -l 2>/dev/null | head -3)
  if [ -z "$MATCHES" ]; then
    # Fallback to grep if rg not available
    MATCHES=$(grep -r "$KW" \
      --include="*.ts" --include="*.tsx" --include="*.go" \
      -l 2>/dev/null | head -3)
  fi
  if [ -n "$MATCHES" ]; then
    RELATED_FILES="${RELATED_FILES}
${MATCHES}"
  fi
done

# Verify explicitly mentioned file paths exist
for PATH_REF in $EXPLICIT_PATHS; do
  if [ -f "$PATH_REF" ]; then
    RELATED_FILES="${RELATED_FILES}
${PATH_REF}"
  fi
done

# Deduplicate and limit
RELATED_FILES=$(echo "$RELATED_FILES" | sort -u | grep -v '^$' | head -10)

echo "Related files found:"
echo "${RELATED_FILES:-  (none — keyword search returned no results)}"
```

#### Step 3.3: Read Key File Context (Bug Path Only)

For bug issues, read the first 1-2 most relevant related files to understand the
code path. This improves the quality of the Root Cause Analysis section.

```bash
if [ "$ISSUE_TYPE" = "bug" ] && [ -n "$RELATED_FILES" ]; then
  FIRST_FILE=$(echo "$RELATED_FILES" | head -1)
  if [ -f "$FIRST_FILE" ]; then
    echo "=== Reading key file: $FIRST_FILE ==="
    # Read first 50 lines for context without overwhelming the agent
    head -50 "$FIRST_FILE"
    echo "=== (truncated) ==="
  fi
fi
```

---

### Phase 4: Construct Refined Issue Body

```bash
printf '<!-- phase:start name="construct-body" index=4 total=6 stage="issue-refine" -->\n'
```

**PURPOSE**: Build the structured body using analysis from phases 2 and 3.

Using the original issue content, inferred type, and codebase file list from
Phase 3, construct a new issue body with the following structure. Reuse existing
sections if they were already present (do not duplicate them).

#### Required Sections

Build the body with these sections in order:

**1. Summary** (always include — expand the title into a clear description):

```markdown
## Summary

{2-4 sentence description of the problem or feature. For bugs: what behavior is
observed vs. expected. For features: what capability is being added and why.
Expand on the original title — do not just repeat it verbatim.}
```

**2. User Story** (features only):

```markdown
## User Story

As a {user persona — developer, user, admin, etc.},
I want {specific action or capability},
So that {business or user value delivered}.
```

**3. Acceptance Criteria** (always include):

```markdown
## Acceptance Criteria

- [ ] {concrete, testable outcome 1}
- [ ] {concrete, testable outcome 2}
- [ ] {concrete, testable outcome 3}
```

Each criterion must be observable and testable. Avoid vague criteria like "works
correctly" — instead: "When X happens, Y is displayed" or "Calling Z returns
status 200 with field F present."

**4. Technical Notes** (always include):

```markdown
## Technical Notes

**Affected files:**
{For each file in RELATED_FILES from Phase 3.2:}

- `{file_path}` — {one line: why this file is relevant}

{If RELATED_FILES is empty:}
**Note:** No files were identified by automated codebase search. Implementation
agent should investigate relevant files during pickup.

**Implementation approach:**
{1-3 sentences of implementation guidance. For bugs: how to reproduce and where
to look. For features: what approach to take and what patterns to follow.}
```

**5. Root Cause Analysis** (bugs only):

```markdown
## Root Cause Analysis

**Observed behavior:** {what the user sees happening}

**Expected behavior:** {what should happen instead}

**Suspected location:** {file or module where the bug likely originates, from
Phase 3.3 analysis — or "Unknown, requires investigation" if no files found}

**Reproduction steps:**

1. {step 1}
2. {step 2}
   (Extract from issue body if present; otherwise note "Not provided — add before
   implementation")
```

**6. Complexity Estimate** (always include):

```markdown
## Complexity Estimate

Size: {XS|S|M|L|XL} — {1-sentence rationale based on number of affected files
and scope of change}
```

Size heuristics:

- **XS**: Single file, <20 lines changed
- **S**: 1-2 files, focused change
- **M**: 3-5 files, moderate scope
- **L**: 5-10 files or cross-cutting concern
- **XL**: 10+ files or architectural change

**7. Related Issues** (include only when dependencies are discovered):

```markdown
## Related Issues

- Depends on #{N} — {reason}
- Part of #{N} — {relationship}
```

**8. Original Issue** (always append — preservation block):

```markdown
<details>
<summary>Original Issue (before refinement)</summary>

{ISSUE_BODY verbatim — even if empty, include the block}

</details>
```

#### Body Construction Rule

- Sections that **already exist** in `ISSUE_BODY` (detected in Phase 2.2) are
  NOT added again. Instead, the existing section content is preserved as-is.
- The `<details>` preservation block is always appended, even if the issue body
  was already structured.
- Do NOT modify the issue title.

---

### Phase 5: Apply Updates to GitHub

```bash
printf '<!-- phase:start name="apply-updates" index=5 total=6 stage="issue-refine" -->\n'
```

#### Step 5.1: Write Body to Temp File

Write the constructed body to a temp file to avoid shell argument escaping issues
with `nightgauge forge issue edit`:

```bash
BODY_FILE=$(mktemp /tmp/issue-refine-body-XXXXXX.md)
# Agent writes NEW_BODY content to $BODY_FILE here
cat > "$BODY_FILE" << 'BODYEOF'
{constructed body from Phase 4}
BODYEOF
```

#### Step 5.2: Update Issue Body

```bash
nightgauge forge issue edit "$ISSUE_NUMBER" --repo $REPO --body-file "$BODY_FILE"

if [ $? -ne 0 ]; then
  echo "ERROR: Failed to update issue #${ISSUE_NUMBER} body."
  rm -f "$BODY_FILE"
  exit 1
fi

rm -f "$BODY_FILE"
echo "Issue #${ISSUE_NUMBER} body updated."
```

#### Step 5.3: Add Type Label if Missing

```bash
if ! echo "$ISSUE_LABELS" | grep -qE '(^|,)type:'; then
  nightgauge forge graphql -f query="mutation{addLabelsToLabelable(input:{labelableId:\"$ISSUE_NUMBER\",labelIds:[\"type:${ISSUE_TYPE}\"]}){clientMutationId}}" 2>/dev/null || \
    echo "WARNING: Could not add type:${ISSUE_TYPE} label (may not exist in repo)."
fi
```

#### Step 5.4: Mark as Refined (Graceful Fallback)

Call the Go binary's `mark-refined` command if available. This depends on
issue #2533. If the command does not exist, skip silently.

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

if [ -n "$BINARY" ]; then
  "$BINARY" issue mark-refined "$ISSUE_NUMBER" 2>/dev/null || true
fi
```

#### Step 5.5: Report Success

```bash
ISSUE_URL=$(nightgauge forge issue view "$ISSUE_NUMBER" --repo $REPO --json url --jq .url 2>/dev/null || \
  echo "https://github.com/$(nightgauge forge repo view --repo $REPO --json nameWithOwner --jq .nameWithOwner)/issues/${ISSUE_NUMBER}")

echo ""
echo "✓ Issue #${ISSUE_NUMBER} refined successfully"
echo "  URL: ${ISSUE_URL}"
echo "  Type: ${ISSUE_TYPE}"
echo "  Files referenced: $(echo "${RELATED_FILES}" | grep -c . 2>/dev/null || echo 0)"
echo ""
echo "Next step: /nightgauge-issue-pickup ${ISSUE_NUMBER}"
```

---

### Phase 6: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=6 total=6 stage="issue-refine" -->\n'
```

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Condition                           | Action                                                       |
| ----------------------------------- | ------------------------------------------------------------ |
| Forge auth not configured           | Exit 1 with `nightgauge forge auth login` remediation        |
| Issue not found                     | Exit 1 with clear message                                    |
| Issue already fully structured      | Detect existing sections, append only missing ones           |
| No related files found              | Include note in Technical Notes; do not fabricate file paths |
| `nightgauge forge issue edit` fails | Exit 1 with error output                                     |
| `mark-refined` binary missing       | Skip silently (graceful fallback) — depends on #2533         |

## Completion Checklist

- [ ] Issue body updated with structured sections
- [ ] Original content preserved in `<details>` block
- [ ] Type label added if it was missing
- [ ] `mark-refined` called (or gracefully skipped if #2533 not merged)
- [ ] Issue title unchanged
- [ ] All file references come from actual `Glob`/`Grep` results
- [ ] Next step (`/nightgauge-issue-pickup`) displayed
