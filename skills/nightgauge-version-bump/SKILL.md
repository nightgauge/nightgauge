---
name: nightgauge-version-bump
description: >
  Derive the next semantic version (major/minor/patch) and a Keep-a-Changelog
  entry from a closed epic's merged sub-issues, then write the pubspec.yaml
  version name and CHANGELOG.md so they stay in sync with the store release
  notes. Use after an epic fully closes, before a store deploy, to land the
  version bump via the normal PR flow — the build number stays store-anchored
  and nothing is auto-submitted.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Bash Read Write Glob Grep
---

# Nightgauge Version Bump

## Description

The app pipeline automates the **build number** (store-anchored, applied at
deploy by `deploy.sh next_build_number`) but never touches the **semantic
version name** (`2.x.y`) or the changelog. The result is drift: a whole feature
epic can ship under the prior version because nothing bumped it, and
`CHANGELOG.md` goes stale.

This skill is the missing version step. When an epic fully closes, it:

1. **Derives the bump** from the merged sub-issues' types using SemVer +
   Conventional Commits — `feat` → **minor**, `fix`/`chore`/`docs`/`refactor`/
   `test` → **patch**, a breaking-change marker (`!` or `BREAKING CHANGE:`) →
   **major** — and takes the **highest** bump across the whole release.
2. **Writes `pubspec.yaml`** `version:` **name only**, preserving the
   store-anchored `+build` suffix verbatim (`deploy.sh` still owns the build).
3. **Prepends a `CHANGELOG.md` entry** (Keep a Changelog format) grouped
   Added/Fixed/Changed, synthesized from the sub-issue titles.

It is the upstream half of the release-prep pair: it sets `pubspec.yaml`
`version:`, and `nightgauge-release-notes` reads that same field for its
"What's new in vX.Y.Z" header — so `pubspec.yaml`, `CHANGELOG.md`, and the
fastlane store notes all derive from **one source** and can never drift again.

The output is a **reviewable change in the working tree**, not an auto-deploy:
the bump lands via the normal PR flow (so it reaches `main` for the next
deploy). The skill never commits, pushes, or submits to stores.

**Use Cases:**

- Bumping the app version + changelog after an epic closes, before a store
  deploy.
- Turning a batch of merged sub-issues into a single SemVer bump and a coherent
  changelog entry.

**When to Use:**

- After an epic fully closes (manually via `/nightgauge:version-bump
<epic>`, or driven off the `epic.completed` signal — see Trigger below),
  **before** `nightgauge-release-notes` and the store deploy.

## Invocation

| Tool        | Command                                                  |
| ----------- | -------------------------------------------------------- |
| Claude Code | `/nightgauge:version-bump <epic> [options]`              |
| Copilot     | Invoke via Agent Skills extension                        |
| Cursor      | Run via Agent Skills or direct SKILL.md                  |
| Standalone  | `claude --skill skills/nightgauge-version-bump/SKILL.md` |

## Arguments

