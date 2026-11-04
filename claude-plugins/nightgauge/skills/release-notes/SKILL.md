---
name: release-notes
description: >
  Draft user-facing fastlane release notes (iOS/macOS + Android) from a closed
  epic's sub-issue titles and bodies, sized to satisfy the store-deploy
  freshness gate. Use after an epic fully closes to produce a human-reviewed
  "what's new" draft before a store deploy — never auto-submitted.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Bash Read Write Glob Grep
disable-model-invocation: true
---

# Nightgauge Release Notes

## Description

When an epic fully closes, this skill drafts customer-facing "what's new"
release notes into a Flutter/fastlane repo's store-metadata files, synthesized
from the epic's sub-issue titles and bodies. The output is a **draft for human
review** — it is written to the working tree so a person can edit it before the
store deploy, and is never pushed or submitted.

It produces two files in the target repo:

- `fastlane/metadata/en-US/release_notes.txt` — iOS **and** macOS (Universal
  Purchase share one file), ≤4000 chars.
- `fastlane/metadata/android/en-US/changelogs/default.txt` — Android, **≤500
  chars (hard limit)**.

The drafted content is shaped to pass the downstream store-deploy freshness
gate (e.g. acme-tracker `scripts/deploy.sh` `check_release_notes()`): BOTH
files must be modified since the last "Bump build number" commit, and the
Android file must be ≤500 chars. Writing fresh content to both satisfies the
modification check; the skill byte-counts and re-condenses the Android draft to
honor the 500-char limit before finishing.

**Use Cases:**

- Drafting release notes after an epic closes, before a store deploy.
- Turning a batch of merged sub-issues into a single coherent "what's new" entry.

**When to Use:**

- After an epic fully closes (manually via `/nightgauge:release-notes
<epic>`, or driven off the `epic.completed` signal — see Trigger below).
- Before running `./scripts/deploy.sh` for a Flutter store release.

## Invocation

| Tool        | Command                                      |
| ----------- | -------------------------------------------- |
| Claude Code | `/nightgauge:release-notes <epic> [options]` |
| Copilot     | Invoke via Agent Skills extension            |
| Cursor      | Run via Agent Skills or direct SKILL.md      |
| Headless    | `claude -p "/nightgauge:release-notes"`      |

## Arguments

| Argument              | Description                                                     | Default                  |
| --------------------- | --------------------------------------------------------------- | ------------------------ |
| `<epic>`              | Epic issue number whose sub-issues seed the notes               | required                 |
| `--repo <owner/repo>` | Target repo (where fastlane files live and sub-issues are read) | current repo             |
| `--version <X.Y.Z>`   | Override the "What's new in vX.Y.Z" header                      | read from `pubspec.yaml` |
| `--dry-run`           | Print both drafts to stdout; do not write the fastlane files    | `false`                  |

### Examples

```bash
# Draft notes for closed epic #142 in the current repo
/nightgauge:release-notes 142

# Target a specific repo and force the version header
/nightgauge:release-notes 142 --repo Acme-Community/acme-tracker --version 2.5.3

# Preview without writing files
/nightgauge:release-notes 142 --dry-run
```

### Trigger

The natural trigger is epic closure. The pipeline already surfaces this as the
`epic.completed` event (emitted from `OnEpicComplete` in
`internal/ipc/server.go`), whose payload carries `repo` + `epicNumber` — exactly
this skill's inputs. This skill is invocation-only: run it manually with the
epic number, or wire a consumer of `epic.completed` to invoke
`/nightgauge:release-notes <epicNumber> --repo <repo>`. It performs no
auto-deploy; a human reviews the draft and runs the store deploy.

---

## Prerequisites

- `nightgauge` binary installed and `nightgauge forge auth` configured
- `jq` installed (JSON processing)
- Target repo is a Flutter/fastlane store repo (has `fastlane/metadata/`)

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with a clear error.

---

## Gotchas

- **Android `changelogs/default.txt` HARD-fails >500 chars.** The store-deploy
  gate aborts the whole deploy above 500 (it is a hard limit, not a warning).
  Always `wc -c` the Android draft and re-condense until it is ≤500 before
  finishing. Build the Android draft as the tighter of the two — do not just
  truncate the iOS draft mid-sentence. Note the limit is 500 **bytes** (`wc -c`,
  matching the gate): non-ASCII punctuation (em-dash `—`, curly quotes) costs 2–3
  bytes each, so a draft at ~480 visible characters can exceed 500 bytes — prefer
  plain ASCII in the Android copy.
