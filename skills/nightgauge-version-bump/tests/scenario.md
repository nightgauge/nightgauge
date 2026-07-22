# Version Bump Tests

Behavioral fixtures for the `nightgauge-version-bump` skill. These
specifications encode the issue #4086 acceptance criteria: a closed epic yields
the correct SemVer bump and a matching `CHANGELOG.md` entry, `pubspec.yaml`,
`CHANGELOG.md`, and the fastlane store notes derive from one source, the
store-anchored build number is untouched, and a human can override the computed
bump. Every change to the skill MUST keep these cases passing.

## Setup Assumptions

- The target repo is a Flutter app repo modeled on
  `Acme-Community/acme-tracker`:
  - `pubspec.yaml` has `version: 2.5.3+666` (name `2.5.3`, store-anchored build
    `+666`).
  - `CHANGELOG.md` may or may not exist; when it does it is Keep-a-Changelog
    formatted, newest version on top.
  - `scripts/deploy.sh` `next_build_number` owns the `+build` counter and must
    be unaffected by this skill.
- The skill is invoked non-interactively (headless): no `AskUserQuestion`.
- `nightgauge forge auth` is configured; the binary is on `PATH`.

---

## TC-1: Feature Epic — Minor Bump + Matching CHANGELOG (Happy Path)

**Scenario**: A fully closed epic whose sub-issues include at least one `feat`.
The primary AC.

**Input**:

- `/nightgauge:version-bump 142 --repo Acme-Community/acme-tracker`
- Epic `#142` is fully closed.
- Sub-issues:
  1. `#143` `type:feature` "Read-only Field Journal entries"
  2. `#144` `type:feature` "Full-screen editor for long entries"
  3. `#145` `type:bug` "Fix folder sort order"
  4. `#146` `type:chore` "Bump lints"

**Expected behavior**:

- Phase 2 asserts `epic check-completion 142 --json` `.complete == true` and
  enumerates `#143–#146`.
- Phase 4 classifies #143/#144 → minor (Added), #145 → fix/patch (Fixed), #146
  → other/patch (Changed); the **highest** bump is **minor**.
- Phase 5 computes `2.5.3 → 2.6.0`; the pubspec line becomes
  `version: 2.6.0+666` (**`+666` preserved verbatim**).
- Phase 6/7 prepend a `## [2.6.0] - <today>` entry with `### Added` (the two
  features), `### Fixed` (the bug fix), and `### Changed` (the chore — a chore is
  not a "Fixed"), carrying the
  `<!-- nightgauge:version-bump epic #142 -->` marker.
- Phase 8 reports `pubspec.yaml` + `CHANGELOG.md` modified. No commit / push /
  deploy.

**Failure modes the test must catch**:

- Bumping patch (or major) instead of minor when a `feat` is present.
- Dropping, zeroing, or recomputing the `+666` build suffix.
- Writing the CHANGELOG entry without the epic marker (breaks idempotency).
- Committing/pushing/deploying instead of leaving a reviewable diff.

---

## TC-2: Fix-Only Set — Patch Bump

**Scenario**: A closed epic whose sub-issues are all `fix`/`chore`/`docs`.

**Input**:

- `/nightgauge:version-bump 142`
- Sub-issues: `#143` `type:bug`, `#144` `type:bug`, `#145` `type:docs`.

**Expected behavior**:

- Phase 4 classifies every sub-issue → patch; highest is **patch**.
- Phase 5 computes `2.5.3 → 2.5.4`; pubspec line `version: 2.5.4+666`.
- CHANGELOG entry has `### Fixed`/`### Changed` only — no `### Added`.

**Failure modes the test must catch**:

- A minor/major bump from a fix-only release.

---

## TC-3: Breaking Marker — Major Bump

**Scenario**: One sub-issue carries a breaking-change marker.

**Input**:

- `/nightgauge:version-bump 142`
- Sub-issues include `#143` titled `feat!: drop legacy entry schema` (or a body
  with `BREAKING CHANGE:`), plus several `fix`es.