| Argument                               | Description                                                                                                                                                                  | Default                 |
| -------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| `<epic>`                               | Epic issue number whose closed sub-issues drive the bump                                                                                                                     | required                |
| `--repo <owner/repo>`                  | Target repo (where `pubspec.yaml`/`CHANGELOG.md` live and sub-issues are read)                                                                                               | current repo            |
| `--bump major\|minor\|patch`           | Human override of the computed bump (the AC's manual override)                                                                                                               | derived from sub-issues |
| `--policy minor-on-feat\|always-patch` | Bump policy. `minor-on-feat`: feat→minor. `always-patch`: suppress the feat→minor escalation (a breaking `major` still escalates); take the minor by hand via `--bump minor` | `minor-on-feat`         |
| `--dry-run`                            | Print the computed bump, version transition, and changelog entry; write nothing                                                                                              | `false`                 |

### Examples

```bash
# Bump from closed epic #142 in the current repo (auto-derived from sub-issues)
/nightgauge:version-bump 142

# Target a specific repo
/nightgauge:version-bump 142 --repo Acme-Community/acme-tracker

# Force a minor bump regardless of the derived type (manual override)
/nightgauge:version-bump 142 --bump minor

# Solo-maintainer policy: never auto-minor; everything is patch unless overridden
/nightgauge:version-bump 142 --policy always-patch

# Preview without writing files
/nightgauge:version-bump 142 --dry-run
```

### Trigger

The natural trigger is epic closure — the same `epic.completed` event
(`OnEpicComplete` in `internal/ipc/server.go`, payload `repo` + `epicNumber`)
that drives `nightgauge-release-notes`. This skill is invocation-only: run
it manually, or wire a consumer of `epic.completed` to invoke
`/nightgauge:version-bump <epicNumber> --repo <repo>` **before**
`/nightgauge:release-notes`. It performs no auto-deploy; a human reviews
the diff and the bump rides the normal PR flow to `main`.

---

## Prerequisites

- `nightgauge` binary installed and `nightgauge forge auth` configured
- `jq` installed (JSON processing)
- Target repo is a Flutter app repo with a SemVer `version:` in `pubspec.yaml`

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with a clear error.

---

## Gotchas

- **Preserve the `+build` suffix verbatim.** `pubspec.yaml` `version:` is
  `name+build` (e.g. `2.5.3+666`). This skill owns ONLY the `2.x.y` name;
  `deploy.sh next_build_number` owns `+build`. Re-attach the exact original
  `+build` bytes — never drop, zero, or recompute it, or you will fight the
  store-anchored build counter (#4086).
- **Idempotent via the CHANGELOG epic marker, NOT the pubspec value.** The bump
  is relative to the current `version:`, so re-running would double-bump
  (2.5.3 → 2.6.0 → 2.7.0). The skill anchors idempotency on a
  `<!-- nightgauge:version-bump epic #N -->` marker in `CHANGELOG.md`: if
  the marker for this epic already exists, it is a clean no-op. Never bump again
  for an epic already recorded.
- **Take the HIGHEST bump across the release, not the last sub-issue.** One
  `feat` among ten `fix`es is a minor bump. One breaking marker anywhere is a
  major. Rank major>minor>patch and keep the max.
- **Classify from BOTH the `type:` label and the Conventional-Commit prefix.**
  The repo has no `type:breaking` label — a breaking change is signaled by `!`
  after the type (`feat!:`) or `BREAKING CHANGE:` in the body. Check both; the
  label is the primary signal, the title prefix the fallback.
- **No pubspec → no-op.** A repo with no `pubspec.yaml` (or no SemVer `version:`
  name) is not an app-version repo; report a clear no-op and exit non-zero
  rather than inventing a version file.
- **This is a reviewable change for the normal PR flow.** Never `git commit`/
  `git push`, never open the store-deploy. The skill stops once `pubspec.yaml`
  and `CHANGELOG.md` are written and reports the diff for review.
- **Synthesize changelog bullets; don't paste raw issue bodies.** Strip the
  Conventional-Commit prefix and internal detail from each sub-issue title into
  one readable line with its `(#n)` reference.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase 0: Repo Identity Check

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

---

### Phase 1: Validate Environment and Resolve Target

Confirm the binary and forge auth, resolve the target repo, and confirm it is an
app-version repo (`pubspec.yaml` with a SemVer `version:`). The skill no-ops
cleanly on repos without one.

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

# Flags.
DRY_RUN=0
[[ "$*" == *"--dry-run"* ]] && DRY_RUN=1
POLICY="minor-on-feat"
[[ "$*" == *"--policy"* ]] && POLICY=$(echo "$*" | sed -n 's/.*--policy[= ]\([^ ]*\).*/\1/p')
BUMP_OVERRIDE=""
[[ "$*" == *"--bump"* ]] && BUMP_OVERRIDE=$(echo "$*" | sed -n 's/.*--bump[= ]\([^ ]*\).*/\1/p')

# Require a SemVer pubspec version name. No pubspec → not an app-version repo.
if [ ! -f pubspec.yaml ]; then
  echo "NO-OP: no pubspec.yaml in this repo. This skill bumps a Flutter app's SemVer version name; nothing to do here."
  exit 1
fi
RAW_VERSION=$(grep -E '^version:' pubspec.yaml | head -1 | sed -E 's/^version:[[:space:]]*//' | tr -d '[:space:]')
BASE_NAME=$(printf '%s' "$RAW_VERSION" | sed -E 's/^([0-9]+\.[0-9]+\.[0-9]+).*/\1/')
if ! printf '%s' "$BASE_NAME" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "NO-OP: pubspec.yaml has no SemVer 'version: X.Y.Z' name (found '${RAW_VERSION:-<none>}'). Nothing to bump."
  exit 1
fi
# Capture the store-anchored +build suffix verbatim (may be empty).
BUILD_SUFFIX=""
[[ "$RAW_VERSION" == *"+"* ]] && BUILD_SUFFIX="+${RAW_VERSION#*+}"
echo "Current version name: ${BASE_NAME}  (build suffix preserved: '${BUILD_SUFFIX:-<none>}')"
```

---

### Phase 2: Confirm the Epic Is Closed and Enumerate Sub-Issues

Use the Go binary to confirm the epic is fully closed and list its sub-issues —
the same enumeration `nightgauge-release-notes` uses, so both skills draw
from the identical sub-issue set.

```bash
EPIC="<epic-number-from-args>"
# Pass --repo: check-completion defaults to nightgauge/nightgauge, which is
# the wrong repo for a store epic (e.g. Acme-Community/acme-tracker).
EPIC_STATUS=$(nightgauge epic check-completion "$EPIC" --repo "$REPO" --json 2>/dev/null || echo '{"complete":false}')
COMPLETE=$(echo "$EPIC_STATUS" | jq -r '.complete // false')
if [ "$COMPLETE" != "true" ]; then
  echo "ERROR: epic #$EPIC is not fully closed. The version bump is derived only after an epic closes."
  echo "$EPIC_STATUS" | jq -r '"  open: \(.open // "?")  closed: \(.closed // "?")  total: \(.total // "?")"'
  exit 1
fi
# Enumerate sub-issues from the epic BODY (#N links) — the established pattern
# (check-completion's JSON carries only the still-open subset, empty once closed).
SUB_NUMBERS=$(nightgauge forge issue view "$EPIC" --repo "$REPO" --json --jq '.body' 2>/dev/null \
  | grep -oE '#[0-9]+' | grep -oE '[0-9]+' | sort -un)
if [ -z "$SUB_NUMBERS" ]; then
  echo "ERROR: no sub-issues found referenced in epic #$EPIC's body (#N links). Cannot derive a bump from an empty set."
  exit 1
fi
```

> Identity is resolved per-repo by the Go binary automatically (#4068): every
> `nightgauge forge …` call authenticates as the `github_user` configured
> for the target repo, with the ambient token stripped — so a store epic in
> another org acts as the right identity without manual account switching.

---

### Phase 3: Idempotency Guard

The bump is relative to the current `version:`, so a second run would
double-bump. Anchor idempotency on a per-epic marker in `CHANGELOG.md`: if this
epic's entry already exists, the bump already landed — clean no-op.

```bash
MARKER="nightgauge:version-bump epic #${EPIC}"
if [ -f CHANGELOG.md ] && grep -qF "$MARKER" CHANGELOG.md; then
  echo "NO-OP: CHANGELOG.md already has the version-bump entry for epic #${EPIC}. Idempotent re-run — nothing to do."
  exit 0
fi
```

---

### Phase 4: Classify Each Sub-Issue and Derive the Bump

For each sub-issue, read its `title`, `body`, and `labels`, then map it to a
bump level. Track the **highest** level across the whole release and collect the
changelog bullets per group as you go.

Classification (highest wins): **major** if a breaking marker is present
(`type:breaking` label — if it ever exists — OR a Conventional-Commit `!` after
the type like `feat!:` OR `BREAKING CHANGE:` in the body); **minor** for a
feature (`type:feature` label OR `feat(...)`/`feat:` title prefix); **patch**
for everything else (`fix`/`chore`/`docs`/`refactor`/`test`/`perf`, or a
`type:bug` label).

`classify()` returns a finer kind than the bump level so the changelog grouping
is correct by default: `feat` → **Added**, `fix` → **Fixed**, everything else
(`chore`/`docs`/`refactor`/`test`/`perf`) → **Changed** (a docs change is not a
"Fixed"). The bump level then follows: breaking → major, feat → minor, fix and
all other → patch.

```bash
RANK=0          # 0=none 1=patch 2=minor 3=major
ADDED=""; FIXED=""; CHANGED=""

classify() {  # echoes one of: major | minor | fix | other
  local title="$1" body="$2" labels="$3"
  # Breaking (→ major): label, "type!:" prefix, or BREAKING CHANGE in the body.
  if echo "$labels" | grep -qiw 'type:breaking' \
     || printf '%s' "$title" | grep -qE '^[a-z]+(\([^)]*\))?!:' \
     || printf '%s' "$body" | grep -qE 'BREAKING[ -]CHANGE'; then
    echo major; return
  fi
  # Feature (→ minor, Added).
  if echo "$labels" | grep -qiw 'type:feature' \
     || printf '%s' "$title" | grep -qiE '^feat(\([^)]*\))?!?:'; then
    echo minor; return
  fi
  # Bug fix (→ patch, Fixed).
  if echo "$labels" | grep -qiw 'type:bug' \
     || printf '%s' "$title" | grep -qiE '^fix(\([^)]*\))?!?:'; then
    echo fix; return
  fi
  echo other   # chore/docs/refactor/test/perf → patch, grouped under Changed
}

for n in $SUB_NUMBERS; do
  ISSUE_JSON=$(nightgauge forge issue view "$n" --repo "$REPO" --json 2>/dev/null)
  TITLE=$(echo "$ISSUE_JSON" | jq -r '.title // ""')
  BODY=$(echo "$ISSUE_JSON"  | jq -r '.body // ""')
  LABELS=$(echo "$ISSUE_JSON" | jq -r '(.labels // []) | map(.name) | join(",")')
  KIND=$(classify "$TITLE" "$BODY" "$LABELS")

  # Strip a Conventional-Commit prefix from the title for a clean bullet.
  CLEAN=$(printf '%s' "$TITLE" | sed -E 's/^[a-z]+(\([^)]*\))?!?:[[:space:]]*//')
  case "$KIND" in
    major) RANK=$(( RANK<3 ? 3 : RANK )); CHANGED+="- ${CLEAN} (#${n})"$'\n' ;;
    minor) RANK=$(( RANK<2 ? 2 : RANK )); ADDED+="- ${CLEAN} (#${n})"$'\n' ;;
    fix)   RANK=$(( RANK<1 ? 1 : RANK )); FIXED+="- ${CLEAN} (#${n})"$'\n' ;;
    other) RANK=$(( RANK<1 ? 1 : RANK )); CHANGED+="- ${CLEAN} (#${n})"$'\n' ;;
  esac
  echo "  #${n}: ${KIND}  (${CLEAN})"
