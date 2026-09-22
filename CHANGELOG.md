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

### Added

- **`scripts/ci-local.sh --changed`, an opt-in change-scoped fast path (#1985).**
  Derives the changed path set from `git diff --name-only origin/main...HEAD`
  plus the working tree and, when nothing in it can reach Go, skips
  `go test ./...` and `go test -race ./...` — 194s and 202s of a 10m31s gate
  measured 2026-09-22 on an idle 12-core Apple M-series, about 63% of it, on
  diffs neither pass can observe. Default behaviour without the flag is
  unchanged, and the flag will not become the default: the rule is still "run
  the complete local gate once before every push". A skipped step is a THIRD
  state alongside passed and failed, in the same spirit as #1983's
  `INFRASTRUCTURE ERROR` — the summary names it with its reason, calls the run
  a PARTIAL gate, and the verdict reads "every check that RAN passed" rather
  than "all checks passed". It still appears in `--list-steps`, so the #983
  step-inventory guard sees the same inventory scoped or not; a step may be
  skipped loudly, never silently deleted. The Go decision is keyed on GENERATOR
  INPUTS rather than file extensions, all derived from the tree: `*.go`,
  `go.mod`/`go.sum`, any file inside a directory holding a tracked `.go` file
  (`internal/terminalkind/table.json`), any `//go:embed` target resolved
  against its declaring package (`internal/adaptercompat/manifests/*.json`),
  and the inputs and outputs of the `go run ./cmd/...` recipes in the
  `Makefile` plus the path defaults in the generator sources — which is how a
  diff touching only `packages/nightgauge-vscode/src/services/IpcClient.generated.ts`
  still runs the Go suites. It fails closed: a missing `origin/main` or an
  empty derivation runs everything. `go build ./...`, `gofmt`, the
  generated-file drift checks, the changelog contract, the publication boundary
  and the credential scan always run. PR CI and `main`'s post-merge run are
  untouched and stay full-scope, so a local miss costs a CI round trip rather
  than a bad merge. `scripts/test-ci-local-changed-scope.sh` is the contract's
  regression suite (37 assertions, in `ci-local.sh` and `lint.yml`), and the
  race step's cost comment now carries both measurements with their dates and
  machine class — the `#428 / #493 / #1218` decision to run the race pass
  whole-tree is unaffected, and the re-measurement confirms its +6% figure.

- **`nightgauge skill render --profile compact` and pr-merge's own compact
  profile (#1654).** A stage skeleton — phase markers, gates, the Input
  Contract and the deny rules — with everything else turned into on-demand
  `Read` directives at `<skillDir>/_profiles/compact.md`, instead of the full
  `_shared`/`_includes` content a full render inlines or references. Omitting
  `--profile`, or passing `--profile full`, renders byte-identically to
  before; a stage with no compact profile falls back to full with a warning.
  `skills/nightgauge-pr-merge/_profiles/compact.md` is the first consumer:
  pr-merge's full render is ~21.8k estimated tokens (over the ADR-023 budget
  at a 32768-token window) and its compact render is ~2.8k, well under it,
  while keeping the `--admin` prohibition and the post-merge build-check text
  verbatim. `internal/skillrender/budget.go` gained `DecideProfile`, the
  compact half of ADR-023's dispatch-outcome order (full → compact → refuse);
  the model-swap re-route hop stays #1645's own. `scripts/evaluate-skills.ts
--render-profile full|compact` prepends the rendered skill to each
  scenario's prompt and tags the recorded JSONL with `render_profile`, so
  #1660-#1664 can compare profiles on the existing scenarios. The plugin
  mirror and the VSIX marketplace bundle both ship the new `_profiles/` tree.

- **OpenCode stages get liveness-aware phase inference, adapter-aware stage
  timeouts, and dispatch-time local-endpoint readiness (#1646).** Timing
  assumptions previously came from Claude speeds: a local model's slow-but-
  healthy decode (research measured a 76s cold prefill and ~8 tok/s on a
  local Qwen3.8 27B) could read as a stall, and nothing refused a stage whose
  local server was down or model unloaded before spawning it. `PhaseInferer`
  now reads OpenCode's `tool_use` events (`bash`/`edit`/`write` → the same
  Bash/Edit/Write phase rules Claude's shape drives), `ResolveStageTimeout`
  is keyed by stage, adapter AND model — an OpenCode local-provider stage
  gets a ×3 factor capped at 4 hours instead of the Claude-tier family scale
  — and the scheduler probes the SPECIFIC local endpoint (LM Studio, Ollama,
  or a declared `opencode.endpoints[]` entry) a stage is about to dispatch
  to before creating anything, refusing as `network_unavailable` (the
  endpoint does not answer) or `model_unavailable` (the model is not
  loaded, or `opencode.limit.context` is unset or larger than the loaded
  window) rather than letting a broken local environment spawn and land as
  a generic `subagent_crash`.

- **Spike #1650 measured OpenCode's server mode, and three of its four questions
  came back negative.** `docs/spikes/1650-opencode-server-mode-warm-serve-run-attach-http-permission.md`
  records the evidence; ADR-022 is amended and its § 15 disposition table now
  reads `non-goal` for `json_schema` output, the GitHub agent and ACP, with
  `serve`/`run --attach` still deferred. The governing finding: an attached
  `run` uses the **server's** configuration, permission map, XDG isolation and
  session database, not its own — so a warm server shared across stages would
  collapse every stage into one identity, one credential set and one
  transcript, while a server per stage saves less than it costs to boot. An
  HTTP permission approver does work (3 ms, over the v1 event and reply pair),
  but it cannot give the model a denial reason, cannot recover a pending ask
  after a restart, and lets an unanswered ask hang forever instead of failing
  closed. `json_schema` output makes a session's transcript unreadable on
  opencode 1.18.31. Plain `opencode run` still opens no TCP listener, so
  ADR-022 § 18 stands.

### Fixed

- **A `network_unavailable` readiness refusal no longer feeds the cascading-
  failure breaker (#1989, AC4 gap in #1646).** The failure handler in
  `internal/orchestrator/autonomous.go` is a sequential if-chain, one block
  per exempted `terminalFailureKind`, each returning before
  `cascadeTracker.RecordFailure`. `TerminalKindModelUnavailable` had a block;
  `TerminalKindNetworkUnavailable` did not, so a local endpoint (e.g. LM
  Studio) being unreachable at dispatch time fell through and was counted
  like an unclassified pipeline failure — a flapping or briefly-sleeping
  endpoint could trip the breaker and halt autonomous mode. The new block
  schedules a retry, like `model_unavailable`, but on the shorter
  `stallKillBackoff` (30m) already used for other short-lived local/infra
  blips rather than the hour-long `streamIdleTimeoutBackoff` sized for a
  remote rate-limit window: an unreachable local endpoint routinely clears on
  its own within minutes, unlike an unloaded model or plan-tier rejection.
- **`scripts/ci-local.sh` could exit non-zero having named nothing, and now
  cannot (#1983).** Three gates running at once in three worktrees produced a
  red "Mirror drift gate regression suite" whose log held 16 assertions, every
  one of them a pass, under the summary line "(no recognised failure marker —
  see the log above for detail)". Two defects combined. The summary's marker
  grep was ANSI-blind — `^[[:space:]]*(×|✗|…)` against
  `  \033[31m✗\033[0m <desc>`, so no coloured failure in any suite in this
  repository had ever matched it — and a grouped step whose child was killed
  under load never wrote an exit code, which the group runner turned into a
  plain `exit 1`, the same shape as a check that asserted false. The reporting
  helpers moved to `scripts/lib/ci_local_failures.sh`, strip ANSI first, and
  print the log's last 20 lines when nothing matches instead of describing the
  absence of a message; a step the harness could not run is reported as `!`
  `[INFRASTRUCTURE — the check could not run]` and counted separately, because
  it asserted nothing about the diff either way. `test-mirror-drift-gate.sh`
  now announces every arm, reports the arm it was inside from its EXIT and
  signal traps, holds each arm to a declared assertion count so a partial arm
  is named rather than showing up as a wrong total, exits 2 with a
  `HARNESS ERROR` line when the harness itself cannot run (no temp space, a
  fixture `git` failure) and retries through `index.lock` contention first.
  `scripts/test-ci-local-concurrency.sh` is the new self-test.
- **`scripts/ci-local.sh`'s concurrency budget is machine-wide again, so
  concurrent gates stop oversubscribing the box (#1983).** #855 made the gate
  concurrency-safe and the workspace has relied on "gates run in parallel, only
  merges serialise" since. #1217/#1219 then made the gate internally parallel
  and bounded it with `CI_LOCAL_JOBS=4` PER PROCESS: three gates asked for
  twelve heavy steps — three `go test ./...`, three `-race` passes, three vitest
  runs — on a 12-core box, reached load 58, and children were killed before they
  could record an exit code. `CI_LOCAL_JOBS` now bounds the MACHINE. Slots live
  in one directory keyed on the repository's shared git dir, so every worktree
  of a repository draws on one budget while an unrelated checkout keeps its own;
  a slot is reclaimed only when its owner pid is dead, never on age (#1697's
  cleanup deleted a live sandbox on an age rule), and a release is a no-op unless
  the slot still carries this gate's `owner` stamp — slot paths are numbered and
  reused, so a child that hands its slot back, a second gate that takes the same
  path, and this gate's exit-time sweep are exactly the sequence in which an
  unguarded release deletes another gate's LIVE slot and silently shrinks the
  budget, and only when gates overlap. Gates stay parallel and simply take
  longer together. A
  single-instance lock was rejected: it would answer a resource-accounting bug
  by removing a capability ADR-013 is built on. The gate also reaps its
  concurrent steps' children by pid on interrupt and verifies they are dead,
  after `go test` children were seen outliving the gate that spawned them.

  **Measured on merge, alone on an idle 12-core Apple M-series: `10m5s` wall
  clock at 419% CPU**, of which `go test -race` is `217s` and the plain pass
  `197s` — so the two Go passes are `414s`, **68% of the gate**, and neither can
  observe a TypeScript-only diff (the argument #1985 makes). The new concurrency
  contract suite costs `112s` of that total. `docs/GIT_WORKFLOW.md` claimed one
  machine runs one gate at a time, contradicting #855, and is corrected.

  **Corrected while landing #1985:** this entry originally also called the race
  step's `2m48s -> 2m58s` comment wrong. It is not. That comment states the plain
  pass alone against plain-and-race run CONCURRENTLY, which is how `ci.yml`'s
  "Test (plain and race, concurrently)" step works — `168s -> 178s`, and an idle
  re-measurement the same day got `166s / 174s`. `ci-local.sh` runs the two as
  separate sequential steps, so a gate log shows `194s` and `202s`, which SUM to
  ~396s. Comparing that sum to a concurrent wall clock is what produced a bogus
  "1.8x stale" claim. The `+6%` conclusion stands and the comment needed no
  correction.

- **The SDK's OpenCode run-env allowlist rejected several variables real
  `nightgauge opencode config --json` output carries, so `checkRunConfig`
  would have rejected the verb's own output on every machine, once #1648
  wires it in.** `childEnv.ts`'s `OPENCODE_RUN_ENV_NAMES` was missing
  `NIGHTGAUGE_OPENCODE_PLUGIN_PATH`/`_NONCE`/`_SENTINEL` (the TS twin of
  `opencodeplugin.EnvPluginPath`/`EnvNonce`/`EnvSentinel`,
  `internal/execution/opencodeplugin/plugin.go`), `OPENCODE_DISABLE_PROJECT_CONFIG`
  (a bare string literal `InstallNightgaugePlugin` sets at its call site in
  `opencode.go`, not one of `opencodeplugin`'s exported constants — the reason
  an earlier version of this fix's own parity test, which read only those
  constants, missed it) and `HOME` (the isolated per-run home
  `OpenCodeIsolationEnv` sets, `internal/execution/adapters/opencode_isolation.go`).
  The `HOME` gap was an isolation defeat, not just a rejection: `SYSTEM_ALLOW`
  already passes an _inherited_ `HOME` through to every opencode child, so
  without a run variable able to override it, a future looser run-config
  provider would have left the operator's real `HOME` in place and let
  OpenCode read the operator's own `~/.opencode` — exactly what ADR-022 § 8
  isolation exists to prevent. All four are now allowlisted and forwarded.
  `NIGHTGAUGE_OPENCODE_PLUGIN_PATH`/`_SENTINEL` are also now checked to be
  absolute paths inside the run's own root, matching
  `opencodeplugin.SentinelPath`'s formula, since the plugin's own init writes
  the sentinel file at this path verbatim (`fs.writeFileSync`).

  `NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK` is accepted by `checkRunConfig`
  too, via a second set (`OPENCODE_RUN_ENV_WITHHELD_NAMES`) consulted only
  there — refusing it would fail `CONFIG_INVALID` closed on any machine where
  an operator's `$HOME/.opencode` happens to be unsatisfied, a condition the
  operator neither sets nor controls. It is never forwarded to the child:
  `curateOpenCodeChildEnv` still applies only `OPENCODE_RUN_ENV_NAMES`, the
  TS twin of the Go adapter's own `BuildCommand`, which deletes this same name
  from the child's env right before returning it (`opencode.go`, pinned by
  `TestOpenCodeBuildCommandWithholdsOperatorInstallRiskFromTheChild`) — #1802's
  child-env leak is already closed there; only the config verb's _printed_
  `env` still carries the name, since the verb prints `RunRoot.Env` directly
  rather than `BuildCommand`'s output.

  `isOpenCodeChildEnvAllowed` (the _inherited_-environment path, used for a
  nested SDK spawn) now also denies every `NIGHTGAUGE_OPENCODE_`-prefixed
  name, not only `OPENCODE_`-prefixed ones: without this, a nested opencode
  dispatch would have inherited its parent run's plugin path, handshake nonce
  and sentinel path from `process.env`, letting a nested child write to the
  parent run's own sentinel file, instead of minting its own.

  A Go/TS parity test now derives its expectation from the real verb's
  output rather than from `opencodeplugin`'s constant definitions:
  `TestOpenCodeConfigVerbEnvKeysMatchGoldenFixture`
  (`cmd/nightgauge/opencode_test.go`) pins the exact `env` key set a
  reference invocation of `nightgauge opencode config --json` prints against
  a checked-in fixture
  (`internal/execution/testdata/opencode_config_verb_env_keys.golden.json`),
  and `childEnv.test.ts` reads the same fixture to assert every key is
  accepted by the TS allowlist — closing the blind spot that let both
  `OPENCODE_DISABLE_PROJECT_CONFIG` and `HOME` through a constants-only parity
  test undetected (#1804, refs #1648).

- **ADR-022's "subagent cost" gap read as a live risk; it is currently
  unreachable.** § 3's watchdog passage, its § 15 `Subagents (task)` row, and
  the `openCodeUnenforcedControls` "subagent cost" warning
  (`internal/execution/adapters/opencode.go`) described a stage's subagents
  as able to spend past its cost budget before the watchdog's settle-time
  check catches it. `gates.js` denies every `task` tool call unconditionally
  as AC9's fallback, so no subagent session can start at all today — zero
  dollars of subagent spend is possible through that path. The docs now say
  so, and a new test
  (`TestNodeHarnessDeniesTaskRegardlessOfCostBudget`) pins that the denial
  holds regardless of a cost budget (#1748).

- **The rate-limit gate was never installed on the CLI path.**
  `WithRateLimitTracker` was called in three places, all inside
  `internal/ipc`, so the shared tracker was a daemon-only mechanism. Every
  one-shot process — `nightgauge run` and all six pipeline stages, the
  post-merge hooks, `issue route` — built a client with a nil tracker, and a
  nil tracker makes the gate return "not gated" at its first line. Those
  processes were never held before a call and never wrote a reading back, so
  they learned about exhaustion only by taking a 403. Every CLI constructor now
  attaches the machine-wide tracker and waits out a reset rather than failing an
  in-flight issue. This is the wiring the other two fixes depend on.

- **The tracker conflated the core and graphql budgets into one slot.** GitHub
  bills REST and GraphQL separately, each with its own remaining count and
  reset second, and the response-header interceptor wrote whichever pool
  answered last into a single per-user slot. Core is almost always the
  healthier pool, so a REST reply routinely erased the GraphQL exhaustion the
  gate existed to see — measured at 20:11Z on 2026-09-21: `graphql remaining=110`
  and `core remaining=4988` in the same window, one number stored. Entries are
  keyed `(user, pool)` from tracker version 2 (v1 entries are dropped on read),
  the pool comes from GitHub's own `X-RateLimit-Resource` header, and each gate
  consults the pool its call will actually spend. A `gh` subprocess, whose
  shape is not known before it starts, consults the more constrained of the two.

- **The machine-wide GitHub rate-limit gate was dead code for most of its
  wall-clock life.** `headroomGate.resetWait` discarded any tracker reading
  older than 15 seconds as "no data" and opened the gate — so a budget already
  measured as exhausted was spent into anyway by every short-lived CLI process
  and by every producer that runs between the attention sweep's bursts.
  Freshness and expiry answer different questions: staleness costs confidence
  in _how much_ is left, but none in whether the window has _reset_, and a
  below-floor reading can only move further down before it does. `ResetAt` is
  now the authority for a below-floor entry, and freshness is required only
  when the entry carries no reset to reason about. Measured on 2026-09-21: the
  tracker had not been written for 7h44m while the account exhausted its
  GraphQL quota twice.

- **One account's budget was tracked under two keys, and each saw half the
  spend.** The IPC server wires the shared tracker with an empty user (which
  collapses to `default`) while the per-repo resolver and the per-user clients
  wire it with the resolved gh username. Both spend one pool, so both gates
  believed roughly twice the real budget remained and neither ever observed
  the other's exhaustion. `SharedRateLimitTracker.GetBudget` now reports the
  most constrained entry sharing the caller's reset window — the same second
  means the same account — and the scheduler's three headroom reads use it.
  `Get` is unchanged for callers that genuinely want their own key.

### Added

- **A stage that cannot fit its model's context window is caught before
  spawn, not after a provider overflow.** `nightgauge skill render
--context-window N` reports estimated tokens, the stage's ADR-023 budget,
  and a verdict (exits non-zero over budget; unchanged, byte-identical output
  without the flag). At dispatch, the Go scheduler now runs the same check:
  when the resolved model's window is known and the rendered stage exceeds
  its share, it re-routes once to the largest-window model on the same
  provider, or refuses with a `context_window_exceeded` reason naming the
  stage, estimated tokens, window and share. `AutoProviderRouter` scores the
  `opencode` adapter by the actually-resolved model's window instead of a
  static 32k placeholder. See
  [ADR-023](docs/decisions/023-model-aware-context-budgets.md) (#1645).

- **The OpenCode adapter is documented across the four adapter reference
  docs.** ADAPTER_GUIDE.md, ADAPTER_MATRIX.md, ADAPTER_DOCTOR.md and
  ADAPTER_ERROR_HANDLING.md now cover its Experimental status, its
  per-feature disposition table (ADR-022), its doctor rows, and its terminal
  failure kinds and remediations — and fix several pre-existing Grok
  omissions in the same files, including a missing deep-dive section
  (#1649).

## [0.4.6] - 2026-09-21

### Changed

- **Every binary now carries a vendor identifier.** Avast's clean software
  guidelines ask that "every executable file should contain a vendor
  identifier". On macOS that is carried by the Developer ID signature, but the
  linux build is unsigned and carried no attribution beyond the Go module
  path. `main.vendor` is now linker-injected on every target and survives
  stripping, and `nightgauge version` reports it. The version stays alone on
  the first line so existing parsers are unaffected.

- **The listing documents complete removal.** VS Code removes the extension
  and its bundled binary, but not `~/.nightgauge`, `~/.config/nightgauge` or
  per-repository `.nightgauge/`. The README now says exactly what to delete
  and states that nothing is installed outside those paths — no services, no
  login items, no changes to other applications or system settings.

- **The Marketplace listing now names the publishing entity.** Avast's clean
  software guidelines require software to "clearly identify the product vendor"
  and "how to contact this entity". The listing named only the handle
  `nightgauge`, while the shipped macOS binary is signed
  `Developer ID Application: Edibu LLC (RZJPN7Y7BG)` — two unexplained names
  for anyone comparing the signature to the publisher. The README and the
  extension manifest now name Edibu, LLC, give a contact address, link the
  Terms alongside the Privacy Policy and licence, and state the signing
  identity with the commands to verify it.

### Added

- **The RC dry-run scans its artifacts too.** `staging.yml` was the one release
  path that did not run the ClamAV gate, which is backwards: the RC is the
  build a release is promoted from, so catching a problem there costs a retag
  rather than a withdrawal.

### Changed

- **`docs/ARTIFACT_VERIFICATION.md` now cites VirusTotal's own verdict on the
  executable.** VirusTotal unpacks archives and scans contained files as
  separate reports, so the v0.4.4 `.vsix` and the Go binary inside it have
  different verdicts: the container scored 6/66, the executable `d6855c8d…96c2`
  scored **0/66**, with every engine that flagged the container — Avast, AVG,
  Avira, WithSecure, Ikarus, Cynet — reporting the binary `Undetected`. That is
  a container-level false positive, and it means Apple, GitHub and VirusTotal
  independently agree the artifact is clean.

### Added

- **Every release artifact is malware-scanned before it is published or
  attested.** v0.4.4's `darwin-arm64` .vsix was flagged on VirusTotal (6 of 66
  engines, all generic heuristics) after it reached the Marketplace. The
  artifact was clean — Developer ID signed, Apple-notarized, provenance-attested
  — but nothing in the release path scanned the packaged artifact, so the first
  scan anyone ran was the Marketplace's. `scripts/malware-scan.sh` now runs
  ClamAV over the per-target .vsix files and the built binaries in both
  `release.yml` and `marketplace-publish.yml`, before attestation and before
  publish, and writes a JSON report as a run artifact.

  The gate is deliberately not `clamscan`'s exit code. `clamscan` exits `0`
  both for a clean artifact and for one it never read: measured on the real
  .vsix, `--max-filesize=1M` reports `Data scanned: 0 B` and still exits `0`.
  The scan therefore also asserts a floor on bytes actually scanned, and fails
  closed when the floor is not met. Size limits are raised for the same reason
  — the .vsix already scans to 77.40 MiB against ClamAV's own 100 MB default
  `--max-scansize`, so the scan was within 23% of silently skipping content.
  `scripts/test-malware-scan.sh` covers all of it, including an arm that
  asserts the real `clamscan` still emits every summary field the script
  parses, so the stubbed arms cannot drift from the tool they imitate.

- **The npm dependency tree is scanned.** `govulncheck` covered the Go module
  graph only, while the extension bundles its entire npm tree into
  `dist/extension.cjs`. `npm audit` now gates CI at high severity — the side of
  the supply chain where a hijacked package is far likelier than in a Go
  binary. The tree is currently clean at every severity.

### Changed

- **Go builds are reproducible.** `-trimpath` is now set in both the Makefile
  and `.goreleaser.yml`, which previously disagreed: GoReleaser trimmed by
  default and the Makefile — which builds the binary bundled into the .vsix —
  did not, so one commit produced two differently-built binaries. Published
  binaries embedded 1198 strings containing the build machine's paths; they now
  embed none. This is what lets a third party rebuild a tag and compare hashes
  rather than take our word that an artifact is clean.

### Fixed

- **`pr-merge` no longer hands a PR to the LLM because GitHub had not finished
  thinking.** GitHub computes a PR's mergeability asynchronously and answers
  `UNKNOWN` for the first seconds after the PR is created; `pr-merge` starts
  seconds after `pr-create`. That absent verdict was read as a negative one
  twice over — the decision function punted on it at its first test, and the
  bounded CI wait, which also requires a known-mergeable PR, never got its turn
  at all. The run then paid an LLM to babysit CI to green, the largest single
  line in a run, for work the deterministic path does for free. The runner now
  waits a short, separate budget (20 s) for the verdict to land, and if it never
  does it punts `mergeability-unresolved` rather than reporting a conflict it
  never observed. A real conflict is still punted immediately.

- **Phase gap-fill now actually records anything.** #1926 derived live phase
  progress from phase ordering, and on the path that runs pipelines from VS
  Code it produced not one record: `SkillRunner.runStage` builds the stage
  runner's callback object as an explicit property list, and nobody added
  `onPhasePassed` to it, so every ordering-derived event was dropped between a
  producer and a consumer that were both correct. A run measured after the fix
  shipped still showed the distribution the fix existed to remove — 11 of
  `feature-planning`'s 14 phases and 14 of `feature-dev`'s 18 arriving in the
  end-of-stage burst as `unreported`, with zero `passed`. The callback is
  forwarded at both relay sites, and a test now derives the phase callbacks the
  runner invokes and fails if a relay site does not forward every one of them.

- **A release build no longer fails on a passing test suite.** The `v0.4.5`
  release workflow failed with 13,875 tests passing and one failing — in a
  teardown, not an assertion. `skillRunner.worktreeContainment.test.ts`'s first
  case ended by emitting `close` without waiting for what that emit starts: the
  containment comparison shells out to `git` in every repo under the test's
  temp directory, and `afterEach` removed that directory while those children
  were still walking it, so `fs.rmSync` threw `ENOTEMPTY`. The case now awaits
  completion like the other five. Confirmed by instrumenting the teardown
  rather than by rerunning until green: before the fix the first case reached
  `afterEach` with the containment work unsettled and the other five did not;
  after it, all six settle first.

## [0.4.5] - 2026-09-20

### Fixed

- **`pr-merge` reported a generic "dirty merge state" instead of the actual
  blocker whenever a PR also had a failed check or a missing review.**
  `Decide()` tested `MergeStateStatus != CLEAN` before it scanned the
  status-check rollup for `FAILURE`/`ERROR`, but GitHub never reports
  `MergeStateStatus` as `CLEAN` once a required check has failed — so the more
  specific `failed-ci-checks` reason was unreachable in production and every
  real CI failure surfaced as `dirty-merge-state: BLOCKED`, the catch-all
  reason reserved for a block the deterministic path could not diagnose more
  specifically. That misattribution sent at least one run down the paid LLM
  path to babysit CI that the bounded CI wait (#297) already handles for free.
  `Decide()` now checks the failed-check rollup and blocking review before the
  merge-state catch-all, so the most specific blocker is reported; no PR's
  merge/no-merge verdict changes. `pr-merge` also now logs the full deciding
  snapshot (state, mergeable, mergeStateStatus, reviewDecision, per-check
  conclusions) at every punt the CI wait and `Decide()` govern, so a punt's
  cause is provable after the fact instead of inferred from a single reason
  string.

- **`issue-pickup` stopped failing on a branch that existed the whole time.**
  The pipeline's first stage failed more often than it succeeded (21 ok / 28
  failed), and every failure was the same shape: the stage exited 0, the branch
  was created in git, and the gate found `"branch": ""` in the context file. The
  skill assigned `BRANCH_NAME` in its branch-creation phase and read it back
  several phases later — in a different shell, and a shell variable does not
  survive between tool calls, so `${BRANCH_NAME:-}` expanded to nothing. The
  orchestrator then escalated to a larger model and paid for the same stage
  twice, which fixed nothing, because a second model loses a variable exactly as
  reliably as the first. Two changes: the skill's context write now re-derives
  every field in its own block from a source that survives (the issue number and
  repo from the process environment, the branch from the worktree's own `HEAD`,
  the issue content from one fetch) and fails loudly instead of writing a blank;
  and the orchestrator recovers an empty `branch` from the worktree's `HEAD`
  after the stage exits and before the gate reads the file. The recovery is
  deliberately narrow — it never overwrites a value the skill did write, and it
  stamps nothing unless `HEAD` is on this issue's own branch, so a stage that
  really created no branch still fails the gate. The same lost shell also left
  the title, body, labels and acceptance criteria empty in every affected
  context file; those are fixed by the same rewrite.

- **77% of this workspace's GraphQL spend was invisible to the API ledger and
  is now recorded and throttled.** Two gaps. First, every `gh` subprocess the
  binary runs — post-condition gates, recovery actions, non-terminal
  reconcile, survival detection, the deterministic pr-merge stage — drew from
  the same budget while writing no ledger record and never waiting on the
  rate-limit headroom gate, so a stage could drain the window the next stage
  needed. Those calls now go through one instrumented helper that pays the
  gate and records the call with its real calling frame, the resource that
  actually moved, and the budget observed right after it. Second, the `serve`
  daemon resolved its ledger path against the process working directory while
  `--workspace` only ever changed a string: a daemon started by the extension
  (which spawned it with no `cwd`) wrote its records to the wrong place, or —
  because the default path only opens inside an existing workspace — nowhere
  at all. The workspace root is now authoritative for the ledger path, the
  extension starts the daemon in the workspace it serves, and the extension's
  project-board writers route their GraphQL through the daemon's instrumented
  client instead of `gh api graphql`, falling back to the subprocess only when
  no daemon is connected. `nightgauge doctor` gained a `ledger_daemon_coverage`
  arm that reports a live, working daemon whose calls are not reaching the
  workspace ledger.
- **Pipeline phases are now visible while they are worked, instead of only at
  landmarks.** On a representative run, 10 of `feature-planning`'s 14 phases
  and 14 of `feature-dev`'s 18 never appeared until the end-of-stage back-fill
  stamped them `unreported` — so a forty-minute stage showed four updates and
  then a burst. Live reporting had rested on two signals that could not cover
  every phase: markers a skill emits voluntarily (edit-heavy stages routinely
  skip them) and landmark inference rules covering ~5 indices of 18. Progress
  is now derived from **ordering** as well: phases are an ordered list, so
  observing phase N is evidence the run moved past every phase below it, and
  those are reported at that moment rather than at stage end. Five landmarks
  now yield continuous progress across all 18 phases.
  - They are recorded as **`passed`**, a new status, never as `complete`. A
    passed phase was never seen to run; only ordering places the run beyond
    it. It is stronger than `unreported`, which claims no ordering at all, and
    weaker than `complete`, which carries evidence. A progress display must not
    manufacture completions it cannot defend.
  - **`nightgauge run` renders phase progress at all.** The data had been in
    the run's `PhaseHistory` the whole time with nothing to print it, because
    only the IPC server registered the phase callbacks — so outside VS Code a
    long stage was indistinguishable from a hung one. Phase lines go to
    stderr, leaving stdout's machine-readable contract intact.
  - Go and TypeScript were changed together and pinned by mirrored tests, the
    drift class that already cost #1247 and #1398.

- **`nightgauge issue route` cost 375 GraphQL points per call and now costs 4.** Measured against the live API, before and after, with byte-identical
  output. It paged the entire project board — every status, `first: 100` a
  page, ~19 pages — to read two fields off one row and discard the rest. The
  API ledger recorded 209 requests from that one call site in a single day
  across 11 invocations: **~4,125 points, when the whole hourly GraphQL
  budget is 5,000**. Fourteen routing calls in an hour exhausted the account,
  which is the shape of the rate-limit exhaustion this workspace has been
  hitting. It now uses `BoardService.GetItem`, the server-side-filtered
  single-item read that `RunQueue` already used for exactly this reason
  (#908). Same change fixes a second bug on the same line: it matched on
  issue number alone, so on a board shared by several repositories two
  issues at the same number collided and whichever page arrived first won.

- The VS Code attention sweep no longer runs on a timer just because the
  editor window has focus. A sweep is ~64 GraphQL points plus 18 REST calls
  across a six-repo workspace, and the timer's only condition was window
  focus — so reading unrelated code cost ~256 points an hour, per open
  window, with autonomous mode off and no pipeline run in flight. It was
  observed sweeping six repositories while the account was already
  exhausted, absorbing the timeouts and retrying. The timer now also
  requires the autonomous dispatch loop to be running. Every other trigger
  is unchanged — activation, an explicit repository-view refresh, a run
  terminating, and focus regained after idling — so the inbox still fills
  for anyone actually looking at it.

- `skills/_shared/AUTO_SELECTION.md` pulled the whole project board with a
  raw `gh project item-list` and no `--limit`, inside a structure documented
  as used by all seven selection tiers, with critical-then-high retries on
  two of them — up to fourteen whole-board pulls for one pickup, and
  invisible to `nightgauge api-usage` because a `gh` subprocess never
  reaches the ledger. It now reads the board once through
  `nightgauge board list --status Ready --json` and runs every tier as a
  `jq` filter over that one payload. The "all issues blocked" path reuses
  the same payload instead of pulling the board a second time.

- `skills/_shared/REPO_IDENTITY_CHECK.md` spent a GitHub API call per stage,
  per repository, asking the CLI for a repository slug that
  `git remote get-url origin` answers locally for free.

### Changed

- The no-direct-`gh` gate now scans the files a stage actually executes.
  Its glob was `skills/*/SKILL.md` — exact filename — so `_includes/` and
  `_shared/`, where every expensive direct call in the tree lived, were
  never opened; a skill inheriting five `_shared` files full of `gh` calls
  passed clean. It also now reads only fenced code and skips comments,
  because the previous scope flagged prose: it was reporting two findings on
  `main` that were both Markdown table cells, and `_shared/CI_GATE.md`
  spends two lines _forbidding_ a hand-rolled `gh pr checks` poll. A gate
  that cries wolf gets allowlisted until it means nothing.

  `scripts/lint-skills/no-direct-gh.sh` was a second, independent
  implementation of the same rule and had drifted to the same blind spot; it
  is now a thin wrapper around the Go gate. The allowlist accepts a
  repo-relative file path as well as a skill name, so a single reviewed file
  can be exempted with its reason recorded instead of waving through a whole
  skill. `TestSkillNoDirectGH_RealTreeIsClean` runs the gate against the
  real `skills/` directory, the way `TestPlatformRawHTTP_RealPackageIsClean`
  does — the gate previously had only synthetic temp-dir fixtures and was in
  neither `ci-local.sh` nor `lint.yml`, so nothing had ever pointed it at
  the tree it guards.

- `nightgauge api-usage` no longer bills a caller for points it cannot prove
  it spent. `cost` is a delta of an account-wide counter against a
  per-process baseline, so a call made after a quiet spell absorbs whatever
  every other process, the `gh` CLI, and any other session on the token
  spent meanwhile. That is how a one-point `node(id:)` read came to be
  reported at 4,638 points, and why investigations into this workspace's
  exhaustion kept arriving at an innocent function. Records now carry
  `since_prev_ms`, costs are charged to a caller only when the previous
  observation was within five seconds, and everything else is reported as an
  explicit UNATTRIBUTED total with a note on where it comes from. Ledger
  caller attribution also stops naming `sync.(*Once).doSlow` and
  `runtime.goexit`, which accounted for 695 of 9,435 records (7.4%) in one
  workspace.

### Added

- `docs/ADAPTER_MATRIX.md`'s OpenCode egress section records the CI run that
  proves it: run 35514658308, 2026-09-20, `non-loopback attempts: 0`. The
  field had been left as `PENDING` by #1644's own PR even though the run
  executed and passed on that PR's head, so the issue's Verification section
  was unmet on `main`. Found by independent verification, not by CI.

- The machine-tier `opencode:` config accepts an `endpoints[]` list, so an
  operator can declare more than one named local model server instance (two
  LM Studio servers, LM Studio beside Ollama, or any OpenAI-compatible
  server) with its own id, readiness check and capacity (#1678). Each
  endpoint gets its own complete OpenCode provider block;
  `nightgauge doctor --adapters` prints one readiness row per endpoint (id,
  kind, reachable, whether the model is loaded, and declared slots); an
  endpoint's `base_url` never leaves the machine tier and is redacted from
  every captured run output. A non-loopback endpoint requires
  `allow_lan: true` and a private-network address.
- `scripts/check-agent-guidance.sh` now fails when the routing file has no
  `decisions/` route, and, when `.nightgauge/config.yaml` sets
  `knowledge.enabled: true`, when it does not mention `.nightgauge/knowledge/`.
  Every repository passed the check while four routing files lacked one of the
  two. Smart Setup's routing template emits both routes.
- A Linux network-namespace + `strace` CI job
  (`.github/workflows/opencode-egress.yml`,
  `scripts/opencode-egress-check.sh`) verifies zero non-loopback egress for a
  real OpenCode dispatch against a local-provider stub, plus a macOS
  `sandbox-exec` recipe (`scripts/opencode-egress-macos.sb`) for an operator
  to repeat the same check against a real local model server (#1644). The
  result is recorded in `docs/ADAPTER_MATRIX.md`.

- OpenCode routing is now provider-aware (#1643). A usage-cap fallback walk
  (`pipeline.adapter_fallback_chain`) skips an `opencode` candidate
  configured against the same account that just capped the run, instead of
  treating every `opencode` candidate as an unrelated provider; effort maps
  to `opencode run`'s `--variant <v>` for a model that declares that effort
  rung in `opencode.endpoints[].models[].variants`, and nothing is guessed
  for one that does not; a retry of the same stage on the same model and
  worktree resumes the prior attempt's OpenCode session via `-s <id>`, and
  any adapter, model or worktree change starts a fresh session instead. The
  new `--variant` and `-s` values are validated the same way `-m` already
  is — never a value read from config, a prompt or model output, only one
  the operator declared or this run itself recorded. See the
  `docs/decisions/022-opencode-multi-provider-adapter.md` 2026-09-20
  amendment for the one deviation from plan: `opencode models --verbose`
  is not parsed for declared variants, since no captured fixture of that
  flag's output exists to verify a parser against.

### Fixed

- A genuine v0.3.x `.nightgauge/complexity-model.yaml` — no
  `lines_changed_thresholds`, no `learnings`, no `critical_files` — now loads
  instead of failing validation with "model calibration sections are
  incomplete" (#1918). #1911/#1917 backfilled `lines_changed_thresholds` alone
  and closed only that one field, leaving the same real-world file broken on
  `learnings`, the third instance of this class in one release
  after #1843's `survival` rename and #1911's own gap. Decoding now backfills
  every additive section a document leaves absent — not just the two named so
  far — from `newBootstrapComplexityModel` in a single reflective pass, so a
  future additive required field is covered without a new code change. A map
  section backfills key-by-key (an operator-set size or entry is left
  untouched); any other absent section is replaced wholesale; a
  present-but-invalid value in any section is still rejected, never silently
  replaced. The next save persists the completed document.

- A v0.3.x `.nightgauge/complexity-model.yaml` now loads instead of failing
  validation with `lines_changed_thresholds.XS must be positive` (#1911).
  `lines_changed_thresholds` was introduced in #1592/v0.4.0 and required by
  `validateComplexityModelDocument`, but no migration ever backfilled it for
  files written before that release — the same upgrade gap #1843 fixed for
  the `survival` → `survival_calibration` rename, in the same v0.4.0 release.
  Decoding now backfills each missing size from the bootstrap defaults
  (`XS:100, S:325, M:850, L:1850, XL:2500`), leaving any operator-set size
  untouched and still rejecting a present-but-non-positive value; the next
  save persists the completed block. `RecordOutcome`, `RecordSelfHealEvent`,
  `ApplySurvivalVerdicts` and `nightgauge doctor` all recover without an
  operator having to delete the model and lose its accumulated calibration.

- `internal/github/outcome.go` renamed `prediction_accuracy.survival` to
  `survival_calibration` in #1592 (v0.4.0), and the model decoder's
  `KnownFields(true)` turned that rename into a hard decode error for any
  `.nightgauge/complexity-model.yaml` still carrying the legacy key from
  v0.3.0/v0.3.1 — breaking `RecordOutcome`, `RecordSelfHealEvent`, and
  `ApplySurvivalVerdicts` for every operator upgrading from v0.3.x.
  `predictionAccuracy` now decodes the legacy `survival` key as a fallback
  (preferring `survival_calibration` when both are present) while still
  rejecting genuinely unknown fields, so a v0.3.x model loads again and
  self-migrates to the new key one-way on its next save (#1843)

- A Go-dispatched pipeline stage (the scheduler's normal path and the
  autonomous issue-refine path) now always carries a real USD cost cap
  (#1749). `execution.StageOptions.CostBudget` was already wired all the way
  downstream to the OpenCode cost watchdog and to `--max-budget-usd` on the
  claude / claude_sdk / lmstudio / ollama adapters, but no production caller
  ever set it, so it was always the zero value and none of that enforcement
  ever ran — an operator's `pipeline.token_budget_ceiling.ceiling_usd` (or
  its default of $75) was silently inert for every Go/auto-mode dispatch.
  Both call sites now pass `PipelineBudgetCeilingUSD(workspaceRoot)`.

- The Output window no longer shows an older run's content while a
  CLI-started pipeline is the live one (#586). A `nightgauge run` discovered
  on disk now registers its own Output-window slot alongside its tree slot,
  every slot panel names the issue, repository and run id it is showing, and a
  slot with no stream says so — a CLI run's output goes to its launching
  terminal — instead of leaving the previous slot's content on screen.

- An OpenCode stage no longer waits on an install into an operator's
  populated `~/.opencode` (#1787). Every non-inheriting OpenCode dispatch now
  runs with its own per-run `HOME`, populated with symbolic links to the
  operator's other home-directory state (`.gitconfig`, `.netrc`, `.ssh`,
  `.aws`, `.config`, ...) except `.opencode`, so OpenCode never finds config
  to install against there and never waits, while every other tool the stage
  starts still resolves the operator's files unchanged.
  `opencode.inherit_user_config` is unaffected: it still leaves `HOME`
  untouched entirely.
- A symlink planted inside an OpenCode `external_directory` allow-listed
  directory (`NIGHTGAUGE_SKILL_DIR`, the context/output file directories, or
  the `/tmp`, `/private/tmp` scratch roots) after the per-run permission map
  was generated, pointing outside every allow-listed directory, is now
  refused (#1816). opencode 1.18.30 matches its permission map lexically and
  never resolves symlinks at match time, so a link's own path — not its
  target — was what the allow-list matched; a `read` tool call now runs a
  new dispatch-time gate (`nightgauge hook external-directory-gate`) that
  resolves symlinks against the actual filesystem before deciding, closing
  the gap the config-generation-time-only resolution could not. A symlink
  that resolves to a location inside the worktree keeps working unchanged.
- CI's `go` job no longer silently skips required verification steps —
  including the OpenCode integration regression test — when an earlier step
  in the same job fails (#1817). The Build, Vet, Gofmt, Test, OpenCode
  integration, and `branch-merged-check.sh` regression steps now carry
  `!cancelled()` in their `if:` conditions, matching the job-level convention
  already used elsewhere in this file, so a failure earlier in the job lets
  them run — and fail — instead of reporting no verdict at all. A new
  "Verify required steps did not silently skip" step publishes a labeled
  fast-path/full-path summary to the job's Summary tab and fails the job if a
  required step is unexpectedly skipped. A new deterministic detector,
  `internal/ci.DetectSilentSkipRisk`, flags this same anti-pattern across
  every job in every workflow file and is wired into the pre-merge ruleset
  check as a non-blocking warning.
- The Go/CLI execution path now completes phases correctly for
  `feature-planning`, `feature-dev` and `feature-validate` (#1885). A phase
  start now settles the stage's previously active phase `complete`, matching
  the extension/IPC path's `phaseTracker.ts` semantics; a successful stage
  boundary settles the last active phase `complete` (not `abandoned`) and
  back-fills every phase name the stage never reported as `unreported`, so
  the denominator is the registry total; `abandoned` is now reserved for an
  abnormal stage boundary (non-zero exit). Previously every Go-path stage
  boundary rewrote every still-running phase to `abandoned` regardless of
  outcome, and the completion path used for a CLI-reported native cost
  (`CompleteStageWithCost`, the common case for agentic stages) never
  settled a stray running phase at all — so a successful run could render
  `0/N phases · N abandoned`.

- The extension and CLI no longer leave a repository's primary clone dirty
  (#1875). An older committed `.nightgauge/.gitignore` is no longer replaced on
  activation, which also discarded the repository's own rules; its newer rules
  apply on your machine through `.git/info/exclude`, and the committed file is
  upgraded by pull request. Creating a pipeline worktree no longer appends
  `.worktrees` to the root `.gitignore`, `.gitkeep` files are written only when
  `.nightgauge/` is first scaffolded, and `nightgauge outcome` no longer
  appends to a committed `.nightgauge/.gitignore`. The template (version 13)
  ends with a `Local additions` section that upgrades keep, which is where a
  repository that commits its knowledge tree un-ignores `/knowledge/`.

- A release whose Homebrew cask pull request is never merged is now caught.
  The daily release watchdog compares the cask version on the tap's `main`
  with the latest stable release, fails when they differ, names the cask pull
  request to merge, and keeps one issue open until the tap catches up. The
  release run now closes older cask pull requests as superseded when it opens
  a new one. The tap had served v0.2.3 through seven releases (#1874).

- OpenCode edit hooks no longer discard valid warnings when Node reports `EPIPE`
  alongside a successful child-process status.

- `TestEveryRecordEscalationHasADurableTwin` no longer annotates a passing CI
  run with `##[error]`. Its `t.Logf` message began with a bare `<path>:` token,
  so the rendered line carried two `file:line`-shaped tokens
  (`escalation_durability_test.go:62: scheduler.go: 3 RecordEscalation...`) and
  matched a Go problem matcher, which marked benign diagnostics as errors. The
  test was always passing: the assertion is `appends < records`, and appends
  legitimately run ahead because the two model-unavailable downgrade sites
  append without a `RecordEscalation`. 81 other `t.Logf` lines in the suite are
  unannotated because their messages lack that shape. Reworded rather than
  silencing the matcher, which still catches real Go build errors. Worth fixing
  because a red error on a green run teaches people to ignore error
  annotations.

## [0.4.4] - 2026-09-17

### Added

- The macOS binaries bundled in the VSIX are now **Developer ID signed and
  notarized** by `release.yml`, `staging.yml` and `marketplace-publish.yml`.
  Each release job runs on `macos-latest`, because Apple `codesign` is
  macOS-only, and signs immediately after `make build-all` and before anything
  packages the binaries: the VSIX freezes whatever is in `dist/bin/`, so
  signing afterwards would ship an unsigned copy while reporting success. The
  VSIX verification step then asserts the **packaged** binary carries an
  `Authority=Developer ID Application` line and `flags=0x10000(runtime)`,
  rather than trusting the signing step's own output, because the 0.4.2 Open
  VSX publish printed a success line for a version the registry would not
  serve. Proven on the rc channel first: `v0.4.4-rc.2` signed and notarized
  both darwin targets and the packaged assertion passed for each. The macOS
  runner is free only because this repository is public and `macos-latest` is a
  standard runner, verified against the timing API rather than assumed; a
  private repository bills macOS at 10x the Linux rate.
- `scripts/sign-macos-binaries.sh` codesigns and optionally notarizes the macOS
  binaries that ship inside the VSIX, which until now carried only an ad-hoc
  linker signature, cryptographically equivalent to unsigned. The Marketplace
  security-and-trust guidance states packages are scanned "using the same
  advanced tech found in Microsoft Defender" and rescanned after publication,
  and a ~28MB statically linked Go executable that spawns processes is the
  highest-risk artifact in the package; a Developer ID signature is the
  strongest provenance signal available for it, and it also stops Gatekeeper
  warning users who install from a VSIX or Open VSX. **Not yet wired into any
  release workflow**: it needs a `Developer ID Application` certificate (the
  team currently holds only `Apple Distribution`, which is App Store scoped)
  and a macOS runner, since the release jobs run on `ubuntu-latest` and
  `codesign` is macOS-only. With no credentials set the script exits 0 and says
  so, so it is safe to enable before the certificate exists.
  `scripts/test-sign-macos-binaries.sh` covers ten behaviours with stubbed
  `codesign`/`security`/`xcrun`, including the two that matter most: no
  credentials must be a clean no-op rather than a release-breaking failure, and
  a signature that does not verify must fail rather than be reported as
  success, because a broken signature looks deliberate. Registered in
  `ci-local.sh` and its step inventory.
- `docs/RELEASE_CHECKLIST.md` gains "Signing the bundled macOS binaries": the
  two prerequisites, the six repository secrets, and an enablement order that
  proves signing on an `rc` tag through `staging.yml` before it touches
  `release.yml`, because an unverified change to the release path is what
  produced the partial Open VSX publish on 2026-09-16. Also records what was
  ruled out: symbol stripping is not a meaningful obfuscation signal, since Go
  keeps function names in `pclntab` regardless of `-s -w`, so the release keeps
  `-s -w` rather than paying 12.7MB for nothing.

### Fixed

- The install instructions in both READMEs pointed at a VS Code Marketplace
  listing that returns 404, which was actively misleading: the extension
  README is also the Open VSX listing's Details tab, so Open VSX visitors were
  being handed a dead link as their first install option. Both now lead with
  Open VSX and document the direct `.vsix` path
  (`code --install-extension <file>.vsix`) for VS Code itself, using the
  per-target assets already attached to every GitHub release. No behaviour
  change; the extension was never Marketplace-dependent at runtime.

### Changed

- Reduced the extension's Marketplace trust surface, following the publisher
  block whose stated reason was only "the Publisher Agreement and Terms of
  Use". Two items that are free to remove and not worth the cost of defending
  are gone: `"claude"` is no longer a listing keyword (naming an integrated
  tool in prose is nominative use and stays, but keywords are the
  search-placement vector the policy on others' marks is aimed at), and the
  Grok adapter's setup message no longer contains a `curl … | bash` one-liner,
  which was always just a help string but is a signature static scanners match
  on. `tests/marketplaceTrustSurface.test.ts` guards both: no foreign mark in
  `keywords`, and no fetch-piped-to-shell anywhere in shipped source.

### Added

- The extension README gains "What this extension does on your machine", which
  inventories the bundled Go binary, process spawning, credential use, bundled
  shell hooks, the single out-of-workspace write and its confirmation, and
  every egress path. The listing is what a Marketplace reviewer reads, so the
  answer to "what is this thing doing" now lives there instead of having to be
  asked for.

### Added

- `nightgauge pipeline aggregate` now reports a per-adapter token/cost
  breakdown for every stage, with a new `--adapter` filter and an `unknown`
  bucket for stage entries with no adapter stamp (#1846)

### Fixed

- `TestNoDirectGitSpawnsInTests` and three other module-root `filepath.Walk`/
  `WalkDir` guards (`TestExactlyOneWorktreeIssueParser`,
  `TestEveryProductionSweepCallSiteOpensTheMergedPRDoor`, and
  `TestSourceFilesAreCleanUTF8`) walked into `.ci-local-logs`,
  `scripts/ci-local.sh`'s own scratch-output directory, which the script
  creates and deletes files in concurrently with the `go test` step that runs
  these guards. A file vanishing between the walk's directory read and its
  stat produced a `lstat: no such file or directory` walk error that each
  guard propagated verbatim, failing the test nondeterministically on a diff
  that could not have caused it. All four now skip `.ci-local-logs` and
  tolerate a mid-walk `ENOENT` via a shared `internal/gittest` helper scoped
  to `fs.ErrNotExist`, so no other walk error is silently swallowed (#1856)

- The SDK and both copies of the product-audit skill declared
  `@types/js-yaml@^4` alongside `js-yaml@^5`. js-yaml 5 ships its own type
  declarations, so the stub package described the wrong major and shadowed
  them whenever npm hoisted a js-yaml 4 copy above the workspace. That turned
  `npm run build` red with TS2353 on the `quoteStyle` dump option in
  `ComplexityModelService.ts` and `KnowledgeService.ts`, and failed the
  quoted-`schema_version` serialization test. The stub and its lockfile
  entries are removed, leaving js-yaml's own declarations as the only source
  of its types. The VS Code extension and the root tooling scripts imported
  js-yaml without declaring it, relying on whatever major happened to be
  hoisted; both now depend on `js-yaml@^5.4.2` explicitly (#1853)

- `scripts/install-agent-skills.sh` printed raw `Error:` lines and a false
  "marketplace add failed" warning on every re-run, because `grok plugin
marketplace add` and `grok plugin install` both exit non-zero once their
  target is already configured or installed. The already-installed case also
  never refreshed the plugin, so Grok kept the snapshot from the first install
  while the Claude arm re-copied from the working tree. Both idempotent states
  are now recognized and routed to `marketplace update` / `plugin update`, and
  command output is only surfaced on a genuine failure (#1851)

- A run that succeeded completely rendered in the pipeline tree as mostly
  unreported with a red ✗ on it, so operators investigated runs that had merged
  their PR and closed their issue. Phase records outlived the stage attempt
  that produced them: `BeginStage` un-booked a re-entered stage's result and
  left its phase rows in place, so a second attempt inherited the first one's
  verdicts — `pr-merge/freshness-check` stayed `failed` after the retry merged
  the PR, timestamped 1.3s before the stage instance it was rendered against
  had started. `phaseHistory` now carries the current attempt only, the same
  contract `completedStages` and `stageErrors` already held, and displaced rows
  move to a new `supersededPhaseHistory` rather than being dropped (#1850)
- A deterministic runner that punted to the LLM path recorded the in-flight
  phase `failed`, which is a verdict on work the skill was about to do
  successfully. Punts now record a new `superseded` status, and a phase that
  genuinely did not succeed on a stage that exits 0 on purpose — pr-create's
  `write-context` — records `degraded`. Neither renders as a failure, and both
  keep the attempt's real duration instead of becoming a zero-width
  placeholder. A stage with `exitCode: 0` and empty `stageErrors` can no longer
  carry a `failed` phase (#1850)
- `feature-validate` emitted no phase markers at all — 0 of 23 across a
  four-minute, $0.68 stage — because its markers are standalone `printf`
  commands the model skips, exactly as `feature-dev`'s were before it got an
  inference fallback. It
  now infers phase progress from the tool calls it actually makes, in both the
  Go and TypeScript execution paths (#1850)
- A stage whose phases could not be measured at all now says so instead of
  rendering `0/N`, which put a zero in the numerator position and read as "this
  stage did none of its work". The denominator stays visible so the amount left
  unmeasured is not hidden either (#1850)
- `packages/nightgauge-vscode/scripts/dev-install.sh` exited silently before
  installing when no version of the extension was already present. The
  old-version cleanup globbed with `ls`, which exits non-zero on no match;
  under `set -euo pipefail` that ended the script, and the block's own
  `2>/dev/null` discarded the only diagnostic. The failure was therefore
  invisible — the build and package steps all succeeded and the run simply
  stopped before `==> Installing` — and it could only ever happen on a
  machine with no prior install, which is why it survived. The glob now expands
  into an array, so "no matches" is an empty list rather than an error.
- The same block sorted with `sort -t- -k4`, addressing a fourth dash-separated
  field that does not exist; the version never participated in the sort, so
  cleanup kept whichever directory sorted last lexicographically rather than
  the newest build. Now a plain version sort, which is correct because the
  paths share an identical prefix.

## [0.4.3] - 2026-09-16

### Fixed

- The registry publish steps in `marketplace-publish.yml` no longer leave a
  partial release behind. Both were a bare `for VSIX in *.vsix; do publish; done`
  under `set -e`, so the first failure aborted the step: the `v0.4.2` Open VSX
  publish landed `darwin-arm64`, hit `503 Service Unavailable` on
  `darwin-x64`, and never attempted `linux-x64`. That advertises a version most
  platforms cannot install, and re-running could not repair it, because the
  target that already landed answers with a conflict and aborts the loop again
  at the first VSIX. Both steps now call `scripts/publish-vsix-set.sh`, which
  retries transient registry failures, treats an already-published target as
  success so a re-run completes a partial release, attempts every target before
  failing so the summary is the whole truth, and still exits non-zero naming
  any target that never landed. `scripts/test-publish-vsix-set.sh` covers all
  six behaviours, including the 503-mid-loop case that motivated it.

## [0.4.2] - 2026-09-16

### Fixed

- The Output Window webview no longer loads executable code from a remote
  origin. It previously pulled marked from
  `https://cdn.jsdelivr.net/npm/marked/marked.min.js` and named that origin in
  its `script-src` and `connect-src`. Both the Visual Studio Marketplace
  Publisher Agreement and the Open VSX publishing terms require an extension to
  execute only the code contained in its published package, so this was a
  violation regardless of intent, and the unversioned `npm/marked` specifier
  resolved to whatever "latest" happened to be at load time. marked's browser
  build is now vendored from the package's own pinned `marked` dependency into
  `dist/vendor/marked.umd.js` by `scripts/vendor-webview-assets.mjs` and loaded
  through `webview.asWebviewUri`, so the CSP carries no remote origin at all.
  As a side effect this restores client-side markdown rendering in the Output
  Window: marked dropped `marked.min.js` from its published files in v5, so the
  CDN request had been returning 404 and the renderer had been silently falling
  back to escaped HTML. `tests/views/webviewNoRemoteCode.test.ts` now fails the
  build if any webview source reintroduces a remote `<script src>` or names an
  `http(s)` origin in a `script-src`, `connect-src` or `worker-src` directive,
  and both `scripts/check-runtime-assets.sh` and the `marketplace-publish.yml`
  VSIX verification refuse an artifact that is missing the vendored asset.

### Changed

- Both READMEs now link the extension's Open VSX listing beside the VS Code
  Marketplace, so users of VSCodium, Cursor, Windsurf and other Open VSX-based
  editors have a documented install path; the root README gains an Open VSX
  version badge (#1788)
- `nightgauge skill render`'s overlay cascade gains a host segment ahead of
  provider (`OverlayKeys` now returns host → provider → id), keyed by the
  execution adapter itself so it resolves even when the model does not — an
  `opencode` dispatch of a local model with no registry entry still applies
  its host overlay. Host fragments live in their own `_overlays/hosts/`
  subdirectory so an adapter and a provider that share a name (`lm-studio`)
  never contend for the same file. The Grok-Build-execution-host prose moves
  from the provider-keyed `xai.md` (deleted) to `hosts/grok.md`, and a new
  `hosts/opencode.md` ships OpenCode's lowercase tool ids, its
  silent-reject-don't-retry posture, and its no-`AskUserQuestion` posture.
  Every overlay key is now sanitized before it becomes a path segment: `..`
  and a NUL byte are refused with a warning, and `/` is encoded rather than
  read as a subdirectory (#1636, ADR-022 §14)
- `runPipelineWithModel.test.ts` now drives the real
  `OpenCodeModelCatalogService` (child_process mocked, catalog service
  unmocked) for the "Run Pipeline with Model" OpenCode picker, closing the gap
  where the service was mocked entirely and the `provider/model` id filter
  itself was never exercised (#1628)
- The VS Code extension now offers OpenCode (labeled Experimental) in the
  switch-adapter quick-pick, the per-stage and global adapter dropdowns, and
  the Adapter Doctor view, gated by `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1`; a
  new `OpenCodeModelCatalogService` sources `provider/model` ids from
  `opencode models` for "Run Pipeline with Model" and the settings panel
  (#1628)
- The VS Code extension's `ExecutionAdapter` now includes `opencode` as a
  first-class value: `ui.core.adapter`, `pipeline.stage_adapters.<stage>` and
  `pipeline.adapter_fallback_chain` all accept it, and an auto-router pick of
  `opencode` dispatches instead of being refused. `opencode` is still not
  added to the built-in default fallback chain (#1623)
- The SDK's eval spawn profile, pipeline cost estimate, fan-out usage
  reporting, and stage calibration now resolve an `opencode` run's provider
  from its model rather than the adapter name, so an `opencode` run against an
  Anthropic model prices, resolves and calibrates the same way a native
  `claude` run does, and one against a local model prices at an exact $0
  instead of an unknown. System-prompt steering is unaffected: `opencode`
  still never receives the Claude-only preset (#1622)
- Agent guidance is now tool-neutral and docs-first: concise `AGENTS.md` files
  route to canonical documentation, `CLAUDE.md` is a thin adapter, and Smart
  Setup generates and validates the same portable structure (#1604)
- Agent guidance now documents how each supported tool actually loads root and
  nested `AGENTS.md` and `CLAUDE.md`, instead of assuming the closest file
  wins, and requires every root-session rule to live in the root file (#1606)
- `scripts/check-agent-guidance.sh` is now a portable, flag-driven check that
  reports every violation at once: import on line 1 of `CLAUDE.md`, no
  symlinked instruction files, indexed nested files, byte and line budgets, and
  a hash-verified shared rules block; `scripts/post-merge-check.sh` takes
  `--repo` and derives the repository from any `origin` URL shape (#1606)
- Smart Setup now migrates repositories in the older `CLAUDE.md`-first layout:
  it produces a migration plan listing every moved rule plus a proposed diff
  for review, instead of adding sections to the old files. It installs the
  bundled agent-guidance check with an `agent guidance` CI job, and
  `/smart-setup verify` reports conformance read-only (issue 1675)
- Codex and Gemini steering now summarizes the repository's `AGENTS.md`, falling
  back to `CLAUDE.md` (without its `@AGENTS.md` import) only when `AGENTS.md`
  has no content of its own (issue 1675)
- `nightgauge doctor --adapters` checks claude against a 2.1.223 floor. A
  claude below it gets a `⚠` row naming the floor and stays usable, so the
  doctor does not mark it not ready (#1613)

### Fixed

- The OpenCode plugin's `session.js` (#1641) spawned every telemetry verb
  (`hook stop-verify` on `session.idle`, `hook notify` on a permission ask,
  `hook skill-usage` on a skill tool call) with `spawnSync`, which blocks
  opencode's entire single-threaded event loop for as long as the child runs,
  up to its 5s bound. `runHook` now spawns asynchronously
  (`node:child_process.spawn`, not `spawnSync`) with the same 5s bound,
  killing the child's whole process group on timeout so a notify verb's own
  `osascript`/`notify-send` grandchild cannot outlive it, and registers no-op
  `'error'` listeners on the child's stdin and stdout before writing: without
  them, a telemetry child that exited before reading its stdin turned the
  write's EPIPE into an unhandled `'error'` event that crashed the whole
  opencode process — a module this repository's own comments call "telemetry,
  it is never a gate" taking the host down with it (#1641, #1810, #1813)
- `session.idle`'s stop-verify verdict is now written by the verb itself,
  not by the plugin. Probing the pinned opencode 1.18.30 binary settled two
  things: it does not await the promise a plugin's `event` hook returns, and
  a one-shot `opencode run` exits within ~10ms of publishing `session.idle`
  (a plugin whose event hook awaited 1200ms never reached the line after its
  await; a `.then()` scheduled 20ms out never ran). So neither awaiting the
  verb inline nor recording it from a continuation can survive that exit, and
  `TestCompactionAutocontinueSuppressionAgainstRealOpenCode` read `0
stop_verify events, want exactly 1`. The plugin now spawns `nightgauge hook
stop-verify --emit-event --session-id <id> [--child]` detached and
  unref'd and never waits on it at all, so no dispatch can be slowed by a
  slow or hung verb; that child appends its own `stop_verify` line through
  the new `opencodeplugin.AppendRunEvent`, honouring the same 1 MiB cap, the
  same single `truncated` sentinel and the same retention contract (ids,
  counts and verdict codes only, never the block reason), and bounds its own
  evaluation at 5s with a `timeout` verdict rather than hanging. The plugin
  still writes the one verdict it can determine synchronously, `no_bin`.
  Readers — `#1653` included — must use the new
  `opencodeplugin.WaitForRunEvent`, because a run's events file now finalizes
  shortly after the OpenCode CLI exits (#1641, #1810)
- `NIGHTGAUGE_BIN`, which the manager exports to every stage and the OpenCode
  plugin SPAWNS, resolved under `go test` to the Go test binary. Running
  `internal/execution`'s own test binary as `… hook stop-verify --workdir X`
  re-runs that entire suite — measured at 104.5s of wall clock, forking git
  into other tests' temp directories, and left orphaned once the CLI died
  with the plugin's 5s bound. That, not any production cost, is the "5s hook
  verb" that pushed `TestOpenCodeIntegrationInheritUserConfigOptIn` towards
  its 20s bound; a real `nightgauge hook stop-verify` answers in ~70ms warm.
  The manager now resolves the export through an injectable `hostExecutable`,
  and every OpenCode integration case that dispatches the real CLI points it
  at a real `nightgauge` build (#1810)
- `adapter-canary.yml` pinned `actions/setup-node` at a SHA
  (`fc7e5e49f31379e40cf9b708e5abd6ebfad0e0fd`, tagged `v5.0.0`) that GitHub
  cannot resolve, failing both the `flag-contracts` and `opencode-canary` legs
  at setup on a `workflow_dispatch` run; both steps now pin the same
  `v7.0.0` SHA (`820762786026740c76f36085b0efc47a31fe5020`) every other
  workflow in the repo already uses (#1639)
- `adapter-canary.yml` now also runs on every push to `main`: main's own
  branch-protection ruleset requires the `flag-contracts` and
  `opencode-canary` status contexts, but the workflow previously ran only on
  `schedule`, `workflow_dispatch` and `pull_request`, so those contexts never
  reported on a push commit and `scripts/post-merge-check.sh` read every merge
  as `NOT-YET` forever. The `changed` job now diffs a push against
  `github.event.before` (falling back to `HEAD^` for a new branch or an
  unreachable before SHA) the same way it already diffs a pull_request against
  its base SHA, so a push touching nothing under
  `internal/adaptercompat/manifests/` still skips both jobs (a skipped
  required job reports success); a push that does change a manifest runs the
  canary at that manifest's own `max_tested`, matching pull_request. The
  `report` drift-issue job already only files or comments on failing rows and
  already excludes only `pull_request`, so it needed no separate push rule —
  it joins schedule/workflow_dispatch there unchanged, gated on
  `needs.changed.outputs.run == 'true'`. The concurrency group is now keyed on
  `github.ref` alone (#1639)
- The required `link-check` job and the local gate no longer request the VS
  Code Marketplace listing linked from `README.md`: `.markdown-link-check.json`
  now ignores `https://marketplace.visualstudio.com/` links host-wide, since
  the listing's availability is a publishing concern, not a property of this
  repository's documentation, and a third-party listing must not be able to
  block every merge (#1767)

### Added

- `plugin/nightgauge/edit.js` gives an OpenCode stage the same
  PostToolUse:Edit|Write coverage Claude Code's hooks.json gives one:
  format-on-save, a version-consistency check, and a test-quality warning,
  run in hooks.json's own order and timeouts (`hook format` 30 s,
  `hook check-version` 10 s, `hook test-quality` 5 s) against a
  Claude-shaped `{tool_name, tool_input:{file_path,...}}` payload built from
  opencode 1.18.30's own `tool.execute.after` arguments. Exactly one
  formatter path runs per dispatch: the plugin calls `hook format` only when
  OpenCode's own `formatter` setting (read from `OPENCODE_CONFIG_CONTENT`)
  is off. opencode 1.18.30's own tool schemas require an ABSOLUTE `filePath`
  for both `edit` and `write`, so an absolute path is resolved against the
  run's worktree (realpath'd on both sides, the same containment
  `internal/hooks/format.go`'s `relativizeHookPath` already does for the
  Claude Code hook path) rather than refused outright; only a path that
  resolves outside the worktree (by that resolution, or by carrying a `..`
  segment) spawns nothing and adds no warning. Every warning is appended to
  the tool's own output (capped at 2 KB total) rather than thrown, so a
  warning is never mistaken for a gate, and a hung verb leaves the tool
  result intact. New Go verb `nightgauge hook test-quality`
  (`internal/hooks/testquality.go`) ports
  `claude-plugins/nightgauge/hooks/test-quality.sh`'s three zero-value-test
  checks (a tautological assertion, an empty test body, a `console.log`
  with no assertion), evaluating the empty-test-body pattern one line at a
  time to keep grep's own line-oriented `\s` semantics (Go's `\s` spans a
  newline; grep's stays within the line it is reading), since the OpenCode
  plugin path has no shell script it can spawn directly; the shell script
  itself is unchanged (#1642)
- `plugin/nightgauge/session.js` gives an OpenCode stage the same session
  coverage Claude Code's hooks give one: compaction context re-injection
  (`experimental.session.compacting` runs `hook inject-context`), suppression
  of opencode 1.18.30's post-compaction synthetic "Continue if you have next
  steps" turn (`experimental.compaction.autocontinue`), idle stop-verification
  (`session.idle` runs `hook stop-verify`), skill-usage telemetry for the
  native `skill` tool (accepted only as an opaque id matching a bounded
  pattern — never a nested object or free text a confused model passed
  instead), and a 60-second-throttled permission-ask desktop notification —
  every one fail-open and never a gate. Permission-ask is driven off
  opencode 1.18.30's own `permission.asked` bus event, read through the
  plugin's `event` hook: the pinned binary never calls the separate
  `permission.ask` plugin hook at all, so that path is kept only for forward
  compatibility. A new bounded run-dir events file
  (`opencode-events-<RUN_ID>.jsonl`, capped at 1 MiB, ids and verdict codes
  only, never transcript or prompt text) records compaction, idle,
  stop-verify, permission-ask and skill events, each tagged `child` when the
  session's own `parentID` says so (unverified against a real child session
  while #1635/AC9 denies the `task` tool);
  `internal/execution/opencodeplugin/events.go` (`ReadRunEvents`,
  `CompactionCount`) is the Go-side reader #1653 will consume, and now also
  rejects a detail value that is not a scalar or that runs long, as a second
  line of defence past the writer's own validation. Driven against the real
  pinned opencode 1.18.30 binary and #1618's offline stub provider: without
  the autocontinue suppression, a compacted session's synthetic continue turn
  resumes the build agent indefinitely (observed 500+ loop steps before the
  test's own bound killed it); with it, the session ends at idle after
  exactly one compaction. #1625's declared steps cap is not itself opencode's
  hard stop on this binary (a run past the cap still executes tool calls
  before ending at idle; see ADR-022's amendment) (#1641)
- The `opencode` adapter's per-run config now carries an explicit `permission`
  map derived from the dispatching stage's `AllowedTools`
  (`openCodePermissionMap`, `internal/execution/adapters/opencode_guard.go`):
  every key (`*`, `read`, `edit`, `glob`, `grep`, `list`, `bash`, `task`,
  `webfetch`, `websearch`, `skill`, `todowrite`, `doom_loop`,
  `external_directory`) is set to `allow` or `deny`, never `ask` (ADR-022
  § 9), with a `bash`/`read`/`edit` deny-list backstop
  (`rm -rf *`, `git push --force*` and friends; `*.env`, `**/.ssh/**`,
  `**/id_rsa*`, the gh hosts file; `opencode.json*` and `.opencode/**` on
  `edit`) present in every map regardless of what the stage's tools grant,
  and an `external_directory` allow-list scoped to `NIGHTGAUGE_SKILL_DIR`
  (read-only: denied on `edit`), the context and output file directories when
  they are outside the worktree, and, since opencode's own `external_directory`
  matching is at directory granularity only (`dirname(file)/*`, never the
  file's own path), a `/tmp/?` and `/private/tmp/?` allow (replacing an
  earlier per-file list that could never match a real request on 1.18.30).
  `?` matters: opencode's own pattern matching turns a configured `*` into a
  regex that DOES cross `/`, so a `/tmp/*` entry — this map's own first draft —
  matched every NESTED `/tmp` request too, not only a flat one, exposing the
  whole `/tmp` and `/private/tmp` trees at any depth rather than the flat,
  one-level cost the map intends; `?` (exactly one character, never `/`)
  matches only the flat request opencode's own dirname-based construction
  always produces for a flat file. The accepted cost — every stage's
  Read/Edit-governed tool calls can reach any OTHER file directly under
  `/tmp` or `/private/tmp`, never a nested one — and a follow-up to move the
  six stage skills' scratch files to a per-run directory instead, are
  recorded in ADR-022's own amendment, along with a same-day correction of an
  earlier reading of this that assumed `*` never crosses `/`. The
  `NIGHTGAUGE_BIN` directory is NOT allow-listed:
  a scan of the six stage skills found no Read, `cat` or `cd` of a path under
  it, only `$BINARY` execution and a `PATH` export, so the entry bought
  nothing they use while letting a Read tool inspect the running binary's own
  directory; `edit` of it stays denied as defense in depth regardless. The
  read/edit deny-list backstop also denies a NESTED secret file (`**/*.env`,
  `**/.env*`; the root-level-only `*.env`/`.env*` alone let one through) and,
  routing a follow-up request from #1752, a secret name carrying `.env` as an
  INFIX rather than a prefix or suffix (`*.env.*`, `**/*.env.*` — matching
  opencode's own bundled default guard's shape for the same file). A new
  pre-spawn check in the adapter's `PreDispatch`
  (`openCodeProjectConfigTamperCheck`) refuses a dispatch whose worktree
  carries a modified, untracked or git-ignored `opencode.json`,
  `opencode.jsonc` or `.opencode/` relative to its base branch — including one
  a prior stage already committed in the same reused worktree, or hid behind
  `git update-index --skip-worktree`, not only a difference from `HEAD`, and,
  now, a worktree git itself reports is not a repository at all, which used
  to pass silently — naming every offending path and spawning nothing; a
  stage must not rewrite the OpenCode config the next stage in the same
  worktree runs under. A tool call OpenCode's permission map rejects with a
  `deny` match (this map's only rejection shape: it never emits `ask`) now
  also counts toward failure classification
  (`internal/execution/stream.go`,
  `packages/nightgauge-sdk/src/cli/adapters/opencodeStream.ts`), naming the
  rejected `tool_use` event's own tool (mapped through the same
  `edit`/`write`/`apply_patch` equivalence the map itself uses, never the
  unnamed `tool=unknown` fallback), so a stage a `deny` stopped is never read
  as a success AND classifies by the tool it actually stopped on. Bounded
  probes against the pinned opencode 1.18.30 binary, run in a real git
  worktree rather than a bare temporary directory, found `edit`'s own pattern
  matching resolves against the path relative to the worktree's git top-level,
  not the leading-slash-stripped absolute path an earlier reading of the same
  probes assumed; both the generated map and ADR-022's own amendment are
  corrected (#1638)
- A scheduled latest-CLI canary (`.github/workflows/adapter-canary.yml`,
  `scripts/adapter-canary.sh`) installs the newest release of every manifest
  CLI daily and on `workflow_dispatch`, and reruns the flag contract (#1617)
  against each one's freshly captured `--help`. Its OpenCode leg
  (`internal/execution/opencode_canary_test.go`, build tag `canary`) drives
  the installed `opencode` against the #1618 stub provider and asserts the
  stream's event-type allow-list, field paths and non-zero token accounting
  (#1624), the permission-reject leg's auto-rejection and exit code, and a
  bad-model leg's exit 1 with a `type: "error"` event. A schema-diff leg
  compares the live `https://opencode.ai/config.json` against the manifest's
  `config_schema_sha256` and, on a difference, re-runs the #1634 suite against
  the live schema. The workflow also runs as a required check on a PR that
  changes a manifest's `max_tested`, at that proposed version, and a `report`
  job (`issues: write` only, no secrets, no CLI or model) files or updates one
  open `canary: <adapter> <version> drift` issue per failing adapter+version
  from the run's JSON summary. Above `max_tested`, OpenCode's own
  endpoint-above-max-tested refusal (ADR-022 § 20) would otherwise block the
  canary from ever driving a real release through the stream contract, so a
  canary-only relaxation (`openCodeCanaryRelax`, gated behind the `canary`
  build tag no production build carries, and only under the explicit
  `NIGHTGAUGE_CANARY=true` signal) lets the leg's own dispatch through while a
  production build's refusal is unchanged; the row records the INSTALLED
  version. The `opencode-canary` job's stream/permission/bad-model leg and its
  schema-diff leg each run regardless of the other's outcome, and the job
  itself goes red if either failed. The `flag-contract` leg attributes a
  malformed or dropped-flag capture to its own adapter and version rather than
  a version-less placeholder, and carries a CLI's hidden, undocumented flags
  forward onto a newer capture instead of failing every adapter with one on
  its next release. The OpenCode leg's stub now runs as a real `stub-provider`
  subprocess per test, its PID captured, killed and confirmed dead in that
  test's own cleanup. The `opencode-canary` row's detail is now the failing
  test's own message in either shape `go test` actually prints it — the
  `-count=1` run `cmd_opencode_canary` invokes has no `-v`, so the
  `--- FAIL:` summary prints before the test's buffered log lines, not after
  — and it never carries realOpenCode's own "pin relaxed" `t.Logf` notice or a
  multi-line failure's own continuation lines. `stubProviderCanaryBinary`'s
  `os.MkdirTemp` build directory is now removed by the package's `TestMain`
  instead of leaking one per test-binary run, and
  `testdata/cli-help/README.md` now notes that a hidden-flag sidecar carried
  forward onto a newer capture (above) is not itself probe evidence at that
  newer version (#1639)
- An OpenCode dispatch now carries the Nightgauge OpenCode plugin: a
  `tool.execute.before` hook, embedded in the binary and written into the
  per-run OpenCode config directory (never installed from npm or Bun), runs
  the same careful-gate verb (`nightgauge hook careful-gate`) the Claude Code
  hook runs, so a Bash tool call a stage runs under OpenCode is blocked the
  same way it would be under Claude Code while careful mode is on. The
  per-run config's `plugin` array names only this file, and
  `OPENCODE_DISABLE_PROJECT_CONFIG=1` is set on every OpenCode dispatch, so a
  target repository's own `.opencode/plugins/*` and `plugin[]` entries never
  load beside it (AC2) — the cost, until #1638 builds the Go-side merge that
  restores it, is that a target repository's own `opencode.json` does not
  merge into a dispatch's resolved config at all (see the ADR-022 amendment).
  The plugin file is written outside the run's OpenCode config directory's
  own `plugin`/`plugins` subdirectory, which 1.18.30 auto-loads on top of the
  config's own `plugin` array: naming both the same file loaded it, and so
  fired every hook in it, twice per tool call. A startup handshake — a nonce
  and sentinel the plugin's init writes, verified the instant the run's first
  `step_start` event is observed (the process group is signalled with a
  bounded, closely-spaced burst of SIGKILLs rather than trusting one
  delivery — a single SIGKILL can miss a child OpenCode forks in the same
  instant on macOS, which does not abort a fork under a pending group-kill
  signal the way Linux does — before anything else, including the `opencode
--version` the failure marker names) and re-checked at exit against the
  first `tool_use`'s own reported start time — kills the stage's whole
  process group and fails it `adapter_incompatible`, forcing a non-zero exit
  code even when the CLI itself exited 0, when the plugin did not load,
  loaded late, or a stale sentinel is read. This handshake now arms for
  every dispatch that actually spawns opencode, including one with no
  runtime identity of its own (the autonomous issue-refine dispatch): it is
  keyed on the run's own root identity, which the manager always mints one
  of, not on the dispatch's possibly-absent runtime identity, so a plugin
  that fails to load can no longer go ungated there. Because opencode 1.18.30
  installs `@opencode-ai/plugin` into every OpenCode config directory its
  resolved config touches — the run's own, `$HOME/.opencode` when it exists,
  and, under `inherit_user_config`, the operator's own XDG OpenCode config
  directory — the moment any of them carries a non-empty `plugin` array,
  independent of whether the plugin itself needs it, and every invocation
  that resolves such a config waits for that install before doing anything
  else, the run's own, freshly-created config directory is now pre-seeded
  from an embedded, version-pinned copy of `@opencode-ai/plugin@1.18.30` (no
  npm binary, no lifecycle script and no network request, ever): a small,
  four-file archive extracted outright (`package.json`, both lockfiles, and
  `@opencode-ai/plugin`'s own version marker — what a real install for that
  directory also produces, though opencode's own "is it installed" check
  reads only `package.json` and `package-lock.json` by dependency name — under
  5 KB gzipped, cut from an ~11 MB, 3,886-file capture of the whole installed
  dependency tree,
  since neither opencode's plugin loader nor the Nightgauge plugin ever
  resolve anything else in that tree at runtime), regenerated deterministically
  by `internal/execution/opencodeplugin/depsdata/regenerate`
  (`--ignore-scripts`) and size-budget-tested so a future regression cannot
  grow it back toward the original capture unnoticed.

  **Nightgauge never seeds, merges into, or otherwise writes to an
  operator-owned OpenCode directory** — `$HOME/.opencode` or, under
  `inherit_user_config`, `OPENCODE_CONFIG_DIR` — narrowing AC1 to: no npm or
  Bun install runs into any Nightgauge-owned directory. An earlier round
  tried seeding these two as well, first from the same four-file archive
  (which satisfies opencode's own install check forever while leaving an
  operator's own `import ... from "@opencode-ai/plugin"` tool file
  permanently unresolvable — OpenCode's own documented way to write a custom
  tool), then from a second, ~10.4 MB re-embedded archive of the complete
  real tree; both are removed. OpenCode's own install into its own config
  directories is the operator's environment now, exactly as it is in the
  operator's own OpenCode runs.

  Offline, or against an unreachable registry, a dispatch touching a
  `$HOME/.opencode` or `OPENCODE_CONFIG_DIR` that does NOT already satisfy
  opencode's own install check — pulled from the pinned binary's own
  `Npm.install`: satisfied if the directory is not writable, or if
  `node_modules` exists and every dependency name `package.json` declares
  (plus `@opencode-ai/plugin` itself) is present in `package-lock.json`'s
  root package entry, checked by name only, never by version — still waits
  on that install, but the wait is now bounded and the failure classified
  rather than left to hang or to read as an unclassified timeout: the
  manager starts a watchdog the instant such a dispatch's config touches an
  unsatisfied directory, bounded at 100s — headroom above the tens of
  seconds a legitimate, reachable-registry install can take (so the online
  case — an operator with an actual internet connection, exactly as their
  own OpenCode run would be — still completes), while still catching the
  truly unbounded case: an unreachable registry's own retry/backoff window,
  observed at ~71s-146.88s in production — but capped at whatever remains
  of the stage's own timeout. The watchdog stands down on EITHER of two
  independent signals, whichever arrives first: the directory becoming
  satisfied (checked read-only, polled every second or two, never written)
  or any output at all arriving, proof the CLI is not stuck — so it never
  caps model latency once OpenCode's own install completes. A directory
  that already satisfies the check BEFORE the dispatch ever spawns opencode
  gets the same local, instant fast path a run's own XDG-resolved config
  directory always did (driving the pinned binary directly with the
  predicate above satisfied: ~1s for `debug config`, ~4.5s for a whole
  dispatch), so it never arms the watchdog at all. A bound that fires kills
  the whole process group (the same reaping the plugin handshake failure path already
  uses, since a single `SIGKILL` can leave a same-instant grandchild fork
  holding the stage's stdout/stderr pipes open) and fails the stage
  `adapter_incompatible`, naming the directory and #1787 (a per-run `HOME`,
  the tracked path to removing the wait entirely rather than only bounding
  it), the same way a genuine handshake failure already does. The plugin also
  denies the `task` tool (subagents) unconditionally: a bounded spike against
  1.18.30 could not confirm whether `tool.execute.before` runs inside a
  subagent's own session, so AC9's fallback applies until that is settled
  (ADR-022, amendment 2026-09-15) (#1632, #1635, #1787)

- The OpenCode config schema published for the newest tested OpenCode
  (1.18.30) is now pinned in the repository, and a contract test validates
  every per-run config the builder generates against it, across LM Studio,
  Ollama and Anthropic dispatches, with and without MCP servers. The test fails
  when a config sets a key the schema does not define or marks deprecated,
  since OpenCode silently drops an unknown key. It also fails when a safety key
  (`share`, `autoupdate`, `enabled_providers`, `instructions`, `mcp`) is
  missing. The OpenCode compat manifest's `config_schema_sha256` records the
  pinned schema's SHA-256 (#1634)
- The SDK now has an `OpenCodeAdapter`, registered under `opencode`, so an SDK
  or extension caller gets an agentic, `sdk-fanout` adapter instead of
  `Unknown adapter 'opencode'`. It runs the Go adapter's exact argv with the
  prompt on stdin. It refuses a malformed model id, an `anthropic/` model
  without `ANTHROPIC_API_KEY`, and an `opencode` binary below the compat
  manifest's floor. The child environment it spawns carries only the
  dispatched provider's key and none of your own `OPENCODE_*` or XDG state. Its
  stream parser reports the same totals, peak input, served model, cost and
  permission-rejection failures as the Go parser. Both are checked against
  one expectations file. Every line the child prints, stdout included, is
  redacted of the provider key, the forge token and the run's password as the
  Go manager redacts them. A `StageExecutor` stage's timeout, Ctrl-C and the
  SDK process exiting each kill the run's whole process group;
  `PipelineOrchestrator.stop()` does not reach fan-out units (#1765). The
  adapter spawns nothing until the per-run config is wired into the SDK
  (#1648) (#1637)
- An OpenCode dispatch to a local model now discovers the model's context
  window from the server itself (LM Studio's loaded context, Ollama's
  `num_ctx`), once per process, so `opencode.limit` becomes an optional
  override: a context limit above the server's loaded window is clamped to it
  with a warning, and a model whose window cannot be discovered and has no
  override is still refused before spawn. `nightgauge doctor` checks and
  reports the context limit a dispatch resolves this way, not the override
  alone (#1633)
- An OpenCode stage with a cost budget is stopped once the cost of its own
  steps, priced from the model registry at every `step_finish`, passes the
  budget: its process group gets SIGTERM, then SIGKILL after 10 seconds, and
  the stage fails as `budget_exceeded` with `[cost-cap-exceeded]` ending its
  stderr. A subagent's steps are not in the stream, so its usage is priced
  only once the stage has ended: a stage its subagents took past the budget
  is not stopped while they run, but fails as `budget_exceeded` then, and the
  enabled-dispatch warning says so. A stage that would otherwise succeed but
  whose subagent usage was only partly read also fails as `budget_exceeded`,
  since its budget cannot be verified, unless its model is priced at zero.
  The budget prices every step, a subagent's included, at the dispatched
  model's rates, so a subagent on a pricier model of the same provider is
  under-counted. OpenCode's own reported cost is never used. A hosted model
  the registry cannot price logs one `[opencode-cost]` warning and runs
  without a cost cap (#1630)

- An OpenCode stage's run record now carries the identity of the model that
  served it on its `model_selection`: `model_provider`, `upstream_model` (the
  `-m` value it was dispatched with, kept when another model served it) and
  `endpoint` (the declared endpoint that served it). The Go scheduler's
  platform telemetry sends the recorded provider as `modelProvider` beside
  the stage's ADR-022 model identity, the VS Code extension's history reader
  keeps the three fields, and a stage re-run on another adapter keeps none of
  the identity of the run it replaced (#1630)

- Three terminal kinds for OpenCode (Experimental) and local-model failures,
  in the rule table, the Go constants, the SDK union, the extension schema and
  `docs/FAILURE_TAXONOMY.md`. `context_window_exceeded` is a prompt that
  outgrew the model's loaded context; `adapter_permission_rejected` is
  OpenCode rejecting a tool the stage is allowed under an `ask` rule, either
  its own default guard on reading `.env` files or a rule in a repository's or
  the user's config (classified ahead of `permission_denied`, whatever tool
  the rejection names); `adapter_incompatible` is a version-policy refusal of
  the OpenCode binary. All three are parked: no model escalation, no retry, no
  lifetime-failure charge, no cascade feed, and held until an operator clears
  the issue's failures, the command the remediation on the failed entry and in
  the auto-retro names
  (`nightgauge autonomous clear-failures <owner/repo#N>`). OpenCode's own
  wording now also maps a down local server to `network_unavailable`,
  `ProviderModelNotFoundError` and an Ollama model that is not pulled to
  `model_unavailable`, and a provider 401 to `adapter_auth_failed`. Every
  OpenCode wording clause requires OpenCode's `AI_APICallError` wrapper, so
  model text quoting overflow wording classifies as nothing. The wording comes
  from real opencode 1.18.30 captures
  (`scripts/capture-opencode-failure-fixture.sh`), and ADR-022 § Failure
  wording records where it differed from the plan: a down server never prints
  `ECONNREFUSED` (#1631)

- The OpenCode stream parser is now tested against real opencode 1.18.30
  runs: a local LM Studio model on two endpoints, `lmstudio` and
  `lmstudio-remote`, a run whose model started two subagents, and a
  stub-provider run dispatched under a hosted provider key that stands in for
  the hosted shape until a real hosted capture exists (#1680). The tests pin
  the summed step usage, the subagent usage folded in from the session table
  and a registry price for the hosted model. For the local model they check
  ADR-022 § 3's rule, which stamps a zero; the stage record itself stays
  unstamped until #1630 lands. The second endpoint's stage records its
  endpoint id in the served and upstream model, and stays unstamped until
  declared endpoints resolve it to a provider (#1678). The captures showed
  that a subagent's rejected permission does not end the run, yet still fails
  the stage; ADR-022 § 9 records it (#1629)
- An OpenCode stage now gets its repository's steering and the pipeline's MCP
  servers. Its `AGENTS.md`, or the `CLAUDE.md` of a repository that has only
  that, and the files it `@`-imports from inside the worktree reach the model
  with the baseline steering Codex stages get. Nightgauge reads none of it
  through a link out of the worktree, and writes nothing into the worktree.
  The MCP servers come from `.mcp.json` and `.claude/settings.json` at the
  head of the run's repository's default branch as GitHub serves it, read in
  one bounded query and never from the worktree, its git refs or its git
  config, so a server one stage adds, or points origin at, is not started by
  the next; a stage whose repository GitHub cannot be read for in 15 seconds
  gets none. `nightgauge opencode config` takes the repository as `--repo`.
  Until #1638, a repository's `opencode.json` can still add one. Their
  credentials reach OpenCode only as `{env:VAR}` references. A server is left
  out, with a warning, when one of its variables holds a value OpenCode cannot
  paste into its config, checked in the environment OpenCode is spawned with,
  and a dispatch whose `ANTHROPIC_API_KEY` holds one, such as a key ending in
  a carriage return, is refused (#1626)
- An experimental `opencode` adapter runs stages through the OpenCode CLI, one
  adapter for local (LM Studio, Ollama) and hosted models. It refuses to
  dispatch unless `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1` is set in the
  environment, and each dispatch it allows warns which controls are not
  enforced yet. A stage names its model as `<provider>/<model>`, and the
  adapter never infers a provider from a bare id. An `anthropic/*` model needs
  `ANTHROPIC_API_KEY` and never uses a stored subscription or OAuth login;
  `claude-headless` runs Anthropic models on a Claude subscription. The prompt
  goes on stdin, never argv, every spawn gets its own server password, and no
  spawn inherits `OPENCODE_AUTH_CONTENT`, a variable OpenCode reads stored
  logins from.
  [ADR-022](docs/decisions/022-opencode-multi-provider-adapter.md) records the
  design (#1612)
- The model registry names the provider behind an `opencode` model from its
  `<provider>/<model>` id, the same way in the Go binary (`ProviderFor`) and
  the SDK (`providerFor`). LM Studio and Ollama models are local; an unknown or
  malformed id is `other`, which is never priced as a local $0. A tier band
  becomes a `<provider>/<model>` for the configured OpenCode model's provider,
  and a provider with no model in that band is refused (#1614)
- The TypeScript SDK knows the `opencode` adapter name, behind the same enable
  gate as the Go binary: whether `opencode` comes from `NIGHTGAUGE_ADAPTER`, a
  per-stage variable or config, resolving it fails with a configuration error
  that names `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1` unless that switch is set in
  the environment. No config file can turn it on, and a refusal never falls
  back to another adapter. The auth hint for `opencode` points at the install,
  a local LM Studio or Ollama server, or the provider's own API-key variable
  (`ANTHROPIC_API_KEY` for `anthropic/*`), never an interactive login. The
  auto-router scores `opencode` like LM Studio, so a paid adapter still wins
  every stage (#1615)
- An `opencode` stage now reports its real token usage: the Go binary parses
  `opencode run --format json` instead of handing it to the Claude parser,
  which booked zero tokens. It sums every step, keeps the largest single
  step's prompt, adds each subagent session's usage from its sanitized export
  after the run (at most 64, each read under a 10-second timeout, from the
  run's own directory with no plugins and no credentials, and not at all
  after an operator stop), and records the model that served the stage. The
  stage's result also carries that model's provider, the `-m` value it was
  dispatched with and the `opencode --version` it ran; the stage record gains
  the provider and the `-m` value with #1630. A stage that stopped because
  OpenCode rejected a permission on its own now fails with
  `[adapter-permission-rejected]` or `[permission-denied]` instead of reading
  as a success on exit 0, also when the rejected command spans several lines.
  OpenCode prints the command unescaped, so after the first rejection the
  stage keeps no stderr line but that rejection and its marker, and the
  marker names only OpenCode's own permissions. Only the first rejection,
  which OpenCode prints before any of the command, decides the failure
  reason; a later one, a subagent's or one the command fakes, is counted as
  drift, so nothing in the command reaches the failure reason.
  Credentials of a known shape (API keys, GitHub tokens, bearer tokens, a
  URL's user and password, credential query parameters) are removed from its
  output before it is kept, including where a tool printed them at the start
  of a line or in colour, and so are the credentials Nightgauge hands the
  stage, also in their JSON-escaped form; provider settings such as
  `AWS_REGION` or a Vertex project are not redacted as secrets. Output
  that no longer matches what OpenCode 1.18.30 printed leaves an
  `[opencode-drift]` marker in the log instead of passing silently (#1624)
- `nightgauge preflight managed-steering` reports generated Nightgauge steering
  committed in any tracked `AGENTS.md`, and `--fix` removes it from the working
  tree (issue 1675)
- Each CLI adapter has a compat manifest (`internal/adaptercompat/manifests/`),
  the one validated record of its floor, newest tested version, release feeds,
  install recipe and fixtures; the doctor reads its floors from it (#1613)
- A flag-contract test builds every CLI adapter's command over every
  combination of stage options and checks each flag against that CLI's real
  `--help`, captured at the manifest's newest tested version. An upstream that
  drops or renames a flag now fails a unit test instead of a live stage. Each
  manifest's `required_flags` must equal the flags the adapter emits, and
  `NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR` points the test at other captures, such
  as the newest CLIs' help. A flag a CLI accepts without listing it is
  recorded for one version in a `.hidden` file beside that version's capture,
  so probing a newer CLI needs no code change. `scripts/capture-cli-help.sh`
  makes the captures: it installs each pinned CLI into a throwaway prefix with
  no credentials in its environment, runs only `--help` under a timeout, and
  removes the prefix.
  The test found two adapter flags the CLIs refuse, now tracked as #1715
  (codex, sandbox-scoped stages) and #1716 (claude, `--max-tokens`) (#1617)
- `cmd/stub-provider` is a new deterministic, scripted, OpenAI-compatible chat
  completions server for adapter contract runs in CI: it answers
  `/v1/chat/completions` (streamed and non-streamed) and `/v1/models` from a
  named script, binds loopback only, and stops itself after a bounded number
  of requests, an idle timeout, or SIGTERM. Turn selection is stateless and
  pure, so identical requests get identical replies (#1618)
- `cmd/adaptercompat-codegen` renders the compat manifests
  (`internal/adaptercompat/manifests/*.json`) into
  `packages/nightgauge-sdk/src/cli/adapters/adapterCompat.generated.ts`, a
  frozen `ADAPTER_COMPAT` map the SDK adapters read their version floor from
  instead of each holding its own literal. `codex`, `gemini` and `grok` now
  source `minVersion` from it, and `claude-headless` gets the same
  warn-don't-block floor check the others already had. `scripts/adapter-cli-pin.sh`
  reads a manifest's `max_tested` to print an npm install pin, which
  `release-watchdog.yml` and `continuous-improvement.yml` now use instead of a
  hardcoded `claude-code@` version (#1621)
- `nightgauge opencode config --stage <s> --worktree <p> --json` prints the
  per-run OpenCode config, isolation environment, the inherited variables to
  withhold, plugin directory and run directory a stage is spawned with, from
  the same code as the Go adapter, and refuses every dispatch the adapter
  refuses before spawning, so the SDK path can run OpenCode under identical
  bytes. The OpenCode adapter now
  reads an `opencode:` block from `~/.nightgauge/config.yaml` alone (a
  committed one is refused): `provider` (`lm-studio` or `ollama`), `base_url`,
  `limit.context` and `limit.output`, `timeouts`, `model`, `binary`,
  `inherit_user_config`, and `snapshot`, `lsp` and `formatter`. A dispatch to a
  local model is refused before spawn unless the block declares its server
  with nonzero limits, because OpenCode never compacts a session whose context
  limit is 0, which is what LM Studio reports (#1625)
- `nightgauge doctor --adapters opencode` checks the OpenCode adapter the way
  a dispatch meets it, and dispatch enforces the same version policy. The
  floor and max-tested version come from the compat manifest. A binary below
  the floor, or whose version cannot be read, is refused before spawn as
  `adapter_incompatible`, naming both versions and the managed install
  (`npm i --prefix ~/.nightgauge/tools/opencode opencode-ai@<max-tested>`). A
  binary above max-tested warns, refuses a model server you run, and runs a
  stage only once a self-test has passed for that binary, version and per-run
  config: `opencode debug config` on the stage's config must exit 0 and keep
  every key it sets, and `opencode run --help` must accept every flag the
  adapter passes. `opencode.binary` pins the binary for the doctor and the
  spawn alike, and must be an absolute path. Every probe of the binary runs in
  a throwaway directory with project config off, so no `opencode.json` or
  plugin in a directory above it loads. The doctor row also runs
  `opencode models` for `opencode.model`, setting a hosted provider's
  variables that your environment holds to a placeholder, never the
  credential, and names them when it holds none; with
  `opencode.inherit_user_config` on, such a provider's model the listing lacks
  is a warning, not a block, because your own OpenCode config can supply the
  key or the model. With that opt-in off, the row blocks, as every dispatch is
  refused, while `~/.opencode` holds config or the machine has managed
  OpenCode config, naming what it found. The row also probes each LM Studio or
  Ollama endpoint, named by id and never by address, for reachability, whether
  the model is loaded and the context it is loaded with, warning when
  `limit.context` is 0 or larger; prints the run's OpenCode directories and an
  offline posture that says egress is unverified until #1644; flags an OAuth
  `anthropic` login in OpenCode's stored logins, reading only its type; and
  warns when the binary changed since the last dispatch. With
  `NIGHTGAUGE_EXPERIMENTAL_OPENCODE` unset the row runs nothing and is not
  usable, so cap recovery never hops onto it (#1627)
- The Nightgauge OpenCode plugin's `gates.js` now runs workflow-gate and
  stage-gate too, not just #1635's careful-gate: a `bash` tool call runs
  workflow-gate, careful-gate, then stage-gate, in `hooks.json`'s own
  PreToolUse order (first deny wins), so a push to main, a force-push, a
  destructive git operation, a secret read/write, an analysis stage
  advancing git/forge state outside its mandate, and a `gh pr merge --admin`/
  `--auto` bypass are all blocked under OpenCode exactly as they already are
  under Claude Code. `edit` and `write` tool calls run workflow-gate with a
  Claude-shaped `file_path` payload, so editing `.env` or writing
  `credentials.json` is blocked the same way. Every tool id opencode 1.18.30
  exposes is now pinned to a classification (`TOOL_CLASSIFICATION`, a frozen,
  null-prototype table so an inherited `Object.prototype` key such as
  `constructor` or `__proto__` can never read back as a defined kind and
  bypass the check below it); a tool id the table does not list is blocked
  closed with `[nightgauge-gate:unknown-tool]`, and `apply_patch` — real,
  and reachable whenever the dispatch model's id matches opencode's own
  `gpt-*` (non-`oss`, non-`gpt-4`) tool-selection rule, which offers
  `apply_patch` instead of `edit`/`write` for that model family — is
  classified and blocked rather than guessed at, since its `patchText` hunks
  are not yet mapped to a Claude-shaped `file_path` payload. opencode's own
  read-only MCP resource tools (`list_mcp_resources`,
  `list_mcp_resource_templates`, `read_mcp_resource`) are classified
  passthrough; a repository's own MCP server tools (`<server>_<tool>`) are
  not yet mapped and stay blocked closed under `[nightgauge-gate:unknown-tool]`
  until #1626's per-run `mcp` config is threaded through to the plugin — see
  the ADR-022 amendment this round for that gap against ADR-022's own
  capability table. A new `commandExecuteBefore` export runs `hook
sanitize-prompt` against a `command.execute.before` expansion, and
  `nightgauge.js` now calls it before any `session.js` delegate (#1641's
  file), fixing a gap in this same change where the export existed but the
  registered hook never called it. The unconditional `task` denial (#1635's
  AC9 fallback) is unchanged. New
  `internal/execution/opencodeplugin/plugin_gates_test.go` and two testdata
  fixtures — `gates_parity_corpus.json` and a captured
  `opencode-1.18.30-tools.txt` — check every gate against the real built
  binary and the real embedded plugin (#1640)

### Fixed

- An OpenCode stage's cost is now priced by the provider that served its
  model, not by the adapter name: a stage on an LM Studio or Ollama model
  records a stamped zero cost instead of an unstamped one, and a stage on a
  hosted model the registry lists is priced at that model's registry rates.
  A hosted model the registry does not list stays unstamped.
  `CalculateCostFor` replaces `CalculateCostForAdapter`; every other adapter
  prices as before. The Go scheduler's platform telemetry now sends a stamped
  zero stage cost, and the run total of a run whose every stage is a stamped
  zero, as `0` instead of `null` (#1630)

- The VS Code extension's run-record schema now accepts a `permission_denied`
  terminal kind, which the Go scheduler has written since #289: such a record
  used to fall through to the V2 schema and lose its kind. A parity test now
  holds the schema to the Go constants (#1631)

- A stage the operator stops is reported as stopped, not as a
  `wait: context canceled` failure, when its CLI exits on the stop but a
  process it started holds its output past the grace period. OpenCode's
  version-policy probes now run under the stage's context: a stage whose
  context is done starts none of them, and a probe it stops is not reported
  as an incompatible binary (#1627)
- ADR-022 no longer says `--yolo` and `--dangerously-skip-permissions` are not
  `opencode run` options. In 1.18.30 both are hidden options that switch on the
  same auto-approval as `--auto`. The adapter still never emits them, and
  `--cors` and `--port` join the flags it must never emit. The test that holds
  it to that also catches the camelCase, `=value` and dot-notation spellings
  opencode accepts, such as `--dangerouslySkipPermissions` (#1617)
- Generated Codex steering no longer ends up in commits. The Go-direct path
  never removed it, and the agent could commit it mid-stage on every path;
  pipeline commits now strip it before committing, a commit that carried it is
  repaired after every stage and before pr-create pushes, and pr-merge refuses
  a pull request whose head still carries it (issue 1675)
- Steering summaries no longer collapse to a document's title when its first
  heading is followed directly by a subsection (issue 1675)
- `scripts/check-agent-guidance.sh` no longer reads Markdown link text in the
  routing table as a path, so a link such as `[sub/thing](../sub/x.md)` passes
  instead of failing as an unresolved `[sub/thing`. Whole links and images are
  parsed, with titles and angle brackets removed from the destination; the docs
  index link check ignores links shown in code, and a nested `AGENTS.md` counts
  as listed only when its whole path appears, not the tail of a longer one
  (issue 1676)
- Check-run, commit-status, workflow-run and branch-rule reads now follow
  every page instead of stopping at GitHub's first 30 results. A required
  check on page 2 was never observed, so `nightgauge ci checks-complete` and
  `scripts/post-merge-check.sh` answered NOT-YET forever and survival detection
  could miss a failure on `main`. The `post-merge-check.sh` fallback also no
  longer reports a false GREEN on a commit with more than one page (#1681)
- `nightgauge hook post-merge` no longer waits out its whole budget when a
  required context is a commit status, such as a CLA status. The hook and
  `nightgauge ci checks-complete` now read both GitHub status surfaces through
  one reader and reach one verdict for the same commit (#1674)
- `nightgauge ci checks-complete` and `scripts/post-merge-check.sh` now report
  RED when any check run or commit status on the SHA failed, and NOT-YET while
  any is still running, required or not. They previously evaluated required
  checks only and could report GREEN over a failed optional check
- `nightgauge ci checks-complete`, and so `scripts/post-merge-check.sh`, no
  longer reports a failure to measure as a red commit. Exit 1 now means a
  completed check failed and nothing else; a token-resolution failure, an
  authentication error, a network error, a rate limit, a 5xx, an unparseable
  response or a bad argument exits 2 with a `could not run: <reason>` line
  instead of sending the operator to fix a `main` that may be green. The
  post-merge hook likewise says it could not verify the branch, not that it is
  red (#1691)
- Sub-issue, blocked-by and blocking reads now page to completion (up to a
  20-page cap) instead of silently stopping at the first 12, 25 or 50 items,
  so epic validate, rollup and wave planning no longer act on a truncated
  list. A list that cannot be read in full is now an explicit error rather
  than being treated as short. See `docs/GITHUB_API_DEPENDENCIES.md` for
  which commands read which lists (#1682)
- `scripts/test-install-agent-skills-targets.sh` no longer rewrites the
  committed plugin-skills mirror. It ran the real installer, which regenerates
  that mirror first, six times per run: in `scripts/ci-local.sh` the
  concurrent "Plugin skills mirror in sync" step could read it half-rebuilt,
  and in CI the suite repaired a stale mirror just before that gate checked
  it. Every arm now runs the installer in a `git archive` sandbox, and the
  suite fails if `git status --porcelain` changes or the mirror is rewritten
  (#1607)
- Two `scripts/ci-local.sh` runs in different worktrees no longer break each
  other in the publication-boundary hermeticity step. It used to delete every
  sandbox under the shared root, including one another run was still using,
  which then failed with `manifest.bak: No such file`. It now runs its suites
  under a root of its own and reclaims a leftover root only when no process
  that owns it is alive. Every sandbox and root is claimed before it becomes
  visible, so another run's cleanup can never take one mid-creation, and
  neither cleanup follows a symlink planted in the shared root to delete what
  it points at (#1697)

### Removed

- `configs/codex/AGENTS.md`, which duplicated `configs/codex/README.md` and was
  loaded as instructions for that directory by tools that read nested
  `AGENTS.md` files (#1606)

### Security

- CI now checks the experimental `opencode` adapter's config-merge and
  plugin-loading assumptions against an exact install of opencode 1.18.30, so
  an OpenCode upgrade that changes them fails the build. ADR-022 § 8 records
  the results: until a stage stops OpenCode loading the target repository's
  `opencode.json` and `.opencode/` (#1638), that config can reorder a run's
  permission patterns so a denied command runs, add plugins and MCP servers
  the run's own lists cannot remove, and add a remote instructions URL that
  a run fetches (#1632)
- OpenCode stages run in a private directory per pipeline run
  (`~/.nightgauge/opencode/runs/<run>/`), deleted when the run ends: a run
  reads none of your own OpenCode config, plugins, logins or `~/.agents/skills`,
  and its transcripts never appear in your `opencode session list`. Every
  other tool's config in your XDG config directory, gh's and git's included,
  Go's build cache and Nightgauge's machine config still resolve to yours. A
  stage inherits no `OPENCODE_*` variable, no `ANTHROPIC_BASE_URL` or
  `OPENAI_BASE_URL`, and none of the variables OpenCode's provider catalog
  binds to a model service other than its own; a stage's tools lose those
  variables too, and each dispatch names the ones it withheld. The forge tokens
  and your cloud platform credentials (AWS, Google Cloud, Cloudflare,
  Databricks and others) are kept whole, so a stage's tools act as the identity
  you chose instead of falling back to a credentials file or profile. That
  does not stop every other provider: the dispatch warning lists what still
  reaches one. The
  server password, the forge tokens and the stage's own provider's credentials
  are redacted from its captured output, and other secrets only by their shape
  (#1624). Dispatch
  is refused while `~/.opencode` or this machine's managed OpenCode config
  holds config, which OpenCode reads whatever the run's directories; move
  `~/.opencode`'s entries to `~/.config/opencode`, or set
  `opencode.inherit_user_config: true` in `~/.nightgauge/config.yaml` to run
  with your OpenCode config (#1616, #1625)
- An OpenCode stage runs under a per-run config Nightgauge builds: it loads
  only the provider the stage names, runs every built-in agent (compaction,
  summaries, subagents) on the stage's model, turns session sharing,
  autoupdate and session titles off, caps tool output, and caps the build
  agent's and each built-in subagent's steps at the stage's turn cap (200 when
  none is set). The repository's and an inherited OpenCode config cannot
  change the pinned `id`/`provider.npm` of the model entry a stage on your
  local model server or on Anthropic uses, or the SDK package that sends it,
  the local model's limits and compaction threshold, the local server's or
  Anthropic's API address, titles or sharing. They can still add settings: a
  `mode` entry can still replace the `general` or `explore` subagent's model
  (on the stage's provider) and steps cap, a block for another hosted
  provider can still send its stage to another server or model, a hosted
  model's limits can still be set, and — without touching the pinned `id` —
  an `options.model` on the dispatched model's own entry or an agent's own
  options, or a variant, can still change the model actually served,
  `options.speed`/`options.fallbacks` can still turn Anthropic's fast mode or
  a server-side fallback on, and an agent's `options.mcpServers` can still
  send `ANTHROPIC_API_KEY` as an MCP authorization token to a server it
  names; the dispatch warning says so, and #1638 is what closes those three.
  A local model server's URL stays in a private file, never in the stage's
  environment, and credentials appear only as `{env:VAR}` references (#1625)
- An OpenCode dispatch to a provider that runs on the forge's or a cloud
  platform's credentials, such as `github-copilot/*` on `GITHUB_TOKEN` or
  `google-vertex-anthropic/*`, is refused before spawn: those credentials can
  be a subscription or OAuth login, and a pipeline run authenticates only with
  a model provider's own API key. So is a provider key that is neither your
  declared model server nor a provider OpenCode knows, such as a second LM
  Studio only your own OpenCode config defines, and an `anthropic/` model
  whose served model the per-run config cannot pin: one OpenCode's bundled
  catalog does not list, which would run with no context limit and never be
  compacted, and a fast-mode entry such as `anthropic/claude-opus-5-fast`
  (#1625)
- The VS Code extension's Grok and Codex setup now installs only the skills
  and Codex commands bundled with the extension. It no longer copies the open
  workspace's `skills/` or `.codex/commands/` folder into `~/.grok` or
  `~/.codex`, which every later session loads in every project. Installing
  skills from a checkout is a separate development command that refuses an
  untrusted workspace or a destination overlapping the source, and names both
  paths in a confirmation. The copy follows no symlink out of the source folder
  and no symlinked folder at all, and it replaces an existing skill folder only
  when the folder holds the `.nightgauge-installed` marker that the extension
  now writes. Any other folder is left untouched with a warning that names it.
  That includes a folder an earlier version of the extension installed and one
  `scripts/install-agent-skills.sh` installed or refreshed, because the script
  writes no marker. Delete such a folder to let the extension install it
  (#1683)

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

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v0.4.5...HEAD
[0.4.5]: https://github.com/nightgauge/nightgauge/compare/v0.4.4...v0.4.5
[0.4.4]: https://github.com/nightgauge/nightgauge/compare/v0.4.3...v0.4.4
[0.4.3]: https://github.com/nightgauge/nightgauge/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/nightgauge/nightgauge/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/nightgauge/nightgauge/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/nightgauge/nightgauge/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/nightgauge/nightgauge/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nightgauge/nightgauge/compare/v0.2.3...v0.3.0
[0.2.3]: https://github.com/nightgauge/nightgauge/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/nightgauge/nightgauge/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/nightgauge/nightgauge/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nightgauge/nightgauge/tree/v0.2.0
