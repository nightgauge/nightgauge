# VS Code Extension Release Checklist

Evidence from the 2026-08-20 release-readiness run. This is not a generic
template: every row is something that was actually executed or judged.

The 2026-08-20 run (the sections after _What is left_) recorded the last
published tag as **`v0.2.0-rc.23`** and set `v0.2.0-rc.24` as the candidate;
`v0.2.0-rc.24` has since been tagged and `main` has moved on.

Green CI on a PR is a prediction. The merge commit on `main` is the
observation. Do not tag until that post-merge observation is green.

## What is left before the first Marketplace version — 2026-08-29 pass

This section is the current answer to "what stands between `main` and the
first VS Code Marketplace release". It was produced by a release-readiness
pass on 2026-08-29 that re-measured every earlier row of this file, walked
the marketing and legal surfaces, and landed the items marked **done**. Every
open row names the evidence that will close it. Re-measure before trusting a
row; `main` moves.

### The one gate that matters

| #   | Gate                                                                                                                                                                                                                 | State                                                                                                                                                                                             |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| G1  | **Clean-machine install, step 5**: install the packaged `.vsix` into a profile with no Nightgauge state, follow the README verbatim, and drive one issue to a merged PR. See _The clean-machine install gate_ below. | **Done — #1137.** Walked on the release commit `e6e98c76` on 2026-09-02: packaged VSIX, bare container, fixture issue → merged PR, unattended. See _Findings from the release-commit walk_ below. |

The pipeline has proven itself end to end on a real repository — an
M-sized feature issue went pickup → plan → dev → validate → PR → merge
unattended on 2026-08-29 (six stages, $6.72, 83 minutes; the run every image
in `docs/images/marketing/` is built from). That run was driven by a
maintainer's install, which is exactly what G1 does not accept as evidence.

### Code readiness