- **The freshness gate keys on file MODIFICATION since the last "Bump build
  number" commit.** Identical bytes to the prior release still read as stale —
  always write the current version's content so the bytes differ.
- **iOS and macOS SHARE one file (Universal Purchase).** Write only
  `en-US/release_notes.txt`; never create a separate macOS metadata file.
- **Never paste raw issue text.** Sub-issue bodies carry internal/technical
  detail (file paths, ticket numbers, acceptance criteria). Synthesize
  customer-facing "what's new" benefits — drop anything a user would not care
  about.
- **This is a draft for HUMAN REVIEW.** Never `git commit`/`git push`, never
  trigger the store-deploy workflow. The skill stops once both files are written
  and the Android length check passes.
- **No fastlane → no-op.** If the target repo has no `fastlane/metadata/`
  directory, this skill cannot apply; report a clear no-op and exit non-zero
  rather than inventing files in a non-fastlane repo.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase 0: Repo Identity Check

### Repo Identity Assertion (HARD GATE — non-recoverable)

If the `NIGHTGAUGE_TARGET_REPO` environment variable is set, the orchestrator
has pinned the exact repo this stage must run in. Verify the current repository
matches **before doing any work**:

```bash
if [ -n "$NIGHTGAUGE_TARGET_REPO" ]; then
  # Derive this checkout's identity from its own git remote. The question
  # is "which repository is this working directory?", and the remote answers
  # it exactly — locally, with no API call. This used to shell out to the
  # GitHub CLI for the same string, spending a request per stage on every
  # repo in the workspace, invisible to `nightgauge api-usage`.
  ACTUAL_REPO=$(git remote get-url origin 2>/dev/null \
    | sed -E 's#^git@[^:]+:#-#; s#^https?://[^/]+/#-#; s#^-##; s#\.git$##')
  if [ -n "$ACTUAL_REPO" ] && [ "$ACTUAL_REPO" != "$NIGHTGAUGE_TARGET_REPO" ]; then
    echo "[repo-mismatch] FATAL: repository identity assertion failed"
    echo "  Expected: $NIGHTGAUGE_TARGET_REPO"
    echo "  Actual:   $ACTUAL_REPO"
    echo "  CWD:      $(pwd)"
    echo ""
    echo "The orchestrator expected this stage to run in $NIGHTGAUGE_TARGET_REPO"
    echo "but the working directory belongs to $ACTUAL_REPO."
    exit 1
  fi
fi
```

**This is a terminal, non-recoverable stop condition. If the check prints
`[repo-mismatch]` and exits 1, you MUST immediately end the stage with a failure.**

Specifically, on a mismatch you must NOT:

- `cd` into a different directory, switch worktrees, or `gh repo set-default` to
  "make it match" — the orchestrator owns CWD; second-guessing it corrupts state.
- Continue to later phases, create or push a branch, or open a PR — every
  downstream artifact would land in the wrong repo.
- Ask the user a question (`AskUserQuestion`) — autonomous runs have no human at
  the prompt; the question is auto-dismissed and you would proceed on a guess.
- Write a context/assessment file that claims success.

The correct and ONLY action is to stop now and let the stage exit non-zero. This
is a configuration fault in repo routing (the orchestrator set the wrong
`NIGHTGAUGE_TARGET_REPO` for this worktree); it is fixed upstream in the
orchestrator, never worked around inside the stage. The `[repo-mismatch]` marker
is how the pipeline classifies and surfaces it.

This check is a no-op — and the stage proceeds normally — when
`NIGHTGAUGE_TARGET_REPO` is unset (e.g. manual single-stage CLI
invocations) or already matches the current repo.


---

### Phase 1: Validate Environment and Resolve Target

Confirm the binary and forge auth, resolve the target repo, and locate the
fastlane layout. The fastlane paths are detected generically so the skill no-ops
cleanly on repos that do not ship store metadata.