done

case "$RANK" in
  3) DERIVED=major ;;
  2) DERIVED=minor ;;
  *) DERIVED=patch ;;
esac
echo "Derived bump from sub-issues: ${DERIVED}"
```

> The grouping above is the default. When writing the entry in Phase 6 you may
> reword bullets for readability, but keep the section headings
> Keep-a-Changelog-canonical (Added/Changed/Fixed/Removed) — the goal is a
> readable entry, not a literal title dump.

---

### Phase 5: Apply Policy and Override, Compute the Target Version

`--bump` (if given) wins outright — the human override. Otherwise apply the
policy to the derived bump, then do the SemVer arithmetic on the **name only**
and re-attach the preserved `+build`.

```bash
BUMP="$DERIVED"
# Policy: always-patch suppresses the feat->minor escalation (solo maintainer
# takes the minor by hand). A breaking 'major' still escalates — silently
# shipping a breaking change as a patch would violate SemVer for consumers; use
# --bump to force it down if you really mean to.
if [ "$POLICY" = "always-patch" ] && [ "$BUMP" = "minor" ]; then
  echo "Policy always-patch: capping derived 'minor' to 'patch' (a breaking 'major' still escalates; use --bump minor to take it by hand)."
  BUMP="patch"
fi
# Manual override wins over everything.
if [ -n "$BUMP_OVERRIDE" ]; then
  case "$BUMP_OVERRIDE" in
    major|minor|patch) echo "Manual override: --bump ${BUMP_OVERRIDE} (was ${BUMP})"; BUMP="$BUMP_OVERRIDE" ;;
    *) echo "ERROR: --bump must be major|minor|patch (got '${BUMP_OVERRIDE}')."; exit 1 ;;
  esac