**Expected behavior**:

- Phase 4 classifies #143 → major (the `!`/`BREAKING CHANGE` marker wins); the
  highest across the release is **major**.
- Phase 5 computes `2.5.3 → 3.0.0`; pubspec line `version: 3.0.0+666`.

**Failure modes the test must catch**:

- Treating `feat!:` as a plain minor feature (must escalate to major).
- A `BREAKING CHANGE:` in a body being ignored.

---

## TC-4: Idempotent Re-Run — No-Op

**Scenario**: The bump for epic #142 already landed; the skill runs again.

**Input**:

- `/nightgauge:version-bump 142`
- `CHANGELOG.md` already contains
  `<!-- nightgauge:version-bump epic #142 -->`.

**Expected behavior**:

- Phase 3 detects the marker and exits 0 as a clean no-op. `pubspec.yaml` is
  **not** bumped again (no `2.6.0 → 2.7.0` double-bump).

**Failure modes the test must catch**:

- Double-bumping on re-run (the marker, not the pubspec value, anchors
  idempotency).

---

## TC-5: Manual Override Wins

**Scenario**: The maintainer forces a specific bump regardless of the derived
type.

**Input**:

- `/nightgauge:version-bump 142 --bump minor` on a fix-only epic (would
  derive patch).

**Expected behavior**:

- Phase 5 uses **minor** (override beats the derived patch): `2.5.3 → 2.6.0`.
- An invalid `--bump foo` exits non-zero with a clear error.

**Failure modes the test must catch**:

- The derived bump overriding an explicit `--bump`.

---

## TC-6: always-patch Policy Caps Auto-Bumps

**Scenario**: A solo maintainer wants patch-only auto-bumps until they choose a
minor by hand.

**Input**:

- `/nightgauge:version-bump 142 --policy always-patch` on a feat-bearing
  epic (would derive minor).

**Expected behavior**:

- Phase 5 caps the derived **minor** down to **patch**: `2.5.3 → 2.5.4`. A
  later `--bump minor` still forces the minor when wanted.

**Failure modes the test must catch**:

- `always-patch` letting a `feat` auto-bump the minor.

---

## TC-7: Non-App Repo — Clear No-Op

**Scenario**: The target repo has no `pubspec.yaml` (or no SemVer `version:`).

**Input**:

- `/nightgauge:version-bump 142 --repo nightgauge/nightgauge`

**Expected behavior**:

- Phase 1 detects no SemVer `pubspec.yaml` `version:` name and reports a clear
  NO-OP, exiting non-zero. No `pubspec.yaml`/`CHANGELOG.md` is invented.

**Failure modes the test must catch**:

- Inventing a `version:`/`pubspec.yaml` in a non-app repo.

---

## TC-8: Epic Not Fully Closed — Refuse to Bump

**Scenario**: The epic still has open sub-issues.

**Input**:

- `/nightgauge:version-bump 142`
- `epic check-completion 142 --json` returns `.complete == false`.

**Expected behavior**:

- Phase 2 stops with a clear error naming open/closed/total. No bump, no
  CHANGELOG write. Non-zero exit.

**Failure modes the test must catch**:

- Bumping from an incomplete epic.

---

## TC-9: One-Source Sync with release-notes

**Scenario**: version-bump and release-notes must agree on the version.

**Input**:

- `/nightgauge:version-bump 142` (bumps `2.5.3 → 2.6.0`), then
  `/nightgauge:release-notes 142`.

**Expected behavior**:

- After version-bump rewrites `pubspec.yaml` to `2.6.0+666`,
  `nightgauge-release-notes` Phase 3 reads `2.6.0` from the same field and
  headers both drafts `What's new in v2.6.0:` — pubspec, CHANGELOG, and fastlane
  notes all derive from the one bumped source.

**Failure modes the test must catch**:

- release-notes reading a stale (pre-bump) version because the two skills used
  different sources.
