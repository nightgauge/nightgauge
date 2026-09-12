# Smart Setup — Instruction-Model Detection and Migration (Phases 0.3 and 4M)

Phase 0.3 classifies the repository's instruction model. Phase 4M replaces
the additive merge for a repository in the OLD model: it produces a migration
plan and a proposed diff for human review, and commits nothing until the human
approves.

## Contents

- [Phase 0.3: Classify the instruction model](#phase-03-classify-the-instruction-model)
- [Phase 4M: Migration plan and proposed diff](#phase-4m-migration-plan-and-proposed-diff)
- [Migration plan format](#migration-plan-format)
- [Removing a committed managed-steering block](#removing-a-committed-managed-steering-block)

---

## Phase 0.3: Classify the instruction model

Run from the repository root, with bash (pipe the block to `bash -s` from
another shell). Each signal is independent; record every one that fires, with
the file and line, for the Phase 1 report.

````bash
TOOL_RE='claude|copilot|cursor|codex|kiro|gemini|windsurf'
SIGNALS=""
add() { SIGNALS="$SIGNALS $1"; echo "  [$1] $2"; }

# M1 — CLAUDE.md does not import AGENTS.md on line 1.
if [ -e CLAUDE.md ] && [ "$(head -n 1 CLAUDE.md)" != "@AGENTS.md" ]; then
  add M1 "CLAUDE.md line 1 is not @AGENTS.md"
fi

# M2 — CLAUDE.md owns routing, commands or portable rules.
if [ -f CLAUDE.md ]; then
  grep -niE '^#+ .*(documentation (map|routing)|doc map|commands|quick start|workflow|standards|conventions|rules)' CLAUDE.md \
    && add M2 "CLAUDE.md carries routing, commands or rules headings (lines above)"
  grep -nE '^[[:space:]]*(```|~~~)[[:space:]]*(bash|sh|shell|zsh)' CLAUDE.md \
    && add M2 "CLAUDE.md carries a fenced command block (lines above)"
fi

# M3 — AGENTS.md defers to CLAUDE.md.
if [ -f AGENTS.md ]; then
  grep -nE '(^|[[:space:]])@CLAUDE\.md' AGENTS.md && add M3 "AGENTS.md imports @CLAUDE.md"
  grep -niE '(read|see|follow|consult)[[:space:]]+(the[[:space:]]+)?`?CLAUDE\.md`?[[:space:]]+(first|instead)' AGENTS.md \
    && add M3 "AGENTS.md tells the reader to use CLAUDE.md first/instead"
fi

# M4 — AGENTS.md's first heading names a tool.
if [ -f AGENTS.md ]; then
  first=$(awk '/^[[:space:]]*(```|~~~)/{f=!f;next} !f && /^#+[[:space:]]/{print;exit}' AGENTS.md)
  printf '%s\n' "$first" | grep -Eiq "($TOOL_RE)" && add M4 "AGENTS.md first heading names a tool: $first"
fi

# M5 — a Nightgauge managed-steering block is COMMITTED (HEAD, not the
# working tree: a running Codex stage writes the block to the working tree).
# `nightgauge preflight managed-steering` makes the same check when installed.
git grep -nF '<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->' HEAD -- ':(glob)**/AGENTS.md' \
  && add M5 "managed steering block committed (lines above)"

# M6 — a tracked instruction file is a symlink.
git ls-files -s | awk -F '\t' '{ split($1, m, " "); if (m[1] == "120000") print $2 }' |
  grep -E '(^|/)(AGENTS|CLAUDE|GEMINI)\.md$|^\.github/copilot-instructions\.md$|^\.github/instructions/|^\.claude/rules/|^\.cursor/rules/|^\.cursorrules$|^\.windsurfrules$|^\.kiro/steering/' \
  && add M6 "instruction file(s) above are symlinks"

# M7 — an instruction file is over budget.
[ -f AGENTS.md ] && [ "$(wc -l < AGENTS.md)" -gt 200 ] && add M7 "AGENTS.md over 200 lines"
[ -f CLAUDE.md ] && [ "$(wc -l < CLAUDE.md)" -gt 100 ] && add M7 "CLAUDE.md over 100 lines"
[ -f .github/copilot-instructions.md ] && [ "$(wc -l < .github/copilot-instructions.md)" -gt 30 ] \
  && add M7 ".github/copilot-instructions.md over 30 lines"

# M8 — project rules live only in a tool-specific file.
for f in .github/copilot-instructions.md .cursorrules .windsurfrules GEMINI.md; do
  [ -f "$f" ] && ! grep -q 'AGENTS\.md' "$f" && add M8 "$f holds rules without pointing to AGENTS.md"
done
[ -d .kiro/steering ] && [ ! -f AGENTS.md ] && add M8 ".kiro/steering holds rules and there is no AGENTS.md"

echo "signals:${SIGNALS:- none}"
````

M2 also fires, by judgement, for any imperative rule in `CLAUDE.md` that is not
about Claude Code itself (a test command, a branching rule, a naming rule).
Record those lines too.

Then run the bundled check read-only, for its complete list of findings (it
changes nothing):

```bash
bash "$SKILL_DIR/scripts/check-agent-guidance.sh" --root . --workspace-block forbidden || true
```

Classify:

| Model  | Condition                                            | Phase 4 path                    |
| ------ | ---------------------------------------------------- | ------------------------------- |
| `NONE` | No `AGENTS.md`, `CLAUDE.md` or tool instruction file | Generate from templates         |
| `OLD`  | Any of M1–M8                                         | Phase 4M: migration plan + diff |
| `NEW`  | `AGENTS.md` present and none of M1–M8                | Additive: offer missing parts   |

A `NEW` repository that still fails the check (for example a missing routing
document) takes the additive path; each failure is a gap to offer.

---

## Phase 4M: Migration plan and proposed diff

**HARD GATE:** in the `OLD` model, never run the additive "preserve and add
missing sections" merge. It leaves the old model in place with new sections
bolted on. Produce the plan and diff below instead.

### M.0 Preconditions

```bash
[ -z "$(git status --porcelain)" ] || { echo "Working tree not clean — stop and ask."; exit 1; }
git switch -c chore/agent-guidance-migration   # or the repository's branch naming
```

### M.1 Inventory every rule

List every rule-bearing line in `CLAUDE.md`, `AGENTS.md`, and each tool file
being migrated (`.github/copilot-instructions.md`, `.cursorrules`,
`.windsurfrules`, `GEMINI.md`, `.kiro/steering/*`, symlink targets). A rule is
any instruction, constraint, command, routing row or import. Number them
`R1…Rn` with `file:line`:

```bash
for f in CLAUDE.md AGENTS.md .github/copilot-instructions.md .cursorrules .windsurfrules GEMINI.md; do
  [ -f "$f" ] && grep -nvE '^[[:space:]]*$' "$f" | sed "s|^|$f:|"
done
```

### M.2 Give every rule a destination — never delete one

| Kind                                                            | Destination                                              |
| --------------------------------------------------------------- | -------------------------------------------------------- |
| Rule every root session needs (safety, git, validation)         | Root `AGENTS.md`                                         |
| Command, build/test/lint step, the local gate                   | Root `AGENTS.md` § Commands (reconciled with CI, M.6)    |
| Documentation map / routing row                                 | `docs/AGENT_GUIDANCE.md` § `## Documentation routing`    |
| Explanation, procedure, background                              | The matching `docs/` file (create one only if none fits) |
| Applies to one subtree only                                     | That subtree's nested `AGENTS.md` (M.5)                  |
| About Claude Code itself (memory, `.claude/rules/`, `/compact`) | `CLAUDE.md` under a `## Claude Code: …` heading          |

Every `R` row gets exactly one destination. When the destination already says
the same thing, record "merged into `AGENTS.md:NN`" and quote both texts, so
the reviewer can confirm nothing was lost. When unsure, move the rule verbatim.
A file may be removed (for example a restating `.cursorrules`); a rule may not.

Never copy a rule from another repository, including Nightgauge's own
`AGENTS.md` and its workspace-rules block. Every rule in the result traces to
an `R` row or to a Smart Setup template default the user approved, listed as
"new (template)".

### M.3 Rewrite the files

- **Root `AGENTS.md`** — the AGENTS.md template from `_includes/templates.md`,
  filled with the rules M.2 routed to it: tool-neutral first heading,
  provenance marker, no deferral to `CLAUDE.md`, the complete local gate, the
  nested-file index, at most 200 lines. Strip any committed managed-steering
  block ([below](#removing-a-committed-managed-steering-block)).
- **`CLAUDE.md`** — line 1 `@AGENTS.md`, then only the Claude Code rows under
  `## Claude Code: …` headings. Remove it entirely when Claude Code is not a
  selected tool and it has no Claude-only rows.
- **Routing** — `docs/AGENT_GUIDANCE.md` with the `## Documentation routing`
  table; every path must resolve. Link it from the documentation index:
  `docs/README.md`, created from the template when absent. If the repository's
  index is elsewhere, link it there and pass that path as `--docs-index`.
- **Symlinked instruction files** — replace each with a regular file
  (`git rm` the link, write the file). Its content is already in the inventory.
- **Tool adapters** — keep `.github/copilot-instructions.md` only when Copilot
  is selected, rewritten as the pointer template. Propose removing
  `.cursorrules`, `.windsurfrules`, `GEMINI.md` and `.kiro/steering/*` files
  that restate `AGENTS.md`, after moving their unique rules.
- **Over-budget files** — move explanations into `docs/`, one row per move.

### M.4 Enforcement

Install the check and its CI job exactly as `_includes/enforcement.md`
describes. Both are part of the migration diff.

### M.5 Nested instruction files

Detect sub-packages with their own toolchain:

```bash
git ls-files -- ':(glob)*/**/pubspec.yaml' ':(glob)*/**/go.mod' ':(glob)*/**/Cargo.toml' \
  ':(glob)*/**/pyproject.toml' | xargs -n1 dirname 2>/dev/null | sort -u   # Dart/Flutter, Go, Rust, Python
[ -f package.json ] && jq -r '(.workspaces.packages? // .workspaces? // empty) | .[]?' package.json  # npm/yarn workspaces
[ -f pnpm-workspace.yaml ] && cat pnpm-workspace.yaml                                              # pnpm workspaces
git ls-files -- '*.tf' | xargs -n1 dirname 2>/dev/null | sort -u                                   # terraform modules
git ls-files | grep -E '(^|/)migrations/' | sed -E 's|(.*migrations)/.*|\1|' | sort -u              # migration directories
```

Propose a nested `AGENTS.md` (plus a sibling `CLAUDE.md` whose line 1 is
`@AGENTS.md`) only where the subtree has commands or rules that differ from the
root — a distinct toolchain, a migrations directory with an ordering or
never-edit rule. List each in root `AGENTS.md`, keep the root-to-nested chain
under 32 KiB, and put nothing in it that a root-launched session needs (Claude
Code and Codex do not load nested files for a root session).

### M.6 Reconcile commands with CI

For each command routed to `AGENTS.md`, find where pull-request CI runs it, and
for each pull-request CI step, find it in `AGENTS.md`:

```bash
ls .github/workflows/*.y*ml 2>/dev/null
grep -l 'pull_request' .github/workflows/*.y*ml 2>/dev/null        # PR workflows
grep -nE '^[[:space:]]*(- )?run:' $(grep -l 'pull_request' .github/workflows/*.y*ml 2>/dev/null)
```

Also read `.gitlab-ci.yml`, `azure-pipelines.yml` or other CI configuration
when present. Record a table `Command | In AGENTS.md | In PR CI | Action`. A
CI step missing from the gate is added to `AGENTS.md`; a documented command no
PR workflow runs is flagged for the team (`[TEAM TO DOCUMENT: …]`), never
silently dropped.

### M.7 Validate, then present

```bash
bash scripts/check-agent-guidance.sh --workspace-block forbidden [--routing …] [--docs-index …]
git add -N .          # make new files visible to diff; nothing is committed
git diff --stat
git diff
```

Present the plan (format below), then the diff. **WAIT for approval.** On
approval, commit on the branch and open a pull request through the
repository's normal workflow. On rejection, change nothing further; the user
decides what to keep.

---

## Migration plan format

```text
MIGRATION PLAN — agent guidance v1
Detected model: OLD   Signals: M1 CLAUDE.md:1, M3 AGENTS.md:4, M5 AGENTS.md:88-140

Rules moved (none deleted):
| #  | Rule (abridged)                 | From              | To                                   |
| R1 | Never push to main              | CLAUDE.md:14      | AGENTS.md § Git workflow             |
| R2 | npm test before pushing         | CLAUDE.md:22      | AGENTS.md § Commands (in ci.yml)     |
| R3 | Documentation map (12 rows)     | CLAUDE.md:30-44   | docs/AGENT_GUIDANCE.md § routing     |
| R4 | Use subagents for searches      | CLAUDE.md:51      | CLAUDE.md § Claude Code: subagents   |

Files:
  modify  AGENTS.md                (tool-neutral heading, provenance, gate, block stripped)
  modify  CLAUDE.md                (thin adapter, 9 lines)
  create  docs/AGENT_GUIDANCE.md   (routing moved from CLAUDE.md)
  create  docs/README.md           (index links AGENT_GUIDANCE.md)
  replace .cursorrules → removed   (restated AGENTS.md; unique rule R7 moved)
  create  scripts/check-agent-guidance.sh  (byte-identical to Nightgauge vX.Y.Z)
  create  .github/workflows/agent-guidance.yml  (job `agent guidance`)

Nested proposals:  packages/app/ (Flutter)  → packages/app/AGENTS.md + CLAUDE.md
Commands vs CI:    2 documented commands not in PR CI (flagged), 1 CI step added to the gate
Check result:      check-agent-guidance.sh exit 0
After merge:       make `agent guidance` a required status check
Open questions:    [TEAM TO DOCUMENT: …]
```

---

## Removing a committed managed-steering block

The Nightgauge pipeline writes generated steering between
`<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->` and
`<!-- END NIGHTGAUGE MANAGED STEERING -->` in `AGENTS.md` for Codex stages and
removes it afterwards. A committed copy is generated content, not a rule: it
is removed, not inventoried.

When the `nightgauge` binary is available, it detects and removes the block
deterministically (exit `0` clean or fixed, `1` a committed block remains, `2`
error). `--fix` edits only the working tree, so the removal lands in the
migration diff:

```bash
nightgauge preflight managed-steering          # report
nightgauge preflight managed-steering --fix    # remove from the working tree
```

Without the binary, remove the markers and everything between them:

```bash
for f in $(git grep -lF '<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->' HEAD -- ':(glob)**/AGENTS.md' | sed 's|^HEAD:||'); do
  awk '/<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->/{skip=1}
       !skip{print}
       /<!-- END NIGHTGAUGE MANAGED STEERING -->/{skip=0}' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
  grep -q '[^[:space:]]' "$f" || git rm -q -f "$f"   # the block was the whole file
done
```

Report each file and line range in the plan. If the working tree holds a block
that HEAD does not, a Codex stage is running (or was interrupted); leave it and
say so.
