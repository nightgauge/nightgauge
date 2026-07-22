# Skill Portability (Cross-Adapter)

Pipeline skills are portable Markdown specs that must run **without modification**
under every execution adapter — Claude (headless + SDK), Codex, Gemini, Copilot,
ollama, lm-studio, Cursor. This document is the contract that keeps them portable
and the guard that enforces it. Introduced by #4029.

> TL;DR for skill authors: never hardcode a `~/.vscode/extensions/...` path,
> resolve the binary via the shared PREFLIGHT cascade, and treat
> `model:` frontmatter, phase markers, and Claude-only tool directives as
> advisory/no-op under non-Claude adapters. The
> `nightgauge preflight skill-portability` gate fails CI on the one
> mechanically-detectable violation (a VSCode-extension binary path).

### Audited core skills (#4029)

The six headless pipeline-stage skills are the AC-critical surface and were all
audited and modified to remove their `~/.vscode/extensions` binary-discovery
fallback: `issue-pickup`, `feature-planning`, `feature-dev`, `feature-validate`,
`pr-create`, `pr-merge`. The portability gate then scans **all** skills (and
their `_includes/`/`_shared/`) so the rest stay clean too. See
[SKILL_EVALUATION.md](SKILL_EVALUATION.md) for the eval scenarios covering these
six.

## 1. Binary discovery — provider-neutral, host-provisioned

The `nightgauge` Go binary is not guaranteed to be on `PATH` when a skill
runs. Historically skills globbed
`~/.vscode/extensions/nightgauge.<version>/dist/bin/nightgauge` to find
the extension-bundled copy. That path only exists under VSCode-hosted Claude — it
silently fails under Codex/Gemini/etc., and it even missed the published,
platform-suffixed binary (`nightgauge-<platform>-<arch>`).

### The host exports the binary

The host that spawns a skill now resolves the binary authoritatively and exports
it, so discovery is identical under every adapter:

| Host             | Where                                                                    | What it sets                                  |
| ---------------- | ------------------------------------------------------------------------ | --------------------------------------------- |
| VSCode extension | `skillRunner.runStageSkillHeadless` (via `BinaryResolver.resolveSync()`) | `NIGHTGAUGE_BIN` + prepends its dir to `PATH` |
| Go auto/CLI mode | `internal/execution/manager.go` (via `os.Executable()`)                  | `NIGHTGAUGE_BIN`                              |

### The skill cascade (PREFLIGHT)

`skills/_shared/PREFLIGHT.md` resolves `$BINARY` in this **provider-neutral**
order, then prepends `dirname($BINARY)` to `PATH`:

1. `$NIGHTGAUGE_BIN` (host-exported — primary under any adapter)
2. `command -v nightgauge` (PATH)
3. `$REPO_ROOT/bin/nightgauge`
4. canonical-repo `bin/nightgauge` (git worktrees)
5. `$HOME/go/bin/nightgauge`

No VSCode-extension path appears anywhere in `skills/`.

### guard.sh is the one intentional divergence

