# Migration Guide

This document tracks migration paths for deprecated components in Nightgauge.

---

## Config File Migration (nightgauge.yaml → config.yaml)

**Status**: Deprecated as of v0.8.0 (Issue #433) **Removal Target**: v1.0.0

### Background

The configuration file was renamed from `nightgauge.yaml` to `config.yaml` to:

- Follow standard naming conventions (like `.eslintrc.yaml` →
  `eslint.config.js`)
- Align with the new 6-tier configuration system (Issue #436)
- Enable the local override file (`.nightgauge/config.local.yaml`)
- Improve discoverability in `.nightgauge/` directory listings

### What Changed

| Aspect          | Old                           | New                             |
| --------------- | ----------------------------- | ------------------------------- |
| Project config  | `.nightgauge/nightgauge.yaml` | `.nightgauge/config.yaml`       |
| Global config   | N/A                           | `~/.nightgauge/config.yaml`     |
| Local overrides | N/A                           | `.nightgauge/config.local.yaml` |
| Config system   | 2-tier (project + env)        | 6-tier (see CONFIGURATION.md)   |

### Migration Options

#### Option 1: Automatic Migration (Recommended)

Run the VSCode command to migrate automatically:

1. Open Command Palette (`Cmd+Shift+P` / `Ctrl+Shift+P`)
2. Run `Nightgauge: Migrate Config File`
3. The command will:
   - Rename `.nightgauge/nightgauge.yaml` to `.nightgauge/config.yaml`
   - Update any internal references
   - Show confirmation message

#### Option 2: Manual Migration

```bash
# Rename the config file
mv .nightgauge/nightgauge.yaml .nightgauge/config.yaml

# Update .gitignore to include local config
echo ".nightgauge/config.local.yaml" >> .gitignore

# Commit the changes
git add .nightgauge/config.yaml .gitignore
git rm --cached .nightgauge/nightgauge.yaml 2>/dev/null || true
git commit -m "chore: migrate nightgauge.yaml to config.yaml"
```

### Backward Compatibility

The legacy `nightgauge.yaml` file is still supported during the deprecation period:

- **Reading**: Both files are read; `config.yaml` takes precedence
- **Warning**: A deprecation warning is shown when `nightgauge.yaml` is detected
- **Suppression**: Set `NIGHTGAUGE_SUPPRESS_DEPRECATION=1` to hide warnings

```bash
# Suppress deprecation warning (not recommended for long-term)
export NIGHTGAUGE_SUPPRESS_DEPRECATION=1
```

### Setting Up the 6-Tier System

After migrating, consider setting up the full 6-tier configuration:

#### 1. Global Config (Personal Preferences)

```bash
# Create global config for user-wide defaults
mkdir -p ~/.nightgauge
cat > ~/.nightgauge/config.yaml << 'EOF'
# Global Nightgauge Configuration
# These settings apply to all repositories

pr:
  merge_strategy: squash
  delete_branch: true

human_in_the_loop:
  auto_accept_stages: false
  auto_accept_permissions: false
EOF
```

#### 2. Local Config (Developer Overrides)

```bash
# Create local config for personal overrides (not committed)
cat > .nightgauge/config.local.yaml << 'EOF'
# Local developer overrides - NOT committed to git

pipeline:
  skip:
    tests: true  # Skip tests while debugging
EOF

# Ensure it's gitignored
echo ".nightgauge/config.local.yaml" >> .gitignore
```

### Viewing Configuration Sources

After migration, use `/nightgauge-config-show` to see the merged configuration with
source annotations:

```
/nightgauge-config-show

Effective Configuration
════════════════════════════════════════════════════════════
pr.merge_strategy: squash                    [global]
pr.reviewers: ["alice", "bob"]              [project]
pr.delete_branch: false                      [local]
pipeline.skip.tests: true                    [env]
project.number: 10                           [project]
branch.base: main                            [default]

Merge time: 2.45ms
Env vars applied: NIGHTGAUGE_PIPELINE_SKIP_TESTS
```

### FAQ

**Q: What if I have both `nightgauge.yaml` and `config.yaml`?**

A: `config.yaml` takes precedence. The legacy file is ignored but generates a
warning. Remove the old file to silence the warning.

**Q: Do I need to update my CI/CD pipelines?**

A: Only if they reference the file path directly. Environment variables
(`NIGHTGAUGE_*`) continue to work unchanged.

**Q: When will `nightgauge.yaml` support be removed?**

A: Planned for v1.0.0. The deprecation warning includes the removal timeline.

**Q: Can I use the old file name indefinitely?**

A: It's not recommended. Support will be removed in v1.0.0, and features like
local config (`config.local.yaml`) only work with the new naming convention.

### Timeline

| Milestone     | Version | Action                              |
| ------------- | ------- | ----------------------------------- |
| Deprecation   | v0.8.0  | Warning added, both files supported |
| 6-tier system | v0.8.0  | Global and local config tiers added |
| Last warning  | v0.9.x  | Final warnings before removal       |
| Removal       | v1.0.0  | `nightgauge.yaml` no longer read    |

### Questions?

If you have questions about this migration, please open an issue with the
`question` label.

---

## sync-project-iteration.sh → ProjectIterationService.ts

**Status**: Deprecated as of v1.0.0 (Issue #132) **Removal Target**: v2.0.0

### Background

The `sync-project-iteration.sh` shell script (537 lines) has been replaced by
`ProjectIterationService.ts` to:

- Eliminate fragile jq date arithmetic
- Provide proper TypeScript type safety
- Enable unit testing of date calculations
- Follow the singleton service pattern used elsewhere in the codebase

### What Changed

| Aspect         | Shell Script             | TypeScript Service                 |
| -------------- | ------------------------ | ---------------------------------- |
| Date handling  | jq strptime/mktime       | date-fns (parseISO, addDays, etc.) |
| Error handling | Exit codes               | Structured SyncResult objects      |
| Testing        | Manual verification only | Comprehensive unit tests           |
| Type safety    | None                     | Full TypeScript interfaces         |
| Integration    | Called via Bash          | Called as service method           |

### Migration Steps

#### For VSCode Extension Users

No action required. The extension now uses `ProjectIterationService` internally.

#### For CLI/Script Users

If you call `sync-project-iteration.sh` directly from scripts:

1. **Immediate**: The shell script still works but shows a deprecation warning
2. **Suppress warning**: Set `NIGHTGAUGE_SUPPRESS_DEPRECATION=1` environment
   variable
3. **Migrate**: Update your scripts to use the VSCode extension or SDK

**Before (deprecated)**:

```bash
# Direct shell script invocation
claude-plugins/nightgauge/hooks/lib/sync-project-iteration.sh 123 @current
```

**After (recommended)**:

```typescript
// Use ProjectIterationService from VSCode extension
import { ProjectIterationService } from "./services/ProjectIterationService";

const service = ProjectIterationService.getInstance();
const result = await service.syncIteration(123, "@current");
```

#### For Skill Authors

The `nightgauge-issue-pickup` skill has been updated to reference the TypeScript
service. If you have custom skills that call the shell script:

1. Update skill documentation to reference `ProjectIterationService`
2. For CLI invocations, continue using the shell script until SDK CLI support is
   added

### API Compatibility

The TypeScript service returns the same JSON structure as the shell script:

**Success response**:

```json
{
  "success": true,
  "issue": 123,
  "project": 10,
  "item_id": "PVTI_lADO...",
  "iteration": {
    "id": "abc123",
    "title": "Sprint 5"
  },
  "action": "assigned"
}
```

**Skip response**:

```json
{
  "skipped": true,
  "reason": "Sprint feature not enabled in .nightgauge/config.yaml"
}
```

### Timeline

| Milestone       | Version | Action                                        |
| --------------- | ------- | --------------------------------------------- |
| Deprecation     | v1.0.0  | Warning added to shell script                 |
| SDK CLI Support | v1.x.x  | `nightgauge iteration sync` command (planned) |
| Removal         | v2.0.0  | Shell script removed from repository          |

### Suppressing Deprecation Warnings

During the transition period, you can suppress the deprecation warning:

```bash
# Single invocation
NIGHTGAUGE_SUPPRESS_DEPRECATION=1 ./sync-project-iteration.sh 123 @current

# In your environment
export NIGHTGAUGE_SUPPRESS_DEPRECATION=1
```

### Questions?

If you have questions about this migration, please open an issue with the
`question` label.

---

## Codex/Gemini Wrapper Scripts → Unified Stage Runner

**Status**: Removed in #1804 (commit `ea074442`,
`chore(#1565): remove shell scripts, complete Go binary migration`)

### Background

Codex (and later Gemini) support was initially shipped as wrapper-first
invocation only — one shell script per stage under
`configs/<tool>/commands/`:

- `configs/codex/commands/issue-pickup.sh`
- `configs/codex/commands/feature-planning.sh`
- `configs/codex/commands/feature-dev.sh`
- `configs/codex/commands/feature-validate.sh`
- `configs/codex/commands/pr-create.sh`
- `configs/codex/commands/pr-merge.sh`

As cross-tool parity matured, command behavior was centralized in shared skills
and stage contracts, and the per-tool wrapper scripts were removed in #1804.
The model is now:

1. Skill behavior is the source of truth (`skills/`)
2. Canonical capability IDs are stable (`nightgauge.<capability>`)
3. Tool-specific syntax is an adapter layer only, served by a single stage
   runner

### Migration Guide

#### Before (removed wrapper-only usage)

```bash
configs/codex/commands/issue-pickup.sh 570
configs/codex/commands/feature-planning.sh 570
configs/codex/commands/feature-dev.sh 570
configs/codex/commands/feature-validate.sh 570
configs/codex/commands/pr-create.sh 570
configs/codex/commands/pr-merge.sh 570
```

#### After (unified stage runner)

`scripts/run-stage.sh <adapter> <stage> <issue>` is the single supported entry
point for non-Claude adapters:

```bash
scripts/run-stage.sh codex issue-pickup 570
scripts/run-stage.sh codex feature-planning 570
scripts/run-stage.sh codex feature-dev 570
scripts/run-stage.sh codex feature-validate 570
scripts/run-stage.sh codex pr-create 570
scripts/run-stage.sh codex pr-merge 570
```

Use the same form with `gemini` for the Gemini adapter. Other tools map to the
same capability contract:

- Copilot/Cursor: invoke shared skills by name
- Claude Code: invoke slash commands mapped to the same capability contract

Reference: `docs/strategy/codex/CROSS_TOOL_COMMAND_CONTRACT.md`

### Notes

- Stage artifacts are unchanged (`.nightgauge/pipeline/*.json`,
  `.nightgauge/plans/*.md`), so an issue started under the old wrappers
  continues cleanly under the stage runner.
- The deterministic project-board and config logic that previously lived in
  shell scripts now lives in the `nightgauge` Go binary
  (see [GO_BINARY.md](GO_BINARY.md)).

---

_Last updated: 2026-07-21 (Issues #132, #570, #1804, #3799)_
