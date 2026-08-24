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

#### Relative links are re-based on the way into the mirror (#831)

`claude-plugins/nightgauge/skills/<short>/` is two levels deeper than
`skills/<name>/` and drops the `nightgauge-` prefix from the directory name.
A verbatim copy therefore breaks two kinds of relative link at once: a doc
reference written as `../../docs/X.md` lands on `claude-plugins/nightgauge/`,
which has no `docs/`, and a sibling reference such as
`../nightgauge-issue-audit/SKILL.md` names a directory that does not exist under
the mirror. `scripts/lib/mirror_links.py` re-bases both during generation, using
only path arithmetic — so the generator emits identical output whether it writes
the real mirror or the temp directory `--check-mirror` compares against.

Two gates, asking different questions:

| Gate                                     | Question                                  |
| ---------------------------------------- | ----------------------------------------- |
| `install-agent-skills.sh --check-mirror` | Does the mirror equal generator output?   |
| `check-mirror-links.py`                  | Does each mirrored link name a real file? |

Only the second can see this class. The drift gate compares the mirror against
the generator's own output, so while the generator copied links verbatim both
sides carried the identical dead links and it was green **by construction** — a
green guard is evidence about the guard's pattern, not about the tree.
`scripts/test-mirror-link-check.sh` pins that the link gate still fails closed.

This is unrelated to the skill-composition **path rewrite** documented in
[../docs/GO_BINARY.md](../docs/GO_BINARY.md#skill-composition-issue-78--adr-016).
That rewrite resolves `skills/_shared/` include directives to absolute paths in
a rendered prompt at run time; it has never looked at markdown doc links, and it
never touches the committed mirror. Nothing regressed there.

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
