---
name: config-show
description: Display the effective Nightgauge configuration with source annotations,
  showing where each value comes from (default, global, project, or environment).
  Use when debugging config precedence or confirming which tier supplies a value.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Bash
disable-model-invocation: true
---

# Config Show

> Display effective configuration with source annotations

## Description

This skill displays the effective Nightgauge configuration, showing:

1. The merged configuration from all sources
2. Source annotations indicating where each value came from
3. Configuration file locations
4. Any validation warnings

## Invocation

| Tool           | Command                                |
| -------------- | -------------------------------------- |
| Claude Code    | `/nightgauge-config-show` (via plugin) |
| OpenAI Codex   | `$nightgauge-config-show`              |
| GitHub Copilot | Invoke via Agent Skills                |
| Cursor         | Invoke via Agent Skills                |

## Arguments

```bash
# Show full effective config
/nightgauge-config-show

# Show specific section
/nightgauge-config-show --section pr

# Show only values from a specific source
/nightgauge-config-show --source global

# Show config paths only
/nightgauge-config-show --paths

# JSON output (for scripting)
/nightgauge-config-show --json
```

## Configuration Precedence

Values are resolved in this order (highest priority first):

```
┌─────────────────────────────────────────────────────────────────┐
│                    CONFIG PRECEDENCE                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Environment Variables (NIGHTGAUGE_*)     ← Highest priority    │
│         ↓ if not set                                             │
│  2. Project Config (.nightgauge/config.yaml)                       │
│         ↓ if not set                                             │
│  3. Global Config (~/.nightgauge/config.yaml)                      │
│         ↓ if not set                                             │
│  4. Built-in Defaults                      ← Lowest priority    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Locate Configuration Files

#### Step 1.1: Find Global Config

```bash
# Check for global config in platform-specific location
# macOS: ~/.nightgauge/config.yaml
# Linux: ~/.config/nightgauge/config.yaml or XDG_CONFIG_HOME/nightgauge/config.yaml
# Windows: %APPDATA%/nightgauge/config.yaml

# Priority: NIGHTGAUGE_CONFIG_HOME > XDG_CONFIG_HOME > platform default
if [[ -n "${NIGHTGAUGE_CONFIG_HOME:-}" ]]; then
  GLOBAL_CONFIG_DIR="$NIGHTGAUGE_CONFIG_HOME"
elif [[ -n "${XDG_CONFIG_HOME:-}" ]]; then
  GLOBAL_CONFIG_DIR="$XDG_CONFIG_HOME/nightgauge"
else
  case "$(uname -s)" in
    Darwin*) GLOBAL_CONFIG_DIR="$HOME/.nightgauge" ;;
    Linux*)  GLOBAL_CONFIG_DIR="$HOME/.config/nightgauge" ;;
    *)       GLOBAL_CONFIG_DIR="$HOME/.nightgauge" ;;
  esac
fi

GLOBAL_CONFIG="$GLOBAL_CONFIG_DIR/config.yaml"

if [[ -f "$GLOBAL_CONFIG" ]]; then
  echo "✓ Global config: $GLOBAL_CONFIG"
else
  echo "○ Global config: $GLOBAL_CONFIG (not found)"
fi
```

#### Step 1.2: Find Project Config

```bash
# Check for project config
GIT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null)
PROJECT_CONFIG="$GIT_ROOT/.nightgauge/config.yaml"
LEGACY_CONFIG="$GIT_ROOT/.nightgauge/nightgauge.yaml"

if [[ -f "$PROJECT_CONFIG" ]]; then
  echo "✓ Project config: $PROJECT_CONFIG"
elif [[ -f "$LEGACY_CONFIG" ]]; then
  echo "⚠ Project config: $LEGACY_CONFIG (legacy, please migrate)"
else
  echo "○ Project config: $PROJECT_CONFIG (not found)"
fi
```

---

### Phase 2: Load and Merge Configuration

#### Step 2.1: Parse YAML Files

Read global and project config files using the YAML parsing utilities.

#### Step 2.2: Merge with Source Tracking

Track the source of each value during the merge:

```
Merge Order:
1. Start with DEFAULT_CONFIG
2. Overlay global config values
3. Overlay project config values
4. Apply environment variable overrides

Track source for each leaf value.
```

---

### Phase 3: Display Configuration

#### Step 3.1: Output Header

```
┌─────────────────────────────────────────────────────────────────┐
│  NIGHTGAUGE CONFIGURATION                                           │
└─────────────────────────────────────────────────────────────────┘

Config Files:
  Global:  ~/.nightgauge/config.yaml (exists)
  Project: .nightgauge/config.yaml (exists)
