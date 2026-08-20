# VS Code Extension Release Checklist

Evidence from the 2026-08-20 release-readiness run. This is not a generic
template: every row is something that was actually executed or judged.

The last published tag at the start of the run was **`v0.2.0-rc.23`**. The
originally requested `v0.2.0-rc.10` was already obsolete. The next candidate
is **`v0.2.0-rc.24`**.

Green CI on a PR is a prediction. The merge commit on `main` is the
observation. Do not tag until that post-merge observation is green.

## Verdict

**Conditional GO for `v0.2.0-rc.24`.**

Ship after squash-merging
[PR #795](https://github.com/nightgauge/nightgauge/pull/795) (`fix(#793)`),
verifying that merge commit's own checks (including both CodeQL languages),
and running the post-merge hook. No remaining VS Code runtime blocker was
found in this run.

## What this run closed

| Item                                | Evidence                                                                                                                                                                                                                 |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Publication-boundary headroom       | Policy-maintenance PR #790 landed before release work continued. Ceiling at the end of this run is `high_water_mark` 789 + slack 25 = 814.                                                                               |
| #785 dashboard loading              | PR #792 squash-merged. The prior session verified all 13 checks on the merge commit, including both CodeQL languages, then ran the post-merge hook and cleaned the branch/worktree.                                      |
| #793 retry command false confidence | PR #795. Tests now invoke the shipped `retryStage` / `retryFromPhase` handlers. Local `bash scripts/ci-local.sh` passed (12,407 VS Code tests). PR checks all passed, including CodeQL `go` and `javascript-typescript`. |

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

## Pre-tag sequence (execute, do not skip)

1. Squash-merge PR #795. Never `--admin`, never `--auto`:

   ```bash
   GH_TOKEN=$(gh auth token) gh pr merge 795 --repo nightgauge/nightgauge --squash
   ```

2. Fetch `main` and identify the squash SHA.

3. Wait for **that commit's** checks, including both CodeQL languages:

   ```bash
   gh api "repos/nightgauge/nightgauge/commits/<merge-sha>/check-runs" \
     --jq '[.check_runs[]|select(.conclusion!="success" and .conclusion!="skipped" and .conclusion!="neutral")]|length'
   ```

   Non-zero means `main` is red. Fix that before tagging.

4. Run the post-merge hook (read its output; exit 0 is not proof):

   ```bash
   nightgauge project resolve --repo nightgauge/nightgauge --json
   nightgauge hook post-merge --issue 793 --owner nightgauge --repo nightgauge \
     --pr 795 --project <PROJECT>
   ```

5. Confirm #793 is closed and on Done. Do **not** close #794.

6. Delete the merged branch/worktree only after
   `scripts/branch-merged-check.sh` reports SAFE-DELETE:

   ```bash
   bash scripts/branch-merged-check.sh fix/793-production-retry-command-tests
   ```

7. Tag `v0.2.0-rc.24` only after steps 3–6. Follow
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

## What this checklist is not

- It is not a GO to cut a stable `v0.2.0`.
- It is not permission to ship producer/resume/restart/discard recovery.
- It is not a claim that every contributed command was hand-tested.
