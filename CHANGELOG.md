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

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v0.2.3...HEAD
[0.2.3]: https://github.com/nightgauge/nightgauge/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/nightgauge/nightgauge/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/nightgauge/nightgauge/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nightgauge/nightgauge/tree/v0.2.0