```bash
if ! command -v nightgauge &> /dev/null; then
  echo "ERROR: nightgauge binary not found. Install via the VSCode extension or build from source."
  exit 1
fi
if ! nightgauge forge auth status &>/dev/null; then
  echo "ERROR: nightgauge forge auth not configured. Run: nightgauge forge auth login"
  exit 1
fi
if ! command -v jq &> /dev/null; then
  echo "ERROR: jq not found. Install with: brew install jq"
  exit 1
fi

# Resolve --repo (defaults to the current repo).
REPO=""
if [[ "$*" == *"--repo"* ]]; then
  REPO=$(echo "$*" | sed -n 's/.*--repo[= ]\([^ ]*\).*/\1/p')
fi
[ -z "$REPO" ] && REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug 2>/dev/null)}"
echo "Target repo: ${REPO:-<current>}"

# --dry-run: print both drafts to stdout instead of writing the fastlane files.
DRY_RUN=0
[[ "$*" == *"--dry-run"* ]] && DRY_RUN=1

# Detect the fastlane layout generically. Both files must exist as a directory
# tree; not every repo uses fastlane — no-op clearly when absent.
IOS_NOTES="fastlane/metadata/en-US/release_notes.txt"
ANDROID_NOTES="fastlane/metadata/android/en-US/changelogs/default.txt"
if [ ! -d "fastlane/metadata/en-US" ] || [ ! -d "fastlane/metadata/android/en-US/changelogs" ]; then
  echo "NO-OP: no fastlane store-metadata layout found in this repo (expected fastlane/metadata/en-US and fastlane/metadata/android/en-US/changelogs)."
  echo "This skill targets Flutter/fastlane store repos. Nothing to draft here."
  exit 1
fi
```

---

### Phase 2: Confirm the Epic Is Closed and Enumerate Sub-Issues

Use the Go binary to confirm the epic is fully closed and list its sub-issues.
`epic check-completion` returns completion state plus the sub-issue set.

```bash
EPIC="<epic-number-from-args>"
# Pass --repo: check-completion defaults to nightgauge/nightgauge, which is
# the wrong repo for a store epic (e.g. Acme-Community/acme-tracker).
EPIC_STATUS=$(nightgauge epic check-completion "$EPIC" --repo "$REPO" --json 2>/dev/null || echo '{"complete":false}')
COMPLETE=$(printf '%s\n' "$EPIC_STATUS" | jq -r '.complete // false')
if [ "$COMPLETE" != "true" ]; then
  echo "ERROR: epic #$EPIC is not fully closed. Release notes are drafted only after an epic closes."
  printf '%s\n' "$EPIC_STATUS" | jq -r '"  open: \(.open // "?")  closed: \(.closed // "?")  total: \(.total // "?")"'
  exit 1
fi
# Enumerate the sub-issues from the epic BODY. check-completion's JSON only
# carries `openIssues` (the still-open subset), which is empty for a fully-closed
# epic — so it cannot be the source of the closed sub-issues we draft from. The
# epic body lists every sub-issue as `#N` (the established pattern, cf.
# nightgauge-retro). `forge issue view --json` exposes no sub-issue array
# either, so body-parsing is the correct route.
SUB_NUMBERS=$(nightgauge forge issue view "$EPIC" --repo "$REPO" --json 2>/dev/null \
  | jq -r '.body // empty' | grep -oE '#[0-9]+' | grep -oE '[0-9]+' | sort -un)
if [ -z "$SUB_NUMBERS" ]; then
  echo "ERROR: no sub-issues found referenced in epic #$EPIC's body (#N links). Cannot draft release notes from an empty set."
  exit 1
fi
```

Fetch each sub-issue's title + body separately — the body is where the
user-facing "what's new" material lives:

```bash
SUB_CONTENT=""
for n in $SUB_NUMBERS; do
  ISSUE_JSON=$(nightgauge forge issue view "$n" --repo "$REPO" --json 2>/dev/null)
  TITLE=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.title')
  BODY=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.body')
  SUB_CONTENT+=$'\n### '"$TITLE"$'\n'"$BODY"$'\n'
done
```

> Identity is resolved per-repo by the Go binary automatically (#4068): every
> `nightgauge forge …` call here authenticates as the `github_user`
> configured for the target repo (e.g. `acmebot` for Acme-Community),
> with the ambient token stripped — so a store epic in another org acts as the
> right identity without any manual account switching. Never switch the
> machine-global active account; it would break other open workspaces.

---

### Phase 3: Resolve the Version Header

Derive "What's new in vX.Y.Z" from the target repo's `pubspec.yaml` `version:`
field (strip the `+build` suffix). Allow `--version` to override.

```bash
VERSION=""
if [[ "$*" == *"--version"* ]]; then
  VERSION=$(echo "$*" | sed -n 's/.*--version[= ]\([^ ]*\).*/\1/p')
fi
if [ -z "$VERSION" ] && [ -f pubspec.yaml ]; then
  # e.g. "version: 2.5.3+666" -> "2.5.3"
  VERSION=$(grep -E '^version:' pubspec.yaml | head -1 | sed -E 's/^version:[[:space:]]*([0-9]+\.[0-9]+\.[0-9]+).*/\1/')
