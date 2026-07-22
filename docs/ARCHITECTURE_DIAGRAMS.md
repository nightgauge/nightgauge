# Nightgauge Architecture Diagrams

This document provides visual representations of the Nightgauge pipeline
and VSCode extension architecture using Mermaid diagrams.

## Table of Contents

1. [Pipeline Stage Flow](#1-pipeline-stage-flow)
2. [State Management Architecture](#2-state-management-architecture)
3. [Token Counting Data Flow](#3-token-counting-data-flow)
4. [Configuration Cascade](#4-configuration-cascade)
5. [Deterministic vs Probabilistic Separation](#5-deterministic-vs-probabilistic-separation)
6. [Dashboard Data Flow](#6-dashboard-data-flow)

---

## 1. Pipeline Stage Flow

This diagram shows how pipeline stages execute with context file handoffs
between stages and UI state updates.

```mermaid
sequenceDiagram
    participant User
    participant Ext as VSCode Extension
    participant CLI as Claude CLI
    participant FS as .nightgauge/pipeline/
    participant PSS as PipelineStateService
    participant Tree as TreeProvider
    participant Out as OutputWindow

    Note over User,Out: Stage Execution Flow

    User->>Ext: Run Stage Command
    Ext->>PSS: startStage(stage)
    PSS->>FS: Write state.json
    PSS-->>Tree: onStageStart event
    PSS-->>Out: onStageStart event

    Ext->>CLI: runStageSkillHeadless()
    CLI->>FS: Write running-{stage}-{N}.json

    loop Stream Output
        CLI-->>Ext: stdout (stream-json)
        Ext->>PSS: updateTokens(usage)
        PSS-->>Out: onStateChanged (tokens)
    end

    CLI->>FS: Write {stage}-{N}.json
    CLI->>FS: Delete running-{stage}-{N}.json

    Ext->>PSS: completeStage(stage)
    PSS->>FS: Update state.json
    PSS-->>Tree: onStageComplete event
    PSS-->>Out: onStageComplete event
```

### Context File Lifecycle

```
.nightgauge/
├── pipeline/
│   ├── issue-{N}.json            # Output of issue-pickup
│   ├── planning-{N}.json         # Output of feature-planning
│   ├── dev-{N}.json              # Output of feature-dev
│   ├── validate-{N}.json         # Output of feature-validate (optional)
│   ├── pr-{N}.json               # Output of pr-create
│   ├── running-{stage}-{N}.json  # Transient: indicates stage is running
│   └── state.json                # Unified state (PipelineStateService)
└── plans/
    └── {N}-{description}.md      # Plan file from feature-planning
```

---

## 2. State Management Architecture

This diagram shows the unified state management pattern where
PipelineStateService is the single source of truth and UI components subscribe
to state changes.

```mermaid
flowchart TD
    subgraph Sources["Event Sources"]
        CLI["Claude CLI<br/>(stream-json output)"]
        FS["File System<br/>(.nightgauge/pipeline/*.json)"]
        CMD["User Commands<br/>(run/stop/retry)"]
    end

    subgraph StateService["PipelineStateService (Singleton)"]
        STATE["state.json<br/>(atomic writes)"]
        EVENTS["Event Emitters<br/>onStateChanged<br/>onStageStart<br/>onStageComplete<br/>onStageError"]
    end

    subgraph Subscribers["UI Subscribers"]
        TREE["PipelineTreeProvider<br/>(sidebar)"]
        OUTPUT["OutputWindow<br/>(WebView panel)"]
        STATUS["StatusBarManager<br/>(bottom bar)"]
        DASH["Dashboard<br/>(analytics)"]
    end

    CLI -->|"Token Usage"| StateService
    FS -->|"File Watcher"| StateService
    CMD -->|"State Mutations"| StateService

    StateService -->|"subscribe"| TREE
    StateService -->|"subscribe"| OUTPUT
    StateService -->|"subscribe"| STATUS
    StateService -->|"subscribe"| DASH
```

### Key Principles

1. **Single Source of Truth**: `PipelineStateService` owns all pipeline state
2. **Event-Driven Updates**: UI components subscribe to events, don't poll
3. **Atomic Writes**: State file uses temp+rename pattern for consistency
4. **Crash Recovery**: Detects orphaned "running" stages after VS Code restart

---

## 3. Token Counting Data Flow

This diagram shows how token usage flows from Claude CLI output through parsing
to state management and UI display.

```mermaid
flowchart LR
    subgraph CLI["Claude CLI Process"]
        SPAWN["spawn('claude',<br/>['-p', '--output-format',<br/>'stream-json'])"]
        STDOUT["stdout stream"]
    end

    subgraph Parser["Token Parser"]
        PARSE["parseStreamJsonLine()"]
        ACCUM["TokenAccumulator"]
    end

    subgraph State["State Layer"]
        PSS["PipelineStateService"]
        FILE["state.json"]
    end

    subgraph UI["UI Layer"]
        OUT["OutputWindow"]
        TREE["TreeProvider<br/>(stage description)"]
    end

    SPAWN --> STDOUT
    STDOUT -->|"JSON lines"| PARSE
    PARSE -->|"ParsedTokenUsage"| ACCUM
    ACCUM -->|"onTokenUsage callback"| PSS
    PSS -->|"atomic write"| FILE
    PSS -->|"onStateChanged"| OUT
    PSS -->|"onStateChanged"| TREE
```

### Token Usage Flow

1. **CLI Output**: Claude CLI emits `stream-json` with `type: 'result'` messages
   containing usage data
2. **Parsing**: `tokenParser.ts` extracts `input_tokens`, `output_tokens`,
   `cache_read_input_tokens`, `cache_creation_input_tokens`, and
   `total_cost_usd`
3. **Accumulation**: `TokenAccumulator` sums tokens across multiple result
   messages
4. **State Update**: `PipelineStateService.updateTokens()` accumulates to total
5. **UI Display**: OutputWindow and TreeProvider subscribe and display formatted
   tokens

### Token Format Examples

```typescript
// Sidebar display
"1.5K tokens | $0.0023";

// OutputWindow header
"Input: 1,234 | Output: 567 | Cache: 890 | Cost: $0.0023";
```

---

## 4. Configuration Cascade

This diagram shows how configuration values are resolved from multiple sources
with a clear priority order.

```mermaid
flowchart TD
    subgraph Defaults["Built-in Defaults<br/>(Lowest Priority)"]
        D1["merge_strategy: squash"]
        D1b["epic_merge_strategy: merge"]
        D2["ci_timeout: 300"]
        D3["delete_branch: true"]
        D4["auto_dates: true"]
    end

    subgraph Config[".nightgauge/config.yaml<br/>(Repository Config)"]
        C1["project.number: 10"]
        C2["pr.delete_branch: true"]
        C3["branch.base: main"]
        C4["validation.require_tests: true"]
    end

    subgraph Env["Environment Variables<br/>(Highest Priority)"]
        E1["NIGHTGAUGE_PR_DELETE_BRANCH=false"]
        E2["NIGHTGAUGE_PROJECT_NUMBER=15"]
        E3["NIGHTGAUGE_CI_TIMEOUT=600"]
    end

    subgraph Effective["Effective Configuration"]
        EFF["get_config_value()<br/>get_config_bool()<br/>get_config_list()"]
    end

    Defaults --> Config
    Config --> Env
    Env --> Effective

    Effective --> Skills["Pipeline Skills"]
    Effective --> Hooks["Hook Scripts"]
    Effective --> Ext["VSCode Extension"]
```

### Configuration Sections

| Section      | Purpose                          | Example Keys                         |
| ------------ | -------------------------------- | ------------------------------------ |
| `project`    | GitHub Project board integration | `number`, `owner`, `auto_dates`      |
| `pr`         | Pull request settings            | `merge_strategy`, `delete_branch`    |
| `branch`     | Branch naming and protection     | `base`, `protected`, `prefixes`      |
| `issue`      | Issue creation defaults          | `auto_assign`, `default_labels`      |
| `pipeline`   | CI/build settings                | `ci_timeout`, `auto_fix`, `skip`     |
| `commands`   | Command overrides                | `test`, `lint`, `build`              |
| `validation` | PR quality gates                 | `require_tests`, `max_files_changed` |

### Environment Variable Naming

```
Config file key       →  Environment variable
────────────────────────────────────────────
pr.delete_branch      →  NIGHTGAUGE_PR_DELETE_BRANCH
project.number        →  NIGHTGAUGE_PROJECT_NUMBER
pipeline.ci_timeout   →  NIGHTGAUGE_PIPELINE_CI_TIMEOUT
```

---

## 5. Deterministic vs Probabilistic Separation

This diagram shows the architectural separation between deterministic operations
(shell scripts) and probabilistic operations (AI skills).

```mermaid
flowchart TB
    subgraph Probabilistic["PROBABILISTIC (AI Skills)"]
        direction TB
        P1["feature-planning<br/>• Interpret requirements<br/>• Design architecture<br/>• Make trade-off decisions"]
        P2["feature-dev<br/>• Generate code<br/>• Write tests<br/>• Handle edge cases"]
        P3["pr-create<br/>• Generate PR description<br/>• Summarize changes<br/>• Write test plans"]
    end

    subgraph Deterministic["DETERMINISTIC (Shell Scripts)"]
        direction TB
        D1["sync-project-status.sh<br/>• Updates project Status field directly<br/>• Via gh project item-edit (GraphQL)<br/>• No label manipulation"]
        D2["signal-stage-start.sh<br/>• Create running-*.json<br/>• Timestamp injection"]
        D3["cleanup-context-files.sh<br/>• Delete all context files<br/>• Delete plan files"]
        D4["add-to-project.sh<br/>• Add issue to project<br/>• Set priority/size fields"]
    end

    P1 -->|"calls"| D1
    P1 -->|"calls"| D2
    P2 -->|"calls"| D2
    P3 -->|"calls"| D1
    P3 -->|"calls"| D3
```

### Decision Framework

| Use Deterministic When                  | Use Probabilistic When                 |
| --------------------------------------- | -------------------------------------- |
| Fixed input → output mapping            | Creative/interpretive work needed      |
| Same input ALWAYS produces same output  | Context understanding required         |
| Accuracy and consistency are critical   | Judgment or trade-offs involved        |
| Cost and latency matter (no LLM tokens) | Natural language interpretation needed |
| Logic can be expressed as rules         | Output format varies with complexity   |

### Deterministic Operations Matrix

| Operation                   | Type          | Script                     | Called By               |
| --------------------------- | ------------- | -------------------------- | ----------------------- |
| Stage start signal          | Deterministic | `signal-stage-start.sh`    | All skills              |
| Stage complete signal       | Deterministic | `signal-stage-complete.sh` | All skills              |
| Project Status field update | Deterministic | `sync-project-status.sh`   | issue-pickup, pr-create |
| Add issue to project        | Deterministic | `add-to-project.sh`        | issue-create            |
| Context file cleanup        | Deterministic | `cleanup-context-files.sh` | pr-merge                |
| Code generation             | Probabilistic | SKILL.md instructions      | feature-dev             |
| PR description generation   | Probabilistic | SKILL.md instructions      | pr-create               |
| Requirements analysis       | Probabilistic | SKILL.md instructions      | feature-planning        |

### Benefits of Separation

1. **Cost Efficiency**: Deterministic operations consume zero LLM tokens
2. **Predictability**: Scripts always behave the same way
3. **Speed**: Shell scripts execute in milliseconds vs seconds for LLM calls
4. **Debuggability**: Deterministic code is easier to test and fix
5. **Reliability**: No LLM hallucination risk for critical operations

---

## 6. Dashboard Data Flow

This diagram shows how the dashboard aggregates data from the authoritative
PipelineStateService and maintains its own historical state.

```mermaid
flowchart TD
    subgraph Authoritative["Authoritative State (Real-time)"]
        PSS["PipelineStateService<br/>(Singleton)"]
        FILE["state.json<br/>(atomic writes)"]
    end

    subgraph Derived["Derived State (Historical)"]
        DS["DashboardState<br/>(workspace storage)"]
        HIST["Run History<br/>(last 50 runs)"]
    end

    subgraph UI["Dashboard UI Components"]
        CHARTS["Charts<br/>• Token usage bar<br/>• Cumulative line<br/>• Cost breakdown"]
        STATS["Summary Cards<br/>• Total runs<br/>• Time saved<br/>• Cost/session"]
        TREND["Trend Analysis<br/>• Token trends<br/>• Efficiency %"]
    end

    PSS -->|"atomic write"| FILE
    PSS -->|"onStateChanged"| DS
    DS -->|"addToHistory()"| HIST
    HIST -->|"persists to"| WS["Workspace Storage"]

    DS -->|"getCurrentRun()"| CHARTS
    DS -->|"getAggregates()"| STATS
    DS -->|"getHistoricalData()"| TREND
```

### Data Ownership Principles

| Data Source             | Owner                | Update Frequency  | Persistence       |
| ----------------------- | -------------------- | ----------------- | ----------------- |
| Current pipeline state  | PipelineStateService | Real-time         | state.json        |
| Current run token usage | PipelineStateService | Per-stage         | state.json        |
| Run history             | DashboardState       | On run complete   | Workspace storage |
| Session aggregates      | DashboardState       | On demand         | In-memory         |
| Time savings config     | VS Code settings     | User-configurable | VS Code settings  |

### Subscription Pattern

```typescript
// Dashboard subscribes to authoritative state
pipelineStateService.onStateChanged((state) => {
  if (state) {
    dashboardState.syncFromPipelineState(state);
    dashboard.updatePanel();
  }
});

// Dashboard also subscribes to individual events for granular updates
pipelineStateService.onStageStart(({ stage }) => {
  dashboardState.setStageRunning(stage);
});

pipelineStateService.onStageComplete(({ stage }) => {
  dashboardState.setStageComplete(stage);
});
```

This ensures the dashboard always reflects the authoritative state while
maintaining its own historical analysis capabilities.

---

## Related Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) - Overall repository architecture
- [CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md) - Context file schemas
- [GIT_WORKFLOW.md](GIT_WORKFLOW.md) - Git workflow and branch strategy

## Author

nightgauge
