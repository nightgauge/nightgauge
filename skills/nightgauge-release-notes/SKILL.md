---
name: nightgauge-release-notes
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

| Tool        | Command                                                   |
| ----------- | --------------------------------------------------------- |
| Claude Code | `/nightgauge:release-notes <epic> [options]`              |
| Copilot     | Invoke via Agent Skills extension                         |
| Cursor      | Run via Agent Skills or direct SKILL.md                   |
| Standalone  | `claude --skill skills/nightgauge-release-notes/SKILL.md` |

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

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

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
[ -z "$REPO" ] && REPO=$(nightgauge forge repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null)
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
COMPLETE=$(echo "$EPIC_STATUS" | jq -r '.complete // false')
if [ "$COMPLETE" != "true" ]; then
  echo "ERROR: epic #$EPIC is not fully closed. Release notes are drafted only after an epic closes."
  echo "$EPIC_STATUS" | jq -r '"  open: \(.open // "?")  closed: \(.closed // "?")  total: \(.total // "?")"'
  exit 1
fi
# Enumerate the sub-issues from the epic BODY. check-completion's JSON only
# carries `openIssues` (the still-open subset), which is empty for a fully-closed
# epic — so it cannot be the source of the closed sub-issues we draft from. The
# epic body lists every sub-issue as `#N` (the established pattern, cf.
# nightgauge-retro). `forge issue view --json` exposes no sub-issue array
# either, so body-parsing is the correct route.
SUB_NUMBERS=$(nightgauge forge issue view "$EPIC" --repo "$REPO" --json --jq '.body' 2>/dev/null \
  | grep -oE '#[0-9]+' | grep -oE '[0-9]+' | sort -un)
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
  TITLE=$(echo "$ISSUE_JSON" | jq -r '.title')
  BODY=$(echo "$ISSUE_JSON" | jq -r '.body')
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

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

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
