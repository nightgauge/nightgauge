# Go Architecture: Unified Orchestration Binary

> **Status**: Draft proposal **Supersedes**: Epic #1543 (shell-to-Go migration —
> narrower scope) **Author**: nightgauge **Date**: 2026-03-07

## Problem Statement

The current architecture has three independent state owners
(PipelineStateService in TypeScript, update-pipeline-state.sh in bash, and
GitHub itself) that drift out of sync, producing ghost checkmarks, stale
pipeline locks, and race conditions across repositories. These are architectural
problems, not language problems — but the planned Go migration (Epic #1543) and
the architectural redesign should be combined into a single effort rather than
done sequentially.

The platform (acme-platform) currently uses tRPC for type-safe
TypeScript-to-TypeScript communication. A Go client breaks this assumption. The
API contract must become language-agnostic before the Go binary can consume it.

## Design Principles

1. **GitHub Project Board is the durable state** — not local files
2. **Skills are pure** — no state writes, no side effects beyond their artifacts
3. **Single execution gateway** — one binary owns all orchestration
4. **Offline-first** — platform enhances, never gates
5. **Strangler fig migration** — each module replaces one piece; pipeline stays
   usable throughout

## Architecture Overview

```
┌───────────────────────────────────────────────────────────┐
│                  Platform (Cloud, TypeScript)              │
│                                                            │
│  Skill serving · License validation · Routing intelligence │
│  Analytics aggregation · Billing · Team management         │
│                                                            │
│  API: OpenAPI 3.1 spec (source of truth)                   │
│  Transport: HTTPS + SSE                                    │
│  Auth: OAuth 2.0 Device Flow + API keys                    │
└──────────────────────┬────────────────────────────────────┘
                       │ OpenAPI client (generated)
┌──────────────────────▼────────────────────────────────────┐
│              Go Binary: nightgauge                    │
│                                                            │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │ Orchestrator │  │  Execution   │  │  GitHub Client   │  │
│  │              │  │  Manager     │  │                  │  │
│  │ Board-driven │  │ Per-issue    │  │ Project Board    │  │
│  │ scheduling   │  │ worktrees    │  │ Issues & PRs     │  │
│  │ Cross-repo   │  │ Skill        │  │ GraphQL + REST   │  │
│  │ coordination │  │ dispatch     │  │ Sub-issues       │  │
│  │ Dep ordering │  │ Process mgmt │  │ Blocking rels    │  │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────────┘  │
│         │                 │                 │              │
│  ┌──────▼─────────────────▼─────────────────▼───────────┐  │
│  │                   Core Services                       │  │
│  │                                                       │  │
│  │  State (board-backed) · Config (YAML) · Events        │  │
│  │  Intelligence (model routing, complexity, health)      │  │
│  │  Platform Client (OpenAPI, offline fallback)           │  │
│  └───────────────────────────────────────────────────────┘  │
│                                                            │
│  Interfaces:                                               │
│    CLI:      cobra commands (run, status, queue, health)   │
│    IPC:      JSON-over-stdio (VSCode ↔ Go)                 │
│    Platform: OpenAPI client (generated from spec)          │
└──────────┬───────────────────────────────┬────────────────┘
           │ JSON-over-stdio               │ CLI
┌──────────▼──────────┐           ┌────────▼──────────┐
│  VSCode Extension   │           │  CI / GitHub       │
│  (thin TypeScript   │           │  Actions / CLI     │
│   UI shell)         │           │                    │
└─────────────────────┘           └───────────────────┘
```

## Module Breakdown

### Module 1: GitHub Client

Replaces: `ProjectBoardService`, shell scripts (`add-to-project.sh`,
`link-sub-issue.sh`, `create-sub-issue.sh`, `check-epic-completion.sh`), and all
`gh` CLI invocations.

**Responsibilities:**

- Project board CRUD (items, fields, status transitions)
- Issue management (create, close, label, sub-issue linking)
- PR operations (create, merge, review status)
- Epic aggregation (cross-repo sub-issue queries)
- Blocking/blockedBy relationship management

**Key design:**

- GraphQL client with typed query builders (no string templates)
- Rate limit awareness with automatic retry + backoff
- Cross-repo queries use issue node IDs, not number+repo tuples
- Results cached with TTL; board state is always re-fetchable

**Why this is Module 1:** Everything else depends on GitHub data. The Go binary
can provide board data to the VSCode extension immediately, replacing the
TypeScript ProjectBoardService without touching execution.

### Module 2: Pipeline State (Board-Driven)

Replaces: `PipelineStateService`, `state.json`, `update-pipeline-state.sh`,
`StaleSlotRecoveryService`, `reconcileWithGitHub()`.

**Current problems this solves:**

- state.json written by TypeScript AND bash scripts that AI may skip
- Singleton state file blocks new pipelines when stale
- No reconciliation between local state and GitHub reality
- Race conditions when repos switch mid-fetch

**New model:**

```
Durable state:    GitHub Project Board fields (Status, Priority, Size)
                  + custom field: "Pipeline Stage" (per-issue)
Runtime state:    In-memory only, per-execution, discarded on completion
Execution log:    Append-only JSONL (local), pushed to platform analytics
```

- **Starting a pipeline**: Board status → "In Progress", Pipeline Stage field
  set to current stage. This is the ONLY state that matters.
- **Completing a stage**: Pipeline Stage field updated on the board. If the Go
  binary crashes, the board still shows where the pipeline was — recovery reads
  the board, not a local file.
- **Completing a pipeline**: Board status → "In Review" or "Done". No local
  cleanup needed.
- **Concurrent pipelines**: Each execution is identified by (repo, issue#).
  Board fields are per-item. No singleton contention.
- **Runtime metrics** (token usage, duration, process PID): In-memory only.
  Written to execution history JSONL on completion. Not persisted during
  execution — if the process dies, these metrics are lost (acceptable).

**Offline behavior:** If GitHub API is unreachable, fall back to local state
file (same as today). Reconcile on reconnection. This is the degraded path, not
the primary path.

### Module 3: Execution Manager

Replaces: `HeadlessOrchestrator`, `runStage`, `WorktreeManager`,
`ConcurrentPipelineManager`, `skillRunner`.

**Responsibilities:**

- Worktree creation/cleanup per execution
- Skill process spawning (Claude CLI, Codex, Gemini)
- Process lifecycle management (PID tracking, timeout, kill)
- Output streaming (stdout → event bus → IPC to VSCode)
- Context file I/O (read previous stage output, validate schema)

**Key design:**

- Each execution is a goroutine with its own context (cancelable)
- Worktree path scoped: `{repo}/.nightgauge/worktrees/{issue-N}/`
- Skill invocation is adapter-based: Claude, Codex, Gemini adapters implement a
  common `SkillRunner` interface
- No state writes — execution manager reports events to orchestrator, which
  updates the board

**Skills remain SKILL.md files.** The Go binary reads them, constructs the
prompt, and passes them to the AI CLI. Skills do NOT call
`update-pipeline-state.sh` — that script is deleted. Skills produce artifacts
(code, context files, commits); the orchestrator observes completion.

### Module 4: Orchestrator

Replaces: Scheduling logic scattered across `HeadlessOrchestrator`,
`IssueQueueService`, `pickupIssue`, `runBatchPipeline`.

**Responsibilities:**

- Read project board → determine what to run next
- Cross-repo epic coordination (which sub-issues are ready, which are blocked)
- Dependency ordering (blockedBy relationships across repos)
- Dispatch to execution manager
- Merge serialization (one merge per repo at a time)
- Stall detection and recovery

**Scheduling algorithm:**

```
1. Fetch all "Ready" items from project board (server-side filtered)
2. Build parent epic map (sub-issue number → epic) for transitive blocking
3. For each item, check blockedBy — skip if any blocker is OPEN
4. For each item, check parent epic blockedBy — skip if parent epic is blocked
   (cross-epic transitive blocking, see issue-create SKILL.md Phase 3.5)
5. Sort by priority (P0 > P1 > P2 > P3), then by issue number (oldest first)
6. For each item, determine repo from issue URL
7. Check concurrent execution limits per repo
8. Dispatch to execution manager
9. On completion, check if epic sub-issues are all done → close epic
```

**Cross-repo epic handling:**

- Epic sub-issues may span repos A, B, C
- Orchestrator sees all of them (board is cross-repo)
- Each sub-issue executes in its own repo context
- Epic completion check runs after any sub-issue completes
- Repos not cloned locally: warn and skip (or auto-clone if configured)

### Module 5: Platform Client ✅ Complete

Replaces: Future `@nightgauge/shared-types` tRPC client in the extension.
**Status**: Implemented in `internal/platform/`.

**API contract decision: OpenAPI 3.1 (chosen).** The platform keeps tRPC
internally. `oapi-codegen` generates a typed Go client from the platform's
`packages/api/openapi/openapi.yaml` spec. This:

- Preserves the platform's tRPC investment
- Adds zero runtime overhead to the platform
- Gives the Go binary a typed, validated client
- Makes a future public SDK free (spec already exists)

**Implementation:**

```text
internal/platform/
├── client.go     — platform.Client: wraps generated OpenAPI client, health polling,
│                   connectivity state machine (online → degraded → offline)
├── license.go    — LicenseService: validate + cache (24h TTL, 7-day grace period)
├── skills.go     — SkillService: resolve + LRU cache
├── analytics.go  — AnalyticsService: fire-and-forget event ingestion
├── team.go       — TeamService: member list, analytics queries
├── billing.go    — BillingService: portal session creation
└── offline.go    — offline/degraded fallbacks for all services
```

**IPC methods exposed (via `internal/ipc/server.go`):**

| IPC Method                     | Platform Endpoint                 | Description                 |
| ------------------------------ | --------------------------------- | --------------------------- |
| `platform.status`              | (local state)                     | Connectivity mode           |
| `platform.license`             | (cached)                          | Current license features    |
| `platform.validateLicense`     | `POST /v1/license/validate`       | Validate key + bind machine |
| `platform.resolveSkill`        | `GET /v1/skills/{skillId}`        | Fetch skill content         |
| `platform.submitAnalytics`     | `POST /v1/analytics/ingest`       | Ingest execution event      |
| `platform.getUsageSummary`     | `GET /v1/dashboard/usage/summary` | Usage dashboard data        |
| `platform.getTeamMembers`      | `GET /v1/team/{teamId}/members`   | Team roster                 |
| `platform.createPortalSession` | `POST /v1/auth/session`           | Billing portal redirect URL |
| `platform.healthCheck`         | `GET /v1/health`                  | Platform API health         |

**Offline fallback:**

- License: cached token, 24h TTL, 7-day grace → community tier
- Skills: bundled free-tier skills in the Go binary
- Routing: local heuristic model selection
- Analytics: buffered locally, pushed on reconnection

### Module 6: Intelligence Migration

Replaces: SDK intelligence services (`ComplexityModelService`,
`AutoModelSelector`, `HealthAnalysisEngine`, `FailurePatternDetector`,
`TokenEfficiencyAnalyzer`, etc.)

**IP protection:** These algorithms are the core product differentiator.
Compiling them into a Go binary protects them better than shipping TypeScript
source.

**Services to migrate:**

- Complexity estimation (issue → size prediction)
- Model routing (stage + complexity → haiku/sonnet/opus)
- Health analysis (7-dimension scoring)
- Failure taxonomy (classify failures for retry/escalation)
- Token economics (budget tracking, cost estimation)
- Pipeline learning system (outcome recording → calibration)
- Epic batch assessment (sequential vs parallel strategy)

**Migration approach:** Port the algorithms, not the TypeScript. The Go
implementations should be clean rewrites based on the documented behavior, not
line-by-line transpilations. The SDK test suite provides the specification.

### Module 7: VSCode Extension Thinning

The extension becomes a **UI shell** that communicates with the Go binary via
JSON-over-stdio IPC.

**What stays in TypeScript:**

- Tree views (PipelineTreeProvider, ProjectBoardTreeProvider, etc.)
- Dashboard webview (HTML/CSS/JS rendering)
- Command palette handlers (thin wrappers that call Go binary)
- Settings UI
- Output window (displays streamed output from Go binary)

**What moves to Go:**

- All business logic currently in `src/services/`
- All GitHub API calls
- All state management
- All shell script invocations
- Process spawning and management

**IPC protocol:**

```jsonc
// VSCode → Go (request)
{"id": 1, "method": "board.getReadyItems", "params": {"repo": "nightgauge/nightgauge"}}

// Go → VSCode (response)
{"id": 1, "result": [{"number": 1311, "title": "...", "priority": "P0", ...}]}

// Go → VSCode (event, no id)
{"event": "stage.complete", "data": {"issue": 1311, "stage": "feature-dev"}}
```

The Go binary runs as a long-lived child process of the extension, started on
activation and stopped on deactivation. Communication is bidirectional
JSON-over-stdio (same pattern as LSP).

## Go Module Structure

```
github.com/nightgauge/nightgauge/
├── cmd/
│   └── nightgauge/
│       └── main.go                    # CLI entry point (cobra)
├── internal/
│   ├── github/                        # Module 1: GitHub Client
│   │   ├── client.go                  # GraphQL + REST client
│   │   ├── board.go                   # Project board operations
│   │   ├── issues.go                  # Issue CRUD + sub-issues
│   │   ├── prs.go                     # PR operations
│   │   └── types.go                   # GitHub domain types
│   ├── state/                         # Module 2: Board-Driven State
│   │   ├── board_state.go             # Read/write board fields
│   │   ├── runtime_state.go           # In-memory execution state
│   │   └── history.go                 # Execution history JSONL
│   ├── execution/                     # Module 3: Execution Manager
│   │   ├── manager.go                 # Goroutine-per-execution
│   │   ├── worktree.go                # Git worktree lifecycle
│   │   ├── skill_runner.go            # Skill process spawning
│   │   ├── adapters/                  # Claude, Codex, Gemini
│   │   │   ├── adapter.go             # Interface
│   │   │   ├── claude.go
│   │   │   ├── codex.go
│   │   │   └── gemini.go
│   │   └── context/                   # Context file I/O
│   │       ├── schemas.go             # Zod → Go struct validation
│   │       └── files.go               # Read/write context JSON
│   ├── orchestrator/                  # Module 4: Orchestrator
│   │   ├── scheduler.go               # Board-driven scheduling
│   │   ├── epic.go                    # Cross-repo epic coordination
│   │   ├── dependencies.go            # Blocking relationship ordering
│   │   └── merge_lock.go              # Per-repo merge serialization
│   ├── platform/                      # Module 5: Platform Client
│   │   ├── client.go                  # Generated OpenAPI client
│   │   ├── offline.go                 # Degraded/offline fallback
│   │   ├── license.go                 # License caching + validation
│   │   └── skills.go                  # Skill fetching + caching
│   ├── intelligence/                  # Module 6: Intelligence
│   │   ├── complexity/                # Complexity model
│   │   ├── routing/                   # Model selection
│   │   ├── health/                    # 7-dimension analysis
│   │   ├── failure/                   # Failure taxonomy
│   │   ├── tokens/                    # Token economics
│   │   └── learning/                  # Pipeline learning system
│   ├── config/                        # Configuration
│   │   ├── loader.go                  # 6-tier config loading
│   │   └── schema.go                  # Config struct + validation
│   └── ipc/                           # IPC for VSCode
│       ├── server.go                  # JSON-over-stdio server
│       ├── protocol.go                # Request/response types
│       └── events.go                  # Event streaming
├── pkg/                               # Public packages (if needed)
│   └── types/                         # Shared domain types
└── go.mod
```

## Platform Integration Plan

### Phase 0: API Contract (Before Go Binary)

The platform has zero application code. Before building either the Go binary or
the platform services, define the API contract:

1. Write OpenAPI 3.1 spec for all platform procedures (based on existing
   `public-api-spec.md`)
2. Generate Go client types from the spec
3. Generate TypeScript types from the spec (replaces
   `@nightgauge/shared-types`)
4. Platform implements tRPC routers that conform to the spec
5. Both sides validate against the same contract

**Spec lives in:** `acme-platform/api/openapi.yaml` **Generated
clients:** Published as build artifacts

### Impact on Platform Backlog

The platform's 15 epics remain valid. Changes:

| Platform Epic     | Impact                                               |
| ----------------- | ---------------------------------------------------- |
| #13 shared-types  | Becomes OpenAPI spec + codegen (not hand-written TS) |
| #28 API server    | tRPC routers must conform to OpenAPI spec            |
| #42 License keys  | Go client consumes license.validate endpoint         |
| #51 Skill serving | Go client consumes skills.resolve endpoint           |
| #113 Analytics    | Go client pushes analytics.ingest                    |
| #129 Public SDK   | OpenAPI spec IS the public SDK contract              |

The platform stays TypeScript. The API contract becomes language-agnostic. This
is additive, not disruptive.

## Migration Phases (Strangler Fig)

Each phase delivers a working increment. The pipeline is usable throughout.

### Phase 1: Go Binary Foundation + GitHub Client (Weeks 1-3)

**Build:**

- CLI scaffold (cobra)
- GitHub GraphQL client
- Project board read operations
- IPC server (JSON-over-stdio)

**Integrate:**

- VSCode extension can optionally use Go binary for board data
- Falls back to TypeScript ProjectBoardService if binary unavailable
- CLI: `nightgauge board list --status Ready`

**Validate:**

- Board data matches TypeScript implementation exactly
- Extension tree views render identically
- Performance: board fetch < 2s (vs current ~5-8s for 600+ items)

**Delete nothing yet.**

### Phase 2: Board-Driven State + Execution (Weeks 4-7)

**Build:**

- Board-driven state management
- Execution manager (worktrees, skill dispatch)
- Process lifecycle management
- Output streaming via IPC

**Integrate:**

- Extension routes pipeline execution through Go binary
- State shown in tree view comes from board fields (via Go)
- `state.json` becomes optional runtime cache, not source of truth

**Validate:**

- Pipeline runs issue through all 6 stages
- State survives extension restart (reads board, not file)
- Concurrent pipelines work without race conditions

**Completed:** `update-pipeline-state.sh` and all other shell script wrappers
deleted; all SKILL.md files now invoke `nightgauge project move-status` and
other Go binary commands directly (Issue #1976).

### Phase 3: Orchestrator + Multi-Repo (Weeks 8-10)

**Build:**

- Board-driven scheduler
- Cross-repo epic coordination
- Dependency ordering
- Merge serialization

**Integrate:**

- Extension delegates "pick next issue" to Go orchestrator
- Batch mode routes through Go orchestrator
- Multi-repo switching handled by orchestrator, not extension

**Validate:**

- Cross-repo epic: sub-issues in 2+ repos process correctly
- Blocked issues are skipped, unblocked issues proceed
- Merge conflicts avoided via serialization

**Delete:** `IssueQueueService`, `pickupIssue` scheduling logic.

### Phase 4: Platform Client + Intelligence (Weeks 11-14)

**Build:**

- OpenAPI client (generated from spec)
- Offline fallback logic
- Intelligence services (complexity, routing, health, etc.)

**Integrate:**

- Go binary handles license validation, skill fetching
- Intelligence runs in Go, results displayed in VSCode dashboard
- Analytics pushed to platform from Go

**Validate:**

- Pipeline works with platform online and offline
- Model routing matches TypeScript implementation decisions
- Health analysis produces same scores

**Delete:** TypeScript SDK intelligence services.

### Phase 5: Extension Thinning + Cleanup (Weeks 15-17)

**Build:**

- Remaining IPC methods for all extension features
- Dashboard data sourced from Go

**Integrate:**

- Extension is pure UI — all business logic in Go
- All `src/services/` replaced by IPC calls

**Validate:**

- Full test suite passes
- Extension startup time improved (no TypeScript service init)
- Binary size < 30MB

**Delete:** TypeScript SDK package, shell scripts, service classes.

## Risk Mitigation

| Risk                                  | Mitigation                                                                                  |
| ------------------------------------- | ------------------------------------------------------------------------------------------- |
| Go binary crashes, extension dead     | Process supervisor with auto-restart; graceful degradation to local-only mode               |
| IPC latency slows UI                  | Board data cached in extension; events are async; only blocking call is initial board fetch |
| Platform API changes break Go client  | OpenAPI spec is versioned; generated client validates at compile time                       |
| Migration takes longer than estimated | Each phase is independently valuable; can ship Phase 1-2 and pause                          |
| Team unfamiliar with Go               | CLI and orchestration code is straightforward Go; no exotic patterns needed                 |
| Skill compatibility                   | Skills are SKILL.md files — unchanged. Only the state-update calls are removed.             |

## Success Criteria

- Zero local state files for durable pipeline state (board is source of truth)
- Zero shell script dependencies (no bash, jq, gh CLI required)
- Cross-repo epic execution without workspace pre-configuration
- Pipeline survives extension restart without state loss
- Single binary distribution: `brew install nightgauge`
- Extension startup < 1 second (vs current 3-5 seconds)
- Pipeline overhead < 50% of current (process spawn reduction)

## Relationship to Existing Epics

| Epic                               | Disposition                                                                          |
| ---------------------------------- | ------------------------------------------------------------------------------------ |
| #1543 (Shell-to-Go migration)      | **Superseded** by this plan. Phase 1-2 covers its scope plus architectural redesign. |
| #1503 (Greenfield pipeline)        | Continues independently. Go binary will handle greenfield detection.                 |
| #1504 (Pipeline visibility)        | Continues. UI stays TypeScript; data source moves to Go.                             |
| #1505 (Cost observability)         | Intelligence migration (Phase 4) covers this.                                        |
| #1452-#1455 (Platform integration) | Phase 4 covers Go-side. Platform epics proceed independently.                        |
| #1752 (Audit trail)                | Board-driven state provides audit trail by design (board history).                   |

## Decision Log

| Decision                       | Rationale                                                                     |
| ------------------------------ | ----------------------------------------------------------------------------- |
| OpenAPI over gRPC              | Platform is HTTP/tRPC; OpenAPI is additive. gRPC requires new transport.      |
| JSON-over-stdio over WebSocket | Simpler, same pattern as LSP, no port conflicts                               |
| Board as state over local DB   | Board is already shared, cross-repo, accessible from CI. No new infra.        |
| Go over Rust                   | Go is simpler, faster to write, sufficient performance. Team alignment.       |
| Strangler fig over big bang    | Pipeline must stay usable. Each phase delivers independently.                 |
| Platform stays TypeScript      | Zero application code yet; tRPC investment is sound. Convert later if needed. |
