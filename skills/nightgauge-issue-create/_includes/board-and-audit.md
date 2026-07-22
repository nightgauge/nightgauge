# Phases 4–6: Board Sync, Audit & Terminal Pass — Procedural Detail

Detail bodies for Phase 4 (Sync Project Board), 4.6 (Promote to Ready), 4.7 (Verification), 4.8 (Cross-Repo Membership Audit), 4.5 (Knowledge Scaffolding), 4.9 (Write Creation Manifest), 5 (Return Structured Result), and 6 (Terminal Audit Pass) of the `nightgauge-issue-create` skill. Sections appear in the skill's execution order (4 → 4.6 → 4.7 → 4.8 → 4.5 → 4.9 → 5 → 6).

## Contents

- [Phase 4: Sync Project Board and Set Fields](#phase-4-sync-project-board-and-set-fields)
- [Phase 4.6: Promote to Ready](#phase-46-promote-to-ready)
- [Phase 4.7: Verification](#phase-47-verification)
- [Phase 4.8: Cross-Repo Project Membership Audit](#phase-48-cross-repo-project-membership-audit)
- [Phase 4.5: Knowledge Scaffolding](#phase-45-knowledge-scaffolding)
- [Phase 4.9: Write Creation Manifest](#phase-49-write-creation-manifest)
- [Phase 5: Return Structured Result](#phase-5-return-structured-result)
- [Phase 6: Terminal Audit Pass](#phase-6-terminal-audit-pass)

## Phase 4: Sync Project Board and Set Fields (Mandatory)

All created issues MUST be added to the project board and have their Status
field set. Without this, issues are invisible in the extension's tree views.

**REPO-PROJECT BINDING (#3232)**: Every `nightgauge project add` call
in this phase MUST pass BOTH `--repo` and `--project` derived from the Phase
2.4 routing manifest (or the workspace yaml fallback for the standalone case).
Defaults exist on the binary, but defaults caused the #3232 silent-misroute
incident — calling without explicit flags is forbidden by this skill.

#### Pre-flight: Check Binary Availability

The binary must already be verified in Phase 1. If it is unavailable, the skill
has already exited with a clear error. There is no fallback.

```bash
if [ -z "${BINARY:-}" ]; then
  echo "ERROR: nightgauge binary required for project board sync."
  echo "Cannot proceed without it — there is no fallback."
  exit 1
fi
```

#### Standalone issues

```bash
# Atomic: add to board AND set Status=Ready in a single deterministic call.
# --repo and --project are MANDATORY (#3232) so the issue lands in the project
# matching its repo. Derive both from the workspace yaml (lookup the issue's
# repo entry in `repositories[]`).
nightgauge project add <issue-number> \
  --repo "<repo-name>" \
  --project <project-number> \
  --status Ready
```

#### Epics with sub-issues

Epics intentionally start in Backlog. Sub-issues need `addSubIssue` and
`blockedBy` relationships wired before the autonomous scheduler is allowed
to dispatch them — Phase 4.6 promotes them to Ready in bulk after that.

```bash
# 0. Read the routing manifest written in Phase 2.4
MANIFEST=".nightgauge/pipeline/issue-create-routing-<epic-number>.json"
test -f "$MANIFEST" || { echo "ERROR: routing-manifest-missing"; exit 1; }

# 1. Add the epic itself to the board (always lives in the workspace primary repo)
nightgauge project add <epic-number> \
  --repo "<primary-repo>" \
  --project <primary-project-number>
nightgauge project sync-status <epic-number> backlog \
  --repo "<primary-repo>" --project <primary-project-number>

# 2. Add each sub-issue to ITS OWN repo's project per the manifest.
#    Cross-repo sub-issues are added to a different project than the epic.
jq -c '.sub_issues[]' "$MANIFEST" | while read -r row; do
  NUM=$(echo "$row" | jq -r .number)
  REPO=$(echo "$row" | jq -r .target_repo)
  PROJ=$(echo "$row" | jq -r .target_project)
  nightgauge project add "$NUM" --repo "$REPO" --project "$PROJ"
  nightgauge project sync-status "$NUM" backlog --repo "$REPO" --project "$PROJ"
done
```

**NOTE**: `nightgauge project add` adds the issue to the board AND
automatically sets Priority and Size fields when matching labels are present.
The internal `syncLabelsToFields` step maps labels as follows:

- `priority:critical` → P0, `priority:high` → P1, `priority:medium` → P2,
  `priority:low` → P3
- `size:XS` → XS, `size:S` → S, `size:M` → M, `size:L` → L, `size:XL` → XL
- `status:*` labels → corresponding Status field value

If the issue has `priority:*` and `size:*` labels at creation time, `project add`
sets the corresponding board fields automatically. If fields are still empty
after `project add` (e.g., labels were added after the add, or the binary
fallback was used), set them explicitly:

```bash
nightgauge project set-field <number> --priority P1 --size M
```

## Phase 4.6: Promote to Ready (After All Relationships Are Configured)

**Epics only** — standalone issues are already promoted in Phase 4 via
`nightgauge project add --status Ready`.

After ALL of the following are complete:

- Issues added to the project board (Phase 4)
- Priority and Size fields set
- `blockedBy` relationships applied (Phase 3.5, if applicable)
- `addSubIssue` linking done (Phase 3, if applicable)

...promote epics and their sub-issues from Backlog to Ready. The autonomous
scheduler only dispatches issues in "Ready" status, so this is the gate that
prevents the race condition where issues are dispatched before relationships
are configured.

#### Epics with sub-issues

```bash
# Promote the epic itself
nightgauge project sync-status <epic-number> ready

# Promote all sub-issues to Ready
# (blockedBy relationships enforce ordering — the pipeline respects them
# even when all sub-issues are in Ready status)
for SUB_NUMBER in <sub-issue-numbers>; do
  nightgauge project sync-status "$SUB_NUMBER" ready
done
```

**WHY**: Issues start in Backlog (Phase 4) to give Phase 3.5 time to set
`blockedBy` relationships. Promoting to Ready here ensures the autonomous
scheduler only sees fully-configured issues. Without this two-step flow,
there is a 30s race window where the scheduler can dispatch an issue before
its dependencies are recorded.

## Phase 4.7: Verification (Mandatory)

After all project board operations are complete, verify that fields were
actually set. This catches silent failures from partial sync, binary
unavailability, or GraphQL errors that were swallowed.

Verification is based on successful exit codes from the binary commands above.
Since `project add` and `set-field` return non-zero on failure, catching their
errors in the preceding phases is sufficient.

For **standalone (non-epic) issues**, also verify Status from the JSON output
of `project add --status Ready`. This catches the case where the GraphQL Status
update silently no-oped (e.g., wrong project board, missing Status field).

```bash
# Standalone issues: re-run with --json and assert .status == "Ready"
STATUS_OUT=$(nightgauge project add <issue-number> --status Ready --json | jq -r .status)
if [ "$STATUS_OUT" != "Ready" ]; then
  echo "ERROR: Status not set to Ready (got: $STATUS_OUT)"
  exit 1
fi

# Epic + sub-issue path: binary commands above returned 0 — fields were set successfully.
# If any command failed, it would have printed an error and this step would not be reached.
echo "Project board fields verified via successful binary command exit codes."
echo "To manually inspect: nightgauge issue view <NUMBER> --json"
```

**If any field is empty**, retry with explicit set commands:

```bash
nightgauge project set-field <number> --priority P1 --size M --status Backlog
```

**CRITICAL**: Do NOT report success if any field is empty. Empty fields make
issues invisible in the extension's filtered tree views.

## Phase 4.8: Cross-Repo Project Membership Audit (Mandatory for Epics)

**Gate**: Runs UNCONDITIONALLY for any epic that produced sub-issues. This is
the safety net for the #3232 footgun. If the routing manifest, Phase 3, or
Phase 4 ever drift such that a sub-issue lands in the wrong project for its
repo, this phase catches it before the skill reports success.

For each entry in the Phase 2.4 routing manifest:

```bash
MANIFEST=".nightgauge/pipeline/issue-create-routing-<epic-number>.json"
EXIT_CODE=0
jq -c '.sub_issues[]' "$MANIFEST" | while read -r row; do
  NUM=$(echo "$row" | jq -r .number)
  EXPECTED_REPO=$(echo "$row" | jq -r .target_repo)
  EXPECTED_PROJECT=$(echo "$row" | jq -r .target_project)

  # Query the issue's actual project memberships
  ACTUAL=$(gh api graphql -f query="query {
    repository(owner: \"nightgauge\", name: \"$EXPECTED_REPO\") {
      issue(number: $NUM) {
        projectItems(first: 10) {
          nodes { project { number } }
        }
      }
    }
  }" -q '.data.repository.issue.projectItems.nodes[].project.number')

  if ! echo "$ACTUAL" | grep -qx "$EXPECTED_PROJECT"; then
    echo "AUDIT FAIL: #$NUM in nightgauge/$EXPECTED_REPO is not a member of project $EXPECTED_PROJECT (actual: ${ACTUAL:-<none>})"
    EXIT_CODE=1
  fi
done

if [ "$EXIT_CODE" -ne 0 ]; then
  echo ""
  echo "Routing audit failed. The skill will NOT report success."
  echo "Resolve by either:"
  echo "  1. Running 'nightgauge project add <num> --repo <repo> --project <proj>' on each failed issue"
  echo "  2. Re-running with --no-route to relax routing (after manual verification)"
  exit 1
fi
echo "Routing audit passed: all sub-issues are in the correct project per Phase 2.4 manifest."
rm -f "$MANIFEST"  # safe to delete the manifest only after audit passes
```

**Why this phase exists**: Pre-#3232, eighteen sub-issues for two unrelated
epics landed in the wrong project despite the workspace yaml clearly mapping
the routing. Audit-after-the-fact is the last line of defense — without it,
silent misplacement is invisible until a human notices weeks later.

## Phase 4.5: Knowledge Scaffolding (--with-knowledge)

**Gate**: Only runs when the `--with-knowledge` flag is passed by the user.

When `--with-knowledge` is requested, scaffold a knowledge directory for the
newly created issue immediately after project board sync. This allows users to
begin writing knowledge (PRD, decisions) before running `/issue-pickup`.

```bash
# Determine if this is an epic (check labels from Phase 2)
IS_EPIC=false
if echo "${LABELS:-}" | grep -q "type:epic"; then
  IS_EPIC=true
fi

# Write issue body to temp file (avoids env var size limits)
ISSUE_BODY_FILE=$(mktemp)
echo "${ISSUE_BODY:-}" > "$ISSUE_BODY_FILE"

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
BODY_CONTENT=$(cat "$ISSUE_BODY_FILE" 2>/dev/null || echo "")
SCAFFOLD_RESULT=$(ISSUE_NUMBER="$ISSUE_NUMBER" TITLE="$ISSUE_TITLE" IS_EPIC="$IS_EPIC" \
  BODY_CONTENT="$BODY_CONTENT" \
  node --input-type=module 2>/dev/null <<'NODEEOF' || echo '{"success":false,"error":"SDK scaffold failed"}')
import { KnowledgeService } from './packages/nightgauge-sdk/dist/index.js';
const svc = new KnowledgeService(process.cwd());
const r = await svc.scaffoldForIssue(
  parseInt(process.env.ISSUE_NUMBER || '0'),
  process.env.TITLE || '',
  process.env.BODY_CONTENT || '',
  process.env.IS_EPIC === 'true'
);
process.stdout.write(JSON.stringify(r));
NODEEOF
rm -f "$ISSUE_BODY_FILE"

SCAFFOLD_SUCCESS=$(echo "$SCAFFOLD_RESULT" | jq -r '.success // false')
KNOWLEDGE_PATH=$(echo "$SCAFFOLD_RESULT" | jq -r '.knowledge_path // empty')
KNOWLEDGE_SKIPPED=$(echo "$SCAFFOLD_RESULT" | jq -r '.skipped // false')

if [ "$SCAFFOLD_SUCCESS" = "true" ] && [ -n "$KNOWLEDGE_PATH" ] && [ "$KNOWLEDGE_SKIPPED" != "true" ]; then
  echo "Knowledge directory scaffolded: $KNOWLEDGE_PATH"
  echo "  Files created: $(echo "$SCAFFOLD_RESULT" | jq -r '.files_created | join(", ")')"
elif [ "$KNOWLEDGE_SKIPPED" = "true" ]; then
  SKIP_REASON=$(echo "$SCAFFOLD_RESULT" | jq -r '.skip_reason // "unknown"')
  echo "Knowledge scaffolding skipped: $SKIP_REASON"
  echo "  To enable: set knowledge.enabled: true in .nightgauge/config.yaml"
else
  echo "WARNING: Knowledge scaffolding failed: $(echo "$SCAFFOLD_RESULT" | jq -r '.error // "unknown error"')"
fi
```

**For epics with sub-issues**: When `IS_EPIC=true`, append a knowledge
directory reference to each sub-issue's body. Since sub-issues are already
created at this point, use `nightgauge issue edit --append-body` to update
them:

```bash
for SUB_NUMBER in <sub-issue-numbers>; do
  nightgauge issue edit "$SUB_NUMBER" \
    --append-body "\n\n> Knowledge directory: \`.nightgauge/knowledge/epics/${EPIC_NUMBER}-${SLUG}/\`\n> See [PRD.md](.nightgauge/knowledge/epics/${EPIC_NUMBER}-${SLUG}/PRD.md) for context."
done
```

This helps contributors immediately locate the planning docs when picking up a
sub-issue without needing to search.

**Note**: The `--force` behavior in the SDK `scaffoldForIssue()` bypasses the
`knowledge.auto_scaffold` config check (since the user explicitly requested
scaffolding via `--with-knowledge`), but still respects `knowledge.enabled`. If
`knowledge.enabled` is false in the project config, inform the user and suggest
enabling it.

## Phase 4.9: Write Creation Manifest

**Gate**: Runs after every successful issue (or epic + sub-issues) creation,
once Phase 4.8's cross-repo audit has passed (or has been skipped because
the workspace is single-repo). The manifest is the strict-mode contract
read by `/nightgauge:issue-audit --manifest <path>` in Phase 6.

```bash
TS=$(date -u +%Y%m%dT%H%M%SZ)
MANIFEST_PATH=".nightgauge/pipeline/issue-create-manifest-${TS}.json"
mkdir -p .nightgauge/pipeline

# ENTRIES_JSON is built progressively as each issue is created (Phase 3) and
# field-set (Phase 4). One entry per issue, conforming to
# CreationManifestEntrySchema. For epics, include the epic plus every
# sub-issue with parent_epic / blocked_by / sub_issues populated.
jq -n \
  --argjson project "$PROJECT_NUMBER_OR_NULL" \
  --argjson entries "$ENTRIES_JSON" \
  --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{
    schema_version: "1.0",
    created_at: $created_at,
    created_by_skill: "nightgauge-issue-create",
    project_number: $project,
    entries: $entries
  }' > "$MANIFEST_PATH"

# Validate the manifest can be parsed back as JSON
jq . "$MANIFEST_PATH" > /dev/null || {
  echo "ERROR: manifest write produced invalid JSON: $MANIFEST_PATH" >&2
  exit 1
}
echo "Manifest written: $MANIFEST_PATH"
```

The manifest schema (`CreationManifestSchema`) is defined in
`packages/nightgauge-sdk/src/context/schemas/creation-manifest.ts` and
documented in [docs/ISSUE_AUDIT.md](../../../docs/ISSUE_AUDIT.md). Each entry
records: `repo`, `number`, `type`, `priority`, `size`, `status`,
`parent_epic` (sub-issues only), `sub_issues` (epic only), `blocked_by`,
`body_sections`, `component_labels`, `knowledge_path`, `spike_artifact`
(spike issues only).

The manifest is preserved alongside the audit report; it is cleaned up by
`pr-merge` along with other pipeline transients.

## Phase 5: Return Structured Result

Return:

- Issue number and URL
- Final metadata (type, priority, size, status, milestone)
- Parent link status when applicable
- Knowledge directory path (when `--with-knowledge` was used)
- Manifest path (Phase 4.9 output)
- Suggested next command: `/nightgauge-issue-pickup <issue-number>`

## Phase 6: Terminal Audit Pass (Mandatory)

**Gate**: Runs UNCONDITIONALLY after Phase 5 unless `--no-audit` is passed.
Terminal audit is the single source of truth for "did this creation flow
leave the issues in a state the pipeline can pick up?"

```bash
if [ "${NO_AUDIT:-false}" = "true" ]; then
  echo "Terminal audit skipped (--no-audit)."
else
  echo "=== Terminal audit: /nightgauge:issue-audit --manifest $MANIFEST_PATH ==="
  # Invoke the audit skill via the slash-command surface. The audit's exit
  # code propagates: 0 READY, 1 NEEDS FIXES (CRITICAL findings remain),
  # 2 skill-level failure.
  AUDIT_EXIT=0
  /nightgauge:issue-audit --manifest "$MANIFEST_PATH" || AUDIT_EXIT=$?

  if [ "$AUDIT_EXIT" -ne 0 ]; then
    echo ""
    echo "ERROR: terminal audit reported NEEDS FIXES (exit=$AUDIT_EXIT)."
    echo "Review the report at: .nightgauge/pipeline/issue-audit-*.md"
    echo "Run the audit with --fix to attempt auto-repair, then re-run this skill."
    exit "$AUDIT_EXIT"
  fi
  echo "Terminal audit: READY"
fi
```

The `--no-audit` opt-out exists for callers that already chained audit
themselves (e.g. autonomous orchestrator runs that batch many creations
and run a single look-back audit at the end). It is NOT for routine human
invocations — manual creation flows MUST run the terminal audit.
