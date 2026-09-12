# Smart Setup — Deterministic Enforcement (Phase 4, step 7)

Instructions guide a model; a check enforces them. Smart Setup installs the
same check Nightgauge runs on itself, byte-identical to the Nightgauge version
this skill shipped with, and a CI job that runs it on every pull request.

## Contents

- [Locate the bundled check](#locate-the-bundled-check)
- [Install it](#install-it)
- [Choose the flags](#choose-the-flags)
- [CI job](#ci-job)
- [Local gate and required status check](#local-gate-and-required-status-check)

---

## Locate the bundled check

The check ships inside this skill at `scripts/check-agent-guidance.sh`, next to
this skill's `SKILL.md`. Pipeline runtimes export the directory as
`NIGHTGAUGE_SKILL_DIR`; otherwise use the directory you loaded `SKILL.md` from.

```bash
SKILL_DIR="${NIGHTGAUGE_SKILL_DIR:-/path/to/the/smart-setup/skill}"
BUNDLED="$SKILL_DIR/scripts/check-agent-guidance.sh"
[ -f "$BUNDLED" ] || { echo "bundled check not found at $BUNDLED — stop"; exit 1; }
```

## Install it

```bash
mkdir -p scripts
if [ -f scripts/check-agent-guidance.sh ] && ! cmp -s "$BUNDLED" scripts/check-agent-guidance.sh; then
  echo "scripts/check-agent-guidance.sh differs from this Nightgauge version's copy:"
  diff -u scripts/check-agent-guidance.sh "$BUNDLED" | head -40
fi
cp "$BUNDLED" scripts/check-agent-guidance.sh
chmod +x scripts/check-agent-guidance.sh
cmp "$BUNDLED" scripts/check-agent-guidance.sh && echo "installed byte-identical copy"
```

When an existing copy differs, show the diff and ask before replacing it.
Never edit the installed copy: repository differences are expressed with flags,
and a local edit makes the next upgrade a merge.

## Choose the flags

| Flag                          | Value for this repository                                               |
| ----------------------------- | ----------------------------------------------------------------------- |
| `--workspace-block forbidden` | Always. Downstream projects never carry the Nightgauge workspace block. |
| `--routing <path>`            | Only when the routing document is not `docs/AGENT_GUIDANCE.md`          |
| `--docs-index <path\|none>`   | Only when the index is not `docs/README.md`; `none` if routing is index |
| `--require-claude yes`        | When Claude Code is a selected tool                                     |
| `--agents-max-lines` etc.     | Leave at the defaults unless the team decides otherwise                 |

Run it once locally with the chosen flags; it must exit `0` before the change
is proposed.

## CI job

GitHub Actions — `.github/workflows/agent-guidance.yml`. The job name must be
exactly `agent guidance` so it can be made a required status check. No path
filter: a required check that does not run blocks the merge. Use the
repository's default branch name and its action-pinning convention.

```yaml
# Agent guidance — enforce the agent-instruction architecture described in
# docs/AGENT_GUIDANCE.md. No path filter: a required check must always run.
name: Agent guidance

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  agent-guidance:
    name: agent guidance
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - name: Agent-guidance architecture
        run: bash scripts/check-agent-guidance.sh --workspace-block forbidden
```

Append the other chosen flags to the `run:` line. On another CI system (GitLab,
Azure Pipelines), add an equivalent job named `agent guidance` that runs on
merge/pull requests and the default branch.

## Local gate and required status check

- If the repository has a local gate (a script, `make check`, an npm script),
  add the same command to it, and name the gate in `AGENTS.md`.
- Tell the user, in the completion summary: **make `agent guidance` a required
  status check** (GitHub: Settings → Rules → Rulesets, or branch protection →
  Require status checks to pass → add `agent guidance`). Until it is required,
  a red check can still be merged.
