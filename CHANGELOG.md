# Changelog

All notable changes to the Nightgauge project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

> **No release has been cut yet.** This repository has zero git tags and zero
> GitHub Releases, so every entry below is unreleased and will ship under the
> **first** semver tag when it is created (see #136). Per
> [docs/GIT_WORKFLOW.md § Versioning](docs/GIT_WORKFLOW.md#versioning), the
> Extension / SDK / Go-binary version is always derived from the git tag at
> release time — the `0.1.0` in `package.json` is a placeholder, never a release
> version. When the first release is cut, rename this heading to that tag
> (e.g. `## [0.2.0] - YYYY-MM-DD`).

### Fixed

### Session 29 additions (wave 2 and 3)

#### #1442

- **Gate**: `scripts/validate-skill-metadata.sh` ran under `pipefail` with `echo | grep -q` and `grep | head -1`, so an early-exiting reader could turn a present field into `missing required field` under machine load. Field checks now use spawn-free shell matching; a regression arm shims `grep`/`head` to exit 141 on first match.

#### #1455

- **Go binary** (`internal/state/history.go`): the append's own retention prune measured the cutoff from `time.Now()` while the daily file was dated from the caller's `now`, so a record could be pruned in the same write that produced it once the calendar crossed the retention window. One clock per write, and the record being written is exempt.

#### Test harness (PR #1443)

- `newHaltedServer`'s cleanup now stops the autonomous dispatch loop it started and fails the test if the loop survives; four tests had been leaking it since the resume verb began spawning on a detached context, and the leak raced a later test's log capture on `main`'s own race run.

#### #502

- Fixed both confirmed adversarial-review findings in the #502 refinement scan rotation.

#### #1334

- Both review findings verified independently and both fixed; nothing refuted.

#### #1426

- All three review findings verified by probe and fixed; none refuted.

#### #489

- Both review findings independently reproduced and fixed; neither was refuted.

#### #1445

- Confirmed the reviewer's single medium finding by mutation and fixed it in the test, not by touching production code (the production fix from 914a1c7d was verified correct and left byte-identical).

#### #1446

- runCycle in internal/orchestrator/autonomous.go had one early return that abandoned the cycle instead of closing it.

#### #1447

- All four review findings were verified as true and fixed with one mechanism change: replaced the narrow allowlist of two literal denial-spelling needles (`permission denied (publickey)`, `remote: permission denied`) with the reviewer's recommended negative-guard approach.

#### #1448

- All 5 findings independently verified and fixed.

#### #1449

- cliVerbExecutor.ExecuteVerb (cmd/nightgauge/attention.go) previously named its context.Context parameter `_` and never consulted it.

#### #1450

- test

#### #1451

- internal/ipc/attention.go's Server.ExecuteVerb doc comment claimed the store calls it "AFTER the resolution is persisted (CAS)".

#### #1452

- Verified the reviewer's medium finding and confirmed it: internal/github/ledgerread.go's SummarizeWindow used HeaderObserved as the _only_ signal that a record's Remaining/Cost reflected a real header read, so any ledger record written by a pre-upgrade binary (which lacks that field and decodes it as false) silently reads as unknown quota — and on the Exhausted arm, as never-exhausted even when Re

#### #1453

- Rewrote internal/execution/manager_registration_wait_test.go's structural guard (TestRegistrationWaitsShareTheirTimeoutBudget) to fix all three medium findings from adversarial review, all confirmed by reproducing the reviewer's mutations against the pre-fix guard.

#### #1454

- Fixed both adversarial-review findings, both confirmed correct by independent verification: (1) HIGH — packages/nightgauge-vscode/src/commands/activateLicense.ts: the entered-key verification path only read info.valid/info.tier from platformValidateLicense() and discarded info.status, so a 403 LICENSE_MACHINE_LIMIT rejection on this path still printed "That license key was not accepted (invalid,

#### Goroutine lifecycle pin widened to wave_orchestrator.go and epic.go (#491)

- **Go binary** (`internal/orchestrator/wave_orchestrator.go`): `runSubagent`
  spawned a second, vestigial goroutine that only ever waited on `<-done` and
  did nothing — a dead completion-channel listener left over from a callback
  path (`completionCh`) nothing ever wrote to. Removed the goroutine, the
  unused channel, and the dead `originalOnComplete` capture; the pipeline
  result is now read directly from `readPipelineState` after the join. The
  package's 3 remaining spawns in this file (wave-parallel and wave-scaled
  workers, and `runSubagent`'s pipeline runner) are all joined — via
  `wg.Wait()` or the done-channel `select` — before their enclosing function
  returns, and each now carries a one-line lifecycle comment saying so.
- **Go binary** (`internal/orchestrator/autonomous_background_lifecycle_pin_test.go`):
  the #428 AST source pin (`TestEveryDetachedSpawnInAutonomousGoesThroughGoTracked`)
  widened from `autonomous.go` only to a table over `autonomous.go`,
  `wave_orchestrator.go` and `epic.go`. The latter two have no single
  wrapping seam like `goTracked`, so each is pinned by an explicit
  `file:line` allowlist instead of a containing-function scan; any detached
  spawn (a `go` statement or `.Go(...)` call) at a line not in its file's
  allowlist fails, naming the file and line — including a second, unreviewed
  spawn added next to an already-allowlisted one. `scheduler.go` stays out of
  scope (owned by #463).

### Added

#### `doctor` flags foreign processes holding open a pipeline worktree (#519)

- `orphaned_processes` now runs a second, cwd-keyed classifier over **every**
  process on the machine (not only ones the pipeline spawned): any process
  whose cwd resolves inside a pipeline worktree base
  (`.nightgauge/worktrees/*`, `.worktrees/*`, or `.claude/worktrees/*`) is
  folded into the same report, tagged `[cwd inside worktree issue-N]` or
  `[cwd inside REMOVED worktree issue-N]` when the worktree no longer appears
  in `git worktree list` — the case that can also block a future
  `git worktree remove` (#110), and detected even once the directory itself
  is gone. Live-worktree holders are age-gated the same way #341's own scan
  is (a stage or session that just started is not a leak); a REMOVED
  worktree's holder is reported at any age. Stale-vs-live is decided per repo
  root, so two repos legitimately mid-flight on the same issue number can
  never paper over each other. Interactive agent harnesses (Claude Code,
  Codex, the VSCode extension) can leak a detached shell into a worktree they
  never clean up; this closes the blind spot in #341's argv-only scan.
  Report-only, unchanged: no process is ever signaled.

#### `nightgauge api-usage --budget` (#1428)

- **`--budget` flag on `nightgauge api-usage`**: reports the hourly GraphQL
  budget spent and remaining from the request ledger, and prices a full
  ProjectV2 board read (17 points/100-item page) against what is left, so a
  session can check before a bulk board pull instead of discovering the
  exhaustion mid-poll.
- **Agent guidance**: `AGENTS.md` and `docs/GO_BINARY.md` now name the cost of
  a raw `gh` board pull (invisible to the ledger, ~340 points for the shared
  board), the one-pull-into-a-file rule, and the REST check-runs idiom for
  watching CI instead of the GraphQL `gh pr checks`.

#### `scripts/ci-critical-path.sh` — where CI's time goes (#1218)

- Ranks a run's **jobs** and **steps** by wall clock, with each step's share of
  the critical path (the longest job). `--pr` / `--sha` aggregate across every
  workflow run on a commit, because the PR gate is several separate runs and a
  single run id cannot see the job that actually holds the critical path.
- REST only: the jobs endpoint already embeds `steps[]`, so a full ranking is a
  handful of 1-point calls with no GraphQL cost. Documented in
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md).

### Changed

#### The PR gate's two most expensive steps (#1218)

- **Mirror drift self-test** was 41% of the critical-path `lint` job because
  `seed_repo` paid for a full `git archive` plus a whole generator run for each
  of its 16 arms. The fixture is now built and normalised **once** and each arm
  is restored from it by copy — every arm still runs in its own private
  sandbox, still fails closed, and a new cost guard asserts the seed count is 1
  so the speed-up cannot silently revert. An assertion-count guard pins that no
  arm was traded away for the speed. Locally 332s/230s → 184s/126s.
- **Go `Test` and `Test (race, whole tree)`** were two sequential steps of one
  job (1m55s + 3m15s). They now run **concurrently inside the same step**, each
  captured to its own file and replayed into its own log group, with both exit
  codes plus the skip accounting decisive. Deliberately not two jobs:
  `Go build & test` is a pinned required-check context, and a split job would
  report a context the live ruleset does not require — silently unenforcing the
  race pass. What was verified locally is that both passes go green side by
  side — the suites do not collide. The local timing was inconclusive (227s vs
  240s and 418s vs 294s over two paired rounds) and a 10-core box is the wrong
  regime for the question anyway, so whether the overlap pays is to be measured
  on the hosted runner with `scripts/ci-critical-path.sh`; split the step back
  into two if it does not.

#### Supply-chain hardening for the release path (#136)

- **All GitHub Actions SHA-pinned**: every `uses:` across `.github/workflows/*`
  is pinned to a full 40-char commit SHA with a `# vX.Y.Z` comment;
  `actions/checkout` unified on one major; the `goreleaser-action` tool
  `version:` and every `vsce` invocation pinned to exact versions.
- **SBOMs**: `.goreleaser.yml` now emits one SPDX SBOM per release archive
  (syft), attached to the Release and covered by `attest-build-provenance`.
- **Attribution**: a build-time `THIRD_PARTY_NOTICES` (generated from the
  production dependency closure) ships in the VSIX, the npm tarball, and every
  GoReleaser archive; `NOTICE` is now vendored into both `packages/*`.
- **License metadata fixed**: `.goreleaser.yml` license set to the valid SPDX
  `Apache-2.0` (was the invalid `SEE LICENSE IN LICENSE`).

#### Marketplace publication prep + first-run onboarding (Part of Epic #4155)

- **Marketplace listing metadata**: `packages/nightgauge-vscode/package.json`
  now has accurate `categories` (`Machine Learning`, `Other`), a `galleryBanner`
  matching the the dark brand background, a `homepage` link, a `bugs` link,
  and a corrected `repository.url` casing.
- **Marketplace-facing README**: `packages/nightgauge-vscode/README.md`
  now leads with a install → sign in → claim an issue → watch a PR quickstart
  instead of contributor setup instructions; contributor/npm-auth setup moved
  into a `## Development` section further down. A `## Screenshots Needed`
  checklist tracks the still-missing real screenshots/GIFs — not yet done.
- **Guided first-run onboarding webview** (`nightgauge.showGettingStarted`):
  a new webview panel walks a first-time user through install → claim an
  issue → watch a pipeline run to completion. Opens automatically once per
  install the first time a workspace is _not_ Nightgauge-initialized,
  reusing the existing `repoInitialized` context-key plumbing
  (`src/commands/quickstart.ts`); reopenable any time from the Command
  Palette.
- **Release workflow**: `.github/workflows/release.yml` now has a
  `Publish to VS Code Marketplace` step, gated behind
  `if: ${{ secrets.VSCE_PAT != '' }}` so it stays a guaranteed no-op until the
  real publisher PAT is added as a repository secret.

### Added

#### Settings Architecture — 3-Tier Model Capstone (Epic #3313, Phase 7 — #3340)

Completes the capstone phase for the settings tier model introduced in
Phases 1–6 of epic #3313:

- **`docs/CONFIGURATION.md` rewritten** to reflect the 7-tier precedence chain
  (`defaults → global → project → local → runtime → env → cli`) with a new
  3-tier conceptual model section (Team / Machine / Runtime) and tier placement
  guide. (#3340)
- **Runtime (memento) tier documented**: UI ephemeral state stored in VSCode
  `globalState` / `workspaceState`, never committed to YAML. (#3340)
- **Test coverage matrix verified** across all 7-tier boundary pairs — see
  `packages/nightgauge-vscode/tests/config/tier-boundary.test.ts`. (#3340)
- **Example config pruned**: `jira-config.yaml` relocated to
  `docs/spikes/2568-jira-integration-config-example.yaml` — it contained
  credential patterns not valid for the team tier. (#3340)

#### Slash-Command & SKILL.md Enforcement (Epic #3342)

Three enforcement layers that prevent the failure modes behind incidents #3329
and #3331 (agent reads command file as spec, skips SKILL.md phases):

- **Layer 1 — ADR-007 canonical banner** (#3343/#3344): Applied to all 33
  applicable command files in `claude-plugins/nightgauge/commands/`. Each
  file now opens with a positional banner that invokes the `Skill` tool before
  any other content, plus `disable-model-invocation: true` frontmatter. Verified
  by `nightgauge preflight skill-banners` (14 unit tests + RealTree
  regression in `internal/preflight/skillbanners_test.go`).
- **Layer 2 — Spike-contract hard-gate** (#3345): `nightgauge spike validate`
  rejects `type:spike` issue creation unless the body contains a valid fenced
  ` ```yaml recommendations ``` ` block (schema-validated), a Spike Contract
  path declaration heading, and an artifact path reference. Gate enforced by
  8 tests in `internal/cmd/spike/validate_test.go`.
- **Layer 3 — Epic-decomposition hard-gate** (#3346/#3347): Phase 2.9 of
  `skills/nightgauge-issue-create/SKILL.md` rejects `type:epic` creation
  unless the body declares one of three valid shapes: Path A (sub-issues
  planned), Path B (`<!-- nightgauge:decompose-later -->`), or Path C
  (`<!-- nightgauge:standalone-epic -->`). Classification logic mirrored
  in `internal/cmd/epicgate` (pure Go, 9 tests). Path B auto-creates a
  follow-up `type:chore` to track decomposition.
- **Documentation**: `CONTRIBUTING.md` extended with a "Slash-Command Contract
  (ADR-007)" section covering the canonical banner template, authoring
  checklist, epic-creation paths, and enforcement gate reference.
- **ADR**: `docs/decisions/007-slash-command-skill-invocation-contract.md`
  reconciled against final implementation.

### Changed

#### 15 skills migrated from direct `gh` calls to `nightgauge forge` (#3363)

Wave 4 of the forge-abstraction epic (#3349). Every direct `gh` invocation in
the 15 top-consumer skills (`repo-init`, `retro`, `project-sync`,
`issue-pickup`, `release-watch`, `issue-refine`, `pipeline-audit`,
`pipeline-health`, `issue-audit`, `pr-merge`, `dep-modernize`,
`modernize-plan`, `smart-setup`, `queue`, `pr-create`) is replaced with the
forge-agnostic `nightgauge forge` Cobra surface. `IB_FORGE=gitlab` now
works end-to-end across these skills.

- **New binary surfaces**: `nightgauge forge auth whoami`,
  `nightgauge forge repo view`, `nightgauge forge graphql` (raw
  GraphQL pass-through for the four GitHub-specific carve-outs documented in
  ADR-008).
- **Deprecation linter**: `scripts/lint-skills/no-direct-gh.sh` (mirrored as
  `nightgauge preflight skill-no-direct-gh`) gates regressions in CI via
  the new `.github/workflows/lint.yml` workflow. The allowlist
  (`scripts/lint-skills/allowlist.txt`) tracks the un-migrated tail (~10
  skills with ≤4 calls each), filed as a follow-up under #3349.
- **Smoke harness**: `.nightgauge/skill-smoke/` holds a per-skill
  `forge × skill` smoke script (15 skills × 2 forges), run by hand — the
  GitLab slot consumes the W5-2 Dockerized GitLab CE harness once it lands.
  No CI workflow runs the harness.
- **JSON shape parity**: `cmd/nightgauge/forge/skill_parity_test.go`
  asserts every `gh ... --json` path the migrated skills extract is also
  present in the corresponding `forge ... --json` output.

See [ADR-008](docs/decisions/008-skill-forge-cli.md) for the full migration
table, carve-out rationale, and consequence analysis.

### Fixed

#### Attention store writes interleaved across processes (#1425)

- `Store.writeMaterializedLocked` staged every writer of a card at the same
  `<id>.json.tmp`. The rename that publishes is atomic; the staging write is
  not, so two processes materializing one card truncated each other's
  in-flight bytes and the loser published a mix. The package's claimed
  cross-process guarantee — "atomic temp+rename plus the terminal-state CAS" —
  covered neither: the CAS runs before the write and guards the lifecycle
  transition, and a rename is only atomic per rename.
- **Go binary** (`internal/attention/store.go`, `streak.go`, `standing.go`):
  the per-directory `sync.Mutex` is now layered over an advisory
  `internal/flock` lock on `<dir>/nightgauge-attention.lock`, following the
  worktree chokepoint (#1163), so the daemon, the `nightgauge attention` CLI
  verbs and the sweep take one critical section. Staging paths are per-writer
  (pid + entropy) so the deliberately fail-open lock degrades to lost
  serialisation rather than torn bytes. The materialized write, the streak
  read-modify-write and the journal append all nest inside the one section.
- **The hold is bounded, because the wait is.** `Store.Resolve` runs an
  option's verb _inside_ that section to keep exactly-once verb execution, and
  the verb was unbounded: the daemon's `issue.close` calls the GitHub client,
  which sleeps through a fully exhausted rate limit for up to 75 minutes —
  while the API-budget sweep raises the very cards reporting that exhaustion.
  Every other producer's bounded wait then expired onto the fail-open branch,
  so the new serialisation lapsed exactly when the store was busiest. The verb
  now runs under `verbTimeout`, strictly below `flockTimeout`, which makes lock
  expiry mean what its comment claims (a wedged holder) instead of "somebody is
  legitimately still working". A verb that blows the ceiling fails, and
  `Resolve`'s existing contract leaves the card open for a retry.
- **Corollary, `internal/ipc/attention.go`:** the autonomous resume verb spawns
  the fleet dispatch loop, and used to hand it the verb's own context — safe
  only while that context happened to be the server-lifetime one. Under the new
  ceiling the same line would have killed the loop the moment the verb
  returned, reintroducing the "running but never dispatching" dead state
  (#405) through a context lifetime. Long-lived spawns now detach
  (`detachedRunCtx`).

#### Fixed-temp-path race in four more write-then-rename sites (#786)

- `workTimeFeedback.ts` (`appendObservationToYAML`) and
  `TelemetryUploaderService.ts` (`saveWatermarks`) in `nightgauge-vscode`,
  and `CalibrationService.ts` / `StageModelCalibrationService.ts` (`save`) in
  `nightgauge-sdk`, each wrote a fixed `<target>.tmp` before renaming it onto
  the target — the same shape #777 fixed in `TelemetryStore.writeIndex`. Two
  concurrent writers raced: the first `rename` won, the second failed
  `ENOENT` on a temp file the winner had already consumed. In the calibration
  services the failure went unreported by construction — both callers in
  `PostPipelineAnalyzer` only `logger.debug` a rejected `save()`.
- The two `nightgauge-vscode` sites now use a temp name unique per write (pid
  and random hex) with cleanup-on-failure, matching `writeFileAtomic`'s idiom.
  The two `nightgauge-sdk` sites now delegate to the SDK's own
  `atomicWriteJSON` (already used by `ContextManager`/`RunStateManager`)
  instead of hand-rolling the write+rename.
  `executionHistoryWriter.ts` already used `writeFileAtomic` (fixed earlier by
  #1212) and needed no change. See
  [docs/TESTING.md § Write-then-rename sites](docs/TESTING.md#write-then-rename-sites-the-fixed-temp-path-race-and-the-workspace-wide-sweep-786)
  for the full sweep of every such site in `nightgauge-vscode` and
  `nightgauge-sdk`.

#### `autonomous.*` IPC handlers synchronized on a 50ms sleep (#494)

- `autonomous.start`, `autonomous.resume`, `autonomous.resumeRepo` and
  `autonomous.stop` each signalled the scheduler and then slept a flat
  `50 * time.Millisecond` before sampling `Status()`. A wall-clock guess is not
  synchronization: the dispatch loop drains `stopCh` **between cycles**, so
  whenever a cycle was in flight the guess expired first and the handler
  answered with a state the scheduler had not reached — `autonomous.stop`
  replying `running` being the visible shape.
- **Go binary** (`internal/orchestrator/autonomous.go`): every write to the
  scheduler's `running` flag now goes through one writer that also wakes a
  broadcast channel, and the new `WaitForRunning(want, timeout)` blocks on
  that transition. On timeout it reports the liveness it actually observed,
  never the liveness the caller asked for.
- **Go binary** (`internal/ipc/server.go`): the four handlers wait on that
  primitive (2s ceiling) instead of sleeping, and log when the wait expires.

#### A credential-less push records its own terminal kind (#878)

- A stage that died on a `git push` with no usable credentials was booked as
  `premature_turn_end` (the classifier saw the retained post-condition symptom
  phrase) or `validation_error` (the post-condition site hardcoded it). Both
  name agent behaviour or a broken output contract for a fault that is neither.
  `terminal_kind` is what the V2 run record carries and what recovery routing
  and the retro path key on, so the misattribution poisoned the learning corpus.
- **New terminal kind** `git_transport_auth_failed`, added as one rule in
  `internal/terminalkind/table.json` **above** `premature-turn-end`, matching the
  transport's own wording (`invalid auth method`, `permission denied (publickey`,
  `could not read Username`/`Password`, `authentication failed for`,
  `authentication required`, `ssh: unable to authenticate`, `bad credentials`,
  `http 401`, `401 unauthorized`) — a deliberate subset of
  `orchestrator.permissionPhrases`, not a copy: the bare words and the 403 forms
  stay out because this ladder matches the whole joined string above sixteen
  other rules, and each exclusion is pinned by a corpus row that would otherwise
  go red. One authority: the SDK mirror and the stress golden are regenerated
  from that file, not hand-written.
- **Go binary** (`internal/orchestrator/scheduler.go`): the missing-output
  post-condition now derives its terminal kind by classifying the FIRST CAUSE
  line it already found in the stage's output tail, falling back to
  `validation_error` only when the table does not recognise it.
- **The cause the daemon logs now reaches the record it books.** The observed
  run's `invalid auth method` came from the scheduler's own non-blocking
  epic-branch auto-create, which only ever called `log.Printf` — so the
  first-cause scan, the escalation gate and the terminal kind were all reading a
  stage output tail that never contained it, and the run still escalated
  haiku → sonnet and booked `validation_error`. `ensureEpicBranchForItem` now
  returns its failure text and `runPipeline` appends it to the stage's evidence
  via the new `RuntimeState.AppendStageOutputTail`.
- **Retro** (`AutoRetroService`): the authoritative `terminal_failure_kind` map
  gained the new kind, with a `credential-failure` category. Without it the
  extractor returned null for the #878 record and the prose keyword table won on
  the retained `did not write expected output context` phrase, answering
  `state-management` and recommending a context re-run for a credential fault.

#### Paused/corrupt snapshot protection was unbounded without a resident server (#443)

- The 14-day snapshot age cap lived as a private constant in the IPC orphan
  reconciler, which runs only from a server's startup timer. In a CLI-only
  workspace — no `nightgauge serve`, no extension — nothing ever aged a paused
  or corrupt snapshot out, and `state.ActiveIssuesFromSnapshots` then protected
  that issue's worktree **forever**: `nightgauge worktree sweep` reported it as
  `active-run` on every run and could never reclaim it.
- **Go binary** (`internal/runstate/concurrent.go`): the cap moved beside
  `LivenessWindow` as the exported `SnapshotRetention`, imported by both
  readers — the reconciler collects past it, the CLI-side scan stops protecting
  past it. The IPC-local constant is deleted; the literal exists once in the
  tree.
- **Go binary** (`internal/state/active_issues.go`): the paused, corrupt and
  name/body-mismatch arms apply that cap and warn by name when they stop
  vouching. The `stat`-failure arm stays unbounded and says why: retention
  needs an mtime.
- **Operator surface** (`internal/execution/worktree_sweep.go`,
  `cmd/nightgauge/worktree.go`): `ActiveIssues.Protected` names the arm and its
  evidence per protected issue, carried onto `SkippedWorktree.ReasonDetail`.
  Text prints `skipped <path> (active-run: paused-snapshot, 13d)`; `--json`
  adds `"reasonDetail"` beside `"reason"`. All six arms used to print the same
  `active-run` word.
- **Second consumer named, not left to inherit silently**
  (`internal/orchestrator/autonomous_compose_reconcile.go`): the same scan feeds
  the autonomous compose reconcile, whose action is `docker compose down -v`. Its
  doc comment claimed a pause protects its stack unconditionally, which the cap
  makes false; it now states the bound. Under `serve` nothing changes — the orphan
  reconciler in that process was already collecting the snapshot past the same cap
  — and under `autonomous run` a fortnight-old pause is debris by the same
  reasoning, named in a warning before anything is torn down.

#### Pause/Resume Pipeline commands could not target a concurrent-slot run (#423)

- `Nightgauge: Pause Pipeline` / `Nightgauge: Resume Pipeline` used to act
  only on the singleton `PipelineStateService`. With only a concurrent-slot
  run live, the singleton holds no run identity (ADR-017 Decision 10), so the
  command fell back to an honest but useless "not persisted (no run
  identity)" refusal instead of pausing the run the operator meant.
- **`packages/nightgauge-vscode/src/commands/runSelector.ts`** (new): resolves
  the run a pause/resume command should act on — the singleton when it holds
  a live run, one candidate per active concurrent slot otherwise, and a
  `QuickPick` (by issue number and run-id prefix) when more than one run is
  live.
- **`pausePipeline.ts` / `resumePipeline.ts`**: now resolve their target via
  `resolveTargetRunService` before checking pipeline state, instead of
  hard-targeting the singleton. Global pause of the whole scheduler stays out
  of scope (a separate verb).
- **`PipelineStateService.getIssueNumber()`** (new): the issue number
  `beginRun` installed alongside `getRunId()`'s identity — the run selector's
  source of truth, independent of the last IPC state snapshot.

#### A timed-out webview render leaked its panel and hid its timing (#1327)

- On `main`'s own post-merge run, a webview panel that crossed the host
  smoke tier's render budget under hosted-runner load skipped disposal —
  its `dispose()` call sat after the awaited render probe, guarded by
  nothing — so one flake reported as two failures: the render timeout, then
  "no panel from this suite is left open" as an unrelated-looking follow-on.
- **`tests/vscode-host/fixture.ts`**: new `waitForRenderThenDispose()` logs
  each panel's elapsed render time in a grep-able `render-ms <panel> <ms>`
  line — pass or fail — and disposes the panel from a `finally`, so a render
  that never settles can no longer skip disposal or cascade into the
  leaked-panel assertion.
- **`tests/vscode-host/suites/webviews.suite.ts`**: the per-panel case now
  goes through `waitForRenderThenDispose()` instead of an unguarded
  `waitFor` + `dispose()` pair.

#### Empty epics invisible in Repositories tree view (#3329)

- A freshly-created epic (`type:epic` label, zero sub-issues) was filtered out
  of the flat list and rendered no group header, making it invisible in the
  Repositories view until it was decomposed.
- **Go binary** (`internal/github/board.go`): `IsEpic` now treats the
  `type:epic` label as the canonical marker, in addition to native sub-issue
  presence. A label-only epic returns `IsEpic: true`.
- **TypeScript view** (`packages/nightgauge-vscode/src/views/items/EpicGroupTreeItem.ts`):
  `groupIssuesByEpic()` now creates a (possibly empty) group entry for every
  `type:epic` issue in the current status filter, and back-fills missing
  metadata from the epic row itself. `EpicGroupTreeItem` renders empty epics
  as leaf items (no expand chevron) with a tooltip prompting `issue create-sub`.
- **Slash-command help** (`claude-plugins/nightgauge/commands/issue-create.md`):
  the project-board sync example now passes `--status Ready` and explicitly
  sets `--size`, matching the full SKILL.md guidance and preventing accidental
  Backlog placement.

### Added

#### nightgauge-feature-dev Expanded Review Subagents (#10)

- **6 parallel review subagents** in Phase 5 Quality Review (up from 3):
  - Code quality reviewer (existing)
  - Security reviewer (existing)
  - Test reviewer (existing)
  - Documentation reviewer (new) — checks API docs, inline comments, README updates
  - Performance reviewer (new) — checks N+1 queries, memory leaks, hot-path costs
  - Accessibility reviewer (new) — checks ARIA labels, keyboard nav, color contrast
- **Parallel execution**: all 6 reviewers spawn in a single `Task` message for
  maximum throughput
- **Aggregate quality report**: consolidated findings table after all reviewers
  complete, with critical issues surfaced for Phase 6 self-correction

#### update-docs Skill Improvements (v1.6.0)

- **Lessons Learned Section**: Added comprehensive documentation of known false
  positive patterns discovered in real-world usage
  - Template content in skill/command files
  - Educational examples showing correct vs incorrect patterns
  - Subdirectory paths vs top-level paths
- **Improved Validation Guidance**:
  - Made AWK-based code block filtering MANDATORY (not optional)
  - Added context-aware directory checking to avoid flagging valid subdirectory
    references
  - Added specific exclusion patterns for skill and command files
- **Known Patterns Documentation**:
  - Grep patterns that work reliably vs unreliable patterns
  - Common validation failure modes and prevention strategies
  - Relative path resolution best practices

### Changed

- **update-docs Skill**:
  - Phase 4.5 now requires AWK-based code block filtering to prevent false
    positives
  - Enhanced directory structure mismatch detection with context awareness
  - Version bumped from 1.5.0 to 1.6.0

#### GitHub Sub-Issues Integration (Issue #38)

- **VSCode Extension**:
  - Added `GitHubService` for interacting with GitHub's native sub-issues API
  - Added `subIssueProgress` utility for calculating epic completion percentages
  - New methods: `fetchSubIssues()`, `linkSubIssueToParent()`,
    `fetchIssueMetadata()`

- **Context Schema** (v1.3):
  - Added `child_issues` field for tracking sub-issue numbers
  - Added `sub_issue_progress` field for epic progress statistics
  - Added `native_parent` field for GitHub native parent references

- **Scripts**:
  - Enhanced `check-epic-completion.sh` to query GitHub's native sub-issues API
  - Maintains backward compatibility with body-text reference parsing
  - Added progress percentage calculation in epic status logs

- **Documentation**:
  - Added `docs/SUB_ISSUES.md` - Comprehensive guide for using sub-issues with
    Nightgauge
  - Covers creation workflows, epic progress tracking, and PR parent linking
  - Includes troubleshooting section for common issues

- **Tests**:
  - Added `subIssueProgress.test.ts` - Unit tests for progress calculations
  - Added `GitHubService.subIssues.test.ts` - Tests for GitHub API interactions
  - Added mock factories in `tests/mocks/sub-issues.ts` for test data generation

### Changed

- **Epic Completion**: Now queries native sub-issues first before falling back
  to body parsing
- **Progress Tracking**: Epic completion now shows percentage in addition to
  fraction (e.g., "60% (3/5)")

---

## Author

nightgauge
