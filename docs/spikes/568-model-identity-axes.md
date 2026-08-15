# Spike #568: Model-Identity Axes — Inventory, Schema, and Selection Design

**Issue**: #568
**Status**: Complete
**Date**: 2026-08-15

## Executive Summary

The registry's band vocabulary (`haiku|sonnet|opus|fable`) is one scalar doing five jobs —
cost tier, capability ladder, adapter routing key, overlay cascade key, and telemetry
vocabulary — and at least two of those jobs are provably lies today: cost (all four xai bands
map to one model, and the declared xai rates are the wrong transport's list prices until #570)
and capability (zero measured evidence exists; the eval lane's historical rows labeled an
effort that was never applied until #571). Transport reachability is not modeled at all: it is
encoded by overloading `deprecated: true` (grok-build-0.1) and tiers-absence (grok-4.5,
11 entries total), which is the #532 defect class.

**Verdicts** (all five epic recommendations are **adopt**, in dependency order):

1. **registry-axis-schema — adopt.** Add a `transports` map (per-transport reachability +
   optional per-transport rate card + provenance), a top-level `effort_levels` ladder
   declaration, and `rate_provenance` to the canonical registry, additively, in one commit
   across the Zod schema, the Go mirror structs, and the sync script. Absorbs #436: the
   packaging test must parse the packaged registry with the packaged schema in the same PR,
   because `.strict()` plus a stale `dist/` bundle is a load-time crash, not a test failure.
2. **fail-closed-axis-enforcement — adopt.** Selection filters candidates on
   `transports[<adapter transport>].served` and fails closed with a classified error naming
   provider, model, and transport. Retires the deprecated/tiers-absence overloading. Absorbs
   #552 (the bare band-name fallthrough to `grok --model` is this exact gap). #569 (in
   flight) is the effort half; this is the transport/model half. #551's doctor catalog probe
   is detection; this is enforcement.
3. **run-record-envelope — adopt.** Run records store the full dispatch envelope
   `(model_id, effort, thinking)` with both key and value axes cross-language pinned (#446
   lesson). Absorbs #434 (derive the effort enum from `EFFORT_LEVELS` — the stored enum is
   two rungs behind) and #462 (selection-mode attribution lives in the same record and the
   same writer).
