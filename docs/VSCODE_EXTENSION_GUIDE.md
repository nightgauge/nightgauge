# VSCode Extension User Guide

Complete guide to using the Nightgauge VSCode extension — from installation to mastering the pipeline and project board management.

## Table of Contents

1. [Installation & Setup](#installation--setup)
2. [Getting Started](#getting-started)
3. [User Interface Overview](#user-interface-overview)
4. [Pipeline Execution](#pipeline-execution)
5. [Project Board & Issue Management](#project-board--issue-management)
6. [Commands Reference](#commands-reference)
7. [Settings Reference](#settings-reference)
8. [Keyboard Shortcuts](#keyboard-shortcuts)
9. [Troubleshooting](#troubleshooting)
10. [Advanced Features](#advanced-features)

---

## Installation & Setup

### Prerequisites

- **VSCode 1.85.0+** or later
- **Node.js 18.0.0+** or later
- **Claude CLI** installed and authenticated (`brew install anthropic/brew/claude-cli` on Mac)
- **GitHub CLI** (`gh`) installed and authenticated
- A GitHub repository with Nightgauge configuration

### Installation Steps

1. **Install the Extension**
   - Open VSCode Extensions (Ctrl+Shift+X / Cmd+Shift+X)
   - Search for "Nightgauge"
   - Click Install

2. **Initialize Your Repository**
   - Open or switch to a repository that contains `.nightgauge/config.yaml`
   - The extension activates automatically when it detects `.nightgauge/pipeline` or `.nightgauge/plans` directories
   - You'll see the **Nightgauge** sidebar appear on the left activity bar

3. **Authenticate with GitHub**
   - If your extension shows "Sign In" prompts, click **Nightgauge: Sign In with GitHub**
   - Authorize the GitHub OAuth flow
   - The extension connects to your GitHub repositories and project boards

4. **Configure Your Project**
   - The extension reads `.nightgauge/config.yaml` for:
     - GitHub owner and repository settings
     - Project board configuration (URL or project number)
     - Pipeline stages and their configuration
   - See [CONFIGURATION.md](CONFIGURATION.md) for the config schema

### Quick Verification

1. Open the Nightgauge sidebar (click the Nightgauge icon in the activity bar)
2. You should see:
   - **Pipeline** view (currently showing "No active pipeline")
   - **Project Board** view (showing Ready, In Progress, and Backlog sections)
   - **Repositories** view (if multi-repo mode is enabled)

---

## Getting Started

### Your First Pipeline Run

#### Step 1: Pick an Issue

1. Navigate to the **Project Board** view in the sidebar
2. Look for the **Ready** section — these are issues flagged as "Ready" on your GitHub project
3. Click on an issue to select it, or right-click and choose **Nightgauge: Pick Up Issue**
4. (Alternative) Use Command Palette: Cmd+Shift+P → "Nightgauge: Pick Up Issue"

#### Step 2: Start the Pipeline

1. Click the issue in the Pipeline view (should now show as "Pending")
2. The status bar at the bottom shows `$(nightgauge) Nightgauge`
3. Click the status bar or use Command Palette → **Nightgauge: Run Pipeline**
4. The extension will ask you to confirm the execution mode

#### Step 3: Monitor Progress

As the pipeline runs, you'll see:

- **Status Bar** updates show the current stage: `$(sync~spin) Running: feature-dev`
- **Pipeline view** expands to show stage statuses (pending → running → complete)
- **Output Window** (Cmd+Alt+O) streams the Claude agent's work in real-time
- **Dashboard** (separate window) shows token usage and cost metrics

#### Step 4: Review Results

When the pipeline completes:

- Status bar shows `$(check) Complete`
- A notification appears with a summary
- **Pipeline view** now shows:
  - All stages marked complete (✓)
  - A green checkmark next to the issue number
  - A link to the created pull request (if pr-create stage ran)

Click the PR link to review the work and merge if satisfied.

---

## User Interface Overview

### Sidebar Views

The Nightgauge sidebar contains four main views:

#### 1. Pipeline View

**What it shows:** The current pipeline execution status and queue

**Elements:**

- **Issue Node** — Shows issue number and title
  - Green `✓` = complete
  - Spinner `↻` = running
  - `X` = failed
- **Phase Nodes** — Expands to show stages within each phase (issue-pickup, planning, development, validation, pr-creation, merge)
- **Stage Nodes** — Individual pipeline stages
  - `[Run]` button appears for pending stages (click to run manually)
  - `[Retry]` button appears for failed stages
  - `[View Context]` button shows the JSON context file for that stage
- **Queue Section** — Shows queued issues waiting to run
- **Completed Section** — Shows successfully processed issues
- **Failed Section** — Shows issues that encountered errors during pipeline execution

**Common Actions:**

- Click an issue to select it for pipeline execution
- Right-click a stage to get context menu options (run, retry, view context)
- Click `[Retry]` on a failed stage to re-run it
- Drag issues to reorder queue priority

#### 2. Project Board View

**What it shows:** Your GitHub project board, organized by status

**Sections:**

- **Ready** — Issues marked "Ready" on the GitHub project board
- **In Progress** — Issues currently being processed
- **Backlog** — Issues not yet ready for work
- **Done** — Issues completed and closed

**Features:**

- **Epic Grouping** — If epic grouping is enabled in settings, clicking an epic header expands to show all sub-issues
  - 🔒 Lock icons show blocked sub-issues (waiting on predecessor to complete)
  - Each sub-issue appears in **exactly one tab** matching its actual GitHub project status
- **Smart Sorting** (default) — Issues sorted by:
  1. Priority (P0 → P1 → P2)
  2. Blocked status (unblocked first)
  3. Size (XS → XL, for quick wins)
  4. Age (older issues first)
- **Filtering** — Right-click a status header to filter by priority or size
- **Searching** — Right-click to search issues by number, label, or title
- **Drag & Drop** — Drag an issue to the Pipeline view to start its pipeline

**Common Actions:**

- Click an issue to view on GitHub
- Drag an issue to the Pipeline view to start its pipeline
- Right-click to sort, filter, or search within a section
- Click the epic header to collapse/expand sub-issues

#### 3. Repositories View

**What it shows:** All workspace repositories in multi-repo mode (optional)

**Organization:**

- **By Repository** — Grouped tabs for each repository in your workspace
- **By Status** — Within each repo: Ready, In Progress, Backlog sections

**Actions:**

- Click a repository to switch the active workspace
- Each issue can be dragged to the Pipeline view to start execution

#### 4. Knowledge Base View

**What it shows:** Architecture decisions, PRD documents, and reference material

**Actions:**

- Right-click to **Create New Entry** — Add an ADR, decision, or reference document
- Click **Scaffold for Issue** — Auto-generate a knowledge entry for the current issue
- Entries are searchable and linked to pipeline runs for continuous learning

### Status Bar

The bottom status bar shows pipeline state with click-to-act functionality:

| State    | Display                            | On Click                              |
| -------- | ---------------------------------- | ------------------------------------- |
| Idle     | `$(nightgauge) Nightgauge`         | Opens Command Palette to run pipeline |
| Running  | `$(sync~spin) {Stage Name}`        | Shows Stop Pipeline option            |
| Paused   | `$(debug-pause) Paused at {Stage}` | Shows Resume Pipeline option          |
| Complete | `$(check) Complete`                | Opens result summary                  |
| Error    | `$(error) Error`                   | Shows error details and retry options |

**Second Item** — Shows selected target branch (for PR creation stage)

### Output Window

**Access:** Cmd+Alt+O / Ctrl+Alt+O

Shows real-time streaming output from the pipeline:

- **Agent thinking process** — What the Claude agent is doing
- **Tool calls** — Files being read, commands being run
- **Results** — Outcomes of each stage
- **Errors** — Any exceptions or validation failures

**Useful Buttons:**

- 🔄 **Clear** — Clear the output window
- 📋 **Copy to Clipboard** — Copy entire output for sharing/debugging

### Dashboard

**Access:** Click Pipeline view → `[Show Dashboard]` button or use Command Palette

Displays comprehensive metrics:

- **Token Usage** — Cumulative tokens used across stages
- **Cost Analysis** — Estimated cost of the pipeline run
- **Stage Performance** — Execution time and token efficiency per stage
- **Trend Chart** — Historical performance over the last 50 runs
- **Model Distribution** — Which models were used and how often

---

## Pipeline Execution

### Execution Modes

#### Headless Mode (Recommended)

**What it is:** Fully automated execution with token tracking and crash recovery

**When to use:**

- Running production workflows
- Processing multiple issues in batch
- Need token and cost tracking
- Prefer fully hands-off execution

**Features:**

- Automated stage progression
- Real-time token tracking
- Crash recovery (resumes from last completed stage)
- No user interaction required
- Batch processing support

**Command:** `Nightgauge: Run Pipeline` (always headless) or `Nightgauge: Run Stage` → choose "Headless"

#### Interactive Mode

**What it is:** Conversational session where you can ask questions and provide input mid-execution

**When to use:**

- Learning how a stage works
- Need to debug or explore behavior
- Want to provide clarifications during execution
- Testing specific stage logic

**Features:**

- Open stdin for user messages
- Real-time back-and-forth with the agent
- Perfect for exploration and debugging
- Single stage only (no batch)

**Command:** `Nightgauge: Run Stage` → choose "Interactive"

**Note:** Interactive mode does NOT support token tracking (requires headless `-p` flag)

### Controlling Pipeline Execution

#### Starting a Pipeline

**Method 1: From Command Palette**

- Cmd+Shift+P → `Nightgauge: Run Pipeline`
- Enter issue number when prompted

**Method 2: From Project Board**

- Right-click an issue → `Nightgauge: Start Pipeline for Issue`

**Method 3: From Pipeline View**

- Click an issue in the Pipeline view
- Status bar now shows `$(nightgauge)` — click it to run

#### Running Individual Stages

- Select the issue in Pipeline view
- Right-click a pending stage → `Run Stage`
- Choose headless (automated) or interactive (conversational)
- Or use Command Palette → `Nightgauge: Run Stage`

#### Pausing/Resuming

- **Pause:** Click status bar while running → `Nightgauge: Pause Pipeline`
  - Pipeline pauses after current stage completes
  - Resume from same point
- **Resume:** Click status bar while paused → `Nightgauge: Resume Pipeline`

#### Stopping Execution

**Stop Current Issue:**

- Click status bar → `Nightgauge: Stop Pipeline`
- Current stage is terminated (no recovery possible)
- Queue processing continues if other issues are queued

**Stop After Current Issue (Batch):**

- View title button → `Nightgauge: Stop After Current Issue`
- Finishes current issue, then stops before next queued issue

**Abort (Force Stop):**

- Pipeline view title → `Nightgauge: Abort Pipeline`
- Immediately terminates pipeline (no graceful shutdown)
- Use only if pipeline is hung or unresponsive

### Handling Failures

#### Retry Failed Stage

1. Right-click the failed stage in Pipeline view
2. Click **Retry Stage** — re-runs from that point
3. Or use **Retry from This Phase** to backtrack to an earlier phase

#### Retry Failed Issue (from Queue)

1. Right-click failed issue in Failed section
2. Click **Retry Failed Issue**
3. Issue moves back to queue for re-execution

#### Manual Stage Execution

1. You can skip stages or run them out of order manually
2. Right-click any pending or failed stage → `Run Stage`
3. Or continue from an intermediate stage using `Nightgauge: Run Stage`

#### Viewing Execution Context

- Right-click any stage → `View Context File`
- Opens the JSON context file for that stage in a read-only editor
- Shows all input data, decisions, and outputs from that stage
- Useful for debugging: check what the agent knew at each stage

---

## Project Board & Issue Management

### Understanding Project Board Integration

The extension syncs with your GitHub project board to:

1. Display ready issues in the sidebar
2. Update issue statuses as pipeline stages complete
3. Manage epic grouping and sub-issue tracking
4. Track blocking relationships between issues

**Key Concept:** Issues move through board statuses (Ready → In Progress → Done) as the pipeline progresses.

### Working with Epics

#### What is an Epic?

An epic is a GitHub issue with sub-issues. Each sub-issue represents a phase of work:

```
Epic: "Implement User Auth"
├── Sub-issue #123: Architecture Design (Ready)
├── Sub-issue #124: Backend Implementation (Blocked by #123)
├── Sub-issue #125: Frontend Integration (Blocked by #124)
└── Sub-issue #126: Testing & Documentation (Blocked by #125)
```

Sub-issues can be executed sequentially using the pipeline's multi-issue support.

#### Epic Grouping in Board View

When **epic grouping** is enabled (Settings → `nightgauge.epicGrouping.enabled`):

1. Epic headers appear in project board sections
2. Sub-issues are nested under their epic
3. 🔒 Lock icons show blocked sub-issues
4. Each sub-issue appears in **exactly one tab** (matching its GitHub project board status)

**Example Display:**

```
Ready Section:
├── Epic: Implement User Auth (3 sub-issues ready)
│   ├── #123 Architecture Design ✓ (Ready)
│   ├── #124 Backend Implementation 🔒 (Blocked)
│   └── #125 Frontend Integration 🔒 (Blocked)
```

When sub-issue #123 completes, #124 unblocks and #125 remains blocked (still waiting on #124).

#### Running an Epic Batch

1. Right-click an epic in Project Board view
2. Click **Nightgauge: Run All Issues in Epic**
3. Extension automatically:
   - Processes each sub-issue sequentially
   - Respects blocking relationships (waits for predecessor to complete)
   - Updates epic status to Done when all sub-issues complete

### Queue Management

#### What is the Queue?

When a pipeline is already running and you pick up another issue, that issue is automatically **queued** for processing after the current one completes.

#### Queue Features

- **Automatic Queueing** — Issues added while pipeline is running go to queue
- **Priority Sorting** — Queue auto-sorts by: Priority (P0 → P3) → Size (XS → XL) → Issue Number
- **Token Estimation** — Shows estimated tokens for each queued issue based on size label
- **Reordering** — Use arrow buttons to move queue items up/down
- **Dynamic Addition** — Add more issues to queue while pipeline is running

#### Queue Status Colors

| Status    | Meaning                                          |
| --------- | ------------------------------------------------ |
| Pending   | Waiting to run (will run when current completes) |
| Running   | Currently executing                              |
| Completed | Successfully processed                           |
| Failed    | Encountered error during execution               |

#### Queue Commands

- **Move Up** — Increase queue priority (Cmd+Shift+↑ or Ctrl+Shift+↑)
- **Move Down** — Decrease queue priority (Cmd+Shift+↓ or Ctrl+Shift+↓)
- **Remove** — Remove from queue (Cmd+Shift+R or Ctrl+Shift+R)
- **Retry** — Re-queue failed issue at top of queue
- **Clear Queue** — Remove all pending items
- **Stop After Current** — Finish current issue, then stop (clear queue)

### Issue Filtering & Sorting

#### Sorting Ready Issues

The Project Board automatically sorts issues by selected strategy:

| Strategy            | Order                                  |
| ------------------- | -------------------------------------- |
| **Smart** (default) | Priority → Unblocked → Size → Age      |
| **Board**           | Preserve GitHub project board order    |
| **Priority**        | P0 → P1 → P2                           |
| **Number**          | Issue number (ascending or descending) |
| **Size**            | XS → S → M → L → XL                    |
| **Dependencies**    | Unblocked → Blocked (topological sort) |

**To Change Sort:**

- Right-click status header in Project Board → `Nightgauge: Sort Project Board`
- Choose sort strategy

#### Filtering by Priority or Size

**To Filter:**

- Right-click status header → `Nightgauge: Filter Project Board`
- Select priority (P0, P1, P2, P3) or size (XS, S, M, L, XL)
- Apply filter — only matching issues display

**Configuration Alternative:**

- Settings → `nightgauge.readyItems.filters.priority`
- Settings → `nightgauge.readyItems.filters.size`

#### Searching Issues

- Right-click status header → `Nightgauge: Search Project Board`
- Enter search term (issue number, label, or title fragment)
- Results filter in real-time

### Refreshing & Syncing

#### Manual Refresh

- Click refresh button (🔄) in Project Board view title
- Or use Command Palette → `Nightgauge: Refresh Project Board`

#### Auto-Refresh (Optional)

Enable in Settings:

- `nightgauge.readyItems.autoRefresh: true`
- `nightgauge.readyItems.refreshInterval: 300` (seconds)

---

## Commands Reference

Commands are organized by function. Access via Command Palette (Cmd+Shift+P / Ctrl+Shift+P) or toolbar buttons.

### Pipeline Control

| Command                                  | Shortcut           | What It Does                                   |
| ---------------------------------------- | ------------------ | ---------------------------------------------- |
| `Nightgauge: Run Pipeline`               | (Status bar click) | Start full pipeline for selected issue         |
| `Nightgauge: Run Stage...`               |                    | Run individual stage (headless or interactive) |
| `Nightgauge: Run Interactive Stage...`   |                    | Run stage in conversation mode                 |
| `Nightgauge: Pause Pipeline`             |                    | Pause after current stage                      |
| `Nightgauge: Resume Pipeline`            |                    | Resume from pause point                        |
| `Nightgauge: Stop Pipeline`              |                    | Stop current issue (queue continues)           |
| `Nightgauge: Stop After Current Issue`   |                    | Finish issue, then stop queue                  |
| `Nightgauge: Run Pipeline with Model...` |                    | Override default model for this run            |

### Issue & Queue Management

| Command                                  | Shortcut    | What It Does                               |
| ---------------------------------------- | ----------- | ------------------------------------------ |
| `Nightgauge: Pick Up Issue`              |             | Select an issue to start pipeline          |
| `Nightgauge: Start Pipeline for Issue`   |             | Begin pipeline for specific issue          |
| `Nightgauge: Add Issue to Pipeline`      |             | Queue an issue while pipeline is running   |
| `Nightgauge: Remove Issue from Pipeline` | Cmd+Shift+R | Remove queued issue                        |
| `Nightgauge: Add Epic to Pipeline (Pro)` |             | Queue all sub-issues in an epic            |
| `Nightgauge: Run All Issues in Epic`     |             | Execute epic batch (sequential sub-issues) |
| `Nightgauge: Move Queue Item Up`         | Cmd+Shift+↑ | Increase queue priority                    |
| `Nightgauge: Move Queue Item Down`       | Cmd+Shift+↓ | Decrease queue priority                    |
| `Nightgauge: Remove from Queue`          |             | Remove specific queue item                 |
| `Nightgauge: Retry Failed Issue`         |             | Re-queue failed issue at top               |
| `Nightgauge: Clear Queue`                |             | Remove all pending queue items             |
| `Nightgauge: Retry Failed Queue Item`    |             | Retry specific failed queue item           |

### Stage Management

| Command                              | What It Does                       |
| ------------------------------------ | ---------------------------------- |
| `Retry Stage`                        | Re-run failed stage                |
| `Retry from This Phase`              | Backtrack to earlier phase + retry |
| `View Context File`                  | Show JSON input/output for stage   |
| `nightgauge-pipeline.showSlotOutput` | View output for concurrent slot    |

### Project Board & Search

| Command                                | What It Does                               |
| -------------------------------------- | ------------------------------------------ |
| `Nightgauge: Refresh Project Board`    | Fetch latest board state from GitHub       |
| `Nightgauge: Sort Project Board`       | Change sort strategy (smart/priority/size) |
| `Nightgauge: Filter Project Board`     | Filter by priority or size                 |
| `Nightgauge: Search Project Board`     | Search by issue number or label            |
| `Nightgauge: Expand All Epic Groups`   | Expand all epics in board view             |
| `Nightgauge: Collapse All Epic Groups` | Collapse all epics                         |
| `Nightgauge: Select All Issues`        | Select all issues in current view          |
| `Nightgauge: Query Project Items`      | Advanced GraphQL query on board            |
| `Nightgauge: Save Query`               | Save query for reuse                       |
| `Nightgauge: Load Saved Query`         | Load previously saved query                |
| `Nightgauge: Manage Saved Queries`     | Delete or edit saved queries               |

### Visibility & Navigation

| Command                                | Shortcut                      | What It Does                        |
| -------------------------------------- | ----------------------------- | ----------------------------------- |
| `Nightgauge: Show Dashboard`           |                               | Open token/cost analytics dashboard |
| `Nightgauge: Show Output Window`       | Cmd+Alt+O                     | Toggle output window visibility     |
| `Nightgauge: Clear Output Window`      | Cmd+Alt+Shift+O               | Clear all output text               |
| `Nightgauge: Copy Output to Clipboard` |                               | Copy entire output for sharing      |
| `Nightgauge: Focus Pipeline View`      | Cmd+Alt+P                     | Jump to Pipeline sidebar view       |
| `Nightgauge: Focus Project Board View` | Cmd+Alt+B                     | Jump to Project Board view          |
| `Nightgauge: Show Settings`            | Cmd+, (in Nightgauge sidebar) | Open extension settings             |
| `Nightgauge: Show Pipeline Summary`    |                               | Show statistics and metrics         |
| `Nightgauge: View Context File`        |                               | Display context for current stage   |

### Configuration & Setup

| Command                                 | What It Does                              |
| --------------------------------------- | ----------------------------------------- |
| `Nightgauge: Open Settings`             | Open extension settings                   |
| `Nightgauge: Switch Execution Adapter`  | Switch between Claude/Codex/Gemini        |
| `Nightgauge: Setup Claude Code Plugins` | Configure Claude Code CLI plugins         |
| `Nightgauge: Setup Codex Commands`      | Configure Codex adapter                   |
| `Nightgauge: Disable Auto-Accept`       | Toggle permission auto-acceptance         |
| `Nightgauge: Select Target Branch`      | Choose branch for PR creation             |
| `Nightgauge: Switch Repository`         | Switch active workspace repo (multi-repo) |

### Cleanup & Reset

| Command                              | Shortcut    | What It Does                         |
| ------------------------------------ | ----------- | ------------------------------------ |
| `Nightgauge: Reset Pipeline`         |             | Clear pipeline state (all issues)    |
| `Nightgauge: Abort Pipeline`         |             | Force stop + clear state             |
| `Nightgauge: Clear Completed Issues` |             | Remove completed issues from sidebar |
| `Nightgauge: Clear Failed Issues`    |             | Remove failed issues                 |
| `Nightgauge: Clear Pipeline History` | Cmd+Shift+X | Delete all execution history         |
| `Nightgauge: Reset Session Metrics`  |             | Clear token usage counters           |
| `Nightgauge: Reset Usage Counter`    |             | Reset daily/monthly limits           |

### Health & Diagnostics

| Command                                         | What It Does                        |
| ----------------------------------------------- | ----------------------------------- |
| `Nightgauge: Run Pipeline Health Check`         | Analyze pipeline efficiency metrics |
| `Nightgauge: Recalibrate Health Score Baseline` | Re-establish health baseline        |
| `Nightgauge: Show Brownfield Dashboard`         | Analyze code quality metrics        |
| `Nightgauge: Rescrub Dashboard History`         | Clean up metrics/outliers           |
| `Nightgauge: Export Telemetry Analytics`        | Export metrics for analysis         |
| `Nightgauge: Telemetry Settings`                | Configure what data is collected    |

### Knowledge Base

| Command                                    | What It Does                           |
| ------------------------------------------ | -------------------------------------- |
| `Nightgauge: New Knowledge Entry`          | Create PRD, decision, or reference doc |
| `Nightgauge: New ADR`                      | Create Architecture Decision Record    |
| `Nightgauge: Scaffold Knowledge for Issue` | Auto-generate docs for issue           |

### Authentication & Subscription

| Command                                       | What It Does                         |
| --------------------------------------------- | ------------------------------------ |
| `Nightgauge: Sign In`                         | Authenticate with Nightgauge account |
| `Nightgauge: Sign Out`                        | Logout from account                  |
| `Nightgauge: Sign In with GitHub`             | Authenticate with GitHub OAuth       |
| `Nightgauge: Manage Subscription`             | View/change plan and billing         |
| `Nightgauge: Configure Discord Notifications` | Setup Discord webhook alerts         |
| `Nightgauge: Show Platform Status`            | Check Nightgauge cloud health        |

---

## Settings Reference

Access settings via **Cmd+, / Ctrl+,** when sidebar is active, or **Nightgauge: Open Settings**.

### Core Settings

| Setting                   | Type   | Default                | Description                                        |
| ------------------------- | ------ | ---------------------- | -------------------------------------------------- |
| `nightgauge.authProvider` | enum   | `max`                  | Auth provider: `max` (Claude), `bedrock`, `vertex` |
| `nightgauge.defaultModel` | enum   | `sonnet`               | Model: `sonnet` (recommended), `opus`, `haiku`     |
| `nightgauge.contextPath`  | string | `.nightgauge/pipeline` | Path to pipeline context files                     |
| `nightgauge.plansPath`    | string | `.nightgauge/plans`    | Path to plan documents                             |

### Execution Adapter

| Setting                        | Type   | Default            | Description                                                            |
| ------------------------------ | ------ | ------------------ | ---------------------------------------------------------------------- |
| `nightgauge.core.adapter`      | enum   | `claude`           | Execution adapter: `claude` (CLI), `codex`, `gemini`, `gemini-sdk`     |
| `nightgauge.gemini.authMethod` | enum   | `api-key`          | For Gemini: `api-key`, `google-login`, `vertex-ai`                     |
| `nightgauge.gemini.model`      | enum   | `gemini-2.5-flash` | Gemini model: `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.0-flash` |
| `nightgauge.gemini.apiKey`     | string | (empty)            | Gemini API key (stored securely, or use env var)                       |

### Ready Items (Project Board)

| Setting                                  | Type   | Default | Description                                                                   |
| ---------------------------------------- | ------ | ------- | ----------------------------------------------------------------------------- |
| `nightgauge.readyItems.autoRefresh`      | bool   | `false` | Auto-refresh ready issues periodically                                        |
| `nightgauge.readyItems.refreshInterval`  | number | `300`   | Refresh interval in seconds (min 60)                                          |
| `nightgauge.readyItems.sortBy`           | enum   | `smart` | Sort strategy: `smart`, `board`, `priority`, `number`, `size`, `dependencies` |
| `nightgauge.readyItems.sortDirection`    | enum   | `asc`   | Sort order: `asc` (ascending) or `desc`                                       |
| `nightgauge.readyItems.filters.priority` | enum   | `all`   | Filter by priority: `all`, `P0`, `P1`, `P2`                                   |
| `nightgauge.readyItems.filters.size`     | enum   | `all`   | Filter by size: `all`, `XS`, `S`, `M`, `L`, `XL`                              |

### Epic Grouping

| Setting                                   | Type | Default | Description                                |
| ----------------------------------------- | ---- | ------- | ------------------------------------------ |
| `nightgauge.epicGrouping.enabled`         | bool | `true`  | Show sub-issues grouped under epic headers |
| `nightgauge.epicGrouping.sortBy`          | enum | `smart` | Sort epics same as ready items             |
| `nightgauge.epicGrouping.expandByDefault` | bool | `false` | Expand all epic groups on startup          |

### Pipeline Execution

| Setting                                           | Type   | Default         | Description                                           |
| ------------------------------------------------- | ------ | --------------- | ----------------------------------------------------- |
| `nightgauge.execution.defaultMode`                | enum   | `headless`      | Default mode for Run Stage: `headless`, `interactive` |
| `nightgauge.execution.interactive.timeoutMinutes` | number | `30`            | Inactivity timeout for interactive sessions           |
| `nightgauge.execution.maxTokensPerRun`            | number | `0` (unlimited) | Budget limit per pipeline run                         |

### Queue Settings

| Setting                         | Type   | Default | Description                                          |
| ------------------------------- | ------ | ------- | ---------------------------------------------------- |
| `nightgauge.queue.autoStart`    | bool   | `true`  | Auto-start next queued issue when current completes  |
| `nightgauge.queue.priorityMode` | bool   | `true`  | Auto-sort queue by priority/size                     |
| `nightgauge.queue.maxQueueSize` | number | `50`    | Maximum queued issues (oldest removed when exceeded) |

### Token Tracking & Analytics

| Setting                                  | Type   | Default | Description                        |
| ---------------------------------------- | ------ | ------- | ---------------------------------- |
| `nightgauge.tracking.enableTokenMetrics` | bool   | `true`  | Record token usage per stage       |
| `nightgauge.tracking.historySize`        | number | `50`    | Keep last N runs in history        |
| `nightgauge.tracking.exportOnComplete`   | bool   | `false` | Auto-export metrics after each run |

### Notifications

| Setting                                       | Type   | Default | Description                               |
| --------------------------------------------- | ------ | ------- | ----------------------------------------- |
| `nightgauge.notifications.onPipelineComplete` | bool   | `true`  | Show popup when pipeline finishes         |
| `nightgauge.notifications.onError`            | bool   | `true`  | Show popup on errors                      |
| `nightgauge.notifications.discordWebhook`     | string | (empty) | Discord webhook URL for alerts (optional) |

---

## Keyboard Shortcuts

Quick reference for built-in shortcuts. Customize via VSCode Keybindings (Cmd+K Cmd+S).

### Pipeline Control

| Shortcut (Mac)  | Shortcut (Windows/Linux) | Command                             |
| --------------- | ------------------------ | ----------------------------------- |
| Cmd+Alt+O       | Ctrl+Alt+O               | Show Output Window                  |
| Cmd+Alt+Shift+O | Ctrl+Alt+Shift+O         | Clear Output Window                 |
| Cmd+Alt+P       | Ctrl+Alt+P               | Focus Pipeline View                 |
| Cmd+Alt+B       | Ctrl+Alt+B               | Focus Project Board View            |
| Cmd+Alt+R       | Ctrl+Alt+R               | Switch Repository (multi-repo mode) |
| Cmd+Shift+X     | Ctrl+Shift+X             | Clear Pipeline History              |

### Queue Management

| Shortcut (Mac) | Shortcut (Windows/Linux) | Command                    |
| -------------- | ------------------------ | -------------------------- |
| Cmd+Shift+↑    | Ctrl+Shift+↑             | Move Queue Item Up         |
| Cmd+Shift+↓    | Ctrl+Shift+↓             | Move Queue Item Down       |
| Cmd+Shift+R    | Ctrl+Shift+R             | Remove Issue from Pipeline |

### Settings

| Shortcut (Mac) | Shortcut (Windows/Linux) | Command                            |
| -------------- | ------------------------ | ---------------------------------- |
| Cmd+,          | Ctrl+,                   | Open Settings (Nightgauge sidebar) |

---

## Troubleshooting

### Extension Hangs on Startup

**Symptoms:**

- VSCode freezes for 1-3 minutes when opening workspace
- All extensions become unresponsive
- Eventually recovers or crashes

**Root Cause:**

- Old code was making blocking (`execSync`) calls during workspace initialization
- Fixed in Issue #1328, but may affect older installations

**Solution:**

1. Update extension to latest version
2. Close all VSCode windows
3. Clear extension cache: `rm -rf ~/.vscode/extensions/nightgauge*`
4. Restart VSCode
5. Re-install extension

**Prevention:**

- Keep extension updated
- Check Settings → Update check is enabled

---

### Ready Items View is Empty

**Symptoms:**

- Project Board view shows "Ready" section but no issues appear
- Other sections (In Progress, Backlog) have issues

**Possible Causes:**

1. **GitHub Authentication Failed**
   - No issues fetched from project board
   - Solution: Run `Nightgauge: Sign In with GitHub`

2. **Project Board Not Configured**
   - `.nightgauge/config.yaml` missing project board URL or number
   - Solution: Add `projectBoard.number` or `projectBoard.url` to config.yaml

3. **No Ready Issues on Board**
   - Issues exist but none are labeled "Ready"
   - Solution: On GitHub, move at least one issue to "Ready" column

4. **Filter Too Restrictive**
   - Settings filter by P0 only, but no P0 issues exist
   - Solution: Settings → `nightgauge.readyItems.filters.priority` → set to "all"

5. **Board Sync Stale**
   - Cache not refreshed from GitHub
   - Solution: Click refresh button (🔄) in Project Board view title

**Debug Steps:**

1. Open Command Palette → `Nightgauge: Query Project Items`
2. Run query: `status:"Ready" is:open`
3. If results show in Query Results view, the board is synced but view may have bug
4. Try: `Nightgauge: Refresh Project Board`

---

### Board Sync Failures

**Symptoms:**

- Error notifications when refreshing project board
- Issues disappear/reappear unexpectedly
- Blocking relationships not showing correctly

**Common Errors:**

**"Failed to fetch project board: NOT_FOUND"**

- Project board URL or number is incorrect
- Solution: Verify `projectBoard.number` in `.nightgauge/config.yaml`
- Get correct number from GitHub: open project → URL shows `/projects/123`

**"GraphQL Error: Field not found"**

- Project board structure changed or doesn't support custom fields
- Solution:
  1. Verify project is a "Classic" GitHub Projects board OR modern ProjectV2
  2. Check that custom Status field exists
  3. Run: `Nightgauge: Migrate Config` to auto-detect board structure

**"Permission Denied"**

- GitHub token missing or insufficient permissions
- Solution:
  1. Run `Nightgauge: Sign Out`
  2. Run `Nightgauge: Sign In with GitHub`
  3. Grant all requested permissions (repo, project, read:org)

**Progressive Rendering Bug (Fixed)**

- Old versions fetched all 677 items locally and filtered, causing race conditions
- Solution: Update to latest version (uses `status:Ready` server-side filtering)

---

### Pipeline State Stuck

**Symptoms:**

- Pipeline shows "Running" but no activity in output window
- Status bar stuck on one stage for 10+ minutes
- Can't stop or pause pipeline

**Possible Causes:**

1. **Claude CLI Hang**
   - Agent is waiting for user input or network issue
   - Solution:
     - Wait 2-3 minutes (network issues resolve)
     - If still stuck, run `Nightgauge: Stop Pipeline`
     - Or use `Nightgauge: Abort Pipeline` (force kill)

2. **Modal Dialog Waiting**
   - Some stages show approval dialogs
   - Solution: Look for modal dialog (may be off-screen or behind output window)
   - Press Tab to cycle through dialogs
   - Press Space/Enter to approve

3. **stdin Deadlock (Interactive Mode)**
   - Interactive mode waiting for user input that never comes
   - Solution: Type something in output window and press Enter
   - Or use `Nightgauge: Stop Pipeline`

**Recovery Steps:**

1. Click status bar → `Nightgauge: Stop Pipeline`
2. If unresponsive, use `Nightgauge: Abort Pipeline`
3. Wait 5 seconds
4. Status should show `$(error) Error` or `Idle`
5. Review Output Window for error details
6. Use `Nightgauge: Retry Failed Issue` to re-queue

---

### Output Channel Shows Errors

**Common Error Messages:**

**"EACCES: permission denied"**

- Extension can't read/write to context files
- Solution:
  1. Check `.nightgauge/pipeline` directory exists and is writable
  2. Run: `chmod -R 755 .nightgauge/`
  3. Restart VSCode

**"Claude CLI not found"**

- `claude` command not installed or not in PATH
- Solution:
  1. Install: `brew install anthropic/brew/claude-cli`
  2. Verify: `which claude` (should show path)
  3. Restart VSCode

**"GitHub CLI authentication failed"**

- `gh` command not authenticated
- Solution:
  1. Run: `gh auth login`
  2. Choose GitHub.com and HTTPS
  3. Grant `repo` and `project` permissions
  4. Restart VSCode

**"Model not available: opus"**

- Your Claude API plan doesn't include requested model
- Solution:
  1. Settings → `nightgauge.defaultModel` → change to `sonnet`
  2. Or upgrade Claude API plan at Anthropic console

**"Token budget exceeded"**

- Pipeline ran over configured cost limit
- Solution:
  1. Settings → increase `nightgauge.execution.maxTokensPerRun`
  2. Or optimize pipeline to use fewer tokens
  3. Review dashboard to find expensive stages

---

### Extension Not Activating

**Symptoms:**

- Nightgauge sidebar doesn't appear
- No Nightgauge icon in activity bar

**Causes & Solutions:**

1. **Workspace doesn't have activation files**
   - Extension activates only when `.nightgauge/pipeline` OR `.nightgauge/plans` directories exist
   - Solution: Create directory: `mkdir -p .nightgauge/pipeline`

2. **Extension disabled**
   - Check VSCode Extensions view (Cmd+Shift+X)
   - Find "Nightgauge" and check if it's disabled
   - Solution: Click Enable

3. **Multiple VSCode instances**
   - Old instance running outdated code
   - Solution:
     1. Close all VSCode windows
     2. Wait 10 seconds
     3. Reopen project

4. **Corrupted installation**
   - Extension files corrupted
   - Solution:
     1. Uninstall via Extensions view
     2. Close VSCode
     3. Delete cache: `rm -rf ~/.vscode/extensions/nightgauge*`
     4. Restart VSCode
     5. Reinstall from Extension Marketplace

---

### Batch Processing Not Working

**Symptoms:**

- Multiple issues in queue but only one runs
- "Run All Issues in Epic" command doesn't queue sub-issues
- Batch mode not starting

**Possible Causes:**

1. **Interactive Mode Selected**
   - Batch processing only works in headless mode
   - Solution: Use `Nightgauge: Run Pipeline` (always headless)
   - Or: `Nightgauge: Run Stage` → choose "Headless"

2. **Queue Auto-Start Disabled**
   - Issues added to queue but not automatically processed
   - Solution: Settings → `nightgauge.queue.autoStart` → set to `true`

3. **Epic Sub-Issues Not Linked**
   - Epic exists but sub-issues not properly linked on GitHub
   - Solution:
     1. Go to epic issue on GitHub
     2. Look for "Sub-issues" section
     3. If empty, use GraphQL to add sub-issues: `gh api graphql -f query='mutation { addProjectV2ItemById(...) { ... } }'`
     4. See `.claude/rules/scripts.md` for mutation examples

4. **Permission Issues**
   - Extension can't modify project board or create PRs
   - Solution: `gh auth refresh` with `repo` and `project` scopes

---

### Token Tracking Shows N/A

**Symptoms:**

- Dashboard shows "Tokens: N/A"
- Token usage not recorded in output

**This is Expected in Interactive Mode:**

Token tracking is only available in **headless mode** because:

- Headless uses `--output-format stream-json` (parseable format)
- Interactive mode doesn't support `-p` flag (closes stdin)
- Raw text output from interactive mode is not machine-parseable

**Solution:**

- Use `Nightgauge: Run Pipeline` (headless, token-tracked)
- Interactive mode is for exploration/debugging only

---

### Performance Issues

**Symptoms:**

- UI is sluggish when project board has 100+ issues
- Sidebar takes 5+ seconds to refresh

**Solutions:**

1. **Disable Epic Grouping (if not needed)**
   - Settings → `nightgauge.epicGrouping.enabled` → `false`
   - Grouping requires extra processing

2. **Reduce Auto-Refresh Interval**
   - Settings → `nightgauge.readyItems.refreshInterval` → increase to 600+ seconds
   - Or disable auto-refresh entirely

3. **Use Filters**
   - Filter by priority: P0 only
   - Filter by size: avoid XL issues
   - This reduces items loaded

4. **Disable Knowledge Base View**
   - Knowledge view can slow down if it has many documents
   - Right-click in sidebar → hide Knowledge view

5. **Clear History**
   - Command Palette → `Nightgauge: Clear Pipeline History`
   - Removes old runs from dashboard

---

## Advanced Features

### Custom Query Language

The **Query Project Items** feature supports GraphQL queries on your GitHub project board:

**Example Queries:**

```graphql
# All issues labeled "bug" in Ready status
status:"Ready" label:bug is:open

# High-priority work
priority:P0 is:open

# Blocked issues (blocked by someone)
has:blockedBy is:open

# Issues assigned to you
assignee:@me is:open
```

**To Run:**

1. Command Palette → `Nightgauge: Query Project Items`
2. Enter query (or leave blank for all Ready items)
3. Results show in **Query Results** sidebar view

**To Save:**

- Right-click results → `Nightgauge: Save Query`
- Load later via `Nightgauge: Load Saved Query`

### Health Analysis

Run comprehensive pipeline health diagnostics:

1. Command Palette → `Nightgauge: Run Pipeline Health Check`
2. Engine analyzes:
   - **Token Economics** — Tokens per stage, cost trends
   - **Stage Effectiveness** — Which stages are expensive/slow
   - **Model Routing** — Optimal model selection
   - **Reliability** — Error rates and retry frequency
   - **Velocity** — Pipeline speed over time

3. Results show in **Health Report**:
   - Overall health score (0-100)
   - Dimension breakdowns
   - Recommended optimizations

4. Click **Recalibrate Health Score Baseline** to reset baseline if config changed

### Knowledge Base Management

Store decisions, architecture docs, and PRDs in the Knowledge Base:

**Create New Entry:**

1. Sidebar → Knowledge view → `[+]` button
2. Choose: PRD, ADR (Architecture Decision Record), or Custom
3. Document opens with template
4. Save to `.nightgauge/knowledge/`

**Auto-Scaffold for Issue:**

1. Click issue in Project Board
2. Command Palette → `Nightgauge: Scaffold Knowledge for Issue`
3. Auto-generates template with issue context
4. Fill in your decisions

**Link to Pipeline:**

- Knowledge entries are stored in git
- Linked to issues by name (e.g., `issue-#123-adr.md`)
- Searchable via Knowledge view

### Concurrent Pipeline Execution

Run multiple issues in parallel using **Slots** (Pro feature):

**What it is:** Multiple "slots" (independent pipeline instances) running concurrently

**How it works:**

1. Queue multiple issues
2. Configure `concurrentSlots: 2` in `.nightgauge/config.yaml`
3. Pipeline starts 2 simultaneous issue executions
4. Queue continues adding issues as slots free up

**Monitoring:**

- Pipeline view shows each slot separately
- Status bar shows `$(sync~spin) Running: 2 slots`
- Click **Stop After Current Issue** to let in-flight slots finish

### Model Escalation

Automatically escalate to more powerful models if cheaper ones fail:

**Configuration:**

```yaml
execution:
  model_escalation: true
  fallback_models:
    - sonnet # Start with Sonnet
    - opus # Retry with Opus
    - opus-pro # Final fallback
```

**Behavior:**

1. Try stage with Sonnet
2. If fails, retry with Opus (same context)
3. If still fails, retry with Opus Pro
4. If Opus Pro fails, mark stage as failed

**Monitoring:**

- Dashboard shows which model completed each stage
- Historical trends show model effectiveness

### Token Budget Enforcement

Set spending limits per run:

**Configuration:**

```yaml
execution:
  maxTokensPerRun: 500000 # ~$25 at current prices
  maxTokensPerStage:
    issue-pickup: 50000
    feature-dev: 250000
    feature-validate: 100000
```

**Behavior:**

1. Extension tracks tokens during execution
2. If budget exceeded, pauses and asks to continue
3. Can set `autoAcceptOverBudget: false` to block (safer)

**Monitor:**

- Dashboard shows current spend vs budget
- Output shows token count per stage
- Notifications warn if approaching limit

---

## Related Documentation

- **[ARCHITECTURE.md](ARCHITECTURE.md)** — Deep dive into extension design
- **[PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md)** — Manual vs automated modes
- **[INTERACTIVE_MODE.md](INTERACTIVE_MODE.md)** — Interactive execution details
- **[CONFIGURATION.md](CONFIGURATION.md)** — `.nightgauge/config.yaml` reference
- **[HEALTH_MONITORING.md](HEALTH_MONITORING.md)** — Pipeline health analysis engine

---

## Support & Feedback

For bugs, feature requests, or questions:

1. **Check this guide first** — Most issues are covered in Troubleshooting
2. **Search GitHub Issues** — Your question may already be answered
3. **Open a GitHub Issue** — Include error message and `.nightgauge/config.yaml` (redact secrets)
4. **Check Output Window** — Often contains detailed error context

---

**Author:** nightgauge
**License:** Apache-2.0
