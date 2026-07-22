---
name: nightgauge-issue-audit
description: Deterministic post-creation gate that verifies every issue created by issue-create
  (or any creation flow) has the labels, board membership, body sections, sub-issue
  links, blockedBy edges, cross-repo references, and knowledge scaffold the pipeline
  depends on. Reports findings by severity and offers --fix / --fix-interactive
  auto-repair using existing Go binary primitives. Use after every issue-creation
  flow or as a CI safety net.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.2"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
---

# Issue Audit

Deterministic post-creation gate for every issue-creation flow. See
[docs/ISSUE_AUDIT.md](../../docs/ISSUE_AUDIT.md) for the finding taxonomy,
severity rules, manifest schema, and repair-primitive table. This skill
encodes the orchestration; the doc encodes the spec.

## When to Use

- **Terminal pass after `/nightgauge:issue-create`** — runs automatically
  unless `--no-audit` is passed
- **After manually creating issues** with `nightgauge forge issue create` followed by board
  sync
- **CI safety net** with `--all-recent 1h` to catch any creation flow that
  bypassed audit
- **Replaces `/nightgauge:epic-validate`** — that skill is now a thin
  wrapper that delegates to `issue-audit --epic <N>`

## Outcomes

- Markdown finding report at
  `.nightgauge/pipeline/issue-audit-<timestamp>.md`
- JSON findings at `.nightgauge/pipeline/issue-audit-<timestamp>.json`
- JSONL audit trail at
  `.nightgauge/pipeline/issue-audit-<timestamp>.audit.jsonl`
- Exit 0 (READY) when no CRITICAL findings remain; exit 1 (NEEDS FIXES) when
  any CRITICAL persists; exit 2 on skill-level failure
- Auto-repair via `--fix` for safe categories (board, fields, links,
  blockedBy edges, body Part-of insertion); `--fix-interactive` for
  `closed-as-not-planned` blockers
- Hard rules: never auto-rewrite human-authored content; spike-contract
  violations stay CRITICAL even with `--fix`

## Input

```
/nightgauge:issue-audit --manifest <path>          # strict mode
/nightgauge:issue-audit --epic <N> [--repo <O/R>]  # inferential, single epic
/nightgauge:issue-audit --issues <N1,N2,N3> [--repo <O/R>]
/nightgauge:issue-audit --all-recent <duration>    # e.g. 1h, 30m
```

Run modes: dry-run (default), `--fix`, `--fix-interactive`.

Output flags: `--json`, `--no-audit-trail`, `--allow-closed`.

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Parse Arguments and Resolve Targets

Distinguish invocation mode from `$ARGUMENTS`:

```bash
MODE=""
MANIFEST_PATH=""
EPIC_NUMBER=""
ISSUE_LIST=""
RECENT_DURATION=""
RUN_MODE="dry-run"
JSON_OUTPUT=false
WRITE_TRAIL=true
ALLOW_CLOSED=false
REPO_OVERRIDE=""

while [ $# -gt 0 ]; do
  case "$1" in
    --manifest) MODE="manifest"; MANIFEST_PATH="$2"; shift 2 ;;
    --epic) MODE="epic"; EPIC_NUMBER="$2"; shift 2 ;;
    --issues) MODE="issues"; ISSUE_LIST="$2"; shift 2 ;;
    --all-recent) MODE="all-recent"; RECENT_DURATION="$2"; shift 2 ;;
    --fix) RUN_MODE="fix"; shift ;;
    --fix-interactive) RUN_MODE="fix-interactive"; shift ;;
    --json) JSON_OUTPUT=true; shift ;;
    --no-audit-trail) WRITE_TRAIL=false; shift ;;
    --allow-closed) ALLOW_CLOSED=true; shift ;;
    --repo) REPO_OVERRIDE="$2"; shift 2 ;;
    *) echo "ERROR: unknown flag $1" >&2; exit 2 ;;
  esac
done

if [ -z "$MODE" ]; then
  echo "ERROR: must specify one of --manifest, --epic, --issues, --all-recent" >&2
  exit 2
fi
```

#### Resolve target issues

- **`--manifest`**: read JSON, parse via `jq`, fail-exit 2 on malformed JSON.
  Each entry's `repo` and `number` becomes a target.
- **`--epic <N>`**: fetch the epic's `subIssues(first: 50)` via GraphQL; the
  epic + every sub-issue are targets.
