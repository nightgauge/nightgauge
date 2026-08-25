# VS Code Extension Release Checklist

Evidence from the 2026-08-20 release-readiness run. This is not a generic
template: every row is something that was actually executed or judged.

The last published tag at the start of the run was **`v0.2.0-rc.23`**. The
originally requested `v0.2.0-rc.10` was already obsolete. The next candidate
is **`v0.2.0-rc.24`**.

Green CI on a PR is a prediction. The merge commit on `main` is the
observation. Do not tag until that post-merge observation is green.

## Verdict

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
  files in a Marketplace artifact; harmless, untidy.
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

| Finding                                                                                   | Issue                                                            |
| ----------------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| README's first instruction is `nightgauge doctor`; a Marketplace install has no such binary | [#898](https://github.com/nightgauge/nightgauge/issues/898)      |
| `nightgauge --version` errors with "unknown flag"; only the subcommand works               | [#899](https://github.com/nightgauge/nightgauge/issues/899)      |
| In Restricted Mode the Nightgauge activity-bar icon does not exist; Quick Start never mentions workspace trust | [#900](https://github.com/nightgauge/nightgauge/issues/900) |
| First activation warns "project config incomplete" on a repo that is not initialized yet    | [#901](https://github.com/nightgauge/nightgauge/issues/901)      |
| **Initialize Repository** runs an interactive `claude` session with its own trust prompt; the README calls it a click that writes files | [#902](https://github.com/nightgauge/nightgauge/issues/902) |

Workspace trust is worth calling out on its own: it is the first thing a new
user hits, it removes the product from the window entirely, and the only
on-screen explanation is VS Code's generic banner, which never names
Nightgauge.

### Still not walked

**Step 5 has not been executed.** Steps 3–4 above were walked on 2026-08-25 and
stopped at **Initialize Repository**, which hands control to an interactive
agent session in a VS Code terminal. Driving one issue through to a merged PR
as a fresh user remains the release gate, and no amount of green CI substitutes
for it — every finding above came from running the packaged artifact, and none
of them was visible to the test suite.

## What this checklist is not

- It is not a GO to cut a stable `v0.2.0`.
- It is not permission to ship producer/resume/restart/discard recovery.
- It is not a claim that every contributed command was hand-tested.