fi
[ -z "$VERSION" ] && { echo "ERROR: could not resolve version. Pass --version X.Y.Z."; exit 1; }
HEADER="What's new in v${VERSION}:"
echo "$HEADER"
```

---

### Phase 4: Draft the Two Versions

Synthesize the sub-issue titles/bodies into customer-facing "what's new" copy.
This is the judgment step — write for an app-store reader, not an engineer.

Quality bar:

- Lead with the benefit, not the implementation. "Read mode keeps a stray swipe
  from wiping your notes" — not "Added read-only WidgetState to JournalEntry".
- Group related sub-issues into one feature bullet where it reads better.
- Drop pure-internal sub-issues (refactors, CI, test-only) — they are not
  "what's new" for a user.
- Keep the existing voice of the repo's prior `release_notes.txt` if one exists
  (read it for tone before drafting).

Produce **two drafts**:

- **iOS/macOS draft** — fuller. The `$HEADER`, then grouped benefit bullets.
  Budget ≤4000 chars.
- **Android draft** — tighter. The `$HEADER`, then the highest-value bullets
  only, condensed. Budget **≤500 chars (hard)**.

Build the Android draft as its own condensation, not a truncation of the iOS
draft, so it reads as complete sentences.

---

### Phase 5: Write Files and Enforce the Android 500-Char Limit

Write the iOS/macOS draft to `release_notes.txt` and the Android draft to
`changelogs/default.txt`. Then **byte-count the Android file and re-condense
until it is ≤500** — this is the hard gate. On `--dry-run`, print both drafts
instead of writing.

```bash
# DRY-RUN: print both drafts and stop — do NOT touch the fastlane files.
if [ "$DRY_RUN" = "1" ]; then
  echo "=== --dry-run: drafts NOT written ==="
  echo "--- $IOS_NOTES (iOS/macOS) ---"; printf '%s\n' "$IOS_DRAFT"
  echo "--- $ANDROID_NOTES (Android, must be <=500 bytes) ---"; printf '%s\n' "$ANDROID_DRAFT"
  printf 'Android draft byte count: %s (limit 500)\n' "$(printf '%s' "$ANDROID_DRAFT" | wc -c | tr -d ' ')"
  exit 0
fi

# (Write the drafted content with the Write tool, not echo, to keep formatting.)
# After writing, enforce the Android hard limit:
ANDROID_LEN=$(wc -c < "$ANDROID_NOTES" | tr -d ' ')
echo "Android changelog: ${ANDROID_LEN} chars (limit 500)"
if [ "$ANDROID_LEN" -gt 500 ]; then
  echo "Android draft is ${ANDROID_LEN} chars — re-condense and rewrite $ANDROID_NOTES until <= 500, then re-check."
  # Loop: shorten the Android draft (drop the lowest-value bullet / tighten
  # wording) and re-write until ANDROID_LEN <= 500. Do NOT finish above 500.
fi

IOS_LEN=$(wc -c < "$IOS_NOTES" | tr -d ' ')
echo "iOS/macOS release notes: ${IOS_LEN} chars (limit 4000)"
[ "$IOS_LEN" -gt 4000 ] && { echo "iOS draft exceeds 4000 chars — condense $IOS_NOTES."; }
```

---

### Phase 6: Confirm the Freshness Gate Is Satisfied and Report

Both files are now modified in the working tree, so the store-deploy freshness
check (BOTH files changed since the last "Bump build number" commit) passes.
Verify and report — do not commit, push, or deploy.

```bash
echo "Modified for review:"
git diff --name-only -- "$IOS_NOTES" "$ANDROID_NOTES" || true
echo ""
echo "DRAFT READY for human review (epic #$EPIC, v$VERSION):"
echo "  - $IOS_NOTES (${IOS_LEN} chars, iOS+macOS)"
echo "  - $ANDROID_NOTES (${ANDROID_LEN} chars, Android, <=500)"
echo "Next: a human reviews/edits the drafts, then runs ./scripts/deploy.sh (no --skip-release-notes)."
```

## Decision Rules

- Prefer the repo's own prior release-notes voice over a generic store template.
- If a sub-issue is purely internal (refactor/CI/tests), omit it from user copy.
- If the epic is not fully closed, stop — do not draft partial release notes.
- Never finish with the Android file over 500 chars.

### Self-Assessment Epilogue

### Self-Assessment Epilogue

**PURPOSE**: Evaluate whether this skill's instructions matched reality during
this execution. This phase is **non-blocking** — skip entirely if any main phase
failed. A perfectly working skill produces **no output** from this phase.

> See [docs/SKILL_SELF_ASSESSMENT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SKILL_SELF_ASSESSMENT.md) for
> the full strategy, synthesis algorithm, and integration architecture.

#### When to Skip

- Any prior phase exited with an error before completing
- The skill was cancelled or timed out
- Running in `--dry-run` mode

#### Step 1: Evaluate Execution Friction

Review the execution that just completed. Answer these questions honestly:

1. **Command failures**: Did any command, script, or binary call in the skill
   instructions fail? (e.g., script not found, wrong flags, missing binary)
2. **Workarounds**: Did you have to deviate from the skill instructions to
   accomplish the goal? (e.g., used `gh` directly because a referenced script
   didn't exist, skipped a step that referenced a nonexistent file)
3. **Stale references**: Did any file path, function name, API endpoint, or tool
   referenced in the instructions not exist in the current codebase?
4. **Unclear instructions**: Were any instructions ambiguous enough that you had
   to guess at the intended behavior?
5. **Missing instructions**: Was there a significant step you had to figure out
   on your own that should have been documented in this skill?

**If ALL answers are "no" — write nothing and complete the skill normally.** The
goal is silence when everything works.

#### Step 2: Write Assessment Record

Only if friction was detected in Step 1. Write a single JSON file,
`assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json` in the clone's pipeline state
directory, through the binary (JSON on stdin; never a direct file write):

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
nightgauge layout write pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json" <<'JSON'
{ ... the record below ... }
JSON
```

