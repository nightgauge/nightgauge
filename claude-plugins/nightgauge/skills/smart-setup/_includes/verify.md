# Smart Setup — Verify Mode (conformance checklist DC-01…DC-22)

`/smart-setup verify` is read-only: it changes no file, commits nothing, and
asks at most one question (which AI tools the team uses, for DC-13, unless an
answer log records it). It reports every item as `PASS`, `FAIL` or `WARN`, and
ends with the migration or additive step that fixes each `FAIL`.

Items marked **(script)** are decided by running the installed check. The rest
are performed by the skill with the commands shown.

## Contents

- [Step V.1: Run the installed check](#step-v1-run-the-installed-check)
- [Step V.2: Checklist](#step-v2-checklist)
- [Step V.3: Report](#step-v3-report)

---

## Step V.1: Run the installed check

Use the flags the repository's CI job passes, so verify and CI agree. Fall back
to the bundled copy only when none is installed (that is itself DC-17 `FAIL`).
Run every block in this file with bash; the heredoc below makes that explicit,
because in zsh an unquoted variable is not split into words.

```bash
SKILL_DIR="$SKILL_DIR" bash -s <<'VERIFY'
WF=$(grep -lE 'check-agent-guidance\.sh' .github/workflows/*.y*ml 2>/dev/null | head -n 1)
FLAGS=$( [ -n "$WF" ] && grep -oE 'check-agent-guidance\.sh[^"'"'"']*' "$WF" | head -n 1 | sed 's/^check-agent-guidance\.sh//' )
[ -n "$FLAGS" ] || FLAGS="--workspace-block forbidden"
CHECK=scripts/check-agent-guidance.sh
[ -f "$CHECK" ] || CHECK="$SKILL_DIR/scripts/check-agent-guidance.sh"
echo "flags:$FLAGS"
# shellcheck disable=SC2086 # FLAGS is a flag list taken from the workflow
bash "$CHECK" --root . $FLAGS; echo "check exit: $?"
VERIFY
```

Map each printed failure to its DC id below. Exit `0` means every (script) item
passes; exit `2` is a usage error — report it and stop.

## Step V.2: Checklist

| ID    | Requirement                                                                            | How           | Level |
| ----- | -------------------------------------------------------------------------------------- | ------------- | ----- |
| DC-01 | Root `AGENTS.md` exists and is a regular file                                          | script        | FAIL  |
| DC-02 | `AGENTS.md` ≤ 200 lines; root-to-deepest `AGENTS.md` chain ≤ 32 KiB                    | script        | FAIL  |
| DC-03 | `AGENTS.md`'s first heading names no tool                                              | script        | FAIL  |
| DC-04 | `AGENTS.md` does not defer to `CLAUDE.md`                                              | script        | FAIL  |
| DC-05 | `CLAUDE.md` regular file, line 1 exactly `@AGENTS.md`, ≤ 100 lines                     | script        | FAIL  |
| DC-06 | No routing heading, routing table or fenced shell block in `CLAUDE.md`                 | script        | FAIL  |
| DC-07 | Routing doc exists, is linked from the docs index, and `AGENTS.md` points to it        | script + V.2a | FAIL  |
| DC-08 | Every path in the routing table resolves                                               | script        | FAIL  |
| DC-09 | Every nested `AGENTS.md` is listed in root `AGENTS.md`                                 | script        | FAIL  |
| DC-10 | Every nested `AGENTS.md` has a sibling `CLAUDE.md` starting `@AGENTS.md`               | script        | FAIL  |
| DC-11 | No tracked instruction file is a symlink                                               | script        | FAIL  |
| DC-12 | No `@` import in any `CLAUDE.md` resolves outside the repository                       | script        | FAIL  |
| DC-13 | Copilot adapter only if Copilot is selected; ≤ 30 lines; references `AGENTS.md`        | script + V.2b | FAIL  |
| DC-14 | No `.cursorrules`/`GEMINI.md`/`.kiro/steering/`/`.windsurfrules` restating `AGENTS.md` | V.2c          | WARN  |
| DC-15 | `AGENTS.md` names the complete local gate                                              | V.2d          | FAIL  |
| DC-16 | Every gate command appears in a pull-request CI workflow                               | V.2e          | WARN  |
| DC-17 | Check installed and run by a PR workflow job named `agent guidance`                    | V.2f          | FAIL  |
| DC-18 | `AGENTS.md` carries `<!-- nightgauge:agent-guidance v1 -->`                            | V.2g          | WARN  |
| DC-19 | No Nightgauge workspace-rules block (`--workspace-block forbidden`)                    | script        | FAIL  |
| DC-20 | No committed managed-steering block                                                    | V.2h          | FAIL  |
| DC-21 | No `AGENTS.md`/`CLAUDE.md` at a multi-repository workspace root                        | V.2i          | WARN  |
| DC-22 | No doc claims to be canonical for a topic the routing map assigns elsewhere            | V.2j          | WARN  |

DC-19 is decided by the flag in V.1. A Nightgauge product repository runs its
CI job with `--workspace-block required` and reports DC-19 as that job does.

### V.2a — DC-07: AGENTS.md points to the routing document

```bash
ROUTING=$(grep -ohE -- '--routing[ =][^ "]+' .github/workflows/*.y*ml 2>/dev/null | head -n 1 | sed -E 's/--routing[ =]//')
ROUTING=${ROUTING:-docs/AGENT_GUIDANCE.md}
grep -nF "$ROUTING" AGENTS.md || echo "DC-07 FAIL: AGENTS.md does not reference $ROUTING"
```

### V.2b — DC-13: Copilot adapter matches the tool selection

```bash
[ -f .github/copilot-instructions.md ] && echo "copilot adapter present ($(wc -l < .github/copilot-instructions.md) lines)"
```

Present but Copilot not among the team's tools: `FAIL` (remove it). Absent but
Copilot selected: `FAIL` (github.com Chat, Visual Studio, JetBrains, Eclipse
and Xcode chat and IDE code review do not read `AGENTS.md`). Size and the
`AGENTS.md` reference are the script's.

### V.2c — DC-14: redundant tool files

```bash
for f in .cursorrules .windsurfrules GEMINI.md $(find .kiro/steering -type f 2>/dev/null); do
  [ -f "$f" ] || continue
  shared=$(grep -vE '^[[:space:]]*$' "$f" | sort -u | comm -12 - <(grep -vE '^[[:space:]]*$' AGENTS.md | sort -u) | wc -l)
  echo "$f: $(wc -l < "$f") lines, $shared lines identical to AGENTS.md"
done
```

`WARN` for a file that shares lines with `AGENTS.md` or restates its rules in
other words (read it). A file holding only tool-specific rules passes.

### V.2d — DC-15: the complete local gate is named

```bash
grep -niE 'local gate|complete gate|before (you )?push|pre-push|ci-local|make (check|ci|verify|test)|npm run (check|ci|verify)' AGENTS.md
```

`PASS` only when `AGENTS.md` names the command (or ordered list) that is the
complete pre-push validation. A pointer to `docs/` alone is not enough: the
gate is a rule a root session needs.

### V.2e — DC-16: gate commands run in PR CI

```bash
# For each command named by DC-15:
grep -nF -- "<command>" $(grep -lE '^[[:space:]]*pull_request' .github/workflows/*.y*ml 2>/dev/null) \
  || echo "DC-16 WARN: <command> is not run by any PR workflow"
```

### V.2f — DC-17: installed and wired

```bash
[ -f scripts/check-agent-guidance.sh ] && [ ! -L scripts/check-agent-guidance.sh ] || echo "DC-17 FAIL: check not installed"
cmp -s scripts/check-agent-guidance.sh "$SKILL_DIR/scripts/check-agent-guidance.sh" \
  || echo "DC-17 WARN: installed check differs from this Nightgauge version's copy"
for wf in $(grep -lE 'check-agent-guidance\.sh' .github/workflows/*.y*ml 2>/dev/null); do
  grep -qE '^[[:space:]]*pull_request' "$wf" && grep -qE '^[[:space:]]*name:[[:space:]]*agent guidance[[:space:]]*$' "$wf" \
    && echo "DC-17 PASS: $wf"
done
```

`FAIL` when no pull-request workflow has a job named exactly `agent guidance`
that runs the check. Remind the user that the job must also be a required
status check; the forge setting is not visible from the tree.

### V.2g — DC-18: provenance

```bash
grep -qF '<!-- nightgauge:agent-guidance v1 -->' AGENTS.md || echo "DC-18 WARN: no provenance marker"
```

### V.2h — DC-20: no committed managed-steering block

```bash
if command -v nightgauge >/dev/null 2>&1; then
  nightgauge preflight managed-steering || echo "DC-20 FAIL: managed steering block committed"
else
  git grep -nF '<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->' HEAD -- ':(glob)**/AGENTS.md' \
    && echo "DC-20 FAIL: managed steering block committed"
fi
```

Both read what is committed at `HEAD`, not the working tree: a running Codex
stage writes the block to the working tree and removes it afterwards. The fix
is in `_includes/migration.md` § Removing a committed managed-steering block.

### V.2i — DC-21: workspace root

```bash
parent=$(dirname "$(git rev-parse --show-toplevel)")
repos=$(find "$parent" -mindepth 2 -maxdepth 2 -name .git 2>/dev/null | wc -l)
[ "$repos" -ge 2 ] && { [ -e "$parent/AGENTS.md" ] || [ -e "$parent/CLAUDE.md" ]; } \
  && echo "DC-21 WARN: $parent holds $repos repositories and an instruction file"
```

A session started at a multi-repository root silently gets that file instead of
the owning repository's rules; start sessions in the repository instead.

### V.2j — DC-22: competing canonical claims

```bash
grep -rniE 'single source of truth|canonical (source|reference|document|doc)|source of truth for' \
  --include='*.md' . | grep -v node_modules
```

For each hit, find its topic in the routing table. `WARN` when the table routes
that topic to a different document.

## Step V.3: Report

```text
AGENT GUIDANCE VERIFY — <repository>   check: scripts/check-agent-guidance.sh --workspace-block forbidden (exit 1)
FAIL  DC-05  CLAUDE.md: line 1 must be exactly @AGENTS.md
FAIL  DC-20  AGENTS.md:88  managed steering block committed
WARN  DC-16  `make e2e` is not run by any PR workflow
PASS  19 items
Next: run /smart-setup (the repository is in the OLD model; it will produce a migration plan).
```

Exit summary: `FAIL` count first. Never fix anything in verify mode.