- **`--issues N1,N2,N3`**: split on comma; each is a target in `--repo` (or
  current repo).
- **`--all-recent <dur>`**: query
  `nightgauge forge issue list --repo $REPO --search 'created:>=<since>' --json number,repository`
  across the configured repos.

For inferential modes, the audit synthesizes a per-target _expected_ shape
from issue type, project config, and body content. Strict mode uses the
manifest as the source of truth.

#### Initialize report files

```bash
TS=$(date -u +%Y%m%dT%H%M%SZ)
REPORT_DIR=".nightgauge/pipeline"
mkdir -p "$REPORT_DIR"
REPORT_MD="$REPORT_DIR/issue-audit-${TS}.md"
REPORT_JSON="$REPORT_DIR/issue-audit-${TS}.json"
TRAIL_JSONL="$REPORT_DIR/issue-audit-${TS}.audit.jsonl"
FINDINGS=()  # populated as JSON-line strings throughout the run
```

---

### Phase 2: Existence & Repo Placement

For each target `(owner/repo, number)`:

```bash
ISSUE_JSON=$(nightgauge forge issue view "$NUM" --repo "$OWNER_REPO" --json 2>/dev/null) || {
  FINDINGS+=("$(jq -nc --arg r "$OWNER_REPO" --arg n "$NUM" '
    {phase:1,type:"MISSING_ISSUE",severity:"CRITICAL",
     issue:{repo:$r,number:($n|tonumber)},
     detail:"Issue not found via nightgauge forge issue view"}')")
  continue
}

# WRONG_REPO: the api response's repository.nameWithOwner ≠ expected
ACTUAL_REPO=$(echo "$ISSUE_JSON" | jq -r '.repository.nameWithOwner // empty')
if [ -n "$ACTUAL_REPO" ] && [ "$ACTUAL_REPO" != "$OWNER_REPO" ]; then
  # emit WRONG_REPO finding
  ...
fi

# UNEXPECTED_STATE: closed but not allowed
STATE=$(echo "$ISSUE_JSON" | jq -r '.state')
if [ "$STATE" = "CLOSED" ] && [ "$ALLOW_CLOSED" != "true" ]; then
  # emit UNEXPECTED_STATE finding (WARNING)
  ...
fi
```

---

### Phase 3: Label Completeness

For each target's labels:

- `MISSING_TYPE_LABEL` (CRITICAL) when no `type:*` present
- `MULTIPLE_TYPE_LABELS` (CRITICAL) when more than one
- `MISSING_COMPONENT_LABEL` (INFO) when body cites a component path but no
  `component:*` label

Priority and Size are project board fields; Phase 4 checks them.

---

### Phase 4: Project Board Membership and Fields

```bash
PROJECT_QUERY=$(nightgauge forge graphql -f query='query($owner:String!,$name:String!,$num:Int!){
  repository(owner:$owner,name:$name){
    issue(number:$num){
      projectItems(first:10){
        nodes{
          project{number}
          fieldValues(first:30){
            nodes{
              ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2SingleSelectField { name } } }
            }
          }
        }
      }
    }
  }
}' -f owner="$OWNER" -f name="$NAME" -F num="$NUM")
```

Findings:

- `MISSING_FROM_BOARD` (CRITICAL): no `projectItems` entry
- `WRONG_BOARD` (CRITICAL): on a project that does not match the expected
  `project_number` (manifest or workspace yaml mapping)
- `MISSING_STATUS_FIELD` (CRITICAL): item exists but Status is unset
- `MISSING_PRIORITY_FIELD` (WARNING): Priority unset
- `MISSING_SIZE_FIELD` (WARNING): Size unset

---

### Phase 5: Body Section Completeness

Required headings table (per type):

| Type     | Required headings                             |
| -------- | --------------------------------------------- |
| feature  | Summary, Acceptance Criteria                  |
| bug      | Summary, Steps to Reproduce, Expected, Actual |
| docs     | Summary, Acceptance Criteria                  |
| refactor | Summary, Acceptance Criteria                  |
| spike    | Summary, Acceptance Criteria, Recommendations |
| chore    | Summary                                       |
| epic     | Summary, Sub-Issues, Acceptance Criteria      |

For each required heading:

```bash
BODY=$(echo "$ISSUE_JSON" | jq -r '.body // ""')
if ! echo "$BODY" | grep -qE "^##[[:space:]]+${HEADING}\\s*$"; then
  FINDINGS+=( /* MISSING_REQUIRED_HEADING (WARNING) */ )
  continue
fi

# Non-empty content check between this heading and the next ##
SECTION=$(echo "$BODY" | awk -v h="^##[[:space:]]+${HEADING}\\s*$" '
  $0 ~ h {flag=1; next}
  flag && /^##[[:space:]]/ {flag=0}
  flag {print}')
if [ -z "$(echo "$SECTION" | tr -d '[:space:]')" ]; then
  FINDINGS+=( /* EMPTY_REQUIRED_HEADING (WARNING) */ )
fi
```

For `type: spike`:

````bash
# Verify the yaml recommendations block exists and parses
if ! echo "$BODY" | awk '/```yaml recommendations/,/```/' | head -2 | grep -q '```yaml recommendations'; then
  FINDINGS+=( /* MISSING_SPIKE_RECS_BLOCK (CRITICAL) — never auto-fix */ )
fi
````

The `MISSING_SPIKE_RECS_BLOCK` finding has **no repair primitive**. Even with
`--fix`, this remains CRITICAL — encoded in Phase 9 logic and pinned by the
`spike-contract-violation` test fixture.

#### Oversized-scope check (all issue types)

Catches issues that bundle many independent refactors into a single executable
ticket — the root cause of pipeline runaways (incident #3811: $112.77 of
feature-dev churn on one issue that meant "refactor ~18 skills"). This mirrors
the `issue-create` Phase 2.85 oversized-scope gate so manually-created issues
(which never pass through issue-create) are flagged post-creation.

```bash
# Signal 1: distinct CHANGE-TARGET files via the shared deterministic
# extractor (#79) — the SAME implementation the epic wave planner uses, so
# audit counting can never drift from wave planning. Markdown-link
# destinations are citations and never count; an explicit `file_ownership`
# list in the `nightgauge:dependency-metadata` block replaces prose inference.
DISTINCT_TARGETS=$(printf '%s' "$BODY" \
  | nightgauge issue extract-targets --json 2>/dev/null \
  | jq -r '.count' 2>/dev/null || echo 0)

# Signal 2: predicted size == XL (objective estimate via the Go binary)
PREDICTED_SIZE=$(nightgauge size predict "$NUM" --json 2>/dev/null \
  | jq -r '.SizeLabel // empty')

# Signal 3: independent acceptance-criteria groups (refactor/migrate/... bullets)
AC_GROUP_COUNT=$(printf '%s\n' "$BODY" \
  | grep -ciE '^[[:space:]]*([-*]|[0-9]+\.)[[:space:]]+(refactor|migrate|convert|split|rewrite|extract|decompose|reduce|trim)[[:space:]]')

# Override marker (same marker honored by issue-create Phase 2.85)
SCOPE_OVERRIDE=false
if printf '%s' "$BODY" | grep -qi "nightgauge:oversized-scope-accepted\|oversized scope accepted"; then
  SCOPE_OVERRIDE=true
fi

# An epic with native sub-issues is the correct shape for large scope — exempt.
# Labels come from ISSUE_JSON (Phase 2); sub-issue count via the same subIssues
# query Phase 6 uses.
IS_DECOMPOSED_EPIC=false
if echo "$ISSUE_JSON" | jq -e '.labels[]? | select(.name == "type:epic")' >/dev/null 2>&1; then
  SUB_ISSUE_COUNT=$(nightgauge forge graphql -f query='query($owner:String!,$name:String!,$num:Int!){
    repository(owner:$owner,name:$name){issue(number:$num){subIssues(first:50){totalCount}}}
  }' -f owner="$OWNER" -f name="$NAME" -F num="$NUM" \
    -q '.data.repository.issue.subIssues.totalCount' 2>/dev/null || echo 0)
  if [ "${SUB_ISSUE_COUNT:-0}" -gt 0 ]; then
    IS_DECOMPOSED_EPIC=true
  fi
fi

# Trigger: >=6 distinct targets OR size==XL OR >=6 independent AC groups,
# AND not already a decomposed epic AND no override marker.
if [ "$IS_DECOMPOSED_EPIC" = "false" ] && [ "$SCOPE_OVERRIDE" = "false" ] && {
     [ "${DISTINCT_TARGETS:-0}" -ge 6 ] || \
     echo "$PREDICTED_SIZE" | grep -qiE '^XL$' || \
     [ "${AC_GROUP_COUNT:-0}" -ge 6 ]; }; then
  FINDINGS+=( /* OVERSIZED_SCOPE (WARNING) — no auto-fix; requires manual decomposition */ )
fi
```

