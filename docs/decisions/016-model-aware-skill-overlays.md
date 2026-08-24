# Model-Aware Skill Overlays — per-provider/per-model adaptation without forking skills

**Date:** 2026-07-25
**Author:** nightgauge
**Status:** Decided — partially implemented (see
[Implementation tracking](#implementation-tracking))
**Builds on:** ADR 010 (progressive disclosure / `_includes`), ADR 011 (model
eval system + `ModelDescriptor` registry), [docs/SKILL_PORTABILITY.md](../SKILL_PORTABILITY.md)

---

## Executive Summary

Pipeline skills are written once and executed by every model the router can
select — Opus, Sonnet, Haiku, GPT-5.x under Codex, Gemini, and local models.
A single procedural document cannot serve that range well: guardrails that keep
a small model on-task are exactly the instructions that railroad a frontier
model, and the scaffolding a frontier model needs removed ("add a verification
step", "use a subagent to check your work") is scaffolding a small model needs
kept.

We adopt **model-aware skill overlays**: the base `SKILL.md` remains the single
source of procedural truth, and model variance is expressed as small **additive
overlay fragments** resolved at render time from the model registry. Resolution
is implemented once, in the Go binary, and exposed as `nightgauge skill render`.
Most overlays are **cross-cutting** (`skills/_shared/_overlays/`), not per-skill,
which is what keeps this from becoming a combinatorial fork of 50 skills.

We explicitly reject whole-file per-model skill overrides as the primary
mechanism.

---

## Context

### What triggered this

Claude Opus 5 shipped with behavior changes that directly contradict guidance
embedded in our skills. From Anthropic's release notes:

- Thinking is **on by default**; the effort ladder gains a `max` tier and
  "effort matters more" — additional effort converts to quality more reliably.
- `thinking: {"type": "disabled"}` with effort `xhigh`/`max` is a **400 error**
  (breaking change from Opus 4.8).
- The model **verifies its own work unbidden**; carried-over verification
  instructions cause _over_-verification and should be removed.
- It **delegates to subagents more readily** in multi-agent frameworks.
- Default responses and written deliverables **run longer**; it narrates
  progress more often.
- 1M token context (default and maximum), 128k max output.

Meanwhile the tree says:

- [`model-registry.json`](../../packages/nightgauge-sdk/src/eval/model-registry.json)
  has no `claude-opus-5` entry, so `claude-opus-4-8` still holds the
  `anthropic/opus` band and every opus-routed stage selects the previous model.
- [`stageResolver.ts`](../../packages/nightgauge-vscode/src/utils/resolvers/stageResolver.ts)
  defines `ClaudeEffort = "low" | "medium" | "high" | "xhigh"`. The `max` tier
  does not exist in our type, our config validators, or `supported_efforts`.
- There is no thinking/effort interlock anywhere, so an `xhigh` + thinking-disabled
  combination is expressible and would 400.
- 14 skills carry subagent / "verification step" prose that Opus 5 documentation
  says to delete.

These are four separate defects with one shared root cause: **we have no
mechanism that lets a skill's instructions differ by the model executing it, and
no intake path that turns a model release into pipeline changes.** Every model
release will reproduce this drift.

### Why this is not solvable by prompt tuning the base skills

Tuning a base skill for the frontier model degrades the small models and vice
versa. The router is free to select any band per stage
([stageResolver → AutoModelSelector → `resolveModelForAdapter`](../SKILL_PORTABILITY.md#1-binary-discovery--provider-neutral-host-provisioned)),
so a single document is always being read by the wrong reader some fraction of
the time. The variance is real and structural; the mechanism has to be too.

---

## Options analyzed

### Option A — Whole-file per-model skill overrides (the intuitive design)

`skills/<skill>/claude-opus-5/SKILL.md` overrides `skills/<skill>/SKILL.md`;
missing override falls back to base.

**Pros:** trivially understandable; maximum expressive freedom; no composition
semantics to specify.

**Cons — decisive:** 50 skills × N models of near-duplicate procedure. The
failure mode is guaranteed and silent: a gate condition fixed in the base skill
stays broken in four override copies, and nothing detects it because each file
is independently valid. We would trade railroading (visible, measurable) for
drift (invisible, unmeasurable). It also assumes the variance is per-skill,
which it mostly is not — "don't add verification scaffolding on Opus 5" is one
fact that would be restated 50 times.

### Option B — Model guidance in the host system prompt only

The extension appends a per-model behavior block to the system prompt; no skill
files change.

**Pros:** zero drift risk (guidance lives outside skill documents entirely);
uniform across every stage; one place to edit.

**Cons:** cannot express per-skill exceptions, which genuinely exist (a review
skill _wants_ the adversarial fan-out that a PR-merge skill must not have). It
is also host-only: skills invoked through the `claude-plugins` slash-command
path never see it. And system-prompt guidance sits far from the procedure it
modifies, which is precisely where instruction-following degrades on the smaller
models this is meant to help.

### Option C — Additive overlay fragments, registry-keyed, rendered by the Go binary (**chosen**)

Base skill unchanged as the procedural source of truth. Small overlay fragments
are composed into the rendered skill at run time, keyed by the already-resolved
concrete model.

**Pros:** one copy of every procedure; cross-cutting guidance written once;
per-skill exceptions possible; unknown/local models resolve to zero overlays and
get exactly today's behavior; enforceable by a gate.

**Cons:** introduces composition semantics and a render step that must be
specified precisely (done below), and overlay content is a claim about model
behavior that can be wrong — mitigated by eval-gating (Decision 6).

---

## Decisions

### 1. Base skill stays the single source of procedural truth

A base `SKILL.md` describes **what the stage does** and must contain **no
model-conditional language**. Any sentence of the form "if you are a large
model…", any verification scaffolding aimed at a specific model generation, and
any subagent-count guidance moves into an overlay. This is enforced
mechanically (Decision 5), because without enforcement both layers accrete
hedging and we end up worse off than a single document.

### 2. Overlay resolution cascade

Overlays are Markdown fragments discovered at two scopes and three specificities:

```
skills/_shared/_overlays/<key>.md        # applies to every skill
skills/<skill>/_overlays/<key>.md        # applies to one skill
```

where `<key>` is derived from the resolved model, most general first:

| Order | Key source                     | Example key     |
| ----- | ------------------------------ | --------------- |
| 1     | `ModelDescriptor.provider`     | `anthropic`     |
| 2     | each `ModelDescriptor.tiers[]` | `opus`          |
| 3     | `ModelDescriptor.id`           | `claude-opus-5` |

> **Amended by #582 (band-vocabulary retirement):** the band segment (order 2)
> is retired with the band vocabulary. The cascade is now provider → concrete
> id. No band-keyed overlay file ever existed on disk, so no rendered output
> changed; a rank-keyed middle segment was deliberately not added back.

All matching fragments are **composed, not replaced**, and appended in
precedence order — shared before skill-specific, general before specific:

```
_shared/anthropic → _shared/claude-opus-5
  → <skill>/anthropic → <skill>/claude-opus-5
```

Later fragments may explicitly countermand earlier ones; a skill-specific
overlay always outranks a shared one at the same specificity. Missing fragments
are skipped silently — **absence is the norm, not an error**.

Expected steady state: ~5–8 files in `_shared/_overlays/` covering the great
majority of variance, and per-skill overlays only where a stage is a genuine
exception.

### 3. Injection site

The composed overlay block is inserted as a single `## Model Adaptation`
section, positioned by an optional `<!-- overlay -->` anchor in the base skill.
When no anchor is present, it is inserted immediately after the skill's context
includes (the region already occupied by `PIPELINE_CONTEXT.md` /
`AUTONOMY_CONTRACT.md`), which is the established "how to behave" region and is
read before the procedure rather than after it.

This ordering matters most for the small models the overlays are meant to help;
burying adaptation guidance below a thousand lines of procedure is how it gets
ignored.

Prompt caching is unaffected in practice: each model has its own cache, so a
model-specific prefix does not invalidate anything.

### 4. Resolution lives in the Go binary — `nightgauge skill render`

Per [`.claude/rules/scripts.md`](../../.claude/rules/scripts.md), deterministic
logic belongs in the compiled binary. `nightgauge skill render --stage <stage>
--model <id> [--adapter <adapter>]` performs frontmatter parsing, `_includes`
expansion (existing behavior), overlay resolution, and emits the composed skill.

Both consumers call it rather than reimplementing it:

- the extension's [`skillRunner.ts`](../../packages/nightgauge-vscode/src/utils/skillRunner.ts)
  (replacing its local `findSkillFile` + include-expansion path), and
- the `claude-plugins/nightgauge` slash-command wrappers, which today read
  `SKILL.md` directly and would otherwise be permanently overlay-blind.

One implementation, no TS/Go mirror to drift. This also closes a pre-existing
duplication between `skillRunner.ts` and
[`internal/execution/skill.go`](../../internal/execution/skill.go).

> **Amendment (#79) — the second consumer no longer exists.**
> The slash-command wrappers this decision names were retired by
> [ADR 007's #3876 amendment](007-slash-command-skill-invocation-contract.md#amendment-2026-06-01-3876--skills-are-the-slash-commands)
> before ADR 016 was written: **the skill IS the slash command.** Typing
> `/nightgauge:<name>` loads `claude-plugins/nightgauge/skills/<name>/SKILL.md`
> directly through the harness loader, and the plugin ships no command wrapper
> except `model-routing-report.md`. `git log --diff-filter=A` over
> `claude-plugins/nightgauge/commands/` returns that one file for the life of
> the repository — the `commands/<stage>.md` layout this decision assumed has
> never existed here.
>
> There is therefore **no process to interpose**. The plugin path is now the
> "raw-file path" that Consequences already accepts as uncovered, and the
> coverage note below should be read that way. Reintroducing a wrapper to
> regain overlay awareness would recreate both defects #3876 fixed: duplicate
> `/nightgauge:<name>` entries, and an agent improvising from the wrapper's
> prose instead of running the skill.
>
> Two things make that acceptable rather than merely unavoidable. Base-only is
> a correct rendering — overlays are additive by construction (§1, §2). And a
> slash-command invocation runs on the **user's own session model**, which the
> pipeline neither selects nor can observe, so there is no "model that actually
> executes" to key a cascade off; guessing one would be worse than not adapting.
> The plugin tree is regenerated from canonical `skills/` by
> `scripts/install-agent-skills.sh`, so `_overlays/` directories authored in
> #81 are mirrored into it — they are simply never resolved there.
>
> What #79 did migrate is the consumer that does exist: `skillRunner.ts`, both
> its headless and interactive dispatchers.

The binary cannot discover skills the way the extension can — `findSkillFile`
resolves through the VSCode extension bundle (`dist/skills/`) including the
garbage-collected-bundle self-heal from #3883. Skill **location** therefore stays
a host responsibility: the caller passes `--skills-root <dir>` (repeatable,
first match wins) and the binary owns only parsing, include expansion, overlay
resolution, and composition. Absolute-path rewriting of `skills/_shared/`
references (skillRunner.ts L1984–2006) moves into the render step so the output
is directly usable by the spawned agent.

### 5. `behavior` block on `ModelDescriptor`, and a preflight gate

`ModelDescriptor` gains an optional `behavior` object carrying the **factual**
model properties overlays reason about — `thinking_default`, `effort_ladder`,
`max_output_tokens`, and coarse propensity enums (`verification`, `delegation`,
`narration` ∈ `low|normal|high`). Facts live in the registry as data; overlays
carry only prose that acts on those facts and must not restate them.

> **Implementation note (#77).** The ladder fields shipped as
> `thinking_disable_max_effort` (the ceiling below which disabling thinking is
> legal) and `effort_default`, rather than a single `effort_ladder` — the
> requestable range already lives in `supported_efforts`, so a second copy of it
> would be the restatement this decision exists to prevent.
>
> `thinking_disable_max_effort` also accepts **`never`**. As first specified the
> field held an effort level, and an absent field meant _unconstrained_. That
> pair cannot describe Fable 5, which rejects `thinking: {"type": "disabled"}` at
> **every** effort: the only way to express it would be to omit the field, which
> asserts the exact opposite and lets the interlock pass a configuration that
> returns a 400. `never` is therefore a member of the field's own union,
> deliberately **not** of `EffortLevel` — adding it there would make it spellable
> in `supported_efforts` and in stage effort config, where it is meaningless.
> Callers that render a remedy must branch on it: "lower the effort to `never` or
> below" is not an action an operator can take, and it points away from the only
> fix (unset the escape hatch).
>
> Undeclared propensity axes read as `normal`, so a model with no `behavior`
> block — and every unknown or local model, which has no registry entry at all —
> renders exactly as it did before the block existed.

`nightgauge preflight skill-overlays` gates:

- every overlay filename resolves to a live registry key (no typos, no overlays
  for deprecated models),
- no base `SKILL.md` contains model-conditional language (Decision 1) — scoped
  to the pipeline-stage skills, since skills whose _subject_ is models
  (`release-watch`, `model-watch`, the eval skills) legitimately name them;
  an inline `<!-- overlay-exempt: reason -->` marker covers the residue,

- overlay size stays within budget (a fragment that grows into a procedure is a
  fork wearing a disguise),
- no thinking/effort combination expressible in config is invalid for the
  target model (catches the Opus 5 `xhigh` + disabled-thinking 400).

### 6. Overlays require evidence before merge

An overlay is a claim about model behavior. Claims are cheap and superstition is
easy, so each new or materially changed overlay must be accompanied by a run of
the cross-model harness ([docs/SKILL_EVALUATION.md](../SKILL_EVALUATION.md)) or
the ADR-011 model-eval matrix, showing the delta it is supposed to produce
(attempts-to-green, token cost, or assertion pass rate). Vendor documentation
justifies _writing_ an overlay; evals justify _keeping_ it.

### 7. Model releases become a first-class intake path

[`nightgauge-release-watch`](../../skills/nightgauge-release-watch/SKILL.md)
watches Claude **Code** releases, not model releases — which is why Opus 5
landed with no registry entry. A new `nightgauge-model-watch` skill reads a
model's release/prompting documentation, diffs it against the registry entry and
existing overlays, and opens a registry patch plus issues for overlay changes.
It **proposes**; Decision 6 gates what merges.

### 8. Whole-file override remains available, and discouraged

`skills/<skill>/_overlays/<key>.SKILL.md` fully replaces the base for one
(skill, model) pair. This exists for the genuine case where a stage needs a
different _procedure_ rather than a different _disposition_. The preflight gate
reports each one as a drift liability so the count stays visible and near zero.

---

## Consequences

**Portability.** Overlays are additive and host-resolved, so the
[SKILL_PORTABILITY](../SKILL_PORTABILITY.md) contract holds: a skill run under
any adapter with no overlay resolution renders as base-only — today's exact
behavior. Local providers (ollama/lm-studio) have no registry entries by design,
so they match no key and get base-only. Fail-open is the designed default.

**Coverage gap, acknowledged.** A skill a human reads by hand (opening
`SKILL.md` in an editor, or an agent reading the file directly rather than
through the render path) sees the base document without adaptation. That is
acceptable precisely because base-only is a correct, if unoptimized, rendering.

> **Amended by #79.** This originally read "Decision 4 closes this for the
> plugin path". It does not, and cannot: ADR 007's #3876 amendment made the
> plugin path _be_ the raw-file path — the harness loads `SKILL.md` itself,
> with no wrapper to interpose. See the amendment under Decision 4. The gap is
> therefore wider than stated here and is closed for exactly one class of
> caller: anything that renders through `nightgauge skill render`, which since
> #79 is every path the pipeline dispatches.

**Migration cost.** The 14 skills carrying verification/subagent prose need it
extracted to overlays. This is a one-time cost that pays for itself at the next
model release.

**Ongoing cost.** Every model release now has an owner-facing checklist:
registry entry, behavior block, overlay review, eval run. That is the cost of
not silently running last generation's model with last generation's prompts.

---

## Implementation tracking

Epic #73 and its sub-issues track: registry/effort correctness fixes, the
`behavior` schema, `nightgauge skill render`, consumer migration, the preflight
gate, authoring the shared overlays, base-skill prose extraction,
`nightgauge-model-watch`, eval validation, and documentation.

**Shipped** (2026-08): the registry/effort corrections (#74, #75, #76), the
`behavior` block on `ModelDescriptor` (#77), the composer
`nightgauge skill render` (#78), the migration of `skillRunner.ts` onto it
(#79), and the Haiku preamble fold-in (#384). The mechanism this ADR chose is
therefore live: overlays resolve, compose, and inject on every path the pipeline
dispatches.

**Outstanding** — Decisions 5 and 6 are not yet enforced, and the overlay corpus
is not yet authored: the `preflight skill-overlays` gate including the size
budget and the `<!-- overlay-exempt -->` marker (#80), the shared overlays
(#81), base-skill prose extraction (#82), `nightgauge-model-watch` (#83), and
the overlay-delta evals (#84, #383). Until #80 lands, base-skill purity, key
validity, and the size budget are review conventions rather than gates.

The status line above stays `Decided` rather than `Implemented` for that reason.
Operational guidance for what _is_ live lives in
[docs/MODEL_ADAPTATION.md](../MODEL_ADAPTATION.md).