fi

IFS='.' read -r MAJ MIN PAT <<< "$BASE_NAME"
case "$BUMP" in
  major) MAJ=$((MAJ+1)); MIN=0; PAT=0 ;;
  minor) MIN=$((MIN+1)); PAT=0 ;;
  patch) PAT=$((PAT+1)) ;;
esac
TARGET_NAME="${MAJ}.${MIN}.${PAT}"
NEW_VERSION="${TARGET_NAME}${BUILD_SUFFIX}"
TODAY=$(date +%F)
echo "Version: ${BASE_NAME} -> ${TARGET_NAME}  (${BUMP});  pubspec line will read 'version: ${NEW_VERSION}'"
```

---

### Phase 6: Build the CHANGELOG Entry

Assemble a Keep a Changelog section for `TARGET_NAME` dated today, with only the
non-empty groups, and the idempotency marker. Synthesize readable bullets (see
the Phase 4 note) — this is the judgment step.

```bash
ENTRY="## [${TARGET_NAME}] - ${TODAY}"$'\n'
ENTRY+="<!-- ${MARKER} -->"$'\n\n'
[ -n "$ADDED" ]   && ENTRY+="### Added"$'\n\n'"${ADDED}"$'\n'
[ -n "$FIXED" ]   && ENTRY+="### Fixed"$'\n\n'"${FIXED}"$'\n'
[ -n "$CHANGED" ] && ENTRY+="### Changed"$'\n\n'"${CHANGED}"$'\n'
printf '%s' "$ENTRY"
```

> The bullets default from the Phase 4 grouping (feat→Added, fix→Fixed,
> everything else→Changed). Reword them for readability with the Write tool
> before prepending if needed, but keep the section headings
> Keep-a-Changelog-canonical (Added/Changed/Fixed/Removed).

---

### Phase 7: Write Files (or Dry-Run) and Report

On `--dry-run`, print the plan and stop — touch nothing. Otherwise write the new
`version:` line into `pubspec.yaml` (preserving everything else and the
`+build`), prepend the entry to `CHANGELOG.md` (creating it with a standard
header if absent), and report the diff for review. **Never** commit, push, or
deploy.

```bash
if [ "$DRY_RUN" = "1" ]; then
  echo "=== --dry-run: no files written ==="
  echo "pubspec.yaml version: ${BASE_NAME} -> ${TARGET_NAME} (${BUMP}); full line 'version: ${NEW_VERSION}'"
  echo "--- CHANGELOG.md entry (would prepend) ---"; printf '%s\n' "$ENTRY"
  exit 0
