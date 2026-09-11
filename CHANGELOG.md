# Changelog

All notable changes to Nightgauge — the Go binary, the VS Code extension, the
SDK and the skills, which ship together under one version — are recorded here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The version is the
git tag (`vX.Y.Z`); see
[docs/GIT_WORKFLOW.md § Changelog](docs/GIT_WORKFLOW.md#changelog) for how an
entry is written and how a section is cut. `scripts/check-changelog.sh`
enforces that every released tag has a section here and in the extension's
changelog, and the release workflow refuses a tag that does not.

## [Unreleased]

## [0.4.1] - 2026-09-11

### Fixed

- Marketplace and Open VSX publish now retry PAT verification through transient
  registry timeouts instead of failing the whole publish on one, and package
  and publish with updated `@vscode/vsce` 3.9.2 and `ovsx` 1.2.0, which drop
  the deprecated `glob@11` dependency from the build (v0.4.0 publish, #1599)

## [0.4.0] - 2026-09-11

### Changed

- VS Code Marketplace and Open VSX releases now use even `0.x` minor lines for
  normal installs and odd minor lines for opt-in previews. The next release can
  install without the misleading "no release version" warning while Nightgauge
  continues to describe its product maturity honestly (#1594)

### Fixed

- Release-candidate staging now resolves its registry channel from the RC's base
  version, so `0.4.0-rc.N` packages exercise the same stable-channel shape as
  the eventual `0.4.0` release (#1599)

- Post-merge verification now receives the required CLA and aggregate CodeQL
  contexts on `main`, so a fully green merge no longer waits until timeout
  before release work can continue (#1596)

### Added

- Claude, Codex, and Grok are all first-class skill consumers. The local
  installer (`scripts/install-agent-skills.sh`) copies skills into
  `~/.grok/skills` by default (with `--grok-only` / `--claude-only` /
  `--codex-only`), the VS Code extension installs Grok skills on activate from
  the bundled VSIX, and Codex marketplace installs no longer depend on a
  workspace `skills/` tree. Progressive-disclosure `Read` paths in SKILL.md are
  skill-relative (`_includes/foo.md`) so they resolve in Grok, Codex, and the
  Claude plugin copy, not only inside this checkout.

- An Action Center card for a PR that is green but behind its base branch now
  offers to update it, instead of offering only "dismiss". The new
  `pr.updateBranch` verb merges the base into the PR's head branch in one
  deterministic forge call, with an empty argument surface — both the repository
  and the PR number come from the card the producer raised, and the repository
  must already be configured. Three of `human-gate`'s four gate codes still
  genuinely need a person (an approving review, a protection rule, a merge
  conflict); being behind the base never did (#1575)

### Fixed

- Required GitHub commit-status contexts now participate in the guarded CI
  completeness verdict instead of appearing permanently absent (#1593)

- Fresh repositories now learn from their first pipeline outcome: the Go recorder
  auto-creates the canonical model, VS Code uses its correct path and a shared
  transaction, and setup guidance uses `nightgauge outcome init` (#1590)

- `nightgauge knowledge index --help` now names `index.md` as the generated
  index file, matching the Open Knowledge Format layout that replaced
  `README.md` in #1370 (#1477)
- A default branch no longer reads as red forever because of one old failing
  check run. For each check name, only its most recent completed run now
  decides pass or fail, so a scheduled workflow that failed once and has
  succeeded on every run since against the same unchanged commit clears the
  fleet blocker instead of holding it up until someone pushes. Recency is
  established from the run's own completion time; an undated run and a
  cancelled re-run are both treated as non-evidence, so neither can suppress a
  real failure (#1572)
- A red default branch no longer produces two cards. `default-branch-health`
  now defers to `merge-commit-checks` whenever that producer already has an
  open card for the same repository and branch, so the operator gets the one
  observation that names which merge turned the branch red rather than that
  one plus a weaker duplicate. Cards raised about a branch now carry it in
  `Context.branch`, which is what the new branch-scoped dedupe lookup keys on
  (#1573)

## [0.3.1] - 2026-09-07

### Fixed

- The check-runs completeness check no longer reads a rollup that is missing
  an in-flight required job as done. `total_count > 0` with zero pending was
  observed while `lint` and `publication boundary` were still `in_progress`
  and simply absent from the response — every reader (the PR gate,
  `nightgauge hook post-merge`, and `scripts/post-merge-check.sh`) counted
  only checks the rollup happened to return, so a missing required check
  never blocked a merge decision. A shared primitive now asserts the
  positive presence of every required check name instead, exposed as
  `nightgauge ci checks-complete <sha>` so all three callers — and
  AGENTS.md's merge idiom — use the same guarded logic (#1540)
- A usage cap no longer idles the whole fleet for an hour. A rate-limit
  rejection is now attributed to the model that was actually in flight, so a cap
  hit while running `fable` descends the existing tier ladder (fable → opus →
  sonnet → haiku) and the run continues. The structured `rate_limit_event` the
  provider sends carries no model at all, so every cap read as an account-wide
  exhaustion and applied a global cooldown that suspended dispatch for every
  repo — with opus, sonnet, haiku, codex and grok all available and nothing
  tried (#1545)
- The global quota cooldown is now a last resort, not a first move. It applies
  only once the tier ladder and the provider walk are both exhausted, which is
  the only evidence the account itself is out rather than one model's window. An
  account-wide rejection with no fallback chain configured still cools the fleet
  down exactly as before (#1545)
- A usage cap on a model now reaches the tier descent instead of being recorded
  as a crash. The vendor CLI exits non-zero with an empty stderr and reports the
  cap inside its streaming-JSON terminal envelope, so the classifier received
  `exit 1: ` with nothing to match and answered `subagent_crash` — a _lifetime_
  failure kind. #1545's fable → opus → sonnet → haiku ladder was correct and
  structurally unreachable, so every dispatch inside a cap window consumed the
  issue's lifetime budget and booked a cascade strike for a condition that
  clears on its own. The transcript's terminal envelope is now read when, and
  only when, the vendor itself set `is_error`, and the table learned the
  wording (#1556)
- `nightgauge run <issue>` acts on the repository it was invoked for. The
  target was a string literal — `<owner>/nightgauge` — so the explicit-issue
  path could not reach any other repo whatever the checkout, the config or
  `--project` said. Issue numbers collide across repositories, so this was not
  only a "not found on board" annoyance: the command could plan, edit and open
  a pull request against a repository the operator never named. A new `--repo`
  flag takes precedence, the checkout's configured `repo:` is the default, and
  a run whose repository cannot be resolved is refused rather than guessed
  (#1553)
- Stage phase progress counts work that was observed. A deliberate skip now
  lowers the denominator instead of raising the numerator, so a stage that
  observed nothing no longer reads as nearly complete — `11/14 phases` on a
  stage whose rows were 11 skipped, 3 unreported and 0 complete. A running
  stage with no markers says so rather than showing a `0/18` that never moves,
  the tree no longer fabricates `complete` for phases before the one reported,
  and skips collapse into one expandable row. `abandoned` became a first-class
  phase status: Go has persisted it since the phase record existed, the schema
  rejected it, and the tree rendered it as `unreported` — losing the one thing
  it knows, that the phase started (#1558)
- The pipeline removes a run's worktree before deleting its branch. git refuses
  to delete a branch a worktree holds, and on the success path that holder is
  the run's own worktree — so `git branch -D` failed on every successful run,
  after the remote copy had already been deleted, and the run reported full
  cleanup anyway. Cleanup now reports whether the local ref is actually gone;
  a leftover branch still never fails a shipped run (#1561)
- A run that closes its own issue resolves the board to Done. The terminal
  status was decided from a reading of the issue taken before the merge, and
  the PR body's `Closes #N` fires at merge — so every self-completed run left a
  row reading In review on an issue GitHub had already closed. Done still means
  exactly one thing: the closure is observed from the forge at completion time,
  never inferred from the merge (#1562)

### Added

- `pipeline.adapter_fallback_chain` is now reachable on usage-cap exhaustion,
  not just on a stage-start prereq failure. When every tier of a provider is
  spent, the stage re-runs on the next installed and authenticated provider in
  the chain — `adapter_fallback_chain: [claude, codex, grok]` — and the hop is
  pinned for the rest of the run. See
  [docs/CONFIGURATION.md § Provider fallback](docs/CONFIGURATION.md#provider-fallback-pipelineadapter_fallback_chain)
  (#1545)
- An Action Center card when a usage cap changes how a run is routed, naming the
  stage, the tier or provider it moved to, and why — so the operator learns it
  from the product instead of from `go-backend.log` at 3am (#1545)
- `nightgauge autonomous start`, and `status` / `stop` that reach the running
  daemon. The daemon has always exposed a full autonomous control surface over
  IPC; the CLI wired two of thirteen. `status` printed `state.json` — which the
  scheduler writes and never re-reads — as the live answer, `stop` wrote the
  same file and reported a stop no running scheduler received, and `start` did
  not exist, so the fleet could only be started from the extension UI. Each verb
  now names which surface answered, so a state-file write is never mistaken for
  a live one (#1555, #1536)

## [0.3.0] - 2026-09-07

### Breaking / behaviour change

- **Sanitization now blocks.** `sanitization.mode` defaults to `block`, not
  `warn`. A Bash command or Task prompt matching a destructive, exfiltration,
  escalation or traversal pattern is refused and logged instead of logged and
  allowed. A repository that wants the old behaviour writes
  `sanitization: {mode: warn}` — that is the documented opt-out for one still
  calibrating its rules (#1519, ADR-021)
- **Scope drift and context budgets now enforce.**
  `pipeline.scope_drift_gate.enforcement_mode` defaults to `strict` and
  `pipeline.context_budgets.mode` to `hard`. A `type:docs` issue whose real diff
  reaches outside the allowlist blocks its PR, and a stage over budget is
  terminated rather than warned about. The `scope:cross-cutting` bypass label
  and `grace_percent` are the escape hatches; "avoid breaking existing
  pipelines" was a migration reason and migrations end (#1519, ADR-021)
- **Auto-merge is no longer set by default.** `pull_request.auto_merge` and
  `auto_merge_epic` both default to `false`, and both are pinned explicitly in
  the committed public-core `.nightgauge/config.yaml`. The pr-merge stage
  merges when CI is green; forge-side auto-merge on top lands the PR the moment
  its last check reports, past the gate the forge itself enforces (#1519)
- **`audit.enabled` is gone as an independent switch.** Audit emission follows
  `platform.enabled` — it needs a platform URL and key to do anything at all.
  An existing `audit.enabled: true` is read, warned about once and otherwise
  ignored; the `audit.*` tuning keys are unchanged, and
  `NIGHTGAUGE_AUDIT_ENABLED` still overrides in both directions (#1519)
- `knowledge.index_on_commit` and `ralph_loop.lint` are removed. Neither gated
  anything — the git hook was never implemented and the loop has no lint step —
  so both were switches that told an operator they had configured something
  (#1519)

### Changed

- **A runtime toggle no longer rewrites the committed team config.** Moving the
  concurrency slider, applying a dashboard recommendation, resetting a setting
  to the project tier or letting the startup `max_concurrent` migration run all
  used to rewrite `.nightgauge/config.yaml` — stripping its header comments and
  blank lines and leaving every checkout permanently dirty, one `git commit -a`
  away from publishing a personal preference as team policy. Runtime and UI
  writes now target `.nightgauge/config.local.yaml`, or `~/.nightgauge/config.yaml`
  for a machine-tier key; the team file changes only from an explicit user
  action that names it, and that write now round-trips through the YAML
  document so untouched keys keep their own comments. `nightgauge forge auth`
  likewise writes `github_auth.token` to the machine tier instead of the
  committed file (#1516)

- The knowledge base is **on by default**. `knowledge.enabled` now resolves to
  `true` when unset, so a repo with no `knowledge:` section scaffolds PRDs and
  decision logs at issue pickup instead of logging `knowledge.enabled=false and
knowledge_path is null` on every run. Every layer that read an absent key as
  "off" — the Go resolver, the SDK scaffold guard, the VS Code command guards
  and the skills' shell readers — now reads it as the default; only an explicit
  `knowledge.enabled: false` opts out, and the two reasons to (repo footprint
  and per-run token cost) are documented beside the flag. A feature that adds
  value to a workspace defaults ON — see
  [ADR-020](docs/decisions/020-value-adding-features-default-on.md) (#1513)

### Added

- `configs/config.example.yaml` — every shipped default written out with the
  reason for it beside the value. Copying it changes nothing; it exists so an
  operator can see what they are running under without reading the source, and
  `TestDefaultsAgree` pins all 44 values in it against the resolvers, so it
  cannot drift into fiction (#1518)
- `docs/CONFIGURATION.md` gains two reference tables that did not exist:
  **What ships on, and what it costs** names the per-run cost or repository
  footprint of every on-by-default gate — thirteen of which appeared nowhere in
  the reference at all — and **Off by default, and why** gives every off switch
  its one-line reason. The reason is also written into the Go struct or Zod
  JSDoc where a switch has one, so it is next to the code that reads it (#1518)

- Six defaults now ship on that were off for a reason that had expired:
  eval-advice routing and router auto-tune (`model_routing.use_eval_recommendations`,
  `auto_tune`), cross-project complexity transfer, multi-repo knowledge
  aggregation, Codex session resume, and gate relaxation for docs-only and
  config-only changes. Each was defaulting off for a rollout or a migration,
  neither of which is the repository-footprint or per-run-cost reason the rule
  allows. `project.sync.enabled` gets a written default (`false`) and a docs
  section — it previously had neither (#1519, ADR-021)

- A shipped default now has one value. `TestDefaultsAgree` in
  `internal/config` reads the extension's `DEFAULT_CONFIG`, the
  `nightgauge config init` template and the `docs/CONFIGURATION.md` reference
  tables and fails when any of them disagrees with the Go resolver, so the
  next divergence is a red build rather than a surprise in someone's
  workspace (#1517)

- A run's size now comes from three sources, not one: the issue's `size:*`
  label, then the size the run's own feature-planning stage assessed, then the
  complexity estimator — with `size_source` recorded beside it, and
  `planner_size` kept even when a label won so a disagreement stays measurable.
  Twelve of fourteen runs on 2026-09-06 recorded no size at all, and every one
  of them had already assessed one, so cost calibration was learning from almost
  nothing (#1515)
- When feature-planning finishes and the issue carries no `size:*` label, the
  planner's assessed size is applied to the issue — one REST call, no model
  spend — so the next run of that backlog is routed from a real size instead of
  the router's default. An existing label is never overwritten, agreeing or not
  (#1515)
- `nightgauge autonomous status` now answers whether a VS Code reload is safe:
  `Stopped — 2 pipeline(s) still running (#313 flutter, #1429 platform); reload
is not safe yet`, and `Stopped — 0 running; safe to reload` once they land.
  Stop lets in-flight slots finish; a reload aborts them — and "Stopped" alone
  read exactly like "safe to reload" at the moment it was not. The count comes
  from the run registry, so manually picked-up runs are included, and
  `--json` carries it as `running_pipelines` (#1511)
- `nightgauge autonomous clear-failures <owner/repo#N>` (or `--all`) lifts the
  per-issue lifetime failure cap that quarantines an issue. It was reachable
  only from the IPC method and a VS Code command, so an operator on a headless
  host could not release a quarantine without editing `state.json` by hand. It
  clears on the live scheduler when a daemon is up and rewrites the state file
  when one is not, and `autonomous status` now lists every issue's counter as
  `n/2` and marks the ones at the cap (#1487)

### Fixed

- The Action Center's mutating verbs can no longer hang. `attention resolve`,
  `ack`, `mute` and `unmute` blocked forever over IPC — an established socket,
  a daemon that logged nothing, and no working way to resolve a card from the
  CLI or the sidebar — while `list` and `show` stayed instant. The store ran its
  transition listeners inside the per-directory lock, and the daemon's listener
  writes `attention.event` to the extension's stdio pipe: a peer that stopped
  draining that pipe blocked the write, and the write held the lock every writer
  in every nightgauge process serialises on. Listeners now fan out on their own
  goroutine after the lock is released, the lock itself has a bounded acquire,
  and every mutating IPC method carries a deadline and returns a real error
  (#1539)

- A card the platform mirror rejects for a schema reason is quarantined instead
  of retried forever. One card was re-pushed and re-rejected on 931 consecutive
  sweeps for a two-character `lifecycle.resolved.actor`; a validation verdict is
  a pure function of the payload, so it is now reported once with the offending
  field named and dropped from the sweep until the card's content changes. The
  matching minimum is enforced locally at the moment a resolution is recorded,
  so an unsyncable card is never created (#1539)

- A deterministic issue-pickup reports its phases. The stage's primary path is
  the extension's `ContextAssembler.generateDeterministicContext` — no LLM, so
  no skill phase markers, so nothing ever reached the phase tracker — while
  `PHASE_REGISTRY["issue-pickup"]` declares 14 phases. Every run therefore
  rendered `0/14` for the stage's whole life and `14 unreported` the moment it
  succeeded. The path now reports the five registry waypoints it actually
  performs (`validate-environment`, `issue-selection`, `issue-analysis`,
  `blocked-dependency-gate`, `write-context`) as start/complete and the
  remaining nine as `skipped` with the reason "deterministic pickup path", so
  the tree shows live progress and settles at 14/14 with zero unreported rows.
  This is the TypeScript half of the reporter #1247/#1398 gave the Go
  deterministic runners; `PHASE_REGISTRY` now documents which of the three
  producers — skill markers, the Go reporter, the TS deterministic path —
  serves each stage (#1534)
- `pr-create` no longer waits for CI, and is no longer killed for doing so. Its
  Phase 3.5 ran `nightgauge ci wait --timeout 15` right after opening the PR and
  sat on it, emitting no commit, file, phase marker or tool call the whole time
  — so every clock the progress-runaway monitor has went cold precisely while
  the stage was behaving correctly. One run was terminated at 930s with its PR
  open and running checks, reported as "PR creation failed", charged a lifetime
  failure and re-dispatched. Phase 3.5 is now a single non-blocking snapshot of
  the check rollup recorded into `ci_monitoring` (`final_status: pending` while
  checks run); pr-merge already owns CI polling and auto-fix, and the
  deterministic Go pr-create runner never waited either (#1531)
- A failure report no longer claims "PR creation failed" about a PR that exists.
  When pr-create is killed after Phase 3.6 verified an OPEN PR, the report names
  and links that PR and describes a post-create stall, so the operator is not
  sent to re-create work that already shipped. The orchestrator now records the
  verified PR number on the pr-create failure path as well as the success path,
  which is what made the distinction visible (#1531)
- A re-dispatched issue that already has an open pipeline PR now resumes at
  pr-merge instead of re-planning. The #500 fast-forward only fired when
  pipeline state remembered pr-create completing, and a re-dispatch satisfies
  neither half of that — the new slot's state is empty while the reused worktree
  still holds `pr-{N}.json` naming the open PR. One re-dispatch spent $2.11 and
  16 minutes re-running issue-pickup through pr-create to rediscover a PR it had
  opened 43 minutes earlier, and pushed more commits onto it. An open PR
  recorded on disk is now proof enough on its own (#1531)
- Refinement now runs in extension (IPC) mode. It had no execution path there at
  all — the daemon builds its scheduler without a CLI adapter, so every cycle
  logged `disabled: no IPC dispatcher registered` and every dispatch went
  unrefined, while the product default said refinement was on. It now crosses
  the same stage bridge every pipeline stage uses, and each refinement writes
  one line naming its tier and source (#1529)
- `refinement_max_concurrent` bounds concurrent refinements, not handoffs. The
  slot is held until the refine skill exits in every mode, so a failed
  refinement is recorded as a failure instead of being labelled refined (#503)
- The Slack notification switch's comment said it was off "so an existing config
  without this block is unaffected" — a backward-compatibility note, which is
  not one of the reasons an opt-out may exist. It is off because it needs a bot
  token and a channel, and now says so (#1518)

- Nineteen configuration keys shipped a different default depending on which
  surface you asked. `platform.telemetry.enabled` was documented as opt-out
  and shipped off; `pipeline.ci_timeout` was 10 in the extension and 300 in
  the docs, in different units; `pipeline.max_concurrent` was 1 in one place
  and 3 in two others; `pull_request.auto_merge` defaulted on against a
  documented off; `knowledge.auto_prune_on_merge` was read by the pr-merge
  stage but stripped by the extension's schema. Each now has one value stated
  once. `autonomous.safety_rails.budget_ceiling` and `health_gate_min` are no
  longer described as "0 = unlimited/disabled" — they always resolved to
  500000 and 30, and they now resolve through `config.ResolveSafetyBudgetCeiling`
  and `config.ResolveHealthGateMin` so writing the block to tune one rail no
  longer zeroes the others (#1517)
- `model_routing.mode` and the top-level `feedback_loop.*` thresholds had
  defaults only on the TypeScript side, so a CLI-only workspace with no config
  block routed and monitored differently from the same repository opened in
  VS Code. Both now resolve in `internal/config` (#1517)

- An Action Center card no longer asks for a decision without showing what is
  being decided. Every card naming a repo and an issue (or PR) now leads with
  `Open in browser` — the link is derived when the producer set none — and a
  `View details` entry opens the card's reason, its options' consequences and,
  for an architecture-approval card, the run's plan file, without resolving
  anything (#1509)
- The dependency scanner recognises the board field's own spelling of the
  keyword — `blockedBy`, `blocked-by`, `blocked_by`, `dependsOn`, `depends-on`,
  `depends_on`, with or without surrounding backticks or bold markers — and a
  keyword now claims every repo-qualified reference in its sentence, not just
  the one next to it. An issue whose body said "`blockedBy` owner/repo#N"
  produced no edge and was dispatched over an open cross-repo blocker (#1505)
- A planning stage that refuses an issue blocked on someone else's open work now
  ends the run `blocked` instead of halting the repository. The feature-planning
  skill mandates one shape for an open prerequisite — a `PLAN_REVISION_NEEDED`
  signal with no backtrack target, a `blocked-on:` evidence marker and no plan
  file — and `readFeedbackSignals` dropped exactly that shape before the fork
  written to handle it could run, so a correct refusal was booked
  `premature_turn_end`: a public failure comment, a repo-wide autonomous halt,
  and the issue reverted to Ready to be dispatched and convicted again. The
  reader now keeps a blocking signal carrying an external-blocker marker
  regardless of its type or target, through the same single marker definition
  the fork uses, and the run leaves the blocked finding, issue comment and
  Action Center card behind. The permanent `owner-action` park stays gated on
  the signal type, so a blocker that will clear does not park the issue for
  good (#1504)
- A dependency keyword in an issue body now claims only its own sentence rather
  than every `#N` to the end of the line, and every keyword on a line is
  honoured rather than only the first. A line reading "Blocked by Epic #295"
  followed by a second sentence of prose that mentions Epic #301 declares one
  dependency; the scheduler was reading the prose reference as a hard edge and
  holding two ready issues — and, through the epic cascade, their siblings —
  indefinitely. A semicolon in front of another reference still separates one
  list, so `Blocked by #1187; #1190` is unchanged (#1502)
- A `Part of #N` parent link under a `## Dependencies` header is no longer read
  as a dependency on the parent epic. The same-repo dependency parsing added in
  #1492 treated every line in a dependency section as a declaration, and real
  issue bodies put the epic membership line there — so a child issue blocked on
  its own epic, which never closes before its children, and the epic cascade
  spread the deadlock to every sibling in the wave. Parent, `Sub-issue of`,
  `Child of`, `Tracks` / `Tracked by`, `Related`, `See also` and the closing
  keywords are excluded on the same grounds, while a dependency that merely
  mentions one of those words in its description still gates (#1497)
- Worktree containment no longer blames a stage for a branch ref that moved
  under a still-untouched checkout. `git status` is relative to HEAD, so a raw
  `git update-ref` on a checked-out branch makes the whole commit delta read as
  dirt nothing wrote — which killed three concurrent slots in three different
  repositories for one identical 31-path "breach" none of them committed. The
  baseline now records each repo's HEAD, subtracts exactly what a HEAD move
  explains, and warns with both SHAs; anything the move does not explain is
  still attributed. A repo another running slot owns is warning-only too.
  `ResetLocalBranchToRemote` — the ref writer that caused it — refuses
  `main`/`master`, refuses a branch another worktree holds, and moves ref and
  tree together when the caller's own checkout is the holder (#1499)
- A `feature-validate` stage waiting on a long external process is no longer
  killed for waiting. The runaway monitor now counts a declared child process
  that is still alive, byte growth of a declared progress log, and a tool call
  still in flight as activity that defers the kill and suppresses the churn
  detector. A Playwright suite mid-run cost one downstream issue two kills at
  ~$1.20 each, and another stage was killed at 810s with its own `git commit`
  still running — the commit counted as progress the moment the call was
  issued, then the window expired underneath it. Stages declare a child with
  `NIGHTGAUGE_PROGRESS: {"pid": …, "log": …}`; deferral is capped at 20 minutes
  so a wedged child cannot make a stage immortal, and a loop that declares
  nothing is still killed at the window (#1488)
- The `blocked` label now actually stops autonomous dispatch: it joins
  `owner-action` in the default `autonomous.exclude_labels` set and in the
  required-label registry that `nightgauge label ensure` provisions. The
  label's own description says "the scheduler skips it" and nothing
  implemented that, so an operator applied it, believed the issue was held,
  and the next scan dispatched it anyway (#1492)
- A dependency declared in an issue body without a repo token — "Depends on:
  #1187", "Blocked by #1187", a list under `## Dependencies` — is now a real
  scheduler edge, and lands in `dependencies.blockedBy` at pickup. Only the
  repo-qualified spelling ("Depends on: platform #535") was parsed before, so
  the scheduler was stricter about a dependency in another repository than
  about one in its own: an issue whose prerequisite was still open dispatched,
  and feature-planning discovered it mid-plan by reading prose the scheduler
  had ignored. A `#N` in narrative prose is still not a dependency (#1492)
- feature-planning has a worked example for the case above — an open
  prerequisite found during planning — so the stage emits a blocking signal
  with the `blocked-on:` evidence marker instead of researching the convention
  in another repository until its turn ends (#1492)
- A stage the scheduler itself killed — an operator pressed Stop, or a cancel
  tore the run down — is recorded as `operator_stop` instead of a bare pipeline
  failure, and is exempt from the issue's lifetime failure cap and from the
  cascading-failures breaker. Two stages in flight when Stop was pressed exited
  143 with nothing a classifier could read, charged their issues a failure
  each, and with one unrelated failure tripped the fleet breaker — halting the
  workspace the operator had only asked to pause (#1487)
- A cascade of pipeline failures that all share one terminal kind is charged to
  the pipeline, not to the issues: the lifetime failure counters are left
  untouched and the increments already made inside the window are refunded.
  Three issues across three repos reached 2/2 on a stage-gate defect that had
  already been fixed and shipped, and the fix could not reach them. Failures of
  differing kinds still count exactly as before (#1487)
- A run halted for a human — the architecture-approval gate, or a stage
  declaring an issue not pipeline work — now stays halted. The autonomous
  rescan re-admitted every still-open failed item, which is exactly what an
  issue awaiting a person looks like, so an approval gate was re-dispatched
  three seconds after raising it and spent 17.6 minutes and $4.25 on a
  production-touching change nobody had approved. Failed items now carry their
  terminal kind, and a human-decision kind is released only by the action its
  message names: the `approved:architecture` label or approval file, or an
  explicit `autonomous resume`. Ordinary failures are still retried, and state
  files written before the field loads unchanged (#1486)
- A completed feature-dev run whose deliverable is missing `build_verification`
  is now derived from git rather than failed, when the stage worktree holds real
  changes — the same repair #1076 already applied to an absent handoff, stamping
  `handoff_source=derived` and `build_verification.status=unverified` so
  feature-validate runs the suite for real. A docs-only run that changed five
  files and ran its build clean was failing here, reverting its issue to Ready
  and halting the whole repository behind it. A clean worktree, and a recorded
  build failure, still fail exactly as before (#1482)
- The deliverable policy no longer stamps a contract version onto a document
  whose required objects it has not checked: a `build_verification` recorded as
  free text under `quality_checks.build` is named untrustworthy rather than
  silently certified, because mapping prose to a build verdict is the inference
  the closed rule table forbids (#1482)
- Run records from the autonomous / extension pipeline now report the route the
  run was actually taken under and the stages it actually skipped. Every record
  written through `pipeline.notifyComplete` carried a hard-coded
  `routing.path: "standard"` and an empty `skip_stages`, so a trivial-route run
  that correctly skipped feature-planning was recorded as a standard run that
  skipped nothing — a record that contradicted its own trace and made a
  fast-tracked run read as a bug (#1484)
- The scheduler reads the issue context's routing decision from
  `routing.suggested_route`, the key the schema defines. It decoded
  `routing.path` — a key no producer writes — so the value was always empty and
  every run record it wrote reported the standard route (#1484)
- `scripts/check-changelog.sh --extract` reads the root changelog only and no
  longer requires the extension changelog to exist — a single-changelog
  repository's first release run failed at its own changelog gate because its
  extract call omitted `--extension none` and the readability check applied to
  every mode (#1473)

## [0.2.3] - 2026-09-05

The first release cut under the changelog contract: every entry below was
written for the reader of this release, the tag was refused until this section
existed, and these notes are the GitHub Release notes verbatim. 100 commits
since 0.2.2.

### Added

- **The changelog is part of the release.** This file is reconciled with the
  three shipped releases (it had read "no release has been cut yet" through all
  of them), `scripts/check-changelog.sh` enforces that every released tag has a
  dated section here and in the extension's changelog, `release.yml` refuses a
  tag without one and publishes the section as the GitHub Release notes, and
  the same script and gate are adopted across the workspace's repositories
- **One scheduler per workspace.** The serve claim is a lease: a second
  `nightgauge serve` in the same workspace attaches to the running scheduler
  instead of starting a rival, and a wedged holder is detected and replaced
  (#1349)
- **The GitHub API ledger is always on** and bounded, read by the status bar,
  `nightgauge api-usage` and the attention sweep; `api-usage --budget` prices
  a full board read against the hour's remaining GraphQL quota before a bulk
  pull spends it (#1347, #1428)
- **Knowledge base provenance and portability.** Every knowledge entry carries
  one frontmatter contract (Go and TypeScript agree on it); stages stamp
  provenance and a trust tier, `knowledge stamp` writes it, `knowledge
validate` enforces OKF conformance pre-merge, recall and metrics weigh trust
  tier, status and `stale_after`, the VS Code tree shows the tier, `index.md`
  and `log.md` replace `README.md`, and `knowledge export --okf` resolves
  wiki-links into a portable bundle (#1365, #1366, #1367, #1368, #1369, #1370,
  #1371)
- **The pipeline watches `main`'s own run after it merges** and raises a card
  when the default branch goes red — the PR check was a prediction, this is
  the observation (#1249). `scripts/post-merge-check.sh` scripts the same
  question for operators, and refuses to call an empty check list green
  (#1038)
- Deterministic stages report their own phases live and in their result,
  instead of jumping from 0/14 to 14/14 skipped; the `pr-create`/`pr-merge`
  CLI route does the same (#1247, #1397)
- `nightgauge doctor` flags any process on the machine whose working directory
  is inside a pipeline worktree — including a worktree git has already removed
  — the leak that blocks a later `git worktree remove` (#519)
- `scripts/ci-critical-path.sh` ranks a CI run's jobs and steps by wall clock
  and each step's share of the critical path (#1218)
- The extension is published to Open VSX alongside the VS Code Marketplace,
  from the same on-demand publish run (#1316)
- A new terminal kind, `git_transport_auth_failed`, so a push that died for
  want of credentials is booked as what it was, not as agent misbehaviour
  (#878)
- The clean-install end-to-end gate provisions its throwaway repository under
  a configurable owner and captures VS Code's output-channel logs as evidence
  (#1324, #1330)

### Changed

- The feature-planning context's `complexity_assessment.size_label` is the
  planner's own assessed size and is always populated. It was documented as
  "Size label from issue", which told the skill to echo a label back — so a
  label-less issue wrote `null` and the size the planner had plainly reasoned
  about was never recorded (#1515)
- The "this run cannot calibrate the pre-flight cost estimate" warning fires
  only when a run has no size from any of the three sources. It used to fire
  whenever an issue had no `size:*` label, i.e. on nearly every run (#1515)
- The autonomous refinement scan now refines issues in **dispatch order**
  instead of oldest-first: board `Ready`, then board `Backlog` with a Priority,
  and only then the rest of the open backlog. Issues that can never dispatch —
  an `owner-action`/`blocked` label, `In progress`/`In review`/`Done` on the
  board, an open linked PR, or a hold awaiting a human — are skipped, and tier 3
  never takes the hourly rate rail while higher-tier work is unrefined. The
  dispatch scan also refines the issue it is about to enqueue when a refinement
  slot is free, and dispatches it unrefined with a log line when none is. The
  new `autonomous.refinement_backlog` (default `false`) is the cost opt-in for
  sweeping the backlog at all — a 165-issue backlog is ~17 hours of model calls
  on issues that may never run (#1514)
- `nightgauge serve` shuts down through a bounded drain: in-flight board
  writes get a grace period, then a bounded cancel, instead of being abandoned
  on SIGTERM (#489)
- The autonomous refinement scan rotates its starting repository each cycle,
  so the same repo no longer consumes the whole budget every time (#502)
- The serve claim registry prunes its dead records on start, and every record
  names its workspace (#1426)
- Paused or corrupt snapshot protection is bounded by one `SnapshotRetention`
  in every workspace — a CLI-only workspace can no longer protect a worktree
  forever — and `worktree sweep` names which arm protected each issue (#443)
- The Pause/Resume Pipeline commands act on the live run, offering a picker
  when more than one is live, instead of always targeting the singleton (#423)
- Board reads: the dependency graph and `board.counts` come from the daemon's
  snapshot cache rather than live queries, and the rate-limit gate releases
  with jitter so waiting processes stop re-firing in lockstep (#1343, #1344,
  #1346)
- The `doctor` token-scope check and the extension's GitHub auth pre-check
  accept fine-grained and GitHub App tokens (#1331, #1333)
- `@nightgauge/sdk` is marked private until an npm launch is decided (#1314)
- CI: the mirror drift self-test builds its fixture once instead of sixteen
  times, and the two Go test passes run concurrently inside one job (#1218)
- Orchestrating sessions pick a subagent's model by the shape of the task;
  the rule is recorded in `CLAUDE.md` (#1381)

### Fixed

- **Licensing:** `license validate` carries the machine binding, and a
  `MACHINE_LIMIT` rejection is reported as a full seat rather than "key not
  accepted" (#1334, #1454)
- **Attention / Action Center:** the store's critical section holds across
  processes and every writer stages at its own temp path, so two processes
  materialising one card no longer tear each other's bytes (#1425); an action
  is attributed to the GitHub login, not the OS username (#1418); an operator
  steer reaches disk before its verb runs, and is written where the run reads
  it (#1407, #1410); a persisted card can no longer carry a shape the platform
  rejects (#1405); a misrouted relayed resolve is named and contained rather
  than mistaken for a rejection (#1421); the CLI verb executor honours its
  context and sweep verb failures are recorded (#1449, #1450, #1451);
  default-branch health raises an FYI card when only non-required checks fail
  (#1250); event-driven sweeps are gated behind the board change probe (#1345)
- **Orchestrator:** one scheduler config builder, `onCycleComplete` fires on a
  graph failure, and a local `EACCES` is no longer classified as a forge
  permission error (#1445, #1446, #1447); the `pr-merge` runner resolves its
  forge client per run (#1396); a graceful-stop CLI that exits 0 keeps its
  failure text (#564); the registration wait shares the file's poll budget
  (#1453); `autonomous run` has a stage adapter, so the CLI-only dispatch
  branch can execute (#1336); a BLOCKED PR with zero check runs waits for CI
  instead of being called a dirty merge state (#1027); every detached
  goroutine in `wave_orchestrator.go` and `epic.go` is pinned to an allowlist
  (#491)
- **IPC:** the `autonomous.*` lifecycle handlers report the state they
  observed instead of the one a 50 ms sleep assumed (#494); `BindSocket`
  probes before it unlinks, so a second daemon cannot steal a live socket, and
  listen-readiness is a synchronous contract (#1158, #1429)
- **Run history:** the append's own retention prune no longer deletes the
  record it just wrote (#1455); a terminal failure before any stage persists
  its reason and a pre-dispatch exit record (#1329); the retro decides the
  terminal kind the record names (#1448)
- **Terminal kinds:** a GitHub throttle is a transient backoff, not a lifetime
  failure (#1391); every gate failure names its kind instead of classifying as
  `subagent_crash` (#1237); a CLI-mode failure's stage-exit record carries
  `terminal_kind` and `stderr_tail` (#563); an auth failure no longer
  classifies as a stall (#565)
- **Learning and health:** a retry escalation is not booked as a
  model-routing miss (#1002); the execution-history feeder populates
  `selectionSource`, making the model-routing dimension reachable (#461); a
  dimension with no data no longer votes in the overall score (#1197)
- **GitHub:** `nightgauge run <issue>` finds Ready issues again — `GetItem`
  filters the board by `repo:<owner>/<repo> #<N>` (#1337); the board scan
  detects label truncation and the dispatch exclusion fails closed (#998);
  Projects V2 "temporary conflict" mutations are retried (#1328);
  `SummarizeWindow` tells "no header" from a genuine cached `remaining: 0`
  (#1452)
- **VS Code:** the last write-then-rename sites use a unique temp name (#786);
  a timed-out webview render still disposes its panel (#1327); the webview
  message-handler gate covers arrow-property handlers and fails closed on
  unparsed forms (#1199); the dashboard firewall badge reflects the resolved
  sanitization mode (#986); the slot output channel is redacted at its sink
  and the CI evidence artifact is scrubbed (#1335)
- **CLI:** the scope-drift and version-downgrade gates receive the `--repo`
  config backfill (#548)
- Audit-filed sub-issue and epic bodies satisfy the required-heading contract
  (#1116)
- The publication-boundary ceiling is read from the mainline, not from
  whatever the diff is pinned to, so a long-lived branch stops measuring
  against a stale ceiling (#1291)
- CI and scripts: the skill-metadata validator's field checks cannot be broken
  by an early-exiting reader (#1442); the change-class test no longer races
  `grep -q` against its writer (#1290); a transient upstream 5xx is not a dead
  link (#1404); the post-merge green-wait says what it is waiting for and how
  to skip it (#1414); the clean-install gate builds on amd64, uploads its
  hidden run directory, and removes its 2 GB image on every exit path (#1325,
  #1326, #1456)

### Security

- `golang.org/x/crypto` to v0.56.0 (GO-2026-6354, GO-2026-6355) (#1323)
- `fast-uri` 3.1.5 → 3.1.7, four high advisories transitive via `ajv` (#1317)
- Post-release audit of the release path: publish on the tag ref only, hardened
  workflows, checklist trued up (#1321)
- Scheduled LLM workflows split into a read-only model job and a model-free
  write job (#1304)

## [0.2.2] - 2026-09-02

The first build published to the VS Code Marketplace (pre-release channel) and
to Open VSX. Same product as 0.2.1; the listing and the package were fixed
before publishing.

### Changed

- The VSIX no longer ships source maps or type declarations (23 MB and 1,000+
  files smaller) and drops the plugin mirror's test files (#1302)
- Listing metadata: an `AI` category, `extensionKind: workspace`, and explicit
  untrusted-/virtual-workspace declarations (#1302)
- README is the Marketplace page: real dashboard and notification renders
  replace the hand-built mockups; contributor sections moved to the repository
  (#1302)
- Release pipeline: one Marketplace publish path (`marketplace-publish.yml`,
  on demand, on the tag ref), 0.x packaged as pre-release, a guard that no
  source maps ship, dead workflows removed, timeouts and concurrency on every
  workflow, CodeQL for Actions, Docker in Dependabot (#1299, #1303)

### Fixed

- `marketplace-publish` no longer binds the tag-only `production` environment,
  which refused to run it (#1299)
- Scheduled LLM workflows: read-only model job, model-free write job (#1304)

## [0.2.1] - 2026-09-02

First public version — GitHub Release with the Go binary for macOS (Apple
Silicon and Intel) and Linux x64, Homebrew cask, and per-target `.vsix` files.
There is no Windows build yet. The Claude Code (`claude` CLI) adapter is the
supported path; the Codex, Gemini, Copilot and direct-API adapters are beta.
Multi-repository workspaces and autonomous mode are available but less finished
than the single-repository loop.

`0.2.0` was tagged on 2026-09-01 but never published (see below). `0.2.1` is
that tree plus the release-pipeline fix (#1296).

### Added

- **Action Center** — repo-scoped attention sweep surfacing decision requests,
  default-branch health, human gates, and terminal failures as cards you can
  act on, with standing-condition semantics and fingerprint-based auto-resolve
  (#98, #99, #103, #189)
- Approve the architecture gate directly from an Action Center card (#181)
- A coverage-gap card can add the missing repository to the workspace in one
  click instead of being a dead end (#728)
- **Slack notifier** — bot-token based, with live-updating run messages, a
  settings UI and a `Configure Slack` command; Go-side alerts reach Slack
  through the same sink (#1081, #1082, #1088)
- **Adapter usage & quota** — a status-bar meter (click to cycle windows) and
  a dashboard panel showing every usage window, per-model breakdown, burn rate
  and projected exhaustion (#685, #694, #698)
- Claude Max plan usage shown as percent used and reset time — the footer
  reports 5-hour and 7-day limits rather than dollars — and the operator can
  declare their Claude plan (#710, #734, #819)
- Opt-in, tiered reporting of adapter usage; telemetry is now opt-out by
  default with the disclosure moved alongside the setting (#737, #739)
- **Workspace Repositories** section in Nightgauge Settings, backed by
  `nightgauge workspace repo add|remove|list` (#715, #729)
- Notifications config block now has a settings surface (#1097)
- Repository-aware project settings — per-repository configuration in
  multi-repo workspaces, with drift detection between layers (#47)
- `max_model` caps automatic model routing per stage (#1216)
- Grok Build CLI adapter (beta) (#529)
- Adapter Doctor probes each CLI adapter's model catalog, flags a stale
  bundled binary, and reports whether every stage can run at all (#509, #602,
  #608, #867)
- `nightgauge --version`, `nightgauge api-usage` (a GitHub API request
  ledger), `nightgauge label rename`, and `nightgauge check-triage` verbs
  (#725, #857, #904, #1265)
- A **Show Diagnostics** command; the six output channels are consolidated
  into one with a durable log sink (#761, #1068)
- Halted repositories are badged in the tree, and unchecked repositories are
  no longer polled (#1236)
- Stage-exit forensics — every stage records its last ten Bash commands, exit
  codes and stderr tail, so a failed run can be diagnosed without re-running
  it (#149, #158)
- Test-execution evidence — `feature-validate` must show a suite actually ran
  before it passes (#176, #1264)
- Scheduled automations that stop firing are noticed and reported (#1005)
- Backlog groom skill for periodic validity, worth and priority re-assessment
  (#614, #1111)
- A guided first-run onboarding webview (`nightgauge.showGettingStarted`)
  walks a new install from claiming an issue to a merged PR; it opens once per
  install in a workspace that is not yet initialised
- `nightgauge forge` — a forge-agnostic command surface (`auth whoami`,
  `repo view`, `graphql`, and the issue/PR/board verbs) that the skills call
  instead of `gh`; fifteen skills migrated, `IB_FORGE=gitlab` works end to
  end, and a lint gate refuses new direct `gh` calls
  ([ADR-008](docs/decisions/008-skill-forge-cli.md))
- Slash-command contract
  ([ADR-007](docs/decisions/007-slash-command-skill-invocation-contract.md)):
  every command file opens with a banner that invokes its skill,
  `nightgauge preflight skill-banners` enforces it, `spike validate` rejects a
  spike without a recommendations block, and epic creation must declare its
  decomposition path
- GitHub native sub-issues: epic progress is read from the sub-issues API,
  with body-text references as the fallback; see `docs/SUB_ISSUES.md` (#38)
- The `feature-dev` quality review runs six parallel review subagents —
  documentation, performance and accessibility alongside code quality,
  security and tests (#10)
- Supply-chain hardening of the release path: every GitHub Action SHA-pinned,
  an SPDX SBOM per release archive, a generated `THIRD_PARTY_NOTICES` in the
  VSIX and every archive, and a valid SPDX license identifier (#136)

### Changed

- The Claude Code adapter is documented as the supported path; other adapters
  are marked beta, and the Quick Start now matches what a Marketplace install
  actually does (#903, #1140)
- Ready Issues view defaults to a smart sort (Priority → Unblocked → Size →
  Age) instead of board order
- Model pricing, effort tiers and thinking interlocks come from the model
  registry alone; the extension's own pricing table is gone (#97, #415, #437)
- Cost forecasts are priced through the serving adapter's provider, and every
  billable token pool (including cache reads and writes) is counted (#393,
  #588, #721)
- Per-stage cost estimates are calibrated from run history rather than
  rescaled proportionally (#114, #241, #1226)
- A stage that reports an issue is not pipeline work exits with a finding
  instead of a failure (#1164, #1242)
- A terminal failure halts only the repository it happened in, not the whole
  machine (#1166)
- A chronically failing issue is quarantined instead of pausing the fleet, and
  transient provider outages back off with a retry ceiling rather than
  tripping the circuit breaker (#197, #201, #211, #284)
- A killed stage's commit is preserved and the run resumes at `pr-create`
  instead of restarting (#209, #269)
- GitHub polling pauses while views are hidden and the window is unfocused;
  board reads are shared across repositories on the same project and gated
  behind a cheap change probe (#496, #859, #922, #1098)
- Worktrees, local branches and stashes the pipeline creates are reclaimed on
  every terminal outcome, with squash merges detected by content rather than
  ancestry (#118, #215, #334, #587)
- Writes that escape the run's worktree are captured and attributed instead of
  silently landing in a sibling repository (#138)
- Autonomous dispatch refuses issues in the running binary's own repository
  and gates on author trust (#272, #310)
- Runaway detection no longer kills a stage that is still making progress, and
  a stage waiting on a tool call is not killed for waiting (#133, #162, #1086)
- Settings follow a documented seven-tier precedence chain
  (`defaults → global → project → local → runtime → env → cli`) with a
  Team / Machine / Runtime placement guide in `docs/CONFIGURATION.md`

### Fixed

- Open GitHub and Open Log tree actions do what they say, and tree item ids
  are stable across refreshes (#1211, #1280)
- The settings webview's dead buttons work again, it no longer nags about
  setup on an uninitialized repo, and it reports its real auto-accept default
  (#905, #1067, #1117)
- Knowledge view finds the knowledge base in every worktree layout, and
  Related Decisions shows real decisions (#1224, #1225)
- Empty, blocked and awaiting-decomposition epics are told apart in the tree,
  and a label-only epic with no sub-issues is visible at all (#671, #681)
- Adapter auth failures are surfaced instead of halting the queue (#1172)
- Merges past failing non-required checks now say which checks they passed
  (#1252)
- Phases the pipeline never observed are no longer reported as skipped, and a
  failed run stops reporting Health 100 / Excellent (#1062, #1251)
- Stage kills terminate the whole process group, not just the direct child
  (#1256)
- Token and cost telemetry is recorded on every stage-exit path, including
  Codex-routed stages and the run's terminating stage (#69, #113, #116, #187)
- The local branch survives a successful merge, and a branch is never called
  safe to delete while a worktree holds it (#167, #640, #1044)
- Forked branches are detected before a run starts rather than at push time,
  and pushes go through the git CLI so SSH remotes work (#164, #880)
- Standing attention cards stop re-syncing on every sweep and stay open when
  their resolve verb fails (#247, #1245)
- Discord and Mattermost embeds report honest labels, totals and cost, with no
  dangling branch arrow (#408, #678)
- Board membership is keyed on (repository, issue number), and a post-merge
  rollup that could not add the board row now repairs it (#145, #813)
- Run records have a single authoritative writer, ending duplicate and
  cross-contaminated history entries (#143, #827)
- Both assistant tool-call delivery shapes are handled by one code path, fixing
  dropped prompt detection and missing command capture (#170)
- Health snapshots and retros are attributed to the repository whose run
  produced them (#1063, #1232)
- The epic checkpoint can be configured and actually fires; a parent epic is
  resolved against its own repository (#1000, #1184)
- The release run resolves the triggering tag, not the release-candidate tag
  on the same commit (#1296)

### Removed

- The Workflow dashboard view (#1225)
- The `stage_overrides` knob, superseded by `max_model` (#1216)
- The configurable `*_env` credential fields and dead sanitization keys (#987,
  #1112)
- The `useGoBinary` setting, the queue-reorder surface, and the Focus Mode
  ROI comparison — none of which did anything (#974, #979, #981)

### Security

- Credentials moved into VS Code secure storage
- Output channel and configuration inspection are redacted; merged settings
  stay local
- Configuration paths that escape the workspace are rejected
- Resolved Go and JavaScript CodeQL findings, including two polynomial-ReDoS
  regexes, and cleared high-severity npm advisories (#72, #317, #320)

## [0.2.0] - 2026-09-01

Tagged but never published. The release run resolved the release-candidate tag
on the same commit and stopped before attaching any artifact (#1296). The tree
shipped as [0.2.1]; nothing was released under this version.

## [0.1.0] - 2026-01-15

The first internal build of the VS Code extension, before the repository was
public; never tagged here. Pipeline orchestration sidebar, Ready Issues view
with GitHub Project board integration, dashboard, context file viewer, and
the first set of commands and settings. Recorded so the extension's changelog
and this one name the same versions.

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/nightgauge/nightgauge/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/nightgauge/nightgauge/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/nightgauge/nightgauge/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nightgauge/nightgauge/compare/v0.2.3...v0.3.0
[0.2.3]: https://github.com/nightgauge/nightgauge/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/nightgauge/nightgauge/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/nightgauge/nightgauge/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nightgauge/nightgauge/tree/v0.2.0
