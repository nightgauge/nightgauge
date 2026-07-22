# Nightgauge

**The cockpit for the Nightgauge issue-to-PR pipeline, built into VS Code.**
Nightgauge guides work through planning, implementation, validation, and pull
request stages, and this extension is where you watch it
work: queue issues, approve plans, follow live pipeline state, and review
the evidence behind each run. A six-stage pipeline combines
documentation-first planning with deterministic checks at key boundaries:
a plan you approve before any code is written, a hard build/test gate, and
a manual test checklist before a PR is opened.

It is not a chatbot or an inline code-completion tool. It's a **pipeline
orchestrator**: each stage runs as a fresh AI agent that receives a
structured JSON handoff from the previous stage instead of the full
conversation history, so context stays focused and any stage can be retried
independently. Deterministic code verifies repository state and selected stage
outputs; provider behavior and manual review remain part of the trust model.

> **Screenshot pending.** A real screenshot/GIF of the sidebar and a
> completed pipeline run hasn't been captured yet — see
> [Screenshots Needed](#screenshots-needed) below. This is not a finished
> marketplace listing until that's done.

## Quick Start

1. **Install** — Before Marketplace publication, install the target-specific
   VSIX from the GitHub Release. Release-candidate testers should use the VSIX
   from the reviewed Actions run, not a local development build.
2. **Sign in** — Run **Nightgauge: Sign In with GitHub** from the
   Command Palette to connect your GitHub account.
3. **Initialize the repo** — Open the Nightgauge sidebar in a repository
   you want to automate. If it hasn't been set up yet, click **Initialize
   Repository** in the welcome view — this writes
   `.nightgauge/config.yaml`, standard labels, and links your GitHub
   Project board. Nothing is written until you opt in.
4. **Claim an issue** — Click **Nightgauge: Pick Up Issue** (or drag an
   item out of the _Ready_ list) to start the pipeline on a GitHub issue.
5. **Watch it run** — The pipeline moves through `issue-pickup` →
   `feature-planning` → `feature-dev` → `feature-validate` → `pr-create` →
   `pr-merge` automatically, pausing only for your plan approval and a manual
   test confirmation. When it finishes, you have a reviewed, issue-linked
   pull request.

### Installing

Download the VSIX matching your OS and architecture from the GitHub Release and
install it through **Extensions → … → Install from VSIX**. For extension
development, clone the repository and use `scripts/dev-install.sh`; that script
builds the working tree with a timestamped development version and is not a
release-validation path. Marketplace installation will be documented after the
first listing is live.

## Features

- **Command Palette Integration** - Run pipeline stages, dashboards, and
  utilities from the command palette
- **Status Bar** - Visual feedback showing current pipeline state
- **Settings** - Configurable authentication and model selection
- **Output Channel** - Structured logging for pipeline events
- **Pipeline Queue** - Queue issues for sequential processing when a pipeline is
  already running
- **Batch Processing** - Process multiple issues through the full pipeline with
  dynamic queue addition
- **Knowledge Value Dashboard** - Aggregates KB telemetry into header cards, a
  hit-rate gauge, per-stage bar chart, top-recalled/stale tables, and
  graduation history (see below)

## Privacy and Telemetry

Telemetry is off by default and requires an explicit opt-in. VS Code's global
telemetry-off setting is honored as a hard stop. When enabled, the
`pipeline-run` stream includes the repository slug and issue number as
correlation keys, plus bounded outcome, duration, and token counters; it never
sends source code, file contents, prompts, secrets, branch names, or commit
SHAs. Streams can be disabled independently at any time.

Read [Telemetry Privacy](../../docs/TELEMETRY_PRIVACY.md) before enabling
telemetry for the complete field list, retention policy, controls, and deletion
instructions.

## Commands

A full, current list of contributed commands is visible in VS Code's
Extensions view under this extension's "Feature Contributions" tab (sourced
directly from `package.json`). The most commonly used:

| Command                                      | Description                           |
| -------------------------------------------- | ------------------------------------- |
| `Nightgauge: Sign In with GitHub`            | Connect your GitHub account           |
| `Nightgauge: Pick Up Issue`                  | Claim an issue and start the pipeline |
| `Nightgauge: Run Stage...`                   | Run a single pipeline stage           |
| `Nightgauge: Show Dashboard`                 | Open the pipeline dashboard           |
| `Nightgauge: Open Knowledge Value Dashboard` | Open the Knowledge Value dashboard    |
| `Nightgauge: Stop Pipeline`                  | Stop the currently running pipeline   |
| `Nightgauge: Open Settings`                  | Open the visual settings panel        |

## Active Issue Knowledge Panel

When a pipeline issue is in progress, the **Active Issue Knowledge** panel
appears in the Nightgauge sidebar showing:

- **PRD.md** — product requirements for the active issue
- **decisions.md** — architecture decisions recorded during planning
- **Related Decisions** — semantically similar decisions from prior issues (powered by `nightgauge knowledge recall`)

The panel refreshes automatically when the active issue changes or when knowledge
files are modified on disk. Click any file item to open it in the editor.

<!-- TODO: capture a screenshot after a real pipeline run with
     populated knowledge files. docs/screenshots/ does not exist yet; do not
     reference an image path here until a real capture is added. -->

## Knowledge Value Dashboard

Opens a webview that aggregates
`.nightgauge/pipeline/history/knowledge-events.jsonl` into a single
"Is my Knowledge doing anything?" view (#3600).

Surfaces:

- **5 header cards**: writes / reads / recalls / hits / graduations, with
  delta vs. prior window
- **Hit-rate gauge** (`recall_hits / recalls`) with color-coded bands
  (green >50%, yellow 20–50%, red <20%)
- **Per-stage bar chart** of reads + writes
- **Top-recalled** table (paths sorted by read + recall_hit counts)
- **Stale-entries** table (paths whose last touch is older than the
  configured `stale_days` threshold)
- **Graduation history** timeline

Window selector: 7 d / 30 d / 90 d. Manual Refresh button + auto-refresh
every 5 min while the panel is visible. Empty states for "telemetry
disabled" and "no events in window" are rendered with actionable
instructions.

**Enable telemetry** by adding to `.nightgauge/config.yaml`:

```yaml
knowledge:
  telemetry:
    enabled: true
    stale_days: 30
```

Identical data is available on the CLI:

```bash
nightgauge knowledge metrics --window 7 --stale-days 30 --json
```

<!-- TODO: capture after the extension is dev-installed and
     a meaningful amount of telemetry has accumulated. -->

## Settings

Configure the extension via VS Code Settings (`Preferences > Settings`):

| Setting                   | Default                | Description                                  |
| ------------------------- | ---------------------- | -------------------------------------------- |
| `nightgauge.authProvider` | `max`                  | Authentication provider (max/bedrock/vertex) |
| `nightgauge.defaultModel` | `sonnet`               | Default model (sonnet/opus/haiku)            |
| `nightgauge.contextPath`  | `.nightgauge/pipeline` | Path to pipeline context files (git root)    |
| `nightgauge.plansPath`    | `.nightgauge/plans`    | Path to plan files (git root)                |

The full settings surface (adapters, orchestration budgets, notifications,
telemetry, and more) is best browsed through
**Nightgauge: Open Settings**, which reads/writes the layered
`.nightgauge/config.yaml` tiers directly.

## Status Bar

The status bar shows the current pipeline state:

| State    | Display                    | Action on Click |
| -------- | -------------------------- | --------------- |
| Idle     | `$(nightgauge) Nightgauge` | Run Pipeline    |
| Running  | `$(sync~spin) {Stage}`     | Stop Pipeline   |
| Complete | `$(check) Complete`        | Run Pipeline    |
| Error    | `$(error) Error`           | Show Dashboard  |

## Ready Issues View

The Ready Issues sidebar displays all issues with "Ready" status from your
GitHub Project board, intelligently sorted to help you focus on the highest
value work.

**Smart Sort (Default)**:

Issues are automatically sorted by:

1. **Priority** - Critical (P0) → High (P1) → Medium/Low (P2) → Unprioritized
2. **Blocked Status** - Unblocked issues appear before blocked ones
3. **Size** - Smaller issues (XS, S) before larger ones for quick wins
4. **Age** - Older issues (lower numbers) first as tiebreaker

**Configuration**:

Change the sort order via Settings → Nightgauge → Ready Items → Sort By:

| Sort Option  | Description                                     |
| ------------ | ----------------------------------------------- |
| Smart        | Priority → Unblocked → Size → Age (Recommended) |
| Board        | Preserve GitHub Project board order             |
| Priority     | Sort by priority labels only                    |
| Number       | Sort by issue number                            |
| Size         | Sort by t-shirt size only                       |
| Dependencies | Topological sort (unblocked issues first)       |

**Epic Grouping**:

When epic grouping is enabled, board tabs show the complete epic — all
sub-issues — whenever at least one sub-issue matches the tab's status. Blocked
sub-issues display with lock icons (🔒) indicating they are waiting on a
predecessor. This gives full visibility into the pipeline sequence so you can
run an epic as a batch and watch issues unblock as each phase completes.

**Auto-Refresh**:

Enable auto-refresh in settings to keep the list up-to-date without manual
refreshes.

## Pipeline Queue

When a pipeline is already running and you try to pick up another issue, the
extension automatically queues the new issue for processing after the current
pipeline completes (Issue #346).

**Key Features**:

- **Automatic Queueing** - Issues are queued automatically when a pipeline is
  active
- **Priority Sorting** - Queue items are sorted by priority (P0-P3), then size
  (S-XL), then issue number
- **Dynamic Addition** - Add issues to the queue while batch processing is
  running
- **Token Estimation** - Queue displays estimated token consumption based on
  issue size labels
- **Visual Feedback** - Queue section in the sidebar shows all queued issues
  with position indicators

**Size Label to Token Estimates**:

| Size Label | Token Estimate |
| ---------- | -------------- |
| XS         | 5,000          |
| S          | 10,000         |
| M          | 20,000         |
| L          | 40,000         |
| XL         | 80,000         |
| (no label) | 20,000         |

## Architecture

The extension acts as a UI layer on top of the SDK:

```
┌─────────────────────────────────────────────────────────────────┐
│  VS Code Extension (nightgauge-vscode)                              │
│  ├── Commands (runPipeline, runStage, showDashboard)            │
│  ├── Settings (auth provider, model selection)                   │
│  ├── Status Bar (pipeline state visualization)                   │
│  └── Output Channel (logs)                                       │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ imports & uses
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  @nightgauge/sdk                                            │
│  ├── PipelineOrchestrator (orchestrates pipeline execution)     │
│  ├── EventBus (stage:start, stage:complete, approval:needed)   │
│  ├── TokenTracker (usage metrics)                               │
│  └── ContextManager (context file I/O)                          │
└─────────────────────────────────────────────────────────────────┘
```

## Event-Driven Updates

The extension subscribes to SDK events for real-time UI updates:

| SDK Event           | UI Update                              |
| ------------------- | -------------------------------------- |
| `stage:start`       | Status bar shows "Running: {stage}"    |
| `stage:complete`    | Status bar shows "{stage} complete"    |
| `stage:error`       | Status bar shows "Error", notification |
| `approval:needed`   | Modal dialog for user approval         |
| `pipeline:complete` | Status bar shows "Complete"            |
| `token:usage`       | Logged to output channel               |

## Development

### Setup

```bash
npm install
```

No registry authentication is required — this repo depends on no private
packages. Building the bundled Go binary additionally needs the Go toolchain
(see `go.mod` for the required version).

### Building

```bash
cd packages/nightgauge-vscode
npm install
npm run build
```

### Packaging

```bash
npm run package
```

This creates a `.vsix` file that can be installed in VS Code.

### Testing

Run the test suite:

```bash
npm run test        # Run tests in watch mode
npm run test:run    # Run tests once
```

To manually verify a packaged build:

1. Run `npm run package` to create the `.vsix` file
2. In VS Code: `Extensions > ... > Install from VSIX...`
3. Select the generated `.vsix` file

Full contributor documentation (key directories, adding commands/settings,
multi-repo workspace notes) is maintained in the project repository.

## File Structure

```
packages/nightgauge-vscode/
├── src/
│   ├── extension.ts              # Main entry point
│   ├── commands/
│   │   ├── index.ts              # Command registration
│   │   ├── runPipeline.ts        # Run full pipeline
│   │   ├── runStage.ts           # Run individual stage
│   │   ├── showDashboard.ts      # Dashboard
│   │   └── stopPipeline.ts       # Stop running pipeline
│   ├── config/
│   │   └── settings.ts           # Settings accessor
│   └── utils/
│       ├── logger.ts             # OutputChannel wrapper
│       └── statusBar.ts          # Status bar management
├── package.json                  # Extension manifest
├── tsconfig.json                 # TypeScript config
└── README.md                     # This file
```

## Repository Configuration Requirements

### Auto-Merge Setting

Disable the repository's `allow_auto_merge` setting before running the pipeline.
The extension monitors this setting on workspace load and displays a warning
notification if enabled.

**Why:** The pipeline's `pr-merge` stage requires exclusive control over PR
merging to detect failures, apply corrections, and keep the UI in sync.
Auto-merge bypasses these mechanisms and causes stale pipeline state.

**To disable via CLI:**

```bash
nightgauge repo disable-auto-merge --owner <org> --repo <repo>
```

**To disable via VSCode:** Open the Command Palette and run
`Nightgauge: Disable Repository Auto-Merge`, or click "Disable Auto-Merge"
in the warning notification that appears on workspace load.

---

## Requirements

- VS Code 1.85.0 or higher
- Node.js 18.0.0 or higher

## Screenshots Needed

Tracked for #4155 — none of these exist yet. **Do not consider the
marketplace listing finished until real captures replace this list:**

- [ ] Nightgauge sidebar (Pipeline + Repositories + Ready Items views)
      on a real repository
- [ ] A pipeline run in progress (stage tree with live status)
- [ ] A completed pipeline run that produced a linked pull request
- [ ] Active Issue Knowledge panel with populated PRD.md/decisions.md
- [ ] Knowledge Value Dashboard with real telemetry

## License

[Apache-2.0](LICENSE)

## Author

nightgauge