4. **selection-query-cutover — adopt.** Stage defaults, mode envelopes, floors/ceilings, and
   downgrade/escalation ladders become queries over a provider-scoped, envelope-valued
   candidate ladder; the eval advisor re-keys to `(job_class, model_id, effort, thinking)` and
   is consumed via a data-file handoff both resolvers read (never TS config over the wire —
   #340). On xai the ladder descends through **effort** within grok-4.6, which resolves the
   #532 "band downgrade is a cost no-op" honestly. Absorbs #435. Capability inputs are
   measured-only: gated on honest cells (#571, in flight) and evidence from #528.
5. **telemetry-overlay-docs-migration — adopt.** The mechanical sweep, landing alone: four
   independent band enums collapse to derivations, the ~12 hand-inlined regex/closed-set sites
   are replaced, the overlay keyspace drops the band segment (no band-named overlay file
   exists on disk today, so this is keyspace + tests + mirror regen under the #549 gate),
   stored telemetry is **accept-void** per store (justified in §5), and a gate prevents band
   reintroduction outside the Anthropic adapter path. Absorbs #543's overlay half and #384.

Wave-1 context: #569, #570, and #571 are in flight in parallel and this artifact assumes they
land. Facts they change are flagged inline: #570 replaces the xai rate provenance rows
(list-price, known-wrong → measured-from-CLI), #569 retires the "zero `supported_efforts`
readers outside the Claude path" finding, and #571 turns every "pending honest eval lane"
capability cell from unmeasurable to measurable.

## 1. Axis Inventory

One row per registry model (canonical file:
`packages/nightgauge-sdk/src/eval/model-registry.json`, version 2, 28 entries). Providers map
to adapters as follows (`internal/doctor/adapters.go:95-103`): anthropic → `claude-headless`
(CLI) + `claude-sdk` (API); openai → `codex` (CLI); google → `gemini` (CLI) + `gemini-sdk`
(API); copilot → `copilot` (CLI); xai → `grok` (CLI); other → no adapter. Local providers
(`ollama`, `lm-studio`, kind `http`) have no registry entries by design — the configured local
model serves every request and unknown ids cost $0 (`$schema_note`).

**Hard rule applied**: every capability cell is `measured` (with cited evidence) or
`pending (#528 / honest eval lane)`. No unmeasured capability tiering appears anywhere in this
artifact. The `tiers` column is reproduced as what it is — a **declared routing membership**,
not capability evidence. Propensity blocks (5 entries) are likewise declared, unmeasured.

Evidence keys used below:

- **M-cat**: measured 2026-08-15 — `grok models` catalog listing, grok CLI 1.0.4
  (unauthenticated; the catalog listing is free). Output: `grok-4.6 (default)`, `grok-4.5`.
  `grok-build-0.1` absent, consistent with the #532 finding (chat proxy rejects it).
- **M-help**: measured 2026-08-15 — CLI `--help` output (free). `claude` 2.1.233 enumerates
  `--effort <level> (low, medium, high, xhigh, max)`; `grok` 1.0.4 exposes
  `--reasoning-effort <EFFORT>` (alias `--effort`) without enumerating values; `codex` 0.145.0
  exposes reasoning only through `-c model_reasoning_effort=` (config override, no first-class
  flag).
- **M-bill**: measured — #531 close-out controlled billing measurement (input and cache held
  constant, output varied): grok-4.5 charged 2× grok-4.6 per token on the live Build CLI while
  the registry declares them identical, cache-read relationship inverted. #570 (in flight)
  lands the corrected figures with provenance.
- **D**: declared in the registry or repo code, not verified against a live surface.
- **P**: pending — `pending (#528 / honest eval lane)` for capability; `pending (#551)` for
  CLI catalog reachability where no free probe exists; `pending (#553)` for the xai API
  transport question.

| Provider  | Concrete id               | Declared tiers            | Transports that can serve it                                                    | Declared effort ladder                | Thinking modes                                            | Rate provenance                                                       | Capability evidence          |
| --------- | ------------------------- | ------------------------- | ------------------------------------------------------------------------------- | ------------------------------------- | --------------------------------------------------------- | --------------------------------------------------------------------- | ---------------------------- |
| anthropic | claude-opus-5             | [opus]                    | CLI (claude-headless, open policy — D) + API (claude-sdk — D); catalog P (#551) | low..max (D; flag vocabulary M-help)  | default on; disable conflicts above high (D)              | list, derivation-pinned (registry_test.go)                            | pending (#528 / honest eval) |
| anthropic | claude-opus-4-8           | [opus], deprecated        | API presumed, CLI by exact id — D; not a routing target                         | low..xhigh (D)                        | default off (D)                                           | list, derivation-pinned                                               | pending (#528 / honest eval) |
| anthropic | claude-sonnet-5           | [sonnet]                  | CLI + API — D; catalog P (#551)                                                 | low..max (D; flag vocabulary M-help)  | default on (D)                                            | list, derivation-pinned; standard sticker recorded pre-2026-08-31     | pending (#528 / honest eval) |
| anthropic | claude-haiku-4-5-20251001 | [haiku]                   | CLI + API — D; catalog P (#551)                                                 | `[]` = no effort axis (#336) (D)      | default off (D)                                           | list, derivation-pinned                                               | pending (#528 / honest eval) |
| anthropic | claude-fable-5            | [fable]                   | CLI + API — D; catalog P (#551)                                                 | low..max (D; flag vocabulary M-help)  | default on; disable **never** allowed (sentinel) (D)      | list, derivation-pinned                                               | pending (#528 / honest eval) |
| anthropic | claude-sonnet-4-6         | [sonnet], deprecated      | API presumed, CLI by exact id — D; not a routing target                         | low..high (D)                         | unknown (no behavior block)                               | list, derivation-pinned                                               | pending (#528 / honest eval) |
| anthropic | claude-opus-4-7           | [opus], deprecated        | API presumed, CLI by exact id — D; not a routing target                         | low..high (D)                         | unknown                                                   | list, derivation-pinned                                               | pending (#528 / honest eval) |
| anthropic | claude-opus-4-6           | [opus], deprecated        | API presumed, CLI by exact id — D; not a routing target                         | low..high (D)                         | unknown                                                   | list, derivation-pinned                                               | pending (#528 / honest eval) |
| openai    | gpt-5.6-sol               | [opus, fable]             | CLI (codex closed set, registry-derived — D); catalog P (#551)                  | low..xhigh (D)                        | unknown; codex reasoning via config key only (M-help)     | list, citation-pinned (registryRatesLiveVerified.test.ts)             | pending (#528 / honest eval) |
| openai    | gpt-5.6-terra             | [sonnet]                  | CLI (codex) — D; catalog P (#551)                                               | low..xhigh (D)                        | unknown                                                   | list, citation-pinned                                                 | pending (#528 / honest eval) |
| openai    | gpt-5.6-luna              | [haiku]                   | CLI (codex) — D; catalog P (#551)                                               | low..xhigh (D)                        | unknown                                                   | list, citation-pinned                                                 | pending (#528 / honest eval) |
| openai    | gpt-5.5                   | none (exact-id-only)      | CLI (codex) by exact id — D                                                     | low..high (D)                         | unknown                                                   | list, citation-pinned                                                 | pending (#528 / honest eval) |
| openai    | gpt-5.4                   | none (exact-id-only)      | CLI (codex) by exact id — D                                                     | low..high (D)                         | unknown                                                   | list, citation-pinned                                                 | pending (#528 / honest eval) |
| openai    | gpt-5.4-mini              | none (exact-id-only)      | CLI (codex) by exact id — D                                                     | low..high (D)                         | unknown                                                   | list, citation-pinned                                                 | pending (#528 / honest eval) |
| openai    | gpt-5.3-codex-spark       | none, research preview    | CLI (codex) by exact id — D                                                     | low..high (D)                         | unknown                                                   | placeholder ($0 is not a price — never listed)                        | pending (#528 / honest eval) |
| openai    | gpt-5.2                   | none, deprecated          | unverified — D; replacement gpt-5.4                                             | low..high (D)                         | unknown                                                   | list, citation-pinned                                                 | pending (#528 / honest eval) |
| openai    | gpt-5.3-codex             | none, deprecated          | unverified — D; replacement gpt-5.5                                             | low..high (D)                         | unknown                                                   | list, citation-pinned                                                 | pending (#528 / honest eval) |
| openai    | gpt-5.1-codex-mini        | none, deprecated          | unverified — D; replacement gpt-5.4-mini                                        | low..high (D)                         | unknown                                                   | list; vendor row retired (`$schema_note` case b)                      | pending (#528 / honest eval) |
| google    | gemini-2.5-pro            | [opus, fable]             | CLI (gemini) + API (gemini-sdk) — D; gemini CLI not installed locally, P (#551) | low..high (D)                         | unknown                                                   | list, citation-pinned; cache storage $/MTok-hour out of model (#392)  | pending (#528 / honest eval) |
| google    | gemini-2.5-flash          | [haiku, sonnet]           | CLI + API — D; P (#551)                                                         | low..high (D)                         | unknown                                                   | list, citation-pinned; same cache-storage caveat                      | pending (#528 / honest eval) |
| google    | gemini-2.0-flash          | none, deprecated          | unverified — D                                                                  | low..high (D)                         | unknown                                                   | list, citation-pinned                                                 | pending (#528 / honest eval) |
| copilot   | gpt-4o-mini               | [haiku]                   | CLI (copilot) — D; local CLI unauthenticated, unverifiable free                 | low..high (D, unenforced anywhere)    | unknown                                                   | subscription (flat-rate 0 by design, not a price)                     | pending (#528 / honest eval) |
| copilot   | gpt-4o                    | [sonnet]                  | CLI (copilot) — D                                                               | low..high (D, unenforced)             | unknown                                                   | subscription                                                          | pending (#528 / honest eval) |
| copilot   | claude-sonnet-4.5         | [opus, fable]             | CLI (copilot) — D                                                               | low..high (D, unenforced)             | unknown                                                   | subscription                                                          | pending (#528 / honest eval) |
| xai       | grok-4.6                  | [haiku,sonnet,opus,fable] | CLI **served — M-cat** (listed as default); API P (#553)                        | low..xhigh (D); flag exists (M-help)  | default on (D); CLI flag is `--reasoning-effort` (M-help) | list, **known-wrong vs CLI billing (M-bill)** → measured after #570   | pending (#528 / honest eval) |
| xai       | grok-4.5                  | none (exact-id-only)      | CLI **served — M-cat** (listed); API P (#553)                                   | low..high (D)                         | default on (D)                                            | list, **known-wrong: bills 2× grok-4.6 (M-bill)** → measured via #570 | pending (#528 / honest eval) |
| xai       | grok-build-0.1            | none, deprecated          | CLI **rejected — M-cat** (absent from catalog; #532); API P (#553)              | `[]` = no effort axis (D)             | default on (D)                                            | list, kept for historical cost replay only (#570 keeps list)          | pending (#528 / honest eval) |
| other     | vendor-x-pro              | none                      | none — fixture placeholder proving a provider is a data entry                   | medium, high (fixture shape, ADR 011) | unknown                                                   | fixture                                                               | not applicable (fixture)     |

Declared facts found wrong or unverifiable at authoring time:

- **Wrong (measured)**: xai rates — identical declared rates for grok-4.6/grok-4.5 versus a
  measured 2× billing difference on the transport in use, cache-read relationship inverted
  (M-bill). #570 (in flight) corrects the figures and the provenance.
- **Wrong (structural)**: transport reachability is expressed by overloading `deprecated`
  and tiers-absence; nothing in the schema can say "exists at the provider, unreachable
  through this adapter's transport" (#532 class). The only reachability machinery anywhere is
  the runtime doctor probe, and it is http-kind only (#520).
- **Unverifiable free**: every CLI catalog except grok's (`grok models` lists without auth).
  #551's fixture-covered doctor probe is the right mechanism for the rest; this spike ran no
  paid invocations.
- **Fictional until Wave 1 lands**: the effort axis outside the Claude path. Zero Go reads of
  `SupportedEfforts` exist; the grok path filters against a static vocabulary
  (`none|minimal|low|medium|high|xhigh|max`) that passes `max` through even though grok-4.6
  declares only up to `xhigh`; the one real gate is `adapter === "claude"`-guarded
  (`skillRunner.ts:3716`). #569 (in flight) makes this row historical.
- **Dishonest historical measurements**: every pre-#571 eval JSONL row carries an effort label
  that was never wired into any spawn, and Claude "reasoning" was prompt keywords, not the
  thinking parameter. Those rows must never be aggregated with honest ones (#571 bumps
  schema_version 2 → 3 and excludes them).

## 2. Axis Schema Design

Additive fields only; no vocabulary change in this phase. The canonical file remains
`packages/nightgauge-sdk/src/eval/model-registry.json`; the Go mirror remains a byte copy.

### 2.1 Registry JSON (canonical)

Top level gains one field:

```jsonc
{
  "$schema_note": "…",
  "version": 2,
  "effort_levels": ["low", "medium", "high", "xhigh", "max"], // NEW — single data authority
  "models": [ … ]
}
```

Each model may gain (additive, optional in this phase):

```jsonc
{
  "id": "grok-4.6",
  "provider": "xai",
  // NEW — per-transport reachability + optional per-transport rate card
  "transports": {
    "cli": {
      "served": true,
      "verified": "2026-08-15", // date of last live catalog/billing check; absent = declared
      "evidence": "grok models, grok CLI 1.0.4",
      "rates": { "input": 0.34, "output": 1.02 }, // optional; absent = inherit top-level rates
      "rate_provenance": "measured", // required whenever transport rates are present
    },
    "api": { "served": true }, // declared until #553 resolves the API transport question
  },
  // NEW — provenance of the top-level (default) rate card
  "rate_provenance": "list", // "measured" | "list" | "subscription" | "placeholder"
}
```

Semantics:

- `transports` keys are the closed set `cli | api`, matching the doctor's adapter kinds with
  `sdk` folded into `api` (http-kind local servers have no registry entries by design).
- `served: false` means "exists at the provider, unreachable through this transport" — the
  fact grok-build-0.1 currently smuggles through `deprecated: true`. After the enforcement
  phase, `deprecated` returns to meaning exactly "superseded, migrate away", and tiers-absence
  returns to meaning exactly "not a band/ladder routing target".
- Transport-level `rates` express the two-rate-card reality #570 documents (Build CLI billing
  ≠ API list price). Absent transport rates inherit the top-level card. `rate_provenance` is
  mandatory wherever a rate card appears — a loader-level assert, like band uniqueness.
- The xai transport rates shown above are illustrative of shape; the authoritative measured
  figures land with #570, which this schema then houses structurally instead of in
  `$schema_note` prose.
- Effort ladders and thinking stay where they are: `supported_efforts` (required, emptiable,
  #336 semantics preserved) is already the first-class per-model ladder — the gap is
  enforcement (#569), not shape. `behavior.thinking_default` +
  `behavior.thinking_disable_max_effort` (with the `"never"` sentinel) already encode the
  thinking axis the runtime actually models (on/off + disable interlock). No new capability
  field is added: **capability never enters the registry** — it lives in the eval records
  store keyed by `(model, effort, thinking)`, sourced exclusively from honest cells
  (post-#571). Propensity remains declared-advisory and is explicitly not a capability claim.

### 2.2 Strict Zod descriptor (TS)

```ts
export const TRANSPORTS = ["cli", "api"] as const;
export const RATE_PROVENANCES = ["measured", "list", "subscription", "placeholder"] as const;

const TransportFactsSchema = z
  .object({
    served: z.boolean(),
    verified: z
      .string()
      .regex(/^\d{4}-\d{2}-\d{2}$/)
      .optional(),
    evidence: z.string().optional(),
    rates: TokenRatesSchema.optional(),
    rate_provenance: z.enum(RATE_PROVENANCES).optional(),
  })
  .strict();

// ModelDescriptorSchema gains (stays .strict()):
//   transports: z.record(z.enum(TRANSPORTS), TransportFactsSchema).optional(),
//   rate_provenance: z.enum(RATE_PROVENANCES).optional(),
```

Loader-level asserts added in `modelRegistry.ts` (same pattern as the band-uniqueness throw):
transport `rates` without `rate_provenance` throws; the file's `effort_levels` must equal
`EFFORT_LEVELS` exactly (see §3).

### 2.3 Go mirror structs

```go
type TransportFacts struct {
    Served         bool   `json:"served"`
    Verified       string `json:"verified,omitempty"`
    Evidence       string `json:"evidence,omitempty"`
    Rates          *Rates `json:"rates,omitempty"`
    RateProvenance string `json:"rate_provenance,omitempty"`
}

// ModelDescriptor gains:
//   Transports     map[string]TransportFacts `json:"transports,omitempty"`
//   RateProvenance string                    `json:"rate_provenance,omitempty"`
// registryFile gains:
//   EffortLevels []string `json:"effort_levels"`
```

`mustLoad` gains the same two asserts as the TS loader.

### 2.4 The one-commit two-mirror landing

Order inside a single PR (verified against the current gate set):

1. Edit the canonical JSON: new fields plus the populated values from §1's inventory
   (grok transport facts carry the M-cat evidence; everything else lands as declared).
2. Edit `modelEvalSchemas.ts` (strict schemas) and `modelRegistry.ts` (loader asserts).
   Failure mode if skipped: `.strict()` throws at module import in both the SDK and the
   extension — a load-time crash, not a test failure.
3. Edit `internal/models/registry.go` structs + `mustLoad` asserts.
4. Run `scripts/sync-model-registry.sh` (plain `cp`). `TestParityWithCanonicalSDKRegistry`
   fails on any drift; `TestGoStructsModelEveryCanonicalField` fails if the Go structs miss
   any new canonical key — both run under `go test ./...` in CI.
5. Add a Go round-trip test for `TransportFacts` and update the terminalkind poison fixture
   (`internal/terminalkind/testdata/predicate-registry-poison.json`) if it embeds the
   descriptor shape.
6. `registryRatesLiveVerified.test.ts` is untouched unless rate figures move (they move in
   #570, not here).
7. **#436, absorbed here**: extend `modelRegistryPackaging.test.ts` from "dist file exists" to
   "the packaged registry parses under the packaged schema". This is the commit where the
   stale-dist crash class becomes live, so the hardening ships with it.

## 3. Vocabulary Unification

Three effort/thinking vocabularies exist plus one drifted enum. Single authority and
derivation rules:

| Vocabulary                                                 | Today                                                                                          | Decision                                                                                                                                                                                                                                                                                                         |
| ---------------------------------------------------------- | ---------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `EFFORT_LEVELS` (`modelEvalSchemas.ts:68`)                 | Already declared the single effort authority (#394); TS surfaces derive from it                | **Remains the authority.** The ladder is additionally written into the registry JSON as `effort_levels` so a _data_ authority exists that both languages can read: the TS loader asserts exact equality with `EFFORT_LEVELS` at load; Go derives `EffortOrder` from the JSON instead of hardcoding.              |
| Go `EffortOrder` (`registry.go:227`)                       | Independent hardcoded ladder, no parity link to TS                                             | **Derives** from the registry JSON's `effort_levels`; the existing sync/parity machinery makes drift a CI failure instead of a silent fork.                                                                                                                                                                      |
| Eval `REASONING_LEVELS` (`none\|low\|medium\|high`)        | Matrix axis; expressed as prompt keywords until #571                                           | **Stays eval-lane-internal.** For the selection key it collapses to the binary thinking axis: `none` → `off`, everything else → `on`. It never enters routing, config, or run-record vocabulary.                                                                                                                 |
| Runtime thinking (`behavior.thinking_default` + interlock) | on/off + `thinking_disable_max_effort` (incl. `"never"`)                                       | **The canonical thinking axis is `on\|off`** — it is all the runtime models and all the interlock constrains. The dispatch envelope everywhere is `(model_id, effort, thinking: on\|off)`.                                                                                                                       |
| Grok CLI mapper (`none/minimal` extras, #523)              | Static adapter-boundary translation collapsing extras onto `low`                               | **Stays a boundary translation and nothing more.** Adapter CLIs may have wider native vocabularies; the boundary maps _into_ `EFFORT_LEVELS` and the canonical enum is never widened (the `modelEvalSchemas.ts:124` warning stands). #569 replaces the static filter's authority with the registry consultation. |
| Run-record effort enum (`executionHistory.ts:209`)         | `low\|medium\|high` — two rungs behind, never written; Go `V2ModelSelect` has no effort at all | **Derived from `EFFORT_LEVELS`** and actually written, per the run-record-envelope phase. Cross-language: both the key axis and the value axis are pinned by a Go↔TS parity test (#446 lesson). Absorbs #434.                                                                                                    |

Who derives from whom, end state: registry JSON `effort_levels` (data authority) ⇄
`EFFORT_LEVELS` (compile-time convenience, load-asserted) → every TS validator/regex →
Go `EffortOrder` (derived from the same JSON) → adapter boundary mappers (translate in, never
widen) → run-record enum (derived, parity-pinned).

## 4. Selection Design

### 4.1 What replaces band lookup

The unit of selection becomes the **dispatch envelope** `(model_id, effort, thinking)` chosen
from a **provider-scoped candidate ladder**:

1. **Candidate set** — all registry models for the dispatching adapter's provider that are
   non-deprecated **and** `transports[<adapter transport>].served == true`. Fail closed: an
   empty candidate set or a request naming an unserved model is a classified error naming
   provider, model, and transport (the fail-closed-axis-enforcement phase; #552's bare-name
   fallthrough dies here).
2. **Ladder** — a strongest-first ordered list of envelope points, derived per provider from
   one registry declaration instead of today's five duplicated enums and positional indices
   (`performance_mode.go:157`, `config.go:609`, `MODEL_TIER_ORDER`, …). Crucially the ladder
   is **envelope-valued**: its rungs are `(model_id, effort)` points, not model names. On
   anthropic the rungs span models (haiku-4-5 → sonnet-5 → opus-5 → fable-5 at their default
   efforts); on xai — where all four bands map to grok-4.6 and a band downgrade is a declared
   cost no-op (#532) — the rungs descend through **effort within one model**
   (`grok-4.6@xhigh → high → medium → low`), which is a real cost/latency ladder the band
   vocabulary structurally could not express. Ladder position is explicitly a _declared_
   ordering, replaced rung-by-rung by measured evidence as it accumulates (§4.3); it makes no
   cost claim — cost always comes from the transport rate card, never inferred from rank
   (the #532 lesson).
3. **Stage defaults, mode envelopes, floors/ceilings** — become clamps over the ladder:
   - Stage defaults name a ladder rung per stage (the existing
     `defaultStageModels`/`DEFAULT_STAGE_MODELS` deliberate cross-language duplication
     survives; only its value vocabulary changes from band names to envelope rungs).
   - `MODE_PROFILES` envelopes become `(rung floor, rung ceiling, effort ceiling, thinking
policy)`. Efficiency already carries an `effortCeiling` — this generalizes what exists.
     `maximum` keeps pinning; `frontier`'s fable-ceiling narrowing (heavy reasoning stages
     only) becomes a rung-range rule on the same table pair
     (`modeProfiles.ts` ⇄ `performance_mode.go`).
   - `minimum_model` floors and the #42 sticky downgrade operate as rung comparisons, same
     semantics, same clamp-ordering rules as documented in
     `docs/PIPELINE_EXECUTION.md § Who Resolves the Model` — none of that section's
     clamp-ordering decisions change; only the value type does.
4. **Downgrade/escalation ladders** — `retry_engine.go`'s escalation and downgrade walks step
   rungs instead of bands. Provider-relative behavior is preserved (today's
   `EvaluateDowngrade` already walks provider-relatively); what disappears is the hardcoded
   `[haiku, sonnet, opus]` triplet and the fable exclusions that drifted per-site.

Resolver ownership does not change: `scheduler.resolveDispatchModel` (Go) owns the IPC and
auto/CLI paths, `resolveModel` (TS) owns the HeadlessOrchestrator path, and the extension
executes the wire envelope verbatim (#340). The wire grows `effort` and `thinking` alongside
`model` — which run-record-envelope then records.

### 4.2 Advisor re-keying and consumption

- **Re-key**: `routingAdvisor.aggregate` keys on
  `${job_class}|${model_id}|${effort}|${thinking}` (today: `${job_class}|${model_id}` only,
  collapsing the axes that #571 makes real). `Recommendation` returns a full envelope.
- **Consumption** (today: none — `use_eval_recommendations` exists only as a comment):
  - Add the real config key `model_routing.use_eval_recommendations` (default `false`).
  - The SDK eval lane materializes advice to a data file
    (`.nightgauge/model-evals/routing-advice.json`): per `(job_class, envelope)` — score,
    sample count, confidence flag, schema_version.
  - **Both** resolvers read that file, exactly as both read the registry today. A data-file
    handoff keeps the extension out of the routing business — threading TS advice over the
    wire would reintroduce the drift #340 removed.
  - The advisor slots between the stage default and the ladder ordering: it may reorder or
    re-pick **within** the candidate set and envelope clamps, never outside them.
- **Honesty gate**: the advisor consumes only schema_version ≥ 3 records (post-#571) and only
  baseline prompt variants, as it does today. Pre-#571 rows are structurally excluded.

### 4.3 Sparsity and confidence

Naive combination space: 28 models × 5 efforts × 2 thinking = 280 combos per job class —
unmeasurable. Constraints that make it tractable:

- The **registry interlock prunes before spawn** (#571 AC): only declared `supported_efforts`
  rungs, thinking values legal under the disable interlock, `served` transports. The real
  per-provider spaces are ~4–20 cells per model, and only candidate-set models matter
  (~8 non-deprecated tiered models today).
- **Sample floor per combination**: n ≥ 5 before an envelope combo is advisable (matches
  `CostPerSuccessContext`'s existing floor; the advisor's current minSamples=3 is per model
  and too loose per combination).
- **Hierarchical backoff** when a combo is sparse: exact `(model, effort, thinking)` →
  `(model, *, *)` aggregate → declared ladder position. Every advice entry carries its
  backoff level so consumers and telemetry can tell measured routing from declared routing.
- **Confidence**: the judge reliability guard's `low_confidence` cells are excluded from
  aggregation (mechanism exists, ADR 011 §4); advice entries below the sample floor are
  emitted with `advisable: false` rather than omitted, so sparsity is visible instead of
  silent.
- **Budget reality**: evidence arrives stage-shaped from #528 (live six-stage matrix) and
  cell-shaped from the honest eval lane; both feed the same records store. Capability-based
  selection ships only where evidence exists — everywhere else the declared ladder rules,
  labeled as such.

## 5. Migration Plan

Per store, with the alias / backfill / accept-void decision justified:

| Store                                                                                                          | Decision                       | Justification                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| -------------------------------------------------------------------------------------------------------------- | ------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Outcomes corpus (`.nightgauge/pipeline/history/outcomes.jsonl`)                                                | **accept-void**                | 9 records exist; 8 have empty model pairs and 1 says `sonnet`. Backfill is impossible (effort/thinking were never recorded and cannot be reconstructed); an alias preserves almost nothing. The writer adds a `schema_version` field at cutover (the file has none today) so exclusion is deterministic rather than vocabulary-sniffing; readers already exclude `""` from denominators (#340) and legacy rows join that bucket.                                                                                                                                                             |
| Run-record history (`model_selection.model` band strings)                                                      | **accept-void**                | Records are operator forensics, not learning inputs; pre-customer, no compat shims (AGENTS.md). New records carry the full envelope (run-record-envelope). The band-substring health consumers (`modelRouting.ts` `model.includes("haiku")`, `analyze-model-routing.ts` literals) are rewritten in the sweep to key off envelope fields — they currently fail open (stop matching), which is exactly why they must be rewritten, not aliased.                                                                                                                                                |
| Overlay keyspace + committed plugin mirror                                                                     | **keyspace change, no rename** | No band-named overlay file exists on disk (only `xai.md`, `grok-4.6.md`, `grok-build-0.1.md`) — the surface is `OverlayKeys` (drops the `Tiers` segment: cascade becomes provider → concrete id), `render_test.go`'s pinned cascades, `docs/GO_BINARY.md` examples, and prose in `grok-build-0.1.md` that names a band. Same commit regenerates `claude-plugins/nightgauge/skills/` via `install-agent-skills.sh` — the #549 drift gate fails otherwise. A rank-keyed middle segment is _not_ added back: no overlay needs it today, and pre-customer we delete paths rather than speculate. |
| SKILL.md frontmatter `model:` advisories                                                                       | **rewrite in sweep**           | `model: haiku`/`sonnet` across pipeline skills are advisory tier names other adapters translate (SKILL_PORTABILITY). They move to the new request vocabulary in the same sweep commit, with the plugin mirror regenerated; machine-local installs (`~/.codex/skills`) lag until operators rerun the installer — acceptable for advisory metadata.                                                                                                                                                                                                                                            |
| Four band enums                                                                                                | **collapse to one derivation** | `MODEL_TIERS` (`schemas.ts:30`), `experiment-types.ts:18`, vscode `DefaultModelSchema`/`soft_route_model` (`config/schema.ts:1889/:502`), Go `Tier*` consts (`performance_mode.go:114-117`). Only the two SDK copies have a compile-time parity check today. End state: one definition per language, derived from the registry data file, parity-gated by the existing sync machinery.                                                                                                                                                                                                       |
| ~12 inlined regex/closed-set sites                                                                             | **replace with derivation**    | Five `validModels` arrays + two YAML band regexes in `stageResolver.ts`, five-plus more in `modelResolver.ts`. The file's own comments record two past incidents where a three-band regex silently dropped `fable` — the motivating evidence that hand-inlined closed sets must become derivations, not be re-inlined with new names.                                                                                                                                                                                                                                                        |
| Config surfaces (`stage_models`, `minimum_model`, `soft_route_model`, `haiku_max`/`sonnet_max` threshold keys) | **hard vocabulary change**     | Pre-customer: no aliases, no migration fallbacks. Schema, `docs/CONFIGURATION.md`, and the mode-profile docs change together. The untracked workspace `config.schema.json` has no in-repo generator (a discovered gap worth its own issue at sweep time); operator workspaces re-scaffold.                                                                                                                                                                                                                                                                                                   |
| Mode-profile tables (both layers)                                                                              | **rewrite in lockstep**        | `modeProfiles.ts` ⇄ `performance_mode.go` are a documented deliberate duplication; the sweep changes both in one commit with their cross-annotations updated.                                                                                                                                                                                                                                                                                                                                                                                                                                |

Sequencing (AGENTS.md mechanical-sweep rule): the sweep
(telemetry-overlay-docs-migration) lands **alone, last, over settled code** — after the schema,
enforcement, run-record, and selection phases have merged — because it touches everything by
definition. It also lands the reintroduction gate: a lint/CI check that fails on new
band-vocabulary usage outside the Anthropic adapter path (where `haiku|sonnet|opus` remain
legitimate Claude CLI model aliases passed verbatim).

## 6. Disposition of Adjacent Open Issues

- **#543** (grok overlays: inert `grok-build-0.1.md`, `grok-4.6.md` instructs on an unwired
  `--effort`) — **absorb** into telemetry-overlay-docs-migration: overlay content is that
  phase's keyspace sweep, and the unwired-effort premise is resolved by #569 (in flight).
- **#552** (`resolveGrokModel` hands the bare band name to `grok --model` on a Resolve miss) —
  **absorb** into fail-closed-axis-enforcement: it is the exact defect the fail-closed
  candidate-set gate exists to kill, in the same file.
- **#435** (codex reasoning ladder hand-listed in four places) — **absorb** into
  selection-query-cutover: the cutover rewrites those resolver ladder sites; deriving the
  codex ladders from `EFFORT_LEVELS` is part of that rewrite, and doing it separately would
  collide on the same files.
- **#434** (run-record effort enum drifted, never written; derive-or-delete) — **absorb** into
  run-record-envelope, whose scope statement already resolves the question as "derive and
  write".
- **#436** (packaging test only checks dist existence; strict Zod + stale dist = load-time
  crash) — **absorb** into registry-axis-schema: the first strict-schema field addition is the
  commit where this crash class goes live, so the packaging-parse hardening ships inside it.
- **#462** (`model_selection.source` cannot distinguish router-chosen from operator-pinned;
  `modelSelectionMode` never written) — **absorb** into run-record-envelope: same record, same
  Go writer, same cross-language pin work.
- **#521** (spike: route non-agentic surfaces to local models) — **leave standalone**:
  local-provider routing policy is orthogonal; local providers have no registry entries by
  design, so the axis schema neither gates nor changes it.
- **#383** (extend eval harnesses to the fable band) — **sequence**: blockedBy #571. Fable
  coverage is only worth running on honest cells; when it runs, its rows key on
  `claude-fable-5` envelopes, not a band.
- **#384** (fold the hardcoded Haiku preamble into a haiku overlay) — **absorb** into
  telemetry-overlay-docs-migration: as filed it would create a new band-keyed overlay that the
  same phase retires; the fold targets the concrete-id (or provider) overlay instead, and the
  `preamble.go` band-substring match is one of the sweep's closed-set sites.
- **#83** (model-watch → registry patches) — **leave standalone**: upstream release monitoring
  is orthogonal; the new provenance fields give its patches a structured target (a
  model-watch patch is by definition `rate_provenance: list` until re-measured).

## Spike Contract (Path A)

Same-repo dependents wait for this spike's PR to merge; the recommendations below are
materialized automatically afterwards.

**Artifact**: [`docs/spikes/568-model-identity-axes.md`](568-model-identity-axes.md)

## Recommendations

```yaml recommendations
spike: 568
recommendations:
  - id: registry-axis-schema
    action: adopt
    title: "feat(models): add orthogonal axis fields to the model registry (additive)"
    type: feature
    priority: high
    size: M
    labels: ["area:go-binary", "area:sdk"]
    body: |
      Add the axis fields selected by the spike artifact (§2): a `transports`
      map per model (per-transport `served` reachability, optional `verified`
      date + `evidence`, optional per-transport `rates` with mandatory
      `rate_provenance`), a top-level `effort_levels` ladder declaration, and
      top-level `rate_provenance` per model — to the canonical registry, the
      strict TS descriptor schema, and the Go mirror structs in one commit,
      with the sync script run and parity tests green. Additive only — no
      vocabulary change, no enforcement change yet. Populate values from the
      spike's §1 inventory (grok transport facts carry measured catalog
      evidence; everything else lands as declared). Loader-level asserts on
      both layers: transport rates require provenance; `effort_levels` must
      equal EFFORT_LEVELS exactly.

      Absorbs #436: extend modelRegistryPackaging.test.ts from "dist file
      exists" to "the packaged registry parses under the packaged schema" in
      the same PR — this commit is where the strict-Zod stale-dist load-time
      crash class becomes live. Also update the terminalkind poison fixture if
      it embeds the descriptor shape. Capability fields are explicitly NOT
      added: capability lives in the eval records store, never the registry.
    depends_on: []
  - id: fail-closed-axis-enforcement
    action: adopt
    title: "feat(models): enforce transport reachability fail-closed at selection"
    type: feature
    priority: high
    size: M
    labels: ["area:go-binary", "area:sdk"]
    body: |
      A model not reachable through the dispatching adapter's transport must be
      unselectable: candidate sets filter on `transports[<transport>].served`,
      and a request naming an unserved model fails closed before spawn with a
      classified error naming provider, model, and transport. Retires the
      deprecated/tiers-absence overloading that caused the #532 class —
      `deprecated` returns to meaning exactly "superseded", tiers-absence to
      "not a ladder routing target". After this lands, loaders reject
      non-deprecated entries without a transports block (load-time strictness
      graduates from optional to required).

      Absorbs #552: resolveGrokModel's bare band-name fallthrough to
      `grok --model` dies at this gate (regression test required). #569 is the
      effort half of fail-closed dispatch; this is the transport/model half.
      Doctor catalog probing (#551) is the detection half; this is the
      enforcement half. Non-gating reference: #551.
    depends_on: ["registry-axis-schema"]
  - id: run-record-envelope
    action: adopt
    title: "feat(history): record the full dispatch envelope (model, effort, thinking) in run records"
    type: feature
    priority: high
    size: M
    labels: ["area:go-binary", "area:vscode"]
    body: |
      Extend run-record model_selection to the full envelope — today the effort
      enum stops at high (two rungs behind EFFORT_LEVELS) and is never written,
      thinking is unrepresentable, and Go's V2ModelSelect has no effort field
      at all, so #528's acceptance criteria cannot be recorded honestly. The
      effort enum derives from EFFORT_LEVELS; thinking is the binary on|off
      axis (spike §3). Cross-language schema change: pin both the key axis and
      the value axis with a Go↔TS parity test (the #446 lesson).

      Absorbs #434 (resolved as derive-and-write, not delete) and #462
      (selection-mode attribution — router-chosen vs operator-pinned — is the
      same record, same writer, same pin work). Historical band-valued records
      are accept-void per spike §5: no alias, no backfill; the writer stamps a
      schema marker at cutover so exclusion is deterministic.
    depends_on: ["registry-axis-schema"]
  - id: selection-query-cutover
    action: adopt
    title: "feat(routing): replace band lookup with an axis selection query; wire the eval advisor"
    type: feature
    priority: high
    size: L
    labels: ["area:go-binary", "area:sdk", "area:vscode"]
    body: |
      The dispatch envelope (model_id, effort, thinking) chosen from a
      provider-scoped, envelope-valued candidate ladder replaces band lookup
      (spike §4). Stage defaults, MODE_PROFILES envelopes (gaining effort
      ceiling + thinking policy), floors/ceilings, and downgrade/escalation
      ladders become rung clamps and rung walks; on xai the ladder descends
      through effort within grok-4.6, resolving the #532 cost no-op honestly.
      Resolver ownership per #340 is unchanged; the wire grows effort and
      thinking. Cost is never inferred from ladder rank — it always comes from
      the transport rate card.

      The eval routing advisor re-keys aggregation to (job_class, model_id,
      effort, thinking), returns full envelopes, and is consumed for real: a
      new `model_routing.use_eval_recommendations` config key (default false),
      advice materialized to a data file both resolvers read (never TS config
      over the wire), consuming only schema_version >= 3 (honest) records.
      Sparsity handling per spike §4.3: registry-interlock pruning, n>=5 per
      combination, hierarchical backoff with the backoff level recorded,
      low_confidence exclusion, advisable:false emission for sparse combos.

      Absorbs #435: the codex reasoning-ladder derivation happens inside this
      rewrite of the same resolver sites. Capability inputs must come from
      measured eval evidence; where none exists the declared ladder rules,
      labeled as such.

      ## Cross-Repo Dependencies

      - ⚠️ #528 — measured matrix evidence must exist before capability-based selection ships
    depends_on: ["registry-axis-schema", "fail-closed-axis-enforcement", "run-record-envelope"]
  - id: telemetry-overlay-docs-migration
    action: adopt
    title: "chore(models): retire the band vocabulary — telemetry, overlays, mirrors, docs"
    type: chore
    priority: medium
    size: L
    labels: ["area:go-binary", "area:sdk", "area:vscode", "area:docs"]
    body: |
      The mechanical sweep, landing alone, last, over settled code (spike §5):
      the four independent band enums collapse to per-language derivations from
      the registry data file; the ~12 hand-inlined band regex/closed-set sites
      become derivations (two past silent-fable-drop incidents are the
      motivating evidence); the overlay cascade drops the band segment
      (provider → concrete id — no band-named overlay file exists on disk, so
      the surface is OverlayKeys, its pinned tests, docs examples, and one line
      of overlay prose), with the plugin mirror regenerated in the same commit
      (#549 drift gate); SKILL.md model: advisories and both mode-profile
      tables move in lockstep; config vocabulary changes hard (pre-customer, no
      aliases); band-substring health consumers are rewritten to envelope
      fields. Stored telemetry is accept-void per store (outcomes corpus and
      run-record history), with schema markers stamped at cutover.

      Absorbs #543's overlay half (content sweep; #569 resolved its unwired
      -effort premise) and #384 (the preamble folds into a concrete-id overlay,
      not a new band-keyed one). Adds the reintroduction gate: CI fails on new
      band-vocabulary usage outside the Anthropic adapter path, where
      haiku/sonnet/opus remain legitimate Claude CLI model aliases.
    depends_on: ["selection-query-cutover"]
```