| #   | Gate                                                                                                                               | State                                                                                                                                                                              |
| --- | ---------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| C1  | No open issue claims install, activation, or any of the six core stages fails for a single-repo user on their own keys             | **Done.** 92-issue v1 triage (2026-08-24): 0 block v1. Re-run the triage over bugs filed since before tagging.                                                                     |
| C2  | Publish-blocker list empty (the four tests: stranger's first hour; no destructive or costly failure; fails loudly; clean boundary) | **2 left** of 11: #442 (compose reconcile can tear down a live stack outside the workspace) and #490 (merge-lock goroutine wedges on cancel). Fix or document both before tagging. |
| C3  | `main`'s own post-merge run green at the release commit (not the PR prediction)                                                    | Re-check at tag time. The scheduled staging smoke attaching to head is not a merge regression — read the job name.                                                                 |
| C4  | Known-issues baseline reviewed                                                                                                     | **Done** (below) — nothing in it is a product defect.                                                                                                                              |

### The listing is true

A Marketplace listing is the README, the gallery images, `package.json`
metadata and the CHANGELOG. Each must describe the product that ships.

| #   | Gate                                                                                                                    | State                                                                                                                                                   |
| --- | ----------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| L1  | Telemetry disclosure matches the code (opt-out, on by default, nothing uploaded without a license key or sign-in)       | **Done** — #1134. README previously claimed opt-in.                                                                                                     |
| L2  | No relative links in the README (they 404 on the listing page)                                                          | **Done** — #1134.                                                                                                                                       |
| L3  | `qna`, `pricing`, `galleryBanner`, `icon`, `license`, `homepage`, `bugs`, `repository` present; VSIX ships no dev files | **Done** — #1134 (`qna`, `pricing: Free`; test tsconfigs, `scripts/**` and stray `.vsix` excluded).                                                     |
| L4  | The README states which adapter path is the supported one and does not advertise autonomous multi-repo mode as finished | **Open** — #1136. The v1 triage's honest-risk section: non-default adapters are rougher; multi-repo/autonomous is less finished.                        |
| L5  | Gallery imagery is generated from the product, not drawn, and regenerable in one command                                | **Done** — #1134 (`npm run -w nightgauge-vscode marketing:screenshots`). The six hand-captured README images still exist; replace or keep deliberately. |
| L6  | CHANGELOG has an entry for the version being tagged                                                                     | **Open** — #1136. Only `[0.2.0] - 2026-07-28` exists and `main` is far past it.                                                                         |
| L7  | Privacy policy and terms the README links to exist and name the right entity                                            | **Done on the site side** (nightgauge.dev `/privacy/`, `/terms/`, sub-processors as a section); README links `/privacy/` — #1134.                       |

### Every surface is real

The product is the extension, the web dashboard, the mobile app and the chat
cards. A release page that shows only one of them is a claim about the other
three.

| #   | Gate                                                                                                               | State                                                                                                                           |
| --- | ------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| S1  | Extension dashboard: the real renderer, real run, VS Code frame                                                    | **Done** — `docs/images/marketing/extension-dashboard-*.png`.                                                                   |
| S2  | Discord and Slack cards: the exact payloads the notifiers send, plus the JSON                                      | **Done** — `docs/images/marketing/notification-{discord,slack}.{png,json}`. Compared against a real screenshot of the same run. |
| S3  | Web dashboard: a mocked-backend Playwright lane that captures the same run                                         | **Done** in the dashboard repository (`npm run screenshots:marketing`).                                                         |
| S4  | Mobile app: an emulator route-walk lane with marketing fixtures for the same run                                   | **In progress** in the mobile repository (`scripts/marketing-screenshots.sh` on a Pixel 9 Pro AVD).                             |
| S5  | The site copies all of the above from the owning repositories (`npm run assets:sync`) and never draws a screenshot | **Done** on nightgauge.dev; the mobile surface slots in when S4 lands.                                                          |

### The publish path

| #   | Gate                                                                                                                             | State                                                                                                                                                                                                                                                                                                                                                                                                                         |
| --- | -------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| P1  | `release.yml` has fired at least once on a stable tag (dogfood-readiness gate D2)                                                | **Done 2026-09-02** — run 33598909653 on `v0.2.1`: GitHub Release (11 assets: 3 CLI archives + SBOMs, checksums, manifest, 3 per-target VSIXs, all attested), cask PR `homebrew-tap#8` merged. The first attempt on `v0.2.0` exposed #1296 (GoReleaser released the rc tag on the same commit); `v0.2.0` stays tagged, unpublished.                                                                                           |
| P2  | Marketplace publish is its own manual dispatch (`marketplace-publish.yml`, #1299) that runs `vsce verify-pat` before any publish | **Done in the workflow, and it worked**: the original double gate in `release.yml` rejected the stored `VSCE_PAT` (401) and stopped before publishing anything; that gate has since moved to `marketplace-publish.yml`, the single publish path. The PAT from 2026-07-22 is no longer valid — the owner must issue a new Azure DevOps token (Marketplace: Manage, all accessible organizations) and `gh secret set VSCE_PAT`. |
| P3  | 0.x publishes to the Marketplace **pre-release** channel                                                                         | **Done** — #1289: `--pre-release` while the tag's major is 0. Not yet exercised: the Marketplace step has not run (P2).                                                                                                                                                                                                                                                                                                       |
| P4  | Open VSX                                                                                                                         | **Decided: not in the first release.** The namespace is claimed; publish there only after the Marketplace listing has soaked. No `ovsx` step exists — nothing to gate.                                                                                                                                                                                                                                                        |
| P5  | Recut the release candidate at the release commit; do not publish an older RC's artifacts                                        | **Done** — `v0.2.0-rc.25` (staging, green) at `35234944`, then `v0.2.0` there (unpublished, #1296), then `v0.2.1` at `57852243` = the same tree plus the release-pipeline fix and changelog.                                                                                                                                                                                                                                  |
| P6  | After the merge: the 72-hour quiet soak, then the announcement, then the Marketplace flip                                        | **Gate 1 complete 2026-09-02 06:40Z** (release + cask merged) — the 72-hour soak runs to 2026-09-05. Marketplace publish (Gate 2) waits only on P2; run `vsce publish --pre-release --packagePath <vsix>` for each attested VSIX from the release, or re-run the release workflow once the PAT is set.                                                                                                                        |

### Dogfood before the tag

G1 proves the install path once. The pipeline should also be seen to handle
the _shapes_ of work a stranger will hand it, not one M-sized feature. Before
tagging, drive one issue of each class through the packaged extension and
record the outcome in the private release runbook:

- an S-sized bug with a clear reproduction;
- an S-sized feature touching UI;
- an M-sized feature with a design decision in it;
- a chore (dependency or CI) with no product change;
- a spike whose deliverable is a written decision, not code;
- an issue with a `blockedBy` edge, to see the queue honour it;
- one that _should_ fail a gate (a wrong premise), to see it fail loudly.

Every card, dashboard row and history record those runs produce is the
release's evidence. Regenerate the marketing imagery from the best of them.

## Verdict — 2026-09-01

**GO for `v0.2.0` on the Marketplace pre-release channel** — executed as
`v0.2.1` on 2026-09-02 (see P1, P2, P5 and _Release day_ below).

Every row in _What is left before the first Marketplace version_ is closed
or re-measured as not blocking:

- G1 walked on the release commit (below). C1/C2 re-triaged over the 12 bugs
  filed since 2026-08-29: none claims install, activation or a core stage
  fails for a single-repo user; #1157 (board link on a fresh install) was the
  one first-hour defect and is fixed (#1288). C4 unchanged.
- L1–L7 done (#1134, #1289): supported-adapter sentence, no autonomous
  production claim, absolute links, `0.2.0` changelog dated 2026-09-01.
- P3 done (#1289): `release.yml` passes `--pre-release` while the major is 0.
  P1/P5 close with the tag; P4 stays "not in the first release".
- _Dogfood before the tag_: between 2026-08-29 and 2026-08-31 the downstream
  workspace ran every class in the list unattended through the extension —
  27 runs completed to a merged PR across four stacks (Flutter, Node API,
  Angular, static site), 6 failed, and every failure was a stage gate refusing
  to record a false success. The `blockedBy` pair was honoured in order; the
  spike produced a written decision; the wrong-premise candidate turned out to
  have a correct premise by the time it ran and was legitimately fixed. The
  per-issue table lives in the private release plan.

Deferred, deliberately: fable-routed runs cost ~10× a sonnet run of the same
size (three of the 27); cap with `max_model` (#1216) in the README's
recommended config before a stranger meets it. Two downstream failures are
open for triage in their own repositories and are not extension defects.

### Release day — 2026-09-02

What the first stable release run taught, in the order it happened:

1. **`v0.2.0` + `MARKETPLACE_PUBLISH=true`** → preflight rejected `VSCE_PAT`
   (401) and stopped. Nothing published. The double gate did its job. (That
   gate, and the variable, have since been removed from `release.yml`; the
   Marketplace publish is the `marketplace-publish.yml` dispatch.)
2. **Re-run with `MARKETPLACE_PUBLISH=false`** → GoReleaser released
   **`v0.2.0-rc.25`** (git's version sort ranks the rc tag above `v0.2.0` on
   the same commit), marked it Latest, opened an rc cask PR, then the manifest
   step failed on `release not found` for `v0.2.0`. The rc release was deleted
   and the cask PR closed by hand within minutes; #1296 pins GoReleaser to the
   triggering tag and guards that the release exists before anything attaches.
3. **`v0.2.1`** on the fix commit → green end to end. Checksums in the cask PR
   matched `checksums.txt`; the darwin-arm64 VSIX's SHA and its build
   attestation verified locally before the cask PR was merged.

Still open for the Marketplace: a valid `VSCE_PAT`. The three VSIXs on the
release are the artifacts to publish; do not rebuild them.

## Verdict — 2026-08-20

**GO for `v0.2.0-rc.24`.**

[PR #795](https://github.com/nightgauge/nightgauge/pull/795) is squash-merged
as `c9598c4f`. That merge commit's own checks are green (13/13, including
CodeQL `go` and `javascript-typescript`). The post-merge hook moved #793 to
Done. No remaining VS Code runtime blocker was found in this run. This is
not a GO to cut a stable `v0.2.0`.

## What this run closed

| Item                                | Evidence                                                                                                                                                                                                                                                                                            |
| ----------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Publication-boundary headroom       | Policy-maintenance PR #790 landed before release work continued. Ceiling at the end of this run is `high_water_mark` 789 + slack 25 = 814.                                                                                                                                                          |
| #785 dashboard loading              | PR #792 squash-merged. The prior session verified all 13 checks on the merge commit, including both CodeQL languages, then ran the post-merge hook and cleaned the branch/worktree.                                                                                                                 |
| #793 retry command false confidence | PR #795 squash-merged as `c9598c4f`. Tests invoke the shipped `retryStage` / `retryFromPhase` handlers. Local `bash scripts/ci-local.sh` passed (12,407 VS Code tests). PR checks all passed, including CodeQL `go` and `javascript-typescript`. Post-merge hook closed #793 and synced it to Done. |

## Known-issues baseline

File:
[`packages/nightgauge-vscode/tests/vscode-host/known-issues.ts`](../packages/nightgauge-vscode/tests/vscode-host/known-issues.ts).

This is a shrinking smoke-tier baseline, not a list of acknowledged product
failures.

| Entry                              | Count                                                                                          | Verdict                                                                                                        |
| ---------------------------------- | ---------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `CONTRIBUTED_WITHOUT_REGISTRATION` | empty (`nightgauge.selectAll` was deleted in #764)                                             | **Ship.** No contributed-but-unregistered commands.                                                            |
| `REGISTERED_WITHOUT_CONTRIBUTION`  | 9 commands, each classified (`tree-item`, `webview-internal`, `notification`, or `status-bar`) | **Defer.** Palette-invisible by design. Not product defects. Revisit only if one of these needs palette reach. |
| `KNOWN_STARTUP_REJECTIONS`         | 1: `DialogService: refused to show dialog in tests`                                            | **Ship.** Test-environment modal refusal, not a product defect.                                                |

Nothing in this file is a release blocker.

## Command surface

Counted from `package.json` `contributes.commands` and
`packages/nightgauge-vscode/tests/commands/` on `origin/main` plus PR #795:

- **143** contributed commands
- **61** command-focused test files
- **12,407** VS Code unit tests passing locally on the #793 tree
- Host tier exercises a smaller set through real activation/rendering

Raw counts are not the release criterion. #793 existed because a green suite
did not import the production handler. The proportionate bar is:

1. High-risk families (retry, stage dispatch, recovery, dashboard refresh)
   must invoke shipped handlers, not simulators.
2. Host/Playwright tiers must exercise the reachable UI contract.
3. Do not hand-test all 143 commands as a release gate.

Retry/recovery now meets that bar. Remaining command families were not shown
to have the same simulator-vs-production split in this run.

## Deferred issues (not RC blockers)

| Issue                                                       | Why it is not a blocker                                                                                                                                                                                                     | Follow-up                                                              |
| ----------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| [#794](https://github.com/nightgauge/nightgauge/issues/794) | State-changing Recovery Dialog actions were deliberately removed from #793. The shipped dialog can only open the run-state directory or cancel. Producer/resume/restart/discard need an identity-bound cross-process lease. | Keep in Ready. Do not reintroduce those actions in an RC.              |
| [#786](https://github.com/nightgauge/nightgauge/issues/786) | Audit of remaining write-temp-then-rename sites. #777 already fixed the known silent `index.json.tmp` race. This is a sweep, not a demonstrated dashboard-zero bug.                                                         | Defer past this RC.                                                    |
| [#787](https://github.com/nightgauge/nightgauge/issues/787) | `getDashboardHtml` takes 41 positional parameters. Maintainability risk, not a current render failure.                                                                                                                      | Defer.                                                                 |
| [#673](https://github.com/nightgauge/nightgauge/issues/673) | Tree cites many issue numbers that do not exist here. Publication-boundary now blocks _newly added_ out-of-range `#N` citations. The remaining corpus is a mechanical sweep that must land alone.                           | Defer. Do not mix with a logic RC.                                     |
| [#545](https://github.com/nightgauge/nightgauge/issues/545) | Docs/ADRs still name `skills-smoke.yml`, `claude-plugin-validation.yml`, and `synthetic-regression.yml`, which have never existed. Docs honesty, not a VS Code runtime failure.                                             | Defer. Do not claim those workflows enforce anything in release notes. |

## Pre-tag sequence

Executed:

1. Squash-merged PR #795 (no `--admin`, no `--auto`). Merge commit:
   `c9598c4f`.
2. Fetched `main`; squash SHA is `c9598c4f`.
3. Post-merge hook (`--issue 793 --pr 795 --project 3`):
   closed #793, synced board status to Done, no parent epic.
4. Confirmed #793 is closed. The follow-up lease issue (794) is in Ready.
   GitHub's closer regex treated a negation in PR #795's body as a closer;
   794 was reopened and the board row put back on Ready.

5. Merge-commit checks on `c9598c4f`: 13 completed, 0 non-success. Both
   CodeQL language jobs (`Analyze (go)` and
   `Analyze (javascript-typescript)`) succeeded.
6. `scripts/branch-merged-check.sh fix/793-production-retry-command-tests`
   reported SAFE-DELETE after the worktree was removed. Local branch deleted.
   Remote branch was already gone.

Still required before tagging:

7. Tag `v0.2.0-rc.24`. Follow
   [RELEASE_RUNBOOK.md](../RELEASE_RUNBOOK.md) for the actual tag/publish
   procedure; this checklist does not replace it.

## Local gate that was run

On `/tmp/nightgauge-793.S45yA2/tree` at commit
`66c8d7c1` (`fix(#793): harden retry command correctness and recovery`):

- `bash scripts/ci-local.sh` — all CI-parity checks passed
- Focused retry/recovery tests — 71 passing
- Playwright Recovery Dialog — observational actions only; re-enable works
- Host pretest — collection, runner coverage, and `tsc -p tests/vscode-host`

PR #795 then passed the full GitHub matrix, including CodeQL for Go and
JavaScript/TypeScript.

## The clean-machine install gate

Added 2026-08-24. The sections above record a **code**-readiness run: what
merged, what CI observed, which issues were deferred. None of it walks the
install path a first-time user walks, and until this section existed that gate
had no written procedure at all — it was named as the thing standing between
the repo and a release, and then left to whoever remembered it.

This is the procedure. Run it against the packaged artifact, never a dev
install (`scripts/dev-install.sh` is explicitly not a release-validation path).

### The steps

```bash
# 1. Package from a clean tree at the release commit.
npm run package -w nightgauge-vscode
#    → packages/nightgauge-vscode/nightgauge-vscode-<version>.vsix

# 2. Install into a profile that shares nothing with your dev setup.
code --extensions-dir /tmp/ng-clean/ext --user-data-dir /tmp/ng-clean/user \
     --install-extension packages/nightgauge-vscode/nightgauge-vscode-<version>.vsix

# 3. Point it at a repository with no Nightgauge state.
# 4. Follow the Marketplace README's Quick Start EXACTLY — no shortcuts an
#    author would take, no environment variables a maintainer already has.
# 5. Drive one issue to a merged PR.
```

Step 4 is the load-bearing one. The failures this gate catches are almost all
of the form "the docs describe a path the product does not have", and they are
invisible to anyone who knows the product well enough to route around them.

### What a real clean machine adds that a clean profile cannot

An isolated `--extensions-dir` / `--user-data-dir` is a good approximation and
worth running first — it is cheap and it catches most of it. It does **not**
isolate:

| Leaks through a clean profile | Why it matters                                                                                                                        |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `gh` keyring credentials      | The maintainer is already authenticated; a new user is not. Scrubbing `PATH` simulates absence but not a fresh login flow.            |
| `~/.vscode/extensions`        | The binary-resolution cascade scans it, so a dev bundle can be resolved instead of the one just installed — with a different version. |
| `~/.claude`, `~/.codex`, etc. | Adapter config and auth the user will not have.                                                                                       |
| Homebrew / toolchain state    | `git`, `gh`, `node` and the agent CLI are all present on a developer machine by definition.                                           |

So: run the clean-profile pass on every release candidate, and a genuine
second-machine pass before the first public release.

### Findings from the 2026-08-24 clean-profile pass

Run against `main` @ `051b74d4`, packaged as `nightgauge-vscode-0.1.0.vsix`
(28.82 MB, 1341 files; binary reports `0.2.0-rc.24`). Packaging itself was
green, and `doctor`'s per-adapter remediation lines were good — every failure
below is about what the product _claims_, not what it does.

| Finding                                                                                              | Status                                                              |
| ---------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `doctor` reported `healthy` / exit 0 with zero usable AI adapters                                    | Fixed — [#862](https://github.com/nightgauge/nightgauge/issues/862) |
| README Requirements omitted `gh` and an AI agent; "Sign In with GitHub" read as pipeline GitHub auth | Fixed — [#863](https://github.com/nightgauge/nightgauge/issues/863) |
| Every docs-only PR failed CI — the ungated test-tree typecheck ran without `npm ci`                  | Fixed — [#865](https://github.com/nightgauge/nightgauge/issues/865) |

Two observations left unfiled, both judged cosmetic:

- The `.vsix` ships `tsconfig.test.json` and `tsconfig.playwright.json`. Dev
  files in a Marketplace artifact; harmless, untidy. _Fixed 2026-08-29 (#1134)._
- `doctor` on a repository with no `origin` prints a raw four-line
  `git fatal: Could not read from remote repository` to stderr before its
  report. Alarming on a first run, and the sweep's warning already says what
  happened.

### Findings from the 2026-08-25 clean-profile pass (steps 3–4)

Run against `main` @ `29879753`, packaged as `nightgauge-vscode-0.1.0.vsix`
(28.87 MB, 1341 files; binary reports `0.2.0-rc.24-…-29879753`). Packaging and
install were green again. Steps 3 and 4 were walked for the first time: an
isolated profile pointed at a repository with no Nightgauge state, following
the Marketplace README verbatim.

Every finding is the predicted shape — the docs describe a path the product
does not have — and none was visible to CI:

| Finding                                                                                                                                 | Issue                                                       |
| --------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------- |
| README's first instruction is `nightgauge doctor`; a Marketplace install has no such binary                                             | [#898](https://github.com/nightgauge/nightgauge/issues/898) |
| `nightgauge --version` errors with "unknown flag"; only the subcommand works                                                            | [#899](https://github.com/nightgauge/nightgauge/issues/899) |
| In Restricted Mode the Nightgauge activity-bar icon does not exist; Quick Start never mentions workspace trust                          | [#900](https://github.com/nightgauge/nightgauge/issues/900) |
| First activation warns "project config incomplete" on a repo that is not initialized yet                                                | [#901](https://github.com/nightgauge/nightgauge/issues/901) |
| **Initialize Repository** runs an interactive `claude` session with its own trust prompt; the README calls it a click that writes files | [#902](https://github.com/nightgauge/nightgauge/issues/902) |

Workspace trust is worth calling out on its own: it is the first thing a new
user hits, it removes the product from the window entirely, and the only
on-screen explanation is VS Code's generic banner, which never names
Nightgauge.

### Automated (landed 2026-09-01, #1150)

Step 5 is now a regression suite rather than a walk (#1150). One command
packages the `.vsix` from the current tree, builds a container that has
nothing but Ubuntu, VS Code from the official `.deb`, `git`, `gh`, Node 22
and the `claude` CLI, installs the extension into a fresh
`--extensions-dir` / `--user-data-dir` there, creates a private throwaway
repository (`<owner>/e2e-clean-install-<utc-timestamp>`, seeded from
`tests/clean-install/fixture/` with one unambiguous feature request from
`tests/clean-install/issue.md`) and a throwaway project board, and drives that
issue to a merged pull request with a real agent and a real forge:

```bash
bash scripts/clean-install-e2e.sh           # the gate; spends tokens, creates + deletes a repo
bash scripts/clean-install-e2e.sh --smoke   # package, install, activate — no forge, no agent
```

Logs, the packaged VSIX and the driver's `report.json` land under
`.clean-install-e2e/<timestamp>/` (gitignored). The same walk runs from
`.github/workflows/clean-install-e2e.yml` on `workflow_dispatch` and weekly —
never on pull requests — with the `ANTHROPIC_API_KEY` and
`CLEAN_INSTALL_GH_TOKEN` secrets (the token must create and delete private
repositories and projects under the owner), uploading the logs as an artifact.

**What the container inherits from the host: agent authentication, and
nothing else.** `ANTHROPIC_API_KEY` when set; otherwise a copy of the host's
Claude Code OAuth credentials (exported from the macOS Keychain, or
`~/.claude/.credentials.json` on Linux) mounted read-only and copied into the
container user's `~/.claude/`, deleted with the run directory on exit. The
gate is about the product's install path, not the agent's login flow. No
`~/.nightgauge`, no `~/.vscode`, no `gh` keyring, no `PATH` binary: the
entrypoint refuses to start if any of those exist, and if
`NIGHTGAUGE_GO_BINARY_PATH` / `NIGHTGAUGE_BIN` are set, because either would
short-circuit the binary-resolution cascade the gate exists to exercise.

**What it proves**, each as an assertion with evidence in the log:

- the extension activated from the VSIX (id, version, and that its path is
  inside the fresh extensions dir and not a development path);
- the binary the extension resolves at tier 3 is the bundle VS Code recorded
  in `extensions.json`, is executable, and reports a version;
- `nightgauge.pickupIssue` accepted the issue and a per-run record
  (`runtime-<issue>-<runId>.json`) reached a terminal state within the wall
  clock (90 min) and under the cost cap (15 USD, read from the run record);
- the history record says `complete`, with non-zero cost and duration;
- the forge says the PR is `MERGED` and the issue is `CLOSED`.

**What it still cannot prove.** The "real clean machine" table above stays
true in one respect and is now covered in the rest: `gh` login, the
extensions directory, adapter config and the toolchain are all genuinely
absent in the container — but the agent's own credentials are inherited, so
a first `claude` login is not walked. Two Quick Start steps are not walked
through the product either, and each is filed as a finding:

| Step                      | How the gate walks it                                                                                                                                                                                      | Finding |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 3 — Trust the folder      | `--disable-workspace-trust`; the banner click is a VS Code surface, and #900 already fixed the product side                                                                                                | —       |
| 4 — Initialize Repository | The command only opens an interactive `claude /nightgauge:repo-init` terminal. The container runs the VSIX's own binary verbs instead (`config init`, `label ensure`, `project ensure-fields`, board link) | #1154   |
| 4 — board link            | The skill's `nightgauge forge graphql` link step fails on the default GitHub forge; the container links the board with `gh api graphql`                                                                    | #1157   |
| 5 — Pick Up Issue         | The command accepts only a live tree item and otherwise opens an input box; the driver types the number with `xdotool`                                                                                     | #1155   |
| 6 — Watch it run          | `pre-push validate` blocked any Node project without an `npm run build` script (the first real run halted at feature-validate after ~3 USD); fixed in #1159, the fixture keeps its `build` script          | #1159   |

The driver (`tests/clean-install/driver/`) is a plain-JavaScript extension
loaded with `--extensionDevelopmentPath`; the product extension is always the
installed VSIX, never a development path, and the driver asserts that.

#### Findings from the first automated walks (2026-08-29 → 2026-09-02)

Three full walks ran before this landed, each on a linux-arm64 VSIX packaged
from the tree under test and installed into a container with nothing else:

| Walk (UTC)       | Tree                     | Outcome                                                                                                                                                                                                                                |
| ---------------- | ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 2026-08-29 21:38 | `6fb043b1`               | Halted at `feature-validate` after 3.21 USD: the fixture had no `build` script — #1159.                                                                                                                                                |
| 2026-08-29 21:57 | `6fb043b1`               | Failed at `feature-dev` after 1.38 USD / 291 s: `dev context does not match the expected schema` — the deliverable-policy defect fixed the same day (#1190).                                                                           |
| 2026-09-02 03:53 | `be184293` (this branch) | **Pickup → plan → dev → validate → PR → merge, unattended: 4.17 USD, 842 s.** The throwaway repo's PR #2 merged at 04:08Z (survival record `merge_commit_sha db76f14f`), the issue closed. The driver still reported FAIL — see below. |

The third walk exposed a defect in the gate itself, not the product: the
driver asserted the PR from the per-run `runtime-*.json` snapshot, which the
pipeline removes when the run latches terminal, so the last poll predated
`prUrl`. The driver now takes PR evidence from the survival record the merge
writes (`pipeline/survival-records.jsonl`) when the snapshot lacks it. The
three product findings the walks routed around are #1154 and #1155 (both
harness-only: the product paths are interactive by design) and #1157, fixed
in #1288 — the container tries `nightgauge forge graphql` first and falls
back with a finding only if it fails, so the next walk records whether the
fix holds on a fresh install.

**Step 5 is therefore walked, once, on a pre-release tree.** The walk that
counts for G1 is the one on the release commit, recorded next.

#### Findings from the release-commit walk (2026-09-02, #1137)

`bash scripts/clean-install-e2e.sh` on `main` @ `e6e98c76` (the #1150 merge),
from the maintainer's machine, Docker linux/arm64:

| Field           | Value                                                                                                                                                                                                                             |
| --------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| VSIX            | `nightgauge-vscode-linux-arm64-0.1.0.vsix`, 20 MB, packaged from the tree; binary `v0.2.0-rc.24-235-ge6e98c76`                                                                                                                    |
| Install         | fresh `--extensions-dir` / `--user-data-dir`; extension activated as `0.1.0`; the binary VS Code recorded in `extensions.json` is the one the extension resolved                                                                  |
| Repository      | private throwaway, seeded from `tests/clean-install/fixture/`, one feature-request issue, one throwaway board                                                                                                                     |
| Stages          | `feature-planning → feature-dev → feature-validate → pr-create → pr-merge`, unattended                                                                                                                                            |
| Result          | PR #2 on the throwaway repo **MERGED** (`026bbc33`), issue **CLOSED**; run outcome `complete`                                                                                                                                     |
| Cost / duration | 3.84 USD / 979 s                                                                                                                                                                                                                  |
| Findings        | 2, both harness-only: #1154 (Initialize Repository is interactive by design) and #1155 (pickup has no programmatic path). **The #1157 finding is gone** — the board linked through `nightgauge forge graphql` on a fresh install. |
| Cleanup         | container, repository and board deleted on exit; verified none remained                                                                                                                                                           |

What this walk still does not prove, unchanged from the table above: the
agent's own login (the container inherits the maintainer's Claude Code
credentials), and the two interactive Quick Start steps, which are walked by
their binary-verb equivalents. A second-machine pass by someone who is not
the maintainer remains the honest next step and is not a blocker for a
pre-release.

## What this checklist is not

- It is not a GO to cut a stable `v0.2.0`.
- It is not permission to ship producer/resume/restart/discard recovery.
- It is not a claim that every contributed command was hand-tested.