`claude-plugins/nightgauge/hooks/lib/guard.sh` is a **Claude-Code-only**
hook (not a skill). It keeps the same shared order **plus** a trailing
`~/.vscode/extensions/...` glob to serve the _standalone-terminal-Claude_ case
where no host exports `NIGHTGAUGE_BIN`. The shared steps stay mirrored
between guard.sh and PREFLIGHT.md (#3262); only the vscode glob is guard.sh-only.
guard.sh is **not** scanned by the portability gate.

## 2. Model-tier frontmatter is advisory

`model: haiku|sonnet|opus` in a SKILL.md is a **Claude tier name** and is
**advisory only** for pipeline execution — it is never read by the Go or
TypeScript execution layers. Authoritative per-stage model selection lives in
the routing layer (#4021), resolved in this priority order and then mapped to a
concrete per-adapter model:

```
env NIGHTGAUGE_PIPELINE_STAGE_MODEL_<STAGE>
  → config pipeline.stage_models.<stage>
  → DEFAULT_STAGE_MODELS (stageResolver.ts)
  → AutoModelSelector
  → provider-aware registry lookup (resolveModelForAdapter, #56)
```

Tier names are the **canonical** vocabulary; each adapter resolves them at spawn
time (e.g. Codex `sonnet → gpt-5.4`). The resolution chain lives in
`packages/nightgauge-vscode/src/utils/resolvers/stageResolver.ts`
(`getStageModel` / `DEFAULT_STAGE_MODELS`); tier→model translation for every
adapter resolves through the provider-aware model registry
(`packages/nightgauge-sdk/src/eval/model-registry.json`, consumed via
`resolveModelForAdapter` — #56). See also [CONFIGURATION.md](CONFIGURATION.md).

Because the field is non-normative, a frontmatter tier may legitimately differ
from the effective default. Concrete example: `feature-planning/SKILL.md`
declares `model: haiku`, but `DEFAULT_STAGE_MODELS` resolves it to `sonnet`
(core-reasoning stage). The resolved value wins; the frontmatter is documentation
only. This divergence is intentional, not drift — frontmatter is deliberately
left untouched here so **Claude invocation is unchanged** (a native
`/nightgauge:*` slash-command invocation may still read it).

## 3. Phase markers are no-op-safe

Skills emit phase markers as HTML comments
(`<!-- phase:start name="…" index=N total=T stage="…" -->`). They are adapter-
neutral: under Codex they pass through the `--json` summarizer as plain text and
are parsed identically to Claude output by the extension's `streamOutputHandler`.
Emitting them under any adapter is harmless — they are never interpreted as code.
No adapter guard is needed. See `packages/nightgauge-sdk/src/events/phaseRegistry.ts`.

## 4. Claude-only directives: what degrades, what does not

**Audit status (#4029, widened by #55 / spike #33 D1):** the six core skills
carry MORE Claude-Code-specific directives than the original audit recorded —
the full set is below, each with its off-Claude behavior. The portability gate
asserts two things mechanically (no VSCode-extension paths; no `hooks:`
frontmatter — see §5); everything else is documented degradation, so a future
skill adding a genuinely Claude-only, non-degrading directive still needs
authoring care.

**Degrade safely** (present in shipped skills; harmless off-Claude):

- **`AskUserQuestion`** — stripped from the tool list in headless mode
  (`splitTools` in `internal/execution/skill.go`); headless stages fail fast on
  an undecidable instead.
- **`agent:` / `context: fork`** — frontmatter directives consumed only by the
  Claude-Code SDK forking layer; never parsed by the Go/SDK pipeline executors,
  so under Codex/Gemini the skill simply runs inline.
- **`Task`, `Bash`, etc.** — mapped to a Codex sandbox/approval mode by #4026
  (`internal/execution/adapters/codex_sandbox.go`). Unknown tools fall through
  to the most restrictive (read-only) mode — conservative, never a privilege
  escalation.
- **Body-level `Task` fan-out instructions** ("launch a subagent to …") — inert
  prose on hosts without subagents. Every fan-out in the core skills must carry
  an inline fallback (run the work in the main context) — feature-planning's
  pattern mining and doc gathering both do (#55). A failure-only fallback is
  NOT enough; the no-capability path must be explicit.
- **`disable-model-invocation`** — consumed by Claude Code's skill loader to
  bar free-form LLM reasoning calls; other hosts never grant those calls in
  the first place, so absence of enforcement changes nothing.
- **`orchestration:` frontmatter** — consumed by the SDK workflow engine,
  which drives non-Claude adapters through the portable `SdkFanoutRunner`
  floor; on hosts with no workflow engine at all, the block is ignored and the
  skill body's inline fallbacks apply.
- **`programmatic-tools:` (PTC)** — a Claude-Code capability grant; other
  hosts ignore the key. Skills must not DEPEND on PTC results — treat them as
  an acceleration, with the deterministic `nightgauge` verbs as the floor.

**Do NOT degrade — banned or requiring explicit design:**

- **`hooks:` (Stop-hook completion gates)** — silently never fire off-Claude,
  which is a correctness hole, not a degradation (spike #33 finding D2). The
  feature-dev / feature-validate completion checks moved into Go StageGates
  (`internal/orchestrator/gates`, #55), and the portability gate now REJECTS
  any `hooks:` key in skill frontmatter.
- **MCP tools (`mcp__*`) / `Skill()` chaining** — available only where the
  host provisions MCP servers / skill invocation. A stage step that _gates
  correctness_ on these must ship a non-MCP fallback in the same skill body
  (see feature-validate's UI gates, which degrade to a logged skip — the skip
  is visible in gate metrics, never silent).

## 5. Validation

| Tier                    | What                                                                                                                             | Where                                                                                               | CI?                                                |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- | -------------------------------------------------- |
| Deterministic gate      | No `.vscode/extensions` binary paths; no `hooks:` frontmatter (#55); no truncated binary-discovery cascades (#55) in `skills/**` | `nightgauge preflight skill-portability` (Go) + `scripts/lint-skills/portability.sh` (shell mirror) | ✅ `.github/workflows/lint.yml`                    |
| Live cross-adapter eval | Behavioral parity of the 6 core skills across adapters                                                                           | `docs/SKILL_EVALUATION.md` (live mode)                                                              | ❌ requires adapter binaries + auth — run manually |

The deterministic gate is the regression guard: it fails the moment any skill
reintroduces a VSCode-extension path, a Claude-only `hooks:` completion gate,
or a binary-discovery cascade that drifted from PREFLIGHT.md. The live multi-adapter eval (running the
skills against real Codex/Gemini binaries) cannot run in CI because it needs
authenticated adapter binaries — it is documented as a manual/opt-in step in
[SKILL_EVALUATION.md](SKILL_EVALUATION.md).

**On the #4029 acceptance criteria.** "Discovery works under Codex" and
"validated by a cross-adapter skill eval" are satisfied by the CI-enforceable
deterministic gate plus the host-provisioning of `$NIGHTGAUGE_BIN` (which is
adapter-agnostic by construction — see §1). Empirical end-to-end execution
against live Codex/Gemini binaries is the manual eval tier above: it is the
behavioral complement to the structural gate, deliberately not gated in CI
because it requires authenticated third-party CLIs. The structural gate is what
holds the line on every PR.

## Related

- `skills/_shared/PREFLIGHT.md` — the canonical discovery cascade
- `claude-plugins/nightgauge/hooks/lib/guard.sh` — Claude-only sibling
- [CONFIGURATION.md](CONFIGURATION.md) — model resolution
- [ADAPTER_DOCTOR.md](ADAPTER_DOCTOR.md) — per-adapter readiness (incl. binary discovery)
- #4026 (allowed-tools → Codex sandbox), #4021 (model routing), #3262 (cascade sync)