fi

# 1) pubspec.yaml — replace ONLY the first `version:` line; keep the +build.
awk -v repl="version: ${NEW_VERSION}" '
  /^version:/ && !done { print repl; done=1; next } { print }
' pubspec.yaml > pubspec.yaml.tmp && mv pubspec.yaml.tmp pubspec.yaml

# 2) CHANGELOG.md — create a header if missing, then prepend the new entry
#    directly under the header block (above the previous latest version).
if [ ! -f CHANGELOG.md ]; then
  {
    echo "# Changelog"
    echo ""
    echo "All notable changes to this project are documented in this file."
    echo ""
    echo "The format is based on [Keep a Changelog](https://keepachangelog.com/),"
    echo "and this project adheres to [Semantic Versioning](https://semver.org/)."
    echo ""
  } > CHANGELOG.md
fi
# Use the Write tool to splice $ENTRY in after the header preamble (the lines
# before the first "## [" version heading), so the newest version sits on top.
# (Read CHANGELOG.md, insert $ENTRY before the first "## [" — or append after
# the preamble if there is no prior version section — then write it back.)
```

> Splice with the Read+Write tools rather than a fragile in-place `sed`: read
> `CHANGELOG.md`, find the first `## [` version heading, insert `$ENTRY` (plus a
> trailing blank line) immediately before it; if there is none yet, append
> `$ENTRY` after the header preamble. This keeps newest-on-top ordering and
> never corrupts the file.

