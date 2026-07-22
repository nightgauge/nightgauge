# Codex `--output-schema` Evaluation

**Date:** 2026-03-13 **Author:** nightgauge **Status:** Decided — Defer
**Issue:** #1663
**Epic:**
#1650 — Mature Codex CLI integration for high-quality outputs

---

## Executive Summary

The Codex CLI `--output-schema` flag passes a JSON Schema file to OpenAI's
structured outputs API as a **hard constraint** (not a post-hoc validator). When
a valid schema is supplied, the model is forced to produce JSON conforming to
that schema. This significantly reduces `ContextValidationError` failures.

**Recommendation: DEFER.**

The flag works and provides real reliability value, but adopting it requires
maintaining strict JSON Schema versions of our 6 context schemas separately from
the authoritative Zod schemas. The OpenAI structured outputs API imposes
constraints (`additionalProperties: false`, all properties in `required`) that
conflict with our `.passthrough()` and `.nullish()` patterns. A proper adoption
requires a `zod-to-json-schema` conversion utility and a schema design pass —
appropriate for a dedicated follow-up issue under epic #1650.

---

## Background

The pipeline currently validates stage outputs **post-hoc**: a Codex agent
writes a JSON context file, then `ContextManager.read()` validates it against a
Zod schema. If validation fails, the pipeline backtracks to feature-planning.
This accounts for a meaningful fraction of pipeline failures in production.

The Codex CLI `codex exec --output-schema <FILE>` flag has been present since
v0.98.0 (our minimum supported version). The spike was commissioned to
determine:

1. Is `--output-schema` a constraint (forces conforming output) or a validator
   (post-generation check)?
2. What are the API requirements for the schema?
3. How complex would adoption be?

---

## Spike Testing

All tests run against Codex CLI v0.98.0 (`gpt-5.4` model) in
`--full-auto --sandbox danger-full-access --json` mode (production arg set).

### Test 1: Constraint vs. Validator (simple schema)

**Schema**:
`{ type: "object", required: ["status", "message"], properties: { status: { enum: ["ok", "error"] }, message: { type: "string" } }, additionalProperties: false }`

**Prompt 1**: "Respond with JSON only: `{"status": "ok", "message": "hello"}`"

**Result**: `{"status":"ok","message":"hello"}` — conforming JSON produced.

**Prompt 2**: "Tell me a fun fact about cats. Just answer conversationally."

**Result**:
`{"status":"ok","message":"Cats can rotate their ears about 180 degrees..."}` —
**conforming JSON produced despite conversational prompt**.

**Conclusion**: `--output-schema` is a **hard constraint**. The schema is passed
to the OpenAI structured outputs API. The model is forced to produce conforming
JSON regardless of prompt framing.

### Test 2: API Requirements Discovery

**Schema**: `PlanningContext`-shaped object with `additionalProperties: true`
(matches our Zod `.passthrough()` pattern).

**Result**:

```
HTTP 400: "Invalid schema for response_format 'codex_output_schema':
In context=(), 'additionalProperties' is required to be supplied and to be false."
```

**Conclusion**: OpenAI structured outputs requires
`"additionalProperties": false` on **every** object — including nested objects.
Our `.passthrough()` pattern is incompatible.

**Schema**: Object with `additionalProperties: false` but without all properties
in `required`.

**Result**:

```
HTTP 400: "In context=('properties', 'decisions', 'items'), 'required' is required
to be supplied and to be an array including every key in properties. Missing 'id'."
```

**Conclusion**: When `additionalProperties: false`, ALL properties must be
listed in `required`. Optional fields must use `["type", "null"]` (nullable
required fields).

### Test 3: Success with Fully-Conformant Schema

**Schema**: `PlanningContext`-shaped object with `additionalProperties: false`
everywhere, all properties in `required`, nullable fields using
`"type": ["string", "null"]`.

**Result**: Codex produced valid `planning-{N}.json`-shaped JSON:

```json
{
  "schema_version": "1.4",
  "issue_number": 42,
  "plan_file": ".nightgauge/plans/42-dark-mode-toggle.md",
  "approach": "simple-feature",
  "files_to_create": ["packages/extension/src/settings/DarkModeToggle.ts"],
  "files_to_modify": ["packages/extension/src/settings/SettingsPanel.ts"],
  "decisions": [],
  "revision_count": 0,
  "knowledge_path": null,
  "created_at": "2026-03-13T06:30:30Z"
}
```

This JSON passes `PlanningContextSchema.safeParse()` validation (Zod's
`.passthrough()` accepts it, and all required fields are present).

**Token usage**: 51k input tokens (Codex read skill files before responding) /
693 output tokens. No measurable overhead from schema processing itself.

### Test 4: `--output-schema` with `--json` Flag

Both flags work together without conflict. The schema-constrained JSON appears
inside `item.completed` JSONL events as the `text` field — which
`summarizeCodexJsonOutput()` already processes correctly.

### Test 5: `--output-schema` with `--output-last-message`

Both flags work together. The last-message file receives the schema-constrained
JSON. No interaction issues.

### Test 6: Impossible/Invalid Schema Fallback

**Schema**: Object with `minimum: 9999, maximum: 9998` (logically impossible).