Finding:

- `OVERSIZED_SCOPE` (WARNING): the issue has ≥6 distinct change-target files
  (per the shared extractor — markdown-link citations excluded, explicit
  `file_ownership` declarations win, #79), OR has predicted size `XL`, OR
  enumerates ≥6 independent refactor/migration acceptance-criteria groups —
  and is neither a decomposed epic (`type:epic` with native sub-issues) nor
  carries the `<!-- nightgauge:oversized-scope-accepted -->` override marker. This
  finding has **no repair primitive**: decomposition into sub-issues under an
  epic is a human/planning decision, never auto-applied by `--fix`. Even with
  `--fix`, `OVERSIZED_SCOPE` remains a WARNING.

---

### Phase 6: Sub-Issue & Parent Linking

Use `nightgauge forge graphql` `subIssues(first: 50)`:

```bash
SUB_ISSUES=$(nightgauge forge graphql -f query='query($owner:String!,$name:String!,$num:Int!){
  repository(owner:$owner,name:$name){issue(number:$num){
    subIssues(first:50){nodes{number repository{nameWithOwner}}}
  }}
}' -f owner="$OWNER" -f name="$NAME" -F num="$NUM" \
  -q '.data.repository.issue.subIssues.nodes')
```

Findings:

- `MISSING_SUB_ISSUE_LINK` (CRITICAL): manifest declares sub-issue #M but no
  matching node in `subIssues`
- `MISSING_PART_OF_ANNOTATION` (CRITICAL): cross-repo sub-issue body lacks
  `Part of <owner>/<repo>#<epic>`
- `ORPHAN_SUB_ISSUE` (WARNING): body has `Part of #X` but parent `X` has no
  matching native sub-issue

---

### Phase 7: blockedBy Alignment

Delegate the structural check to the Go binary:

```bash
if [ "$MODE" = "epic" ] && [ -n "$EPIC_NUMBER" ]; then
  EPIC_VALIDATE=$("$BINARY" epic validate "$EPIC_NUMBER" --json 2>/dev/null)
  GAPS=$(echo "$EPIC_VALIDATE" | jq -c '.gaps // []')
  echo "$GAPS" | jq -c '.[]' | while read -r gap; do
    GAP_TYPE=$(echo "$gap" | jq -r '.gapType')
    SUB=$(echo "$gap" | jq -r '.subIssueNumber')
    DETAIL=$(echo "$gap" | jq -r '.detail')
    case "$GAP_TYPE" in
      circular_blocker) /* emit CIRCULAR_BLOCKER (CRITICAL) */ ;;
      stale_blocker)    /* emit STALE_BLOCKED_BY (WARNING) */ ;;
    esac
  done
fi
```

For body-declared dependencies (`Depends on:` lines and the
`<!-- nightgauge:dependency-metadata -->` YAML block), parse and verify
each declaration has a matching native `blockedBy` edge:

- `MISSING_BLOCKED_BY` (CRITICAL): declared but not wired
- `STALE_BLOCKED_BY_NOT_PLANNED` (WARNING): blocker is closed-as-not-planned
- `CROSS_REPO_BLOCKER_MISSING` (CRITICAL): cross-repo blocker not found

---

### Phase 8: Cross-Repo Consistency

When the audit set spans multiple repos (manifest entries with different
`repo`, or cross-repo body annotations):

```bash
"$BINARY" audit lifecycle --json 2>/dev/null > /tmp/lifecycle.json
```

Parse the lifecycle output for cross-repo references and emit:

- `MISSING_PARENT_BACKREF` (WARNING): cross-repo sub-issue references parent
  but parent epic body has no link back
- `INCONSISTENT_PROJECT_MAPPING` (CRITICAL): two sub-issues in the same
  target repo are in different projects

---

### Phase 9: Knowledge Scaffold

When `knowledge.enabled: true` in `.nightgauge/config.yaml` OR the
manifest entry sets `knowledge_path`:

```bash
KNOWLEDGE_PATH=$(echo "$ENTRY" | jq -r '.knowledge_path // empty')
if [ -n "$KNOWLEDGE_PATH" ]; then
  if [ ! -d "$KNOWLEDGE_PATH" ]; then
    FINDINGS+=( /* MISSING_KNOWLEDGE_DIR (WARNING) */ )
  elif [ ! -f "$KNOWLEDGE_PATH/PRD.md" ]; then
    FINDINGS+=( /* MISSING_PRD_FILE (WARNING) */ )
  else
    PRD_STRIPPED=$(sed -e 's/<!--.*-->//g' -e '/^#/d' "$KNOWLEDGE_PATH/PRD.md" | tr -s '[:space:]' ' ' | sed 's/^ *//;s/ *$//')
    if [ ${#PRD_STRIPPED} -lt 30 ]; then
      FINDINGS+=( /* EMPTY_PRD_FILE (INFO) */ )
    fi
  fi
fi
```

---

### Phase 10: Findings Synthesis

Aggregate `${FINDINGS[@]}` into a JSON array. Compute summary counts. Verdict
is `READY` when `critical == 0`, else `NEEDS FIXES`.

Severity-tiered Markdown output (CRITICAL first, then WARNING, then INFO).
Per finding, include:

- Issue (`<owner>/<repo>#<n>`)
- Phase number
- Finding type
- Detail
- Repair command (or `(no auto-fix available)` for human-only items)

---

### Phase 11: Auto-Repair (when `--fix` / `--fix-interactive`)

Walk findings in order. For each repairable category, invoke the listed
primitive (see `docs/ISSUE_AUDIT.md` repair-primitive table). Each repair
appends one JSON line to the audit trail:

```bash
echo '{"ts":"'$(date -u +%Y-%m-%dT%H:%M:%SZ)'","issue":"'$ISSUE'","finding":"'$TYPE'","action":"'$ACTION'","before":'$BEFORE',"after":'$AFTER',"actor":"nightgauge-issue-audit"}' >> "$TRAIL_JSONL"
```

#### Hard rules (encoded; pinned by negative test fixture)

1. **Never auto-rewrite human-authored content**: Summary, Acceptance
   Criteria, User Story, Technical Notes, spike artifact body. Findings that
   require human content stay flagged.
2. **`MISSING_SPIKE_RECS_BLOCK` is never auto-fixed**: even with `--fix`, this
   stays CRITICAL.
3. **`OVERSIZED_SCOPE` is never auto-fixed**: decomposing a bundled issue into
   sub-issues under an epic is a human/planning decision. Even with `--fix`,
   this stays a WARNING.
4. **`STALE_BLOCKED_BY_NOT_PLANNED` requires `--fix-interactive`**: under
   `--fix` alone, the finding remains a WARNING. Under `--fix-interactive`,
   prompt the operator and only repair on explicit `yes`.
5. **Repair failures become CRITICAL**: a non-zero exit from the primitive
   reclassifies the finding as CRITICAL with the underlying error in
   `repair_error`. The audit does not retry — operator re-runs.

#### Repair-primitive map (CRITICAL/WARNING items only)

| Finding type                    | Primitive command                                                               |
| ------------------------------- | ------------------------------------------------------------------------------- |
| `MISSING_FROM_BOARD`            | `nightgauge project add <num> --repo <owner/repo> --project <num>`              |
| `WRONG_BOARD`                   | `nightgauge project add <num> --repo <owner/repo> --project <num>` (idempotent) |
| `MISSING_STATUS_FIELD`          | `nightgauge project sync-status <num> <expected_status>`                        |
| `MISSING_PRIORITY_FIELD`        | `nightgauge project set-field <num> Priority <P0\|P1\|P2\|P3>`                  |
| `MISSING_SIZE_FIELD`            | `nightgauge project set-field <num> Size <XS\|S\|M\|L\|XL>`                     |
| `MISSING_SUB_ISSUE_LINK` (same) | `nightgauge issue link-sub <epic> <sub>`                                        |
| `MISSING_PART_OF_ANNOTATION`    | `nightgauge issue edit <num> --append-body "Part of <owner>/<repo>#<epic>"`     |
| `MISSING_BLOCKED_BY`            | `nightgauge issue add-blocked-by <blocked> <blocker>`                           |
| `STALE_BLOCKED_BY` (completed)  | `nightgauge issue remove-blocked-by <blocked> <blocker>` (auto in `--fix`)      |
| `STALE_BLOCKED_BY_NOT_PLANNED`  | `nightgauge issue remove-blocked-by` (only in `--fix-interactive` after `yes`)  |
| `CIRCULAR_BLOCKER`              | `nightgauge issue remove-blocked-by <sub> <epic>` (auto in `--fix`)             |
| `MISSING_PARENT_BACKREF`        | `nightgauge issue edit <epic> --append-body "<cross-repo link line>"`           |

`MISSING_TYPE_LABEL`, `MULTIPLE_TYPE_LABELS`, `MISSING_REQUIRED_HEADING`,
`EMPTY_REQUIRED_HEADING`, `MISSING_SPIKE_RECS_BLOCK`, `OVERSIZED_SCOPE`,
`INCONSISTENT_PROJECT_MAPPING`, `EMPTY_PRD_FILE` — no auto-fix. Human action
required.

---

### Phase 12: Report Write and Exit

```bash
# JSON
jq -n \
  --argjson summary "$SUMMARY_JSON" \
  --argjson findings "$FINDINGS_JSON" \
  --argjson audited "$AUDITED_JSON" \
  --arg verdict "$VERDICT" \
  --arg started "$STARTED_AT" \
  --arg completed "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{schema_version:"1.0",verdict:$verdict,summary:$summary,
    audited:$audited,findings:$findings,
    started_at:$started,completed_at:$completed}' > "$REPORT_JSON"

# Markdown — written line-by-line (omitted here for brevity)
# ...

# stdout: verdict line and report paths
echo "Verdict: $VERDICT ($CRITICAL_COUNT CRITICAL, $WARNING_COUNT WARNING, $INFO_COUNT INFO)"
echo "Report: $REPORT_MD"
echo "JSON:   $REPORT_JSON"
[ "$WRITE_TRAIL" = "true" ] && echo "Trail:  $TRAIL_JSONL"

if [ "$JSON_OUTPUT" = "true" ]; then
  cat "$REPORT_JSON"
fi

if [ "$CRITICAL_COUNT" -gt 0 ]; then
  exit 1
fi
exit 0
```

---

## Decision Rules

- **Strict vs inferential**: when `--manifest` is set, every assertion
  declared in the manifest must hold; missing fields in inferential mode
  derive expectations from issue type and project config.
- **Closed issues**: by default, closed issues are flagged
  `UNEXPECTED_STATE` (WARNING). `--allow-closed` suppresses this and runs
  only state-relevant phases (existence, body sections, knowledge scaffold).
- **Single-issue runs**: `--issues <single>` is valid; the audit runs every
  applicable phase but skips Phase 6 sub-issue checks.
- **Cross-repo**: when an issue is in a different repo than the current one,
  every `nightgauge forge` call must pass `--repo <owner/repo>`; never rely on the current
  repo default.
- **Idempotency**: re-running `--fix` on a previously-fixed audit MUST be a
  no-op. Repair primitives are idempotent.

## Failure Conditions (exit 2)

The skill exits 2 — distinct from finding-driven exit 1 — when the audit
itself cannot run:

- Go binary not in `$PATH` and not at `bin/nightgauge`
- `nightgauge forge` not authenticated
- `--manifest <path>` does not exist or fails JSON parse
- `.nightgauge/config.yaml` unreadable when project number must be
  derived

## Completion Checklist

- [ ] Mode and target issues resolved
- [ ] Existence + repo placement checked (Phase 2)
- [ ] Label completeness checked (Phase 3)
- [ ] Project board membership and fields checked (Phase 4)
- [ ] Body section completeness checked, including spike contract (Phase 5)
- [ ] Sub-issue and parent linking checked (Phase 6)
- [ ] `blockedBy` alignment checked via `epic validate --json` (Phase 7)
- [ ] Cross-repo consistency checked (Phase 8)
- [ ] Knowledge scaffold checked (Phase 9)
- [ ] Findings synthesized with verdict (Phase 10)
- [ ] Repairs applied (only if `--fix` / `--fix-interactive`); failures
      reclassified as CRITICAL (Phase 11)
- [ ] Markdown + JSON report written; audit trail JSONL written unless
      `--no-audit-trail` (Phase 12)
- [ ] Exit code reflects verdict (0 READY, 1 NEEDS FIXES, 2 skill failure)

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->