The assessment record MUST follow this schema:

```json
{
  "schema_version": "1",
  "skill": "{STAGE_NAME}",
  "skill_file": "skills/nightgauge-{STAGE_NAME}/SKILL.md",
  "issue_number": 42,
  "timestamp": "2026-03-10T14:30:00Z",
  "friction": [
    {
      "type": "command_failure",
      "severity": "high",
      "description": "hooks/lib/add-to-project.sh not found — script was deleted",
      "skill_line_hint": "claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>",
      "actual_resolution": "Used gh api graphql to add issue to project board directly",
      "suggested_fix": "Replace with: nightgauge project add <issue-number>"
    }
  ]
}
```

**Friction types**: `command_failure`, `workaround`, `stale_reference`,
`unclear_instruction`, `missing_instruction`

**Severity levels**:

- `high` — instruction is **broken**. Required manual workaround to complete.
- `medium` — instruction is **misleading**. Agent adapted without user help.
- `low` — instruction is **suboptimal**. No functional impact.

**Rules**:

- **One record per execution** — multiple friction items go in the `friction`
  array, not separate files.
- **Be specific** — quote the exact instruction text that was wrong. Not "some
  commands didn't work" but "Step 5.2 calls `hooks/lib/add-to-project.sh` but
  this script was deleted in commit 65915701."
- **Suggest the fix** — every finding MUST include `suggested_fix` with the
  concrete SKILL.md change needed. Not "update the docs" but "replace
  `hooks/lib/add-to-project.sh <N>` with `nightgauge project add <N>`."
- **Don't invent friction** — only report issues you actually encountered during
  this execution. Do not speculate about potential problems.

#### Step 3: Validate and Complete

```bash
# Validate JSON if written
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
ASSESSMENT_FILE="$(nightgauge layout path pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json")"
if [ -f "$ASSESSMENT_FILE" ]; then
  python3 -m json.tool "$ASSESSMENT_FILE" > /dev/null 2>&1 || \
    echo "WARNING: Assessment record is not valid JSON" >&2
fi
```


---

## Failure Conditions

Fail with clear remediation when:

- The binary is unavailable or `nightgauge forge auth` is not configured
- The target repo has no fastlane store-metadata layout (no-op)
- The epic is not fully closed
- The version cannot be resolved and no `--version` is given
- The Android draft cannot be condensed to ≤500 chars

## Completion Checklist

- [ ] Epic confirmed fully closed (`epic check-completion --json`, `.complete == true`)
- [ ] Sub-issue titles + bodies fetched (`forge issue view --json`)
- [ ] Version header resolved (from `pubspec.yaml` or `--version`)
- [ ] iOS/macOS draft written to `fastlane/metadata/en-US/release_notes.txt` (≤4000 chars)
- [ ] Android draft written to `fastlane/metadata/android/en-US/changelogs/default.txt`
- [ ] **Android file byte-counted and ≤500 chars** (re-condensed if over)
- [ ] BOTH files modified vs HEAD (freshness gate satisfied)
- [ ] No commit / no push / no deploy dispatch — draft left for human review
