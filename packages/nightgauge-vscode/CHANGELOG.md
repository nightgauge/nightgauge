# Changelog

All notable changes to the Nightgauge VS Code Extension will be documented in this
file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **"Cloud features (optional)" in this README** says what the two cloud
  switches do: `"nightgauge.cloud.enabled": true` shows sign-in and the account
  and team commands, and `platform.enabled` in `.nightgauge/config.yaml`
  decides whether the extension talks to the hosted service. Both are off by
  default, and the local pipeline needs neither.
- **Each window sends the platform a random instance id** with its
  registration, every heartbeat and its deregistration, so the hosted service
  can keep each window's execution profile apart. It is made when the window
  activates, held in memory only, and never derived from your machine, user
  or workspace (#2395).
- **A concurrency cap set on the workspace from the dashboard applies to this
  window.** While you are signed in, the window applies the cap of the
  workspace its `.vscode/nightgauge-workspace.yaml` names: no new pipeline
  starts above it, running ones are never stopped, and queued issues start as
  soon as the cap is raised, cleared or ends. A cap on another workspace
  never applies here. The Queued Issues header shows the cap, Resume Queue
  says when it holds every slot, it survives a window reload, and signing out
  drops it (#2337).

### Fixed

- **Two repositories' issues with the same number can run at once** (#2403).
  While one ran, the other was dropped from the queue as if it were a second
  copy of the first. Each now gets its own slot, its own row in the pipeline
  view and its own output channel, and Stop Slot stops only the one you
  picked, named `owner/repo#N`.
- **A stage on Codex, OpenCode, Grok, Gemini or Copilot runs the skill composed
  for it.** It used to be prompted with the base skill, so the adaptations for
  its host and model, and a skill the platform resolved, never reached it, and
  it was granted the base skill's tools. It now gets the same render a Claude
  stage gets, or the platform's skill, and that skill's tools (#2381).
- **A stage whose skill only asks questions is refused with a message naming
  the skill**, instead of running with no tool restriction under Codex (#2390).

- **Stop Epic stops only the epic you picked** (#2382). In a workspace with
  several repositories, two epics can share a number; stopping one stopped
  the other's running issues and removed its queued ones too. Stop Epic, its
  quick pick and the running slot now name the epic as `owner/repo#N`.
  Removing or draining a queued issue no longer takes another repository's
  issue with the same number, and dragging an epic onto the queue checks and
  queues each sub-issue in its own repository.
- **An issue the queue started on its own can be queued again after its run.**
  It stayed marked as processing until the window reloaded, so adding it again,
  **Remove from Queue** and a remote trigger for it were all refused. Stopping
  the pipeline during the few seconds before a queued issue starts now also
  keeps it from starting, and leaves it in the queue; so does a start that
  fails before the run begins (#2397).
- **Nothing about your runs is sent unless cloud features are on.** Signing in
  was enough for the Nightgauge binary and this extension to upload your runs.
  Now they upload only with `platform.enabled: true` in your machine-tier
  config, and the binary follows your VS Code telemetry settings, including a
  change, at once: turning the `pipeline-run` stream off now stops the run
  records the binary sends, too. `platform.telemetry.enabled: false` in that
  config stops this extension's uploads as well as the binary's, and the
  telemetry settings can be set in your user settings only, so a repository
  cannot turn them back on (#1796).
- **With cloud features off, nothing reaches the hosted service in the
  background.** After you signed in, the Nightgauge binary checked the
  service's health every minute under your session, cloud features or not.
  Now only your own actions reach it: signing in or out, activating a license,
  starting a trial, managing your subscription, or opening a dashboard tab with
  your cloud data.
- **A repository cannot set any `platform` setting.** A committed
  `.nightgauge/config.yaml` could point this extension, and your license key or
  session with it, at another host, or override your telemetry switches. The
  extension now ignores the `platform` block in a repository's config, as the
  binary already did. Saving the Project or Local tab of the settings panel
  moves a `platform` setting you changed there to your machine-tier config and
  never copies one the file already held, and **Switch Platform Environment**
  writes your machine-tier config.
- **The privacy notice, the telemetry setting and the Telemetry Settings panel
  say what a run sends:** its repository and issue number, the issue title,
  labels and the first 8,192 characters of its body, the branch, timings,
  tokens, cost and outcome, and a failed stage's error message. They used to
  call it anonymous usage data that never included branch names, so the notice
  is shown once more, to everyone who has not turned telemetry off.
- **A stored license key waits for `platform.enabled`.** Activating a license
  or starting a trial stores the key in VS Code and the OS keychain, and the
  extension used to hand it to the Nightgauge binary as an explicit opt-in, so
  the binary registered this machine and sent heartbeats with
  `platform.enabled: false`. The binary now treats it as a stored key and
  uses it only when `platform.enabled` is true. Signing in, activating a
  license and starting a trial still work with the switch off (#2398).
- **Reloading the window while a pipeline runs keeps your queue.** The reload
  ended the running pipelines and also cleared every queued issue, including
  runs triggered from the dashboard. Now only the running pipelines end, and
  the queued issues, those about to start included, are still there when the
  window comes back. Stop All still clears the queue (#2396).
- **A pipeline worktree's `npm install` no longer replaces a hook directory
  set for the whole clone.** A checkout whose `prepare` script runs husky set
  husky's per-worktree hooks path for every worktree of the clone, so resuming
  an issue whose branch predates Nightgauge's publication hook turned that
  hook off. An absolute `core.hooksPath` is now put back after the install,
  with a warning in the log (#2389).
- **The epic base-branch check after issue pickup no longer assumes the
  `nightgauge/nightgauge` repository** when it cannot identify the window's
  repository. It used to look up that repository's issue for a parent epic;
  it now skips the lookup, and an epic branch it has to create is created for
  the checkout's own repository (#2388).
- **Model escalation stays inside the performance mode's ceiling.** Under
  `efficiency`, a stage that asked for a stronger model, a proactive
  escalation and the health-gated escalate-all policy could each move a Sonnet
  stage to Opus, and `model_routing.max_model` did not cap them (#2386).
- **Runs the extension orchestrates consult eval routing advice unless
  `model_routing.use_eval_recommendations` is `false`,** as autonomous runs
  already did. The extension treated an unset key as `false`, although the
  shipped default is `true` (#2387).
- **Codex and OpenCode stages get the tools their skill grants.** They ran
  with no tool list, so a skill that grants only read tools still ran Codex
  with full access, and OpenCode reported a refusal of a granted tool as one
  the stage was never granted. A `Tool(pattern)` entry such as `Bash(gh *)` is
  now read whole, and a comma-separated `allowed-tools` list reads as a
  space-separated one (#2358).
- **A skill's `allowed-tools` is read in every YAML form.** A list written
  inline (`[Read, Grep]`) or one entry per line, or a value on the lines below
  the key, used to read as no list, so a stage ran with the default tools,
  `Bash`, `Write` and `Edit` among them, instead of the ones its skill grants.
  A `# comment` after the list is no longer read as tools, and a field that
  lists no tool now fails the stage (#2358).
- **More spellings of a skill's `allowed-tools` read as YAML reads them.** A
  quoted key or a space before the colon used to read as no list, so the stage
  ran with the default tools, `Bash`, `Write` and `Edit` among them. A block
  scalar (`|` or `>`), or a list continued on the lines below its key, lost its
  tools. A list written only in a form the extension cannot read now fails the
  stage (#2385).
- **Resuming a run from the phone app or the dashboard after its window was
  reloaded says to resume it in the window,** instead of waiting five minutes
  to expire. The reload ended the paused run; the window's own Resume prompt
  continues it. Cancelling such a run from there ends it, its Resume prompt
  no longer comes back, and choosing Resume on a prompt already on screen
  starts nothing. With several paused runs, each is answered for while the
  first prompt waits. With several windows on one repository, only one of
  them answers for it, and only one of them can resume it (#2339).
- **The window warns you when the platform refused to write your workspace.**
  When your role on the team cannot create or update the workspace, its
  repositories stay unlinked and triggers for them are refused; the warning
  names the workspace and the permission needed, and the workspace sync
  status shows the sync as failed rather than synced (#2372).
- **A command from the phone app or the dashboard for a run no window has is
  answered again within about two seconds,** instead of waiting five minutes
  to expire. With several windows open, only the one running the pipeline
  answers for it, so the app never shows a stop or a pause as refused while
  the run is stopping or holding, and a pause and a resume sent together
  apply in that order, so the run is never left paused after a resume. After
  a window reload, a command for a run that did not survive it is answered
  once the reloaded window has had a minute to come back (#2357).
- **Cancelling a run from the phone app or the dashboard before it starts
  works.** A run still waiting for a free slot, or whose worktree was being
  created, refused the cancel as not started. It is now removed and never
  starts, even when the cancel arrives the moment the run is triggered or
  picked up for a slot, and triggering the same issue again runs normally.
  Triggering an issue you already queued, even one already starting, follows
  your queued run, and cancelling it from there leaves your run as you queued
  it; a trigger that asks for its own adapter and model is refused instead,
  since your run uses yours (#2344).
- **Approve and reject from the phone app or the dashboard say why they
  cannot apply:** no run in the extension waits at an approval gate, so the
  window running the pipeline answers both with that reason (#2336).
- **A pause of a paused run, or a resume of a running one, from the phone app
  or the dashboard no longer shows the opposite state there.** The window now
  answers that the run was already in that state instead of refusing the
  command, which made the platform restore the run's earlier status (#2341).
- **Another window no longer undoes a pause or resume from the phone app or
  the dashboard.** With two windows open on the same repository, the one not
  running the pipeline could answer first that it had no such run, and the
  platform then restored the run as if the pause had not happened. Only the
  window running the pipeline answers now (#2340).
- **A pipeline push is no longer killed after 30 or 60 seconds.** A
  repository's pre-push hook may scan what it is about to send, and this
  repository's publication hook does, which can take longer than that on a busy
  machine. The orchestrator now gives every push ten minutes, the one that
  saves a run's work when its budget stops it included (#2365).
- **`pipeline.performance_mode.default` is reported as invalid.** The key was
  documented as the mode used when no state file is present, but the extension
  never read it. Choose the mode with the status-bar picker, or set
  `NIGHTGAUGE_PERFORMANCE_MODE`, and remove the key (#2343).
- **A `stall_kill_multiplier` under `performance_mode` or `supercharge` no
  longer changes every stage's stall window.** Only
  `pipeline.stall_kill_multiplier` itself sets the global multiplier (#2378).
- **A sub-issue whose epic lives in another repository is based on its own
  default branch.** The base-branch check after issue pickup and the concurrent
  slots used to look for the epic's branch by number in the sub-issue's
  repository, where it belongs to a different issue, and the check created one
  there when none existed (#2377).
- **An auto-retro issue is no longer linked under another repository's
  issue.** With `feedback_loop.auto_retro.auto_create_issues` on, the issue
  filed for a failed sub-issue was made a sub-issue of the parent epic's
  number in the failed issue's repository. It is now linked only when the
  epic is in that repository (#2377).
- **Pause, resume, cancel, approve and reject from the phone app or the
  dashboard are acknowledged.** Each one used to show as unacknowledged and
  then expired, even when it had been carried out. Pause and resume now work:
  the run holds before its next stage, as with `Nightgauge: Pause Pipeline`,
  and resume continues from there. The status bar and the pipeline view's
  Pause/Resume button follow them, and a command delivered twice is carried
  out once (#2334).
- **A pause holds the run before the next stage starts** even when it lands
  just after a stage finished; the next stage used to run in full first
  (#2334).
- **Remote commands reach this window when the platform routes them to the
  background service's agent** (#2335).
- **Interactive Codex starts without a shell command.** The terminal now runs
  `codex` directly with the stage prompt as its argument, instead of decoding
  the prompt from a temporary file in a shell (#2321).
- **A parked OpenCode failure no longer halts the whole repository queue.**
  The issue is held for you and the rest of the queue continues (#1753).
- **Auto-routing keeps OpenCode as a candidate when `opencode.model` is set**
  (#2330), and never hands it a model name it cannot run (#1725).
- **No install hint pipes a download into a shell.** The Grok install hints
  link to the Grok documentation instead (#2320).

## [0.5.1] - 2026-09-30

**0.5.1 is the first 0.5 build on the normal release channel.** 0.5.0 reached
the registries only as a preview. **It moves your files, and you should not
downgrade afterwards.** The bundled binary moves per-clone state out of
`.nightgauge/` on first use and machine state (including `machine-id`) out of
`~/.nightgauge`. After upgrading, do not run a 0.4.x extension or binary on the
same machine: it reads `machine-id` from the old path and would register the
machine as a new device. Every location is listed in
[Where Nightgauge keeps its data](https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md#where-nightgauge-keeps-its-data).

### Changed

- **No preview channel.** Every version of the extension is published as a
  normal release (#2305).
- **The listing page shows the demo.** The README now leads with the same
  31-second tour the repository README shows (#2303).

## [0.5.0] - 2026-09-30

**Upgrading from 0.4.x: this release moves your files, and you should not
downgrade afterwards.** The bundled binary moves per-clone state out of
`.nightgauge/` on first use and machine state (including `machine-id`) out of
`~/.nightgauge`. After upgrading, do not run a 0.4.x extension or binary on the
same machine: it reads `machine-id` from the old path and would register the
machine as a new device. Every location is listed in
[Where Nightgauge keeps its data](https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md#where-nightgauge-keeps-its-data).

### Added

- **Doctor panel.** `Nightgauge: Run Doctor` runs every doctor check and
  shows its progress, then one card per finding, grouped by severity: the
  finding's code (linked to its reference), title, cause and evidence. A
  card's buttons follow its fix: **Fix** applies a safe fix, **Review & fix**
  shows the preview in the card and asks before applying, and **Open** and
  **Check again** serve fixes only you can make. After a fix the card shows
  whether the check still reports the problem, without reloading the panel.
  Housekeeping is collapsed, with **Fix all safe**. Adapter health is the
  panel's Adapters group.
- **Doctor status bar item.** `Nightgauge: ✓ healthy`, `N warnings` or
  `N blockers`; click it to open the Doctor panel. It follows every doctor
  result and rescans in the background at most every ten minutes, without
  probing adapters.
- A "Demo" status bar badge appears whenever the extension is connected to
  the demo daemon, so a recording never passes for a real run. In demo mode
  scenario UI steps can open the dashboard, switch its tab, focus a
  Nightgauge view or expand the active issue, and the platform event streams
  and agent heartbeat stay off.
- Stages routed to the `sonnet` band now run Claude Sonnet 5.5
  (`claude-sonnet-5-5`), priced at $2/$10 per MTok.

### Fixed

- The Pipeline view shows a pipeline that was already running when the
  extension connected, instead of "No issue active" until its next stage.
- In demo mode the Runs, Cost and Trends tabs show the demo's data instead of
  asking you to sign in.
- The Overview's Project Board Summary shows the real counts when the
  dashboard opens during startup, instead of zeros until you press refresh.
- The Audit Trail tab shows your local run history when `platform.enabled` is
  false, instead of "No Access", and no longer contacts the platform then.
- The dashboard's first Refresh now shows health metrics when run history
  exists, instead of "Run your first pipeline" until a second Refresh.
- Stage cost for Claude Sonnet 5 is now priced at $2/$10 per MTok instead of
  $3/$15, because the announced price increase was cancelled.

### Changed

- The Claude usage meter reads `usage/claude-rate-limits.json` from the
  Nightgauge machine-state directory (`NIGHTGAUGE_STATE_HOME`, then
  `$XDG_STATE_HOME/nightgauge`, then `~/.nightgauge/state` on macOS or
  `~/.local/state/nightgauge` on Linux) instead of `~/.nightgauge/usage/`,
  the same place the `nightgauge hook claude-statusline` writer now uses.
  `nightgauge doctor --fix` moves an existing file.
- The extension reads the machine-tier `config.yaml` only where the binary
  does (`NIGHTGAUGE_CONFIG_HOME`, `$XDG_CONFIG_HOME/nightgauge`, then the
  platform default). On Linux it no longer falls back to a legacy
  `~/.nightgauge/config.yaml`; `nightgauge doctor --fix` moves that file.

- The extension's own diagnostic logs — the "Nightgauge" output channel's disk
  sink and the IPC transport log (`ipc-client.log`) — now write under VS
  Code's per-extension `logUri` directory (visible through "Open Extension
  Logs Folder") instead of the workspace's `.nightgauge/logs/`. Neither is
  read by the Go binary, so nothing changes for pipeline session logs or the
  sanitization log, which still land under the shared per-clone log directory
  (#2030).
- Run state, contexts and history, plans, retros and pipeline logs now live
  in the clone's git directory (`.git/nightgauge/`; from a linked worktree,
  the main clone's) instead of the working tree's `.nightgauge/`, so nothing
  the pipeline produces can show up in `git status` or be committed. Every
  checkout of a clone shares them. The extension asks the `nightgauge` binary
  where they are once per workspace folder at activation, and no longer
  creates `.nightgauge/pipeline`, `plans` or `logs` directories or `.gitkeep`
  files. Outside a git repository these views stay empty (#2037).
- The extension activates automatically in a workspace containing
  `.nightgauge/config.yaml`, rather than one containing `.nightgauge/pipeline`
  or `.nightgauge/plans` (#2037).

### Removed

- The `ui.core.context_path`, `ui.core.plans_path`, `pipeline.logs.dir` and
  `automations.log_file` settings. These locations are the clone's own and
  have no override (#2037).
- The **Adapter Doctor** command (`nightgauge.adapterDoctor`) and its panel.
  Run **Nightgauge: Run Doctor** instead; adapter health is its Adapters group.
- The remote-command status bar item and the `remote.notifyOnPipelineRun`
  setting. They reported a command poller that never received a command
  (#2113).

### Security

- `nightgauge.backend.binaryPath`, `nightgauge.plugins.marketplaceUrl` and
  `nightgauge.dashboardUrl` are now machine-scoped: a value in a workspace's
  `.vscode/settings.json` is ignored, so a repository cannot choose which
  binary the extension runs. Set them in your user settings (#2044).

## [0.4.8] - 2026-09-25

### Security

- The extension you install from the VS Code Marketplace or Open VSX is now
  the exact file attached to the GitHub Release: same SHA-256 as the
  release's `checksums.txt`, covered by the same provenance attestation.
  Registry builds used to be a separate rebuild with a different digest
  (#2151).

## [0.4.7] - 2026-09-25

### Removed

- **Breaking:** the LM Studio and Ollama execution adapters, and their
  settings panels, are removed (#2128). Run local models with the OpenCode
  adapter against any OpenAI-compatible server. A config that still selects
  `lm-studio` or `ollama` now fails with an error that names the setting and
  the replacement.

### Fixed

- **The auto-retro's `adapter_incompatible` advice no longer mentions a
  max-tested self-test (#2147).** A harness newer than the tested version is
  never refused, so the advice names only the floor and an unreadable
  version.

- A cap-recovery adapter hop from the Go scheduler now reaches every pipeline
  stage, not only refinement (#1656).

- The generated `.nightgauge/.gitignore` (template version 16) no longer ignores
  `/knowledge/`, so scaffolded knowledge shows as new files to commit; only the
  derived `/knowledge/.recall-cache/` stays ignored. Opt out in the root
  `.gitignore` (#2042).

### Added

- The usage meter and the Dashboard's usage panel show token counts and a
  "Local model" badge for a model running on your own LM Studio or Ollama
  server, instead of "usage unknown" (#1665).

- A dashboard or mobile trigger can name the adapter and model its run executes
  on. The extension asks the Go binary to validate the pair before acking; a
  pair this machine cannot serve is acked `rejected` with a short reason
  category and never queued. A valid pair pins every stage, including retries
  and auto-started queue items, and is never swapped for another adapter or
  model (#1656).

- Pipeline health reads each OpenCode stage's context-window utilization and
  compaction count from the run history (#1653), so a stage that uses under
  30% of its model's window on average is reported as a low-utilization
  pattern.

### Fixed

- The license key no longer disappears for the CLI and daemon once the
  extension has run. Activating a license, starting a trial, saving the key in
  Settings and the startup migration now also store it in the OS keychain
  through `nightgauge auth license set`, so `nightgauge serve` and
  `nightgauge pipeline backfill` from a terminal still find it. The key is
  removed from `~/.nightgauge/config.yaml` only after that succeeds; if it
  fails, the key stays and one warning names the command to run. If you
  change the key from a terminal (`nightgauge auth license set`), VS Code
  notices on the next start, stops using its old copy, and asks you to
  activate the current key.

- The generated `.nightgauge/.gitignore` (template version 15) now ignores
  `.nightgauge/worktrees/`, so a pipeline worktree no longer shows up in
  `git status` as an embedded repository, and the per-machine files it
  missed: the chat-command authorization log, the work graph, the focus lens,
  the performance mode, the careful-mode lock, the audit queue and two
  reports. An older extension no longer downgrades a newer file, and an
  upgrade moves custom rules found outside the `Local additions` section into
  it instead of dropping them. The Nightgauge CLI now writes the
  same file from `config init` and `serve`, so a clone that never opened VS
  Code gets it too.

- An attention sweep that outlives the 30-second request deadline no longer
  leaves the window "never swept": the daemon finishes it and its cards still
  arrive, and later triggers in that window ask the board change probe
  instead of sweeping again. A window reload remembers the last completed
  sweep, so activation asks the probe too; only the Attention Sweep command
  always sweeps. The timer keeps its schedule even when a sweep started a
  little late, so checks the probe cannot see (default-branch CI, Dependabot,
  branch protection) are not delayed to twice the interval.

- The Repositories view reads each project board once per refresh instead of
  three times per repository, and serves re-expands and daemon reconnects from
  cache without touching GitHub. Row counts on a board shared by several
  repositories now show each repository's own issues.

### Changed

- A GitHub token in `.nightgauge/config.yaml` or `.nightgauge/config.local.yaml`
  is used only when it is an `env:VAR_NAME` reference (#2023). A literal token
  there is no longer exported to terminals and subprocesses as `GH_TOKEN`; the
  binary refuses such a config. Put a literal token in the machine config file,
  or name the account in `github_user` and let `gh` hold it. The extension now
  finds that file where the binary does (`NIGHTGAUGE_CONFIG_HOME`,
  `XDG_CONFIG_HOME`, `~/.config/nightgauge` on Linux), not only in
  `~/.nightgauge`.
- A `platform.license_key` in the workspace's `.nightgauge/config.yaml` is no
  longer imported into the extension's secret storage at startup, so a cloned
  repository cannot replace your license key (#2023). The extension warns
  instead, and leaves the file alone.

- OpenCode stages now run from the editor. They check the experimental
  switch, the `opencode` CLI, the Nightgauge binary and a configured model
  before launch, and refuse interactive mode. Each model step and tool call
  counts as activity while the stage runs. The model comes from the stage or
  `opencode.model`. Stage cost, the cost cap and stall thresholds follow the
  model's provider. A local model costs $0, uses time-cap mode, and gets its
  own stall calibration with a floor that its thresholds never go below. A
  hosted model with no registry price also runs in time-cap mode, and
  time-cap mode now defaults to a 4-hour cap when none is configured. The
  activity lines never appear in Run Stage or the slot output channels.

- Stages routed to the `opus` band now run Claude Opus 5.5
  (`claude-opus-5-5`), and stage cost is priced at its $4/$20 per MTok rates.

## [0.4.6] - 2026-09-21

### Changed

- The Go binary bundled in this extension is now built with `-trimpath`, so it
  no longer embeds build-machine paths and a given release can be rebuilt from
  its tag and hash-compared. Every published .vsix is also malware-scanned
  before it is attested or uploaded.

### Fixed

- Phase progress within a stage is visible while the stage is worked, rather
  than appearing all at once when it ends. The ordering-derived phases added in
  0.4.5 were computed and then discarded before they reached the pipeline
  state, so a long stage still showed a handful of updates followed by a burst.

## [0.4.5] - 2026-09-20

### Fixed

- The extension's GitHub spending is now visible to the API ledger. The Go
  daemon is started in the workspace folder it serves, instead of inheriting
  the extension host's working directory — which is why a daemon that had been
  sweeping six repositories for three hours had written no ledger records at
  all. Project-board field and iteration writes now run their GraphQL through
  that daemon rather than shelling out to `gh api graphql`, so they are
  recorded and rate-limit-gated like every other call; with no daemon
  connected they still fall back to the subprocess.
- The pipeline tree shows phase progress as it happens, not in a burst at the
  end of each stage. Most phases previously never lit up at all: a stage
  reported a handful of landmarks and the rest arrived at the end-of-stage
  back-fill as `unreported`. Because phases are ordered, seeing one phase is
  evidence the run is past the earlier ones, so those are now settled live
  under a new `passed` status — rendered distinctly from `complete`, since the
  phase was never observed running and a check mark would claim otherwise.

- The attention sweep no longer runs on its timer just because the editor
  window has focus. A sweep costs roughly 64 GraphQL points plus 18 REST
  calls across a six-repo workspace, and window focus was the timer's only
  condition — so reading unrelated code with Nightgauge installed spent
  about 256 GitHub points an hour, per open window, with autonomous mode off
  and no pipeline run in flight. On a busy account that is enough to help
  exhaust the hourly limit for every other tool sharing the token. The timer
  now also requires the autonomous dispatch loop to be running. Arriving at
  the window, refreshing the repositories view, a run finishing, and
  regaining focus after idling all still sweep as before.

- The Output window is no longer blind to pipeline runs started from a
  terminal (#586). A run discovered by CLI reconciliation now gets its own
  Output window tab — marked `CLI` — which is removed when that run settles.
  Every slot tab's panel opens with a strip naming the issue, repository and
  run id it is showing, so no panel can be misread as the current run, and a
  tab with no stream says where the stream is instead of showing nothing or
  the previously selected run's output. A CLI run's live output still lands in
  its launching terminal; this makes the extension honest about that rather
  than appearing to display it.

- The pipeline tree view no longer shows a successful auto/CLI-mode run of
  `feature-planning`, `feature-dev` or `feature-validate` as `0/N phases · N
abandoned` (#1885). The Go execution path now completes a phase when the
  next one starts and settles the last phase `complete` — not `abandoned` —
  at a successful stage boundary, matching the IPC path's existing behavior.
  `abandoned` still appears when a stage genuinely ends abnormally.

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

## [0.4.4] - 2026-09-17

### Changed

- The `nightgauge` binary bundled in the extension is now signed with an Apple
  Developer ID certificate and notarized by Apple, with the hardened runtime
  enabled. Previously it carried only an ad-hoc signature, which macOS treats
  as unsigned. If Gatekeeper warned you when the pipeline first started its
  backend, it should no longer.

### Fixed

- Install instructions now lead with Open VSX, and document installing the
  `.vsix` for your platform directly in VS Code with
  `code --install-extension <file>.vsix` (or **Extensions: Install from VSIX**).
  Every release attaches all three per-target builds, so VS Code users install
  the same artifact either way.

### Added

- The listing now includes "What this extension does on your machine": the
  bundled Go binary and the fact that nothing is downloaded at runtime, which
  processes get spawned and why, that your existing `gh` credentials are used
  and none are stored, the bundled shell hooks, the one file written outside
  your workspace and the confirmation shown first, and every path by which data
  leaves the machine.

### Changed

- The Grok adapter's setup message links the install page instead of printing a
  `curl … | bash` command.

### Fixed

- A pipeline run that succeeded completely no longer renders in the tree with a
  red ✗ on it. A phase belonging to an earlier attempt of a re-run stage was
  kept as the current attempt's verdict, so `pr-merge` showed a failed
  freshness check on the very run that merged the PR. Phase rows now belong to
  the attempt that produced them.
- A deterministic stage that hands work to the AI path no longer marks that
  phase failed. It shows as superseded — the work is being done, just by the
  other path. A phase that genuinely did not succeed on a stage that completed
  anyway shows as degraded, with a warning rather than an error icon.
- Feature Validation now shows phase progress. It reported nothing at all for
  the whole stage, so a four-minute validation looked like a stage doing
  nothing; progress is inferred from the work it is observed doing, as Feature
  Development already did.
- A stage whose phases could not be measured now says "phases not reported (N)"
  instead of "0/N phases", which read as though the stage had done none of its
  work.

## [0.4.3] - 2026-09-16

### Fixed

- 0.4.2 reached Open VSX for Apple Silicon only. The publish aborted partway on
  a transient registry error, so the Intel Mac and Linux builds of that version
  were never uploaded. 0.4.3 is the same extension published for all three
  targets. If you are on Intel macOS or Linux and 0.4.2 never appeared, this is
  the version to install.
- The bundled markdown renderer moves to marked 18.0.13, picked up
  automatically by the build from the extension's own pinned dependency.

## [0.4.2] - 2026-09-16

### Fixed

- The Output Window no longer fetches its markdown renderer from a CDN. marked
  is vendored from the extension's own pinned dependency into
  `dist/vendor/marked.umd.js` and loaded through `webview.asWebviewUri`, so the
  webview executes only code that ships inside the package and its
  Content-Security-Policy names no remote origin. This is required by both the
  Visual Studio Marketplace Publisher Agreement and the Open VSX publishing
  terms. It also restores markdown rendering in the Output Window, which had
  been falling back to escaped HTML since marked stopped publishing
  `marked.min.js` in v5 and the CDN request began returning 404.

### Changed

- The README now names both registries the extension is published to — the
  VS Code Marketplace and Open VSX — with links to each listing (#1788)
- OpenCode now appears, labeled Experimental, in the switch-adapter
  quick-pick, the per-stage and global adapter dropdowns, and the Adapter
  Doctor view. Choosing it from the quick-pick while
  `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1` is unset shows how to enable it instead
  of writing the adapter. A new `OpenCodeModelCatalogService` lists
  `provider/model` ids from `opencode models` (bounded `execFile`, 10s
  timeout) for "Run Pipeline with Model" and the settings panel, with the
  configured model first and a non-throwing fallback when the CLI is missing
  or times out (#1628)
- `opencode` is now a selectable execution adapter: `ui.core.adapter`,
  per-stage `pipeline.stage_adapters` and `pipeline.adapter_fallback_chain`
  entries all accept it, and picking it from the auto-router no longer
  refuses to dispatch the stage (#1623)
- The Adapter Doctor checks the claude CLI against a 2.1.223 floor. A claude
  below it shows its version as a warning and stays ready (#1613)

### Fixed

- Run records with the `permission_denied`, `context_window_exceeded`,
  `adapter_permission_rejected` or `adapter_incompatible` terminal kinds keep
  their kind instead of falling back to the V2 schema, and the auto-retro
  names a remediation for the three new OpenCode (Experimental) kinds: a
  larger context, another model or a split issue; for a rejected tool, the
  OpenCode `ask` rule behind it, including OpenCode's own `.env` read guard,
  which is never loosened; the max-tested OpenCode build, pinned. Each ends
  with the `nightgauge autonomous clear-failures` command that releases the
  issue (#1631)
- Codex stages no longer leave generated `AGENTS.md` steering in your commits:
  if the agent committed it, the stage's cleanup adds a commit removing it, and
  the steering summary now comes from `AGENTS.md` rather than `CLAUDE.md`
  (issue 1675)
- Board views now show every sub-issue of an epic and every blocker of an
  issue. They stopped at the first 12 sub-issues and 5 blockers, so an issue
  whose only open blocker came later showed as unblocked (#1682)

### Security

- `Nightgauge: Setup Grok Skills` and `Nightgauge: Setup Codex Commands` now
  install only the skills and Codex commands bundled with the extension, never
  the open workspace's `skills/` or `.codex/commands/` folder. To install
  skills from a checkout while developing them, run
  `Nightgauge: Install Grok Skills from This Workspace (Development)` or its
  Codex counterpart: it refuses an untrusted workspace or a destination that
  contains or sits inside the source folder, and asks you to confirm the source
  and destination paths it names. Linked files are copied only from inside the
  skills folder and linked folders are skipped. An existing skill folder is
  replaced only when it holds the `.nightgauge-installed` marker the extension
  now writes. Any other folder is left untouched and named in a warning in the
  output channel. That includes a folder an earlier version installed and one
  `scripts/install-agent-skills.sh` installed or refreshed, because the script
  writes no marker. Delete such a folder to let the extension install it
  (#1683)

## [0.4.1] - 2026-09-11

### Fixed

- Extension publication now retries transient Marketplace and Open VSX token
  verification failures, preventing a temporary registry outage from blocking
  an otherwise valid stable release (#1599)

## [0.4.0] - 2026-09-11

### Changed

- Marketplace releases now use even `0.x` minor lines for normal installs and
  odd minor lines for opt-in previews, so the release version installs without
  requiring users to select **Install Pre-Release** (#1594)

### Added

- Grok is a first-class skill consumer. Activating the extension (or running
  `Nightgauge: Setup Grok Skills`) installs Nightgauge skills into
  `~/.grok/skills` from the bundled VSIX, and marketplace Codex installs now
  copy bundled skills into `~/.codex/skills` even when the workspace has no
  `skills/` directory. Progressive-disclosure `Read` paths are skill-relative
  so they resolve outside this checkout.

- An Action Center card for a green PR that is behind its base branch now offers
  "Update the branch from the base" as its primary action, with dismiss as the
  secondary. Previously the only option was dismiss, so clearing three stale
  dependabot PRs meant leaving the sidebar and doing by hand what one click can
  now do (#1575)

### Fixed

- Post-pipeline outcome recording now uses the correct complexity-model path and
  the Go-owned transaction, allowing safe first-run calibration through the
  shared automatic bootstrap (#1590)

## [0.3.1] - 2026-09-07

### Fixed

- A usage cap no longer stops every repo for an hour. A cap hit while running
  `fable` now descends the tier ladder and the run continues, and the fleet-wide
  quota cooldown applies only after both the tier ladder and the provider
  fallback chain are exhausted. A cap-driven tier or provider change raises an
  Action Center card naming the stage, the destination and the reason (#1545)
- Stage progress in the pipeline view counts work that actually happened. A
  phase the stage skipped no longer counts toward the total, so a stage that
  reported nothing stops reading as nearly finished — `11/14 phases` on a stage
  whose rows were eleven skips, three unreported and nothing complete. A running
  stage that has not reported a phase now says so instead of showing a `0/18`
  that never moves, phases before the one being reported are no longer shown as
  complete on no evidence, and a long run of skipped phases collapses into one
  expandable row (#1558)
- A pipeline run that merges its own pull request now moves its issue's board
  row to Done. The row was left in In review even after GitHub had closed the
  issue, so finished work accumulated in a column that reads as a queue (#1562)
- A completed run no longer reports that it cleaned up a branch it did not
  delete (#1561)

## [0.3.0] - 2026-09-07

### Changed

- Settings changes made from the UI no longer dirty the committed
  `.nightgauge/config.yaml`. A concurrency change, an applied dashboard
  recommendation, a reset to the project tier and the startup `max_concurrent`
  migration now write `.nightgauge/config.local.yaml` (or
  `~/.nightgauge/config.yaml` for a personal key). Saving the Settings panel's
  **Project** tab still writes the team file — that is the one place that
  names it — and now preserves its comments and blank lines instead of
  reflowing the whole document (#1516)

- The knowledge base is on by default, so the Knowledge view and the New Entry /
  New ADR / Scaffold commands work in a workspace that never set
  `nightgauge.knowledge.enabled`. They previously reported "Knowledge base is
  disabled" whenever the setting was simply absent; only an explicit `false`
  disables them now (#1513)

### Added

- A pipeline run started from VS Code now labels its own issue: when
  feature-planning finishes and the issue carries no `size:*` label, the size
  the planner assessed is applied to it. Runs in this mode were recording no
  size at all, so the pre-flight cost estimate had almost no history to project
  from — and an existing size label is never overwritten (#1515)
- After Stop, the status bar says whether a window reload is safe —
  `Autonomous: Stopped — 2 running` while pipelines are still finishing, then
  `Stopped — safe to reload` when the last one lands, updating as they go. Stop
  lets running slots finish; reloading the window aborts them, and nothing used
  to say when the drain was done. `dev-install.sh` now names the running issues
  and asks before a rebuild that would kill them (`--force` skips) (#1511)
- `autonomous.refinement_backlog` is accepted in `.nightgauge/config.yaml`
  (default `false`). It turns on refinement of the open backlog; with it off,
  refinement only touches board work that is about to dispatch, plus anything
  labelled `auto-process` (#1514)

### Fixed

- Resolving a card from the Action Center works again. Clicking an option — and
  `attention resolve` / `ack` from the CLI — hung indefinitely with no error
  while the card list kept loading normally, so there was no working way to
  clear a card. The daemon-side write path could block forever behind its own
  event push; it is now bounded and reports what went wrong instead of never
  returning (#1539)

- The pipeline tree shows real phase progress for a deterministic
  issue-pickup. Pickup's primary path runs no LLM, so it emitted no phase
  markers and the stage rendered `0/14` while running and `14 unreported` when
  it finished. It now reports the waypoints it performs and marks the rest
  skipped with a reason, so the stage progresses live and settles at 14/14
  with nothing unreported. A skipped phase's reason is shown beside its status
  in the tree (#1534)
- A pipeline failure comment no longer says "PR creation failed" about a PR that
  is open and running checks. When pr-create is terminated after it verified an
  OPEN PR — the runaway monitor firing while the stage watched CI was the
  reported case — the report now names and links that PR and describes a
  post-create stall, instead of sending the operator to re-push a branch that
  already shipped. pr-create itself no longer waits for CI, so the kill that
  produced this report should not recur (#1531)
- Re-queuing an issue that already has an open pipeline PR no longer re-runs
  planning, development and validation. The run now fast-forwards to pr-merge on
  the strength of the `pr-{N}.json` the previous run left behind, instead of
  spending a second full pass to rediscover its own PR and push more commits
  onto it (#1531)
- The extension now executes issue refinement. The Go daemon dispatches it over
  the same stage bridge it uses for pipeline stages, and the refine skill runs
  headless in the checkout of the repo the issue belongs to. Before this,
  refinement was silently off for every extension user (#1529)
- An Action Center card no longer asks for a decision without showing what is
  being decided. Every card naming a repo and an issue (or PR) now leads with
  `Open in browser` — the link is derived when the producer set none — and a
  `View details` entry opens the card's reason, its options' consequences and,
  for an architecture-approval card, the run's plan file, without resolving
  anything (#1509)
- A pipeline stage is no longer failed for "writing outside its worktree" when
  another checkout's branch ref moved without its working tree, or when another
  running slot was simply working in its own repo. Both looked identical to a
  stage write and, on 2026-09-06, halted the fleet by killing three slots at
  once for one breach none of them committed (#1499)
- Validation stages that wait on a long test suite, docker run or emulator boot
  are no longer killed mid-run. The runaway monitor reads a declared child
  process, a growing declared log, and a tool call still in flight as proof the
  stage is working, so waiting on real work no longer looks like a stall (#1488)
- Pressing Stop no longer counts the stages it kills against their issues.
  A stopped run is recorded as `operator_stop` — exempt from the per-issue
  failure cap and from the cascading-failures breaker — so a pause no longer
  quarantines the work that was in flight or halts the whole workspace (#1487)

## [0.2.3] - 2026-09-05

### Added

- **One scheduler per workspace** — a second `nightgauge serve` attaches to the
  running scheduler instead of starting a rival (#1349)
- The GitHub API ledger is always on; the status bar meter and `nightgauge
api-usage --budget` read it, and the budget prices a board pull before it
  spends the hour's quota (#1347, #1428)
- Knowledge tree shows each entry's trust tier and provenance; `knowledge
export --okf` produces a portable bundle (#1369, #1371)
- The pipeline watches `main`'s own run after a merge and raises an Action
  Center card when the default branch goes red (#1249)
- Deterministic stages and the `pr-create`/`pr-merge` route report their
  phases live, instead of jumping from 0/14 to 14/14 skipped (#1247, #1397)
- `nightgauge doctor` flags foreign processes holding a pipeline worktree open
  (#519)
- Published to Open VSX alongside the Marketplace (#1316)

### Changed

- Pause/Resume Pipeline act on the live run, with a picker when more than one
  is live (#423)
- `nightgauge serve` shuts down through a bounded drain instead of abandoning
  board writes (#489)
- Paused or corrupt snapshot protection is bounded everywhere, and `worktree
sweep` names which arm protected each issue (#443)
- The GitHub auth pre-check and `doctor` accept fine-grained and GitHub App
  tokens (#1331, #1333)
- Board reads come from the daemon's snapshot cache; the rate-limit gate
  releases with jitter (#1343, #1344, #1346)

### Fixed

- A `MACHINE_LIMIT` license rejection is reported as a full seat, not "key not
  accepted" (#1334, #1454)
- Action Center: cross-process writes no longer tear a card (#1425); actions
  are attributed to the GitHub login (#1418); an operator steer reaches disk
  before its verb runs (#1407, #1410); a persisted card cannot carry a shape
  the platform rejects (#1405); a relayed resolve is never mistaken for a
  rejection (#1421); default-branch health raises an FYI when only
  non-required checks fail (#1250); event-driven sweeps are gated behind the
  board change probe (#1345)
- `nightgauge run <issue>` finds Ready issues again (#1337)
- The `autonomous.*` lifecycle handlers report the state they observed (#494);
  a second daemon cannot steal a live socket (#1429)
- The append's own retention prune no longer deletes the run record it just
  wrote (#1455); a failure before any stage persists its reason (#1329)
- A timed-out webview render still disposes its panel (#1327); the webview
  message-handler gate fails closed on unparsed forms (#1199); the dashboard
  firewall badge reflects the resolved sanitization mode (#986); the slot
  output channel is redacted at its sink (#1335); the last write-then-rename
  sites use a unique temp name (#786)
- A GitHub throttle is a transient backoff, not a lifetime failure (#1391); a
  credential-less push is booked as `git_transport_auth_failed` (#878)
- A retry escalation is not booked as a model-routing miss (#1002); a
  dimension with no data no longer votes in the health score (#1197)

### Security

- `golang.org/x/crypto` v0.56.0 (GO-2026-6354, GO-2026-6355) (#1323);
  `fast-uri` 3.1.7 (#1317)

## [0.2.2] - 2026-09-02

The first build published to the VS Code Marketplace (pre-release channel).
Same product as 0.2.1; the listing and the package were fixed before publishing.

### Changed

- The VSIX no longer ships source maps or type declarations (23 MB and 1,000+
  files smaller) and drops the plugin mirror's test files (#1306)
- Listing metadata: an `AI` category, `extensionKind: workspace`, and explicit
  untrusted-/virtual-workspace declarations (#1306)
- README is the Marketplace page: real dashboard and notification renders
  replace the hand-built mockups; contributor sections moved to the repository
  (#1306)
- Status bar uses the `dashboard` codicon (#1306)
- Release pipeline: one Marketplace publish path, pre-release packaging for
  0.x, and a guard that no source maps ship (#1300, #1307)

## [0.2.1] - 2026-09-02

First public version on the VS Code Marketplace, published as a **pre-release**
(0.x) on the Marketplace pre-release channel. Builds ship for **macOS (Apple
Silicon and Intel) and Linux x64 only**; there is no Windows build yet. The
Claude Code (`claude` CLI) adapter is the supported path; the Codex, Gemini,
Copilot and direct-API adapters are beta. Multi-repository workspaces and
autonomous mode are available but less finished than the single-repository
loop.

`0.2.0` was tagged on 2026-09-01 but never published: its release run resolved
the release-candidate tag on the same commit and stopped before attaching any
artifact. `0.2.1` is that tree plus the release-pipeline fix; nothing shipped
under `0.2.0`.

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

### Fixed

- Open GitHub and Open Log tree actions do what they say, and tree item ids
  are stable across refreshes (#1211, #1280)
- The settings webview's dead buttons work again, it no longer nags about
  setup on an uninitialized repo, and it reports its real auto-accept default
  (#905, #1067, #1117)
- Knowledge view finds the knowledge base in every worktree layout, and
  Related Decisions shows real decisions (#1224, #1225)
- Empty, blocked and awaiting-decomposition epics are told apart in the tree
  (#671, #681)
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
shipped as [0.2.1].

## [0.1.0] - 2026-01-15

### Added

- Pipeline orchestration sidebar with stage visualization
- Ready Issues view with GitHub Project board integration
- Dashboard with pipeline metrics and time savings tracking
- Context file viewer for inspecting pipeline state
- Auto-refresh capability for Ready Issues list
- Notification system with macOS alert sounds and system notifications
- Dock badge bounce for user attention (macOS)
- Output window with configurable verbosity levels
- Token usage and cost estimation display
- Automatic Claude Code plugin setup prompt
- Commands: Run Pipeline, Stop Pipeline, Refresh Pipeline
- Commands: Pick Up Issue, View Issue on GitHub
- Commands: Setup Claude Code Plugins, Show Dashboard
- Settings for authentication provider, model selection, and paths
- Settings for notification sounds, volume, and Do Not Disturb respect

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v0.4.8...HEAD
[0.4.8]: https://github.com/nightgauge/nightgauge/compare/v0.4.7...v0.4.8
[0.4.7]: https://github.com/nightgauge/nightgauge/compare/v0.4.6...v0.4.7
[0.4.6]: https://github.com/nightgauge/nightgauge/compare/v0.4.5...v0.4.6
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