**Result**: Codex ignored the schema and returned plain text ("hello world"). No
API error was emitted; the constraint was silently dropped.

**Conclusion**: Schema validation is not 100% atomic. Invalid schemas may cause
silent fallback to unstructured output.

---

## Constraints Analysis: Codex Schema vs. Our Zod Schemas

| Constraint             | OpenAI Requirement                                | Our Zod Schemas                                        | Conflict?                   |
| ---------------------- | ------------------------------------------------- | ------------------------------------------------------ | --------------------------- |
| `additionalProperties` | Must be `false` on all objects                    | `.passthrough()` → `true`                              | **Yes**                     |
| Optional fields        | Must be in `required` as nullable `["T", "null"]` | `.nullish()` → not in required                         | **Yes** (design, not logic) |
| Union types            | Limited support; `anyOf` with simple types OK     | `z.union([z.string(), z.object(...)])` for `decisions` | **Likely**                  |
| Custom enum            | `"enum": [...]` with all values                   | `flexEnum()` (case-insensitive)                        | Partial                     |
| Nested objects         | Each needs `additionalProperties: false`          | Nested objects vary                                    | **Yes**                     |
| `$defs` / `$ref`       | Supported                                         | Not used currently                                     | No                          |

Our most-used pattern — `.passthrough()` + `.nullish()` — was adopted
intentionally: it lets AI agents include extra fields without validation
failures, which is valuable for forward-compatibility. Adopting
`--output-schema` requires abandoning `.passthrough()` in the schema files Codex
sees, which means maintaining **two representations** of each context schema:

1. **Zod schema** (authoritative, used by `ContextManager`): retains
   `.passthrough()` and `.nullish()` for validation
2. **JSON Schema** (strict, passed to Codex via `--output-schema`): strict
   `additionalProperties: false`, all fields required/nullable

---

## Integration Complexity (Minimum Adoption Path)

If adopting, the minimum changes required:

1. **`ICliAdapter.ts`**: Add `outputSchemaPath?: string` to
   `QueryFunctionOptions`
2. **`CodexAdapter.ts`**: When `outputSchemaPath` is set, append
   `--output-schema ${outputSchemaPath}` to CLI args
3. **Context schema files**: Add a `generateJsonSchema()` export to each of the
   6 context schema files that returns a strict JSON Schema compatible with
   OpenAI structured outputs
4. **Schema generation utility**: Either install `zod-to-json-schema` (with a
   custom converter for `.passthrough()`, `flexEnum`, and mixed unions) or
   maintain hand-written JSON Schema files
5. **`StageExecutor.ts`**: Thread `outputSchemaPath` from stage config through
   to `createQueryFunction()`
6. **Failure handling**: `turn.failed` events from schema validation errors
   already propagate through `createCliQueryFn` — no new failure path needed

**Estimated size**: M–L issue (the schema generation utility is the complex
part).

---

## Decision

**Recommendation: DEFER to a dedicated follow-up issue under epic #1650.**

### Rationale

**For adoption** (what makes this valuable):

- `--output-schema` IS a hard server-side constraint — not a hint
- Zero additional failure-handling code needed (errors already propagate)
- Token overhead is minimal (schema processing is server-side)
- Works with `--json` JSONL mode (no changes to `summarizeCodexJsonOutput`)
- Could virtually eliminate `ContextValidationError` failures from malformed
  JSON

**Against immediate adoption** (why to defer rather than skip):

- Requires dual schema maintenance (Zod + strict JSON Schema)
- Schema design pass needed for all 6 context schemas
- Custom converter needed for `.passthrough()`, `flexEnum()`, and union types
- Logically impossible schemas fall back silently — need schema validation in CI
- Scope is M–L; too large to include in this spike PR

**Why not "skip"**: The constraint behavior is confirmed and meaningful. A
properly designed schema generation utility would be a one-time cost; ongoing
maintenance is low if the utility is automated.

---

## Consequences

If deferred (chosen path):

- Current post-hoc Zod validation remains the only validation mechanism
- ContextValidationError failures from Codex continue at current rate
- A follow-up implementation issue should be created under epic #1650

If adopted (follow-up issue):

- `ContextValidationError` failures from Codex stages should drop significantly
- `StageExecutor` gains `outputSchemaPath` threading (opt-in, Codex-only)
- Each context schema gains a strict JSON Schema companion
- `zod-to-json-schema` added as a dev/build dependency to the SDK

---

## Follow-Up Issue (if adopting)

Create under epic #1650:

**Title**: `feat: Integrate Codex --output-schema for structured stage outputs`

**Acceptance criteria**:

1. `zod-to-json-schema` (or equivalent) converts each context Zod schema to a
   strict JSON Schema passable to OpenAI structured outputs API
2. `QueryFunctionOptions.outputSchemaPath?: string` added to `ICliAdapter`
3. `CodexAdapter` appends `--output-schema` when `outputSchemaPath` is set
4. `StageExecutor` threads schema path through from `HeadlessOrchestrator`
5. Strict schema files validated in CI (no logically impossible schemas)
6. Spike test file `CodexAdapter.outputSchema.test.ts` updated with integration
   tests against the new wiring