---

### Phase 8: Confirm and Report

```bash
echo "Modified for review:"
git diff --name-only -- pubspec.yaml CHANGELOG.md || true
echo ""
echo "VERSION BUMP READY for review (epic #$EPIC):"
echo "  pubspec.yaml  version: ${BASE_NAME} -> ${TARGET_NAME} (${BUMP}); build suffix '${BUILD_SUFFIX:-<none>}' preserved"
echo "  CHANGELOG.md  new [${TARGET_NAME}] entry prepended"
echo "Next: open a PR (normal flow) so the bump reaches main; then run"
echo "  /nightgauge:release-notes $EPIC --repo $REPO   (reads the bumped pubspec version)"
echo "  and finally the store deploy."
```

## Decision Rules

- Take the **highest** bump across all sub-issues (one feat ⇒ minor; one
  breaking marker ⇒ major).
- `--bump` overrides the derived/policy bump outright; `--policy always-patch`
  caps auto-bumps at patch.
- Preserve the `+build` suffix exactly — never recompute the store-anchored
  build number.
- If the epic is not fully closed, stop — do not bump from an incomplete epic.
- Idempotent: if the epic's marker is already in `CHANGELOG.md`, no-op.
- Never commit, push, or deploy — the bump lands via the normal PR flow.

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Failure Conditions

Fail with clear remediation when:

- The binary is unavailable or `nightgauge forge auth` is not configured
- The target repo has no `pubspec.yaml` SemVer `version:` name (no-op)
- The epic is not fully closed
- `--bump` is given a value other than `major|minor|patch`

## Completion Checklist

- [ ] Epic confirmed fully closed (`epic check-completion --json`, `.complete == true`)
- [ ] Sub-issue titles + bodies + labels fetched (`forge issue view --json`)
- [ ] Each sub-issue classified; **highest** bump taken across the release
- [ ] Policy + `--bump` override applied
- [ ] `pubspec.yaml` `version:` name updated, **`+build` suffix preserved verbatim**
- [ ] `CHANGELOG.md` entry prepended (Keep a Changelog) with the epic idempotency marker
- [ ] Idempotency verified (re-run with the marker present is a no-op)
- [ ] No commit / no push / no deploy — change left in the working tree for the PR flow
