# Release Notes Drafting Tests

Behavioral fixtures for the `nightgauge-release-notes` skill. These
specifications encode the issue #4075 acceptance criteria: a closed epic with
sub-issues yields both fastlane release-notes files, the Android file is ≤500
chars, and both files are modified so the store-deploy freshness gate passes.
Every change to the skill MUST keep these cases passing.

## Setup Assumptions

- The target repo is a Flutter/fastlane store repo modeled on
  `Acme-Community/acme-tracker`:
  - `pubspec.yaml` has `version: 2.5.3+666`.
  - `fastlane/metadata/en-US/release_notes.txt` exists.
  - `fastlane/metadata/android/en-US/changelogs/default.txt` exists.
  - `scripts/deploy.sh` has a `check_release_notes()` gate that requires BOTH
    files modified since the last "Bump build number" commit and the Android
    file ≤500 chars.
- The skill is invoked non-interactively (headless): no `AskUserQuestion`.
- `nightgauge forge auth` is configured; the binary is on `PATH`.

---

## TC-1: Closed Epic — Both Fastlane Files Drafted, Android ≤500, Gate Passes

**Scenario**: A fully closed epic with four user-facing sub-issues. The AC
happy path.

**Input**:

- `/nightgauge:release-notes 142 --repo Acme-Community/acme-tracker`
- Epic `#142` is fully closed.
- Sub-issues:
  1. `#143` "Read-only Field Journal entries" — entries open read-only; tap Edit
     to change.
  2. `#144` "Full-screen editor for long entries" — editor grows; full-screen
     mode for poems.
  3. `#145` "Folders for journal entries" — group entries into folders alongside
     tags.
  4. `#146` "Inline notes on highlighted text" — highlight a word/line and pin a
     note.

**Expected behavior**:

- Phase 2 calls `nightgauge epic check-completion 142 --json`, asserts
  `.complete == true`, and enumerates `#143–#146`.
- For each sub-issue, `nightgauge forge issue view <n> --repo
Acme-Community/acme-tracker --json title,body` fetches the body
  (`SubIssueRef` carries no body).
- Phase 3 resolves the header `What's new in v2.5.3:` from `pubspec.yaml`
  (`2.5.3+666` → `2.5.3`).
- Phase 4 drafts customer-facing benefit bullets (NOT raw issue text), grouped
  by feature.
- Phase 5 writes:
  - `fastlane/metadata/en-US/release_notes.txt` (iOS+macOS, ≤4000 chars).
  - `fastlane/metadata/android/en-US/changelogs/default.txt` (Android).
- Phase 5 byte-counts the Android file; it is **≤500 chars**.
- Phase 6: `git diff --name-only` shows BOTH files modified — the freshness gate
  is satisfied. No commit, no push, no deploy dispatch.

**Failure modes the test must catch**:

- Android file > 500 chars (the gate hard-fails the deploy). MUST NOT happen —
  the skill re-condenses until ≤500.
- Only one of the two files written (gate fails for the missing one).
- Raw issue body / internal detail (file paths, ticket numbers) pasted into
  store copy.
- A separate macOS metadata file created (iOS/macOS share one file).
- The skill commits/pushes/deploys (it must stop at a reviewable draft).

---

## TC-2: Epic Not Fully Closed — Refuse to Draft

**Scenario**: The epic still has open sub-issues.

**Input**:

- `/nightgauge:release-notes 142`
- `epic check-completion 142 --json` returns `.complete == false` (e.g.
  `open: 1, closed: 3, total: 4`).

**Expected behavior**:

- Phase 2 stops with a clear error naming the open/closed/total counts. No
  fastlane file is written. Non-zero exit.

**Failure modes the test must catch**:

- Partial release notes drafted from an incomplete epic. MUST NOT happen.

---

## TC-3: Non-Fastlane Repo — Clear No-Op

**Scenario**: The target repo has no fastlane store metadata.

**Input**:

- `/nightgauge:release-notes 142 --repo nightgauge/nightgauge`
- No `fastlane/metadata/` directory exists.

**Expected behavior**:

- Phase 1 detects the missing layout and reports a clear NO-OP (this skill
  targets Flutter/fastlane store repos), exiting non-zero. No files are
  invented in the non-fastlane repo.

**Failure modes the test must catch**:

- Files written into `fastlane/...` paths that do not belong to the repo.

---

## TC-4: Android Draft Over 500 — Re-Condense Before Finishing

**Scenario**: The first Android draft synthesized from a feature-heavy epic is
540 chars.

**Input**:

- Same closed epic as TC-1, but the initial Android condensation overflows 500.

**Expected behavior**:

- Phase 5 `wc -c` reports > 500; the skill re-condenses (drops the
  lowest-value bullet / tightens wording) and rewrites until the Android file is
  ≤500, then re-checks. The skill does not finish above 500.

**Failure modes the test must catch**:

- Finishing with the Android file > 500 chars.
- Truncating the iOS draft mid-sentence to make the Android draft (it must be
  its own complete-sentence condensation).

---

## TC-5: Version Override

**Scenario**: The header must use an explicit version, not the pubspec value.

**Input**:

- `/nightgauge:release-notes 142 --version 2.6.0`

**Expected behavior**:

- Phase 3 uses `What's new in v2.6.0:` for both drafts, ignoring the
  `pubspec.yaml` `version:` field.
