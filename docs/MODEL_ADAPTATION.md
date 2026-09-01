# Model Adaptation (Skill Overlays)

Pipeline skills are written once and executed by whichever model the router
selects. A single procedural document cannot serve that range well: the
guardrails that keep a small model on-task are the same sentences that railroad
a frontier model. **Model adaptation** is how that variance is expressed without
forking skills — the base `SKILL.md` stays the single source of procedural
truth, and per-provider/per-model differences ship as small **additive overlay
fragments** composed at render time.

This is the operational guide. The decision and the rejected alternatives are
in [ADR 016](decisions/016-model-aware-skill-overlays.md); the CLI reference for
the composer is
[GO_BINARY.md § Skill Composition](GO_BINARY.md#skill-composition-issue-78--adr-016).

> **Status.** The composer (`nightgauge skill render`), the registry `behavior`
> block, and the extension's migration onto the composer have shipped. The
> `preflight skill-overlays` gate, the full shared-overlay set, base-skill prose
> extraction, `nightgauge-model-watch`, and the overlay-delta evals are still
> open — see [Not yet enforced](#not-yet-enforced) before relying on any of them.

## 1. The mental model

- A base skill says **what the stage does**. It carries no model-conditional
  language.
- An overlay says **how this model should carry it out** — disposition, not
  procedure.
- Overlays are **additive**. Rendering with no model, or with a model the
  registry does not know, produces the base document unchanged. That is a
  correct rendering, not a degraded one.
- Resolution happens in exactly one place: `internal/skillrender`, exposed as
  `nightgauge skill render`. Nothing else resolves overlays.

## 2. Where overlays live

```text
skills/_shared/_overlays/<key>.md          # applies to every skill
skills/<skill>/_overlays/<key>.md          # applies to one skill
skills/<skill>/_overlays/<key>.SKILL.md    # whole-file override (discouraged)
```

`<key>` is derived from the **resolved** model descriptor, never from the string
a caller typed. Tier bands are still valid `--model` inputs — they resolve
through the registry to a concrete id — but no band-keyed file is ever consulted
(the band segment was retired with the band vocabulary in #582).

### The corpus today

Every shipped overlay is shared-scope; no skill-specific fragment and no
whole-file override exists. All four land at the `after-context-includes` site
(§4) because no base skill carries an `<!-- overlay -->` anchor.

| Key                | Carries                                                                                         |
| ------------------ | ----------------------------------------------------------------------------------------------- |
| `xai`              | Grok Build as execution host: no Stop hooks, no `AskUserQuestion`, optional subagent fan-out.   |
| `grok-4.6`         | Thinking-on-by-default; drop redundant verify-your-work scaffolding and extra review subagents. |
| `grok-build-0.1`   | The cheaper Grok coding model: stay inside the stage contract, no extra research loops.         |
| `claude-fable-5-1` | The Fable 5.1 behavioral shifts (#1276), one named block each — see below.                      |

`claude-fable-5-1.md` carries five named blocks, in this order:

| Block                     | What it does                                                                                    |
| ------------------------- | ----------------------------------------------------------------------------------------------- |
| `Targeted edits`          | Surgical edits over whole-file rewrites, which 5.1 reaches for more readily than Fable 5.       |
| `Scope and test coverage` | No unrequested fixes; scratch checks stay out of the repo; commit tests only where asked.       |
| `Operating autonomously`  | The unattended-run block. Reinforces `_shared/AUTONOMY_CONTRACT.md` rather than replacing it.   |
| `Holding the scope`       | Don't narrow, widen, or swap the deliverable; a decided step is run, not announced.             |
| `Batching tool requests`  | The one-response batching nudge, scoped in its heading to `feature-dev` and `feature-validate`. |

Three things are deliberately **not** in it. The registry `behavior` block
already records this model's `thinking_default`, `effort_default`,
`thinking_disable_max_effort: never` and `narration: low`, so the overlay acts
on those facts without restating them (§5 rule 1). Search triggering at `low`
effort does not arise, because the effort conformer floors Fable at `high`.
And `thinking.display: "updates"` is a request option the host sets, not prompt
text.

The batching nudge is scoped by **prose in its heading**, not by the cascade:
the composer injects one block per render, so a stage-scoped instruction has
nowhere else to live short of duplicating the fragment per skill. The vendor
guidance gates that nudge on measuring the share of assistant turns carrying
more than one tool call, and the pipeline cannot measure it —
`diagnostics.ToolCallRecord` (the `tool_calls` array on a `V2RunRecord`) has no
turn identifier, so the share is not derivable even from a populated record.
The two stages named are the bash-and-editor loops the guidance itself calls
out. Widen it only against a real measurement.

## 3. The cascade and precedence

Two specificities (provider, concrete id) across two scopes (shared,
skill-specific), general before specific and shared before skill-specific:

```text
_shared/anthropic → _shared/claude-opus-5
  → <skill>/anthropic → <skill>/claude-opus-5
```

Every matching fragment contributes; fragments are **composed, not replaced**.
Later fragments may explicitly countermand earlier ones, so a skill-specific
overlay outranks a shared one and a concrete-id overlay outranks its provider
overlay — by being read last, not by suppressing the others. Missing fragments
are skipped silently: **absence is the norm, not an error.**

The provider used for the cascade is the one on the _resolved_ descriptor, not
the one implied by `--adapter`. Concrete ids are globally unique, so an exact-id
lookup legitimately crosses providers and must key off where the model actually
lives.

The whole-file override is the one exception to "composed, not replaced": if
`<skill>/_overlays/<key>.SKILL.md` exists it replaces the base entirely, most
specific key winning, and no fragments are collected at all. It exists for the
genuine case where a stage needs a different _procedure_ rather than a different
_disposition_. Treat every one of them as a drift liability.

## 4. The injection anchor

The collected fragments are joined into a single section titled
`## Model Adaptation` and placed at the first site that applies:

| Precedence | Site                     | Trigger                                                               |
| ---------- | ------------------------ | --------------------------------------------------------------------- |
| 1          | `anchor`                 | an explicit `<!-- overlay -->` comment in the base                    |
| 2          | `after-context-includes` | after the last `PIPELINE_CONTEXT.md` / `AUTONOMY_CONTRACT.md` include |
| 3          | `top-of-body`            | neither of the above is present                                       |

Put the anchor in a base skill when the default position is wrong for that
document; leave it out otherwise. The fallback is deliberate — the context-include
region is the established "how to behave" region and is read _before_ the
procedure. Burying adaptation guidance under a thousand lines of steps is how it
gets ignored, and the small models overlays exist to help are the ones that
ignore it first.

Injection runs **before** `_includes` expansion, so an overlay may itself carry
`<!-- include: -->` directives, and the anchor is matched against directives
that expansion would otherwise have erased.

## 5. Authoring rules

1. **Prose that acts on facts, never the facts themselves.** Vendor-documented
   properties — thinking default, effort ceiling, output limits, propensities —
   belong in the registry `behavior` block as data. An overlay that restates
   them is a second copy that will drift.
2. **Disposition, not procedure.** "Skip the redundant verification pass" is an
   overlay. "Run the tests, then open the PR" is the base skill.
3. **No model-conditional language in a base skill.** Any sentence of the form
   "if you are a large model…" moves into an overlay. Skills whose _subject_ is
   models (`nightgauge-release-watch`, the eval skills) legitimately name them;
   that residue is covered by an inline `<!-- overlay-exempt: reason -->` marker.
4. **Write for composition.** Your fragment may be read after a provider-level
   one. Countermand explicitly ("ignore the subagent guidance above") rather
   than assuming yours is the only text present.
5. **Keep it short.** The size budget exists because a fragment that grows into
   a procedure is a fork wearing a disguise. Steady state is roughly 5–8 shared
   fragments carrying the great majority of variance, with per-skill overlays
   only where a stage is a genuine exception.
6. **Bring evidence.** An overlay is a claim about model behavior, and claims
   are cheap. Vendor documentation justifies _writing_ one; a cross-model eval
   run showing the delta — attempts-to-green, token cost, assertion pass rate —
   justifies _keeping_ it. See [SKILL_EVALUATION.md](SKILL_EVALUATION.md).

## 6. Adding a model

1. Add the descriptor to `packages/nightgauge-sdk/src/eval/model-registry.json`
   and mirror it into `internal/models/model-registry.json` with
   `scripts/sync-model-registry.sh`. The Go binary reads the embedded mirror, so
   an unsynced registry adapts nothing.
2. Fill in the `behavior` block with the facts overlays reason about —
   `thinking_default`, `effort_default`, `thinking_disable_max_effort`
   (which also accepts `never`, for a model that rejects disabled thinking at
   every effort), and the coarse `propensity` axes `verification`, `delegation`
   and `narration`. An undeclared axis reads as `normal`, so a descriptor with
   no `behavior` block renders exactly as it did before the block existed.
3. Decide whether an overlay is needed **at all**. Most models need none; the
   provider-level fragment usually covers them.
4. If one is needed, write it against a concrete id
   (`skills/_shared/_overlays/<id>.md`) and confirm the resolution with
   `nightgauge skill render --stage <stage> --model <id> --skills-root ./skills --json`.
   The envelope's `resolved_keys`, `fragments`, and `injection_site` are the
   provenance; `warnings` names anything that was present but unreadable.
5. Run the overlay-delta evals and keep the fragment only if it moved a number.

## 7. Fail-open, everywhere

Unknown model, local provider with no registry entry (ollama, lm-studio — by
design), unreadable fragment: every one of these renders base-only and exits 0.
A malformed overlay must never take down a run. Unreadable-but-present fragments
are reported in `warnings` rather than swallowed, because that case is a typo,
not an absence.

## 8. Not yet enforced

Two things in this guide are specified but not yet mechanically gated:

- **`nightgauge preflight skill-overlays`** — key validity, base-skill purity,
  the size budget, and the thinking/effort interlock. Until it lands, an overlay
  filename that resolves to no registry key silently applies to nothing, and the
  size budget is a review convention (#80).
- **`<!-- overlay-exempt: reason -->`** is the marker that gate will honour. It
  is inert today.

`nightgauge-model-watch` (#83), the shared-overlay authoring pass (#81), the
base-skill prose extraction (#82), and the overlay-delta harness (#84) are open
for the same reason. Write overlays as if the gate existed; it is coming, and
the review burden until then is human.

## 9. The coverage gap

Overlays are resolved by the **render path**. A skill that is read directly —
opened in an editor, or loaded by the Claude Code harness when a user types
`/nightgauge:<name>` — is the base document with no adaptation.

That is acceptable rather than merely unavoidable. Base-only is a correct
rendering by construction, and a slash-command invocation runs on the user's own
session model, which the pipeline neither selects nor observes; there is no
"model that actually executes" to key a cascade off, and guessing one would be
worse than not adapting. The plugin tree is regenerated from canonical `skills/`
by `scripts/install-agent-skills.sh`, so `_overlays/` directories are mirrored
into it — they are simply never resolved there.

The gap is closed for exactly one class of caller: anything that renders through
`nightgauge skill render`, which is every path the pipeline dispatches.

## See also

- [ADR 016 — Model-Aware Skill Overlays](decisions/016-model-aware-skill-overlays.md)
- [GO_BINARY.md § Skill Composition](GO_BINARY.md#skill-composition-issue-78--adr-016)
- [SKILL_PORTABILITY.md § 2](SKILL_PORTABILITY.md#2-model-tier-frontmatter-is-advisory-the-resolved-model-is-not)
- [MODEL_EVALUATION.md](MODEL_EVALUATION.md)
- [SKILL_EVALUATION.md](SKILL_EVALUATION.md)