```

#### Step 3.2: Output Effective Config

Display each section with source annotations:

```
┌─────────────────────────────────────────────────────────────────┐
│  project                                                         │
├─────────────────────────────────────────────────────────────────┤
│  number: 10                              [project]              │
│  auto_dates: true                        [default]              │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  pr                                                              │
├─────────────────────────────────────────────────────────────────┤
│  merge_strategy: squash                  [global]               │
│  delete_branch: true                     [default]              │
│  reviewers: []                           [default]              │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  branch                                                          │
├─────────────────────────────────────────────────────────────────┤
│  base: main                              [default]              │
│  protected: [main, master]               [default]              │
│  prefixes:                                                      │
│    feature: feat/                        [default]              │
│    bugfix: fix/                          [default]              │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  pipeline (budget)                                               │
├─────────────────────────────────────────────────────────────────┤
│  budget_preset: standard                 [project]              │
│  budget_mode: hard                       [default]              │
│  budget_grace_percent: 50                [default]              │
│  stage_budgets: (none)                   [default]              │
└─────────────────────────────────────────────────────────────────┘
```

#### Step 3.3: Source Legend

```
Source Legend:
  [default] = Built-in default value
  [global]  = From ~/.nightgauge/config.yaml
  [project] = From .nightgauge/config.yaml
  [env]     = From NIGHTGAUGE_* environment variable
```

---

### Phase 4: Validation Warnings (if any)

```
┌─────────────────────────────────────────────────────────────────┐
│  ⚠ VALIDATION WARNINGS                                          │
├─────────────────────────────────────────────────────────────────┤
│  global: pr.merge_strategy - Invalid value "invalid"            │
│  project: project.number - Must be a positive integer           │
└─────────────────────────────────────────────────────────────────┘
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Output Formats

### Default (Human-Readable)

Formatted tables with source annotations as shown above.

### JSON (--json flag)

```json
{
  "config": {
    "project": {
      "number": 10,
      "auto_dates": true
    },
    "pr": {
      "merge_strategy": "squash"
    }
  },
  "sources": {
    "project.number": "project",
    "project.auto_dates": "default",
    "pr.merge_strategy": "global"
  },
  "files": {
    "global": {
      "path": "/Users/user/.nightgauge/config.yaml",
      "exists": true
    },
    "project": {
      "path": "/path/to/repo/.nightgauge/config.yaml",
      "exists": true,
      "isLegacy": false
    }
  },
  "warnings": []
}
```

---

## Error Handling

### Global Config Parse Error

```
┌─────────────────────────────────────────────────────────────────┐
│  ❌ ERROR: Global Config Parse Error                             │
├─────────────────────────────────────────────────────────────────┤
│  File: ~/.nightgauge/config.yaml                                    │
│  Error: YAML syntax error at line 5: unexpected character       │
│                                                                  │
│  Global config will be skipped. Fix the syntax error or         │
│  remove the file to use project config only.                    │
└─────────────────────────────────────────────────────────────────┘
```

### Project Config Parse Error

```
┌─────────────────────────────────────────────────────────────────┐
│  ❌ ERROR: Project Config Parse Error                            │
├─────────────────────────────────────────────────────────────────┤
│  File: .nightgauge/config.yaml                                      │
│  Error: YAML syntax error at line 12: duplicate key             │
└─────────────────────────────────────────────────────────────────┘
```

---

## Examples

### Example 1: No Config Files

```
$ /nightgauge-config-show

┌─────────────────────────────────────────────────────────────────┐
│  NIGHTGAUGE CONFIGURATION                                           │
└─────────────────────────────────────────────────────────────────┘

Config Files:
  Global:  ~/.nightgauge/config.yaml (not found)
  Project: .nightgauge/config.yaml (not found)

Note: Using default configuration values.
Run 'nightgauge init' to create a project config file.
```

### Example 2: Global Config Only

```
$ /nightgauge-config-show --section pr

┌─────────────────────────────────────────────────────────────────┐
│  pr                                                              │
├─────────────────────────────────────────────────────────────────┤
│  merge_strategy: rebase                  [global]               │
│  delete_branch: true                     [global]               │
│  reviewers: ["alice"]                    [global]               │
│  auto_merge: false                       [default]              │
└─────────────────────────────────────────────────────────────────┘
```

### Example 3: Global + Project Config

```
$ /nightgauge-config-show --section pr

┌─────────────────────────────────────────────────────────────────┐
│  pr                                                              │
├─────────────────────────────────────────────────────────────────┤
│  merge_strategy: squash                  [project]              │
│  delete_branch: true                     [global]               │
│  reviewers: ["alice", "bob"]             [project]              │
│  auto_merge: false                       [default]              │
└─────────────────────────────────────────────────────────────────┘
```

### Example 4: Environment Override

```
$ NIGHTGAUGE_PR_MERGE_STRATEGY=merge /nightgauge-config-show --section pr

┌─────────────────────────────────────────────────────────────────┐
│  pr                                                              │
├─────────────────────────────────────────────────────────────────┤
│  merge_strategy: merge                   [env]                  │
│  delete_branch: true                     [global]               │
│  reviewers: ["alice", "bob"]             [project]              │
│  auto_merge: false                       [default]              │
└─────────────────────────────────────────────────────────────────┘
```

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) -
Issue-to-PR Pipeline.
