# Nightgauge Scripts

This directory provides discoverable entry points to the Nightgauge
deterministic helpers. Most pipeline-state logic now lives in the compiled
`nightgauge` Go binary (see [../docs/GO_BINARY.md](../docs/GO_BINARY.md));
the scripts kept here are the few shell entry points that are still used
directly.

## Available Scripts

### install-agent-skills.sh

Installs/refreshes the Nightgauge skills into the agent tools on this
machine, sourced from the **local working tree** — so skills you edit here are
usable in any project you open with Claude Code or Codex.

```bash
# Usage
./scripts/install-agent-skills.sh                # refresh Claude Code + Codex
./scripts/install-agent-skills.sh --claude-only  # only Claude Code plugins
./scripts/install-agent-skills.sh --codex-only   # only Codex (~/.codex/skills)
```

What it does:

- **Claude Code:** points the `nightgauge-plugins` marketplace at this
  local checkout and force-reinstalls its plugins (invoked as
  `/nightgauge:<name>`).
- **Codex:** mirrors every `skills/*` skill into `~/.codex/skills/` (invoked as
  `$nightgauge-<name>`).

Runs automatically as part of the VS Code extension `dev-install.sh` (set
`NIGHTGAUGE_SKIP_SKILL_SYNC=1` to skip). The extension itself bundles the
pipeline skills into the `.vsix` separately.

### run-stage.sh

Unified stage entry point for non-Claude adapters. Used by `skillRunner.ts` and
available for direct invocation.

```bash
# Usage
./scripts/run-stage.sh <adapter> <stage> <issue-number> [stage-options...]

# Examples
./scripts/run-stage.sh codex issue-pickup 554
./scripts/run-stage.sh gemini feature-dev 554 --model gemini-2.5-pro
```

**Supported Adapters:** `codex`, `gemini` (plus `lm-studio`, `copilot` for the
adapters wired in `skillRunner.ts`).

**Supported Stages:**

- `issue-pickup`
- `feature-planning`
- `feature-dev`
- `feature-validate`
- `pr-create`
- `pr-merge`

**Runtime Requirements:**

- `node`, `git`, and `gh` installed
- Adapter-specific CLI tool installed (`codex` or `gemini`)
- `packages/nightgauge-sdk/dist/cli/index.js` exists
  (`npm run -w @nightgauge/sdk build`)
- `gh auth` is configured

## Project Board And Pipeline State

Project board sync, config generation, config validation, and context cleanup
are handled by the `nightgauge` Go binary rather than standalone shell
scripts:

| Task                       | Command                                  |
| -------------------------- | ---------------------------------------- |
| Move board status          | `nightgauge project sync-status <n> <s>` |
| Add issue to project board | `nightgauge project add <n>`             |
| Epic completion check      | `nightgauge epic check-completion <n>`   |
| Environment health check   | `nightgauge doctor`                      |

See [../docs/GO_BINARY.md](../docs/GO_BINARY.md) for the full CLI reference.

## Configuration

Configuration is read from `.nightgauge/config.yaml`:

```yaml
project:
  number: 10 # GitHub Project number (from URL: /orgs/owner/projects/10)
```

## Environment Variables

| Variable                     | Description                         |
| ---------------------------- | ----------------------------------- |
| `NIGHTGAUGE_PROJECT_NUMBER`  | Override project number from config |
| `NIGHTGAUGE_SKIP_PROJECT`    | Skip project board sync entirely    |
| `NIGHTGAUGE_HOOKS_DEBUG`     | Enable debug logging for hooks      |
| `NIGHTGAUGE_SKIP_SKILL_SYNC` | Skip skill sync during dev-install  |

## Author

nightgauge
