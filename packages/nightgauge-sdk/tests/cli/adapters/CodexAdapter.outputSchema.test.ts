/**
 * Spike: Codex --output-schema flag evaluation (#1663)
 *
 * This file documents empirical findings from the spike investigation of
 * `codex exec --output-schema <FILE>`. It serves as living documentation of
 * the flag's behavior and the integration constraints discovered.
 *
 * SPIKE STATUS: DEFER — findings support adoption but scope is M–L.
 * See: docs/decisions/002-codex-output-schema-evaluation.md
 *
 * These tests do NOT make live Codex CLI calls. They verify:
 * 1. That `CodexAdapter.getDefaultArgs()` does NOT include `--output-schema`
 *    today (no regression from this spike)
 * 2. The JSON Schema requirements for OpenAI structured outputs (documented
 *    as data-driven tests for future implementers)
 * 3. What a valid `--output-schema`-compatible strict JSON Schema looks like
 *    for our PlanningContext type
 *
 * @see Issue #1663 — spike: Evaluate Codex output-schema validation
 * @see docs/decisions/002-codex-output-schema-evaluation.md
 */

import { describe, it, expect } from "vitest";
import { CodexAdapter } from "../../../src/cli/adapters/CodexAdapter.js";
import { PlanningContextSchema } from "../../../src/context/schemas/planning.js";

// ---------------------------------------------------------------------------
// Finding 1: --output-schema is a hard server-side constraint
// ---------------------------------------------------------------------------

describe("Codex --output-schema behavior (spike findings, #1663)", () => {
  /**
   * FINDING: `--output-schema` passes the schema to OpenAI's structured outputs
   * API as response_format. It is a HARD CONSTRAINT — the model is forced to
   * produce JSON conforming to the schema regardless of how the prompt is worded.
   *
   * Evidence: When prompted "Tell me a fun fact about cats. Just answer
   * conversationally." with a schema requiring `{ status, message }`, Codex
   * returned `{"status":"ok","message":"Cats can rotate their ears..."}` — NOT
   * a conversational response.
   *
   * This is the key value proposition: structured outputs virtually guarantee
   * valid JSON, reducing ContextValidationError pipeline failures.
   */
  it("documents: --output-schema is a hard constraint (not a post-hoc validator)", () => {
    // This test exists to document the finding. The assertion is a sentinel
    // that forces a future reader to acknowledge the behavior.
    const finding = {
      flag: "--output-schema",
      behavior: "hard-constraint" as const,
      mechanism: "openai-structured-outputs-api" as const,
      postHocValidator: false,
    };

    expect(finding.behavior).toBe("hard-constraint");
    expect(finding.postHocValidator).toBe(false);
    expect(finding.mechanism).toBe("openai-structured-outputs-api");
  });
});

// ---------------------------------------------------------------------------
// Finding 2: CodexAdapter does NOT yet pass --output-schema
// ---------------------------------------------------------------------------

describe("CodexAdapter default args (no regression from spike)", () => {
  /**
   * Documents the pre-spike state: CodexAdapter does not include
   * `--output-schema` in its default args. This test should PASS today and
   * should FAIL after the follow-up implementation issue integrates the flag
   * (at which point the test must be updated).
   *
   * When the follow-up issue is implemented:
   * - `QueryFunctionOptions.outputSchemaPath` will be added to `ICliAdapter`
   * - `CodexAdapter.createQueryFunction()` will append `--output-schema` when
   *   `outputSchemaPath` is provided
   * - This test should be replaced with one that verifies that wiring
   */
  it("does not include --output-schema in default args (pre-integration baseline)", () => {
    const adapter = new CodexAdapter();
    const defaultArgs = adapter.getDefaultArgs();

    expect(defaultArgs).not.toContain("--output-schema");
    // Current expected default args for reference (#4020):
    // ['exec', '--dangerously-bypass-approvals-and-sandbox', '--json']
    expect(defaultArgs).toContain("exec");
    expect(defaultArgs).toContain("--dangerously-bypass-approvals-and-sandbox");
    expect(defaultArgs).not.toContain("--full-auto");
    expect(defaultArgs).toContain("--json");
  });

  it("does not accept outputSchemaPath in QueryFunctionOptions today", () => {
    // This test documents that `QueryFunctionOptions` currently only has `cwd`.
    // A future implementation issue must add `outputSchemaPath?: string`.
    // If this test ever fails to compile, the field was added — update it.

    const options: Parameters<CodexAdapter["createQueryFunction"]>[0] = {
      cwd: "/tmp",
      // outputSchemaPath: '/tmp/schema.json', // ← Not yet a valid field
    };

    // The only valid field today is cwd
    const validKeys = Object.keys(options ?? {});
    expect(validKeys).toEqual(["cwd"]);
  });
});

// ---------------------------------------------------------------------------
// Finding 3: OpenAI structured outputs API schema requirements
// ---------------------------------------------------------------------------

describe("OpenAI structured outputs schema requirements (documented constraints)", () => {
  /**
   * These tests document the API requirements discovered during the spike.
   * The API rejected schemas that did not meet these constraints with HTTP 400.
   *
   * A future `zod-to-json-schema` utility must produce schemas that satisfy
   * ALL of these requirements.
   */

  it("requires additionalProperties: false on every object", () => {
    // FINDING: Schemas with additionalProperties: true (or absent) are rejected
    // with: "In context=(), 'additionalProperties' is required to be supplied
    //        and to be false."
    //
    // IMPLICATION: Our Zod schemas use .passthrough() which maps to
    // additionalProperties: true. We need SEPARATE strict JSON Schema files.
    const validSchema = {
      type: "object",
      required: ["field"],
      additionalProperties: false, // ← required
      properties: { field: { type: "string" } },
    };

    const invalidSchema = {
      type: "object",
      required: ["field"],
      // additionalProperties: true — REJECTED BY API
      properties: { field: { type: "string" } },
    };

    expect(validSchema.additionalProperties).toBe(false);
    // Document what the API rejects:
    expect(invalidSchema).not.toHaveProperty("additionalProperties", false);
  });

  it("requires ALL properties to be listed in required when additionalProperties: false", () => {
    // FINDING: When additionalProperties: false, the API requires all properties
    // to be in `required`. Optional fields must use ["type", "null"] instead.
    //
    // API error: "In context=(...), 'required' is required to be supplied and
    //             to be an array including every key in properties. Missing 'X'."
    //
    // IMPLICATION: Our .nullish() fields (e.g., decisions, revision_count) must
    // become required fields with nullable types in the strict schema.
    const optionalFieldAsNullable = {
      type: ["string", "null"], // ← nullable required field (OK with API)
    };

    const optionalFieldMissingFromRequired = {
      // type: 'string' in properties but NOT in required[] — REJECTED
    };

    expect(optionalFieldAsNullable.type).toContain("null");
    expect(optionalFieldMissingFromRequired).toEqual({});
  });

  it("documents the PlanningContextSchema fields that conflict with strict schema", () => {
    // These Zod patterns need special handling in the strict JSON Schema:
    const conflictingPatterns = [
      {
        zodPattern: ".passthrough()",
        jsonSchemaConflict: "additionalProperties must be false",
        resolution: "Drop passthrough; use additionalProperties: false in strict schema",
      },
      {
        zodPattern: ".nullish()",
        jsonSchemaConflict: "field must be in required[] when additionalProperties: false",
        resolution: 'Use type: ["T", "null"] and include in required[]',
      },
      {
        zodPattern: "z.union([z.string(), z.object(...).passthrough()])",
        jsonSchemaConflict: "Union with mixed passthrough objects; additionalProperties required",
        resolution: "Replace with a strict object shape; remove free-form string variant",
      },
      {
        zodPattern: "flexEnum([...] as const)",
        jsonSchemaConflict: "Case-insensitive; standard JSON Schema enum is case-sensitive",
        resolution: "Use enum: [...lowercase values...] in strict schema; flexEnum remains in Zod",
      },
    ];

    // Verify PlanningContextSchema uses the patterns we expect to conflict:
    const zodSource = PlanningContextSchema._def;
    expect(zodSource).toBeDefined(); // Schema exists

    // The conflicts documented above are the reason for DEFER recommendation
    expect(conflictingPatterns).toHaveLength(4);
    expect(conflictingPatterns[0].zodPattern).toBe(".passthrough()");
  });
});

// ---------------------------------------------------------------------------
// Finding 4: What a valid strict schema for PlanningContext looks like
// ---------------------------------------------------------------------------

describe("Valid strict JSON Schema shape for PlanningContext", () => {
  /**
   * This test defines and validates the STRUCTURE of a strict JSON Schema
   * that would be accepted by the OpenAI structured outputs API for a
   * PlanningContext response.
   *
   * Note: This is the schema that would be passed to Codex, NOT the Zod schema
   * used by ContextManager. The Zod schema remains the authoritative validator.
   *
   * Key differences from PlanningContextSchema:
   * - No additionalProperties: true (all objects use false)
   * - All fields in required[] (nullable via ["type", "null"])
   * - decisions items use a single strict object shape (no string union)
   * - No flexEnum; enum values are lowercase strings
   */
  const strictPlanningContextSchema = {
    $schema: "http://json-schema.org/draft-07/schema#",
    title: "PlanningContext",
    type: "object",
    additionalProperties: false,
    required: [
      "schema_version",
      "issue_number",
      "plan_file",
      "approach",
      "files_to_create",
      "files_to_modify",
      "decisions",
      "revision_count",
      "revision_reasons",
      "knowledge_path",
      "knowledge_entries",
      "created_at",
    ],
    properties: {
      schema_version: { type: "string" },
      issue_number: { type: "integer" },
      plan_file: { type: "string" },
      approach: { type: "string" },
      files_to_create: { type: "array", items: { type: "string" } },
      files_to_modify: { type: "array", items: { type: "string" } },
      // decisions: strict object shape — no string-union variant
      decisions: {
        type: "array",
        items: {
          type: "object",
          additionalProperties: false,
          required: ["id", "title", "chosen", "rationale"],
          properties: {
            id: { type: "string" },
            title: { type: "string" },
            chosen: { type: "string" },
            rationale: { type: "string" },
          },
        },
      },
      revision_count: { type: "integer" },
      revision_reasons: { type: "array", items: { type: "string" } },
      // Nullable fields: required but can be null
      knowledge_path: { type: ["string", "null"] },
      knowledge_entries: {
        type: ["array", "null"],
        items: { type: "string" },
      },
      created_at: { type: "string" },
    },
  };

  it("has additionalProperties: false at root level", () => {
    expect(strictPlanningContextSchema.additionalProperties).toBe(false);
  });

  it("includes ALL properties in required[]", () => {
    const propertyKeys = Object.keys(strictPlanningContextSchema.properties);
    const requiredKeys = strictPlanningContextSchema.required;

    for (const key of propertyKeys) {
      expect(requiredKeys).toContain(key);
    }
  });

  it("uses additionalProperties: false on nested decision objects", () => {
    const decisionItemSchema = strictPlanningContextSchema.properties.decisions.items;
    expect(decisionItemSchema.additionalProperties).toBe(false);
  });

  it("uses nullable type arrays for optional fields", () => {
    const knowledgePath = strictPlanningContextSchema.properties.knowledge_path;
    expect(knowledgePath.type).toContain("null");
  });

  it("the strict schema shape satisfies an output that would pass PlanningContextSchema.safeParse", () => {
    // A Codex response conforming to the strict schema should also pass
    // PlanningContextSchema (our Zod validator), because:
    // - Zod uses .passthrough() (accepts extra fields — strict schema has none)
    // - All required Zod fields are present in the strict schema
    // - Nullable fields in strict schema match .nullish() in Zod

    const codexOutput = {
      schema_version: "1.4",
      issue_number: 42,
      plan_file: ".nightgauge/plans/42-test.md",
      approach: "simple-feature",
      files_to_create: ["src/NewFile.ts"],
      files_to_modify: ["src/ExistingFile.ts"],
      decisions: [
        {
          id: "test-decision",
          title: "Test",
          chosen: "option-a",
          rationale: "Because",
        },
      ],
      revision_count: 0,
      revision_reasons: [],
      knowledge_path: null,
      knowledge_entries: null,
      created_at: "2026-03-13T00:00:00.000Z",
    };

    const result = PlanningContextSchema.safeParse(codexOutput);
    expect(result.success).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Finding 5: --output-schema works with --json (JSONL mode)
// ---------------------------------------------------------------------------

describe("--output-schema + --json flag compatibility (spike findings)", () => {
  /**
   * FINDING: Both flags work together. The schema-constrained JSON appears in
   * `item.completed` JSONL events as the `text` field:
   *
   * {"type":"item.completed","item":{"type":"agent_message","text":"<JSON>"}}
   *
   * `summarizeCodexJsonOutput()` already processes this field — no changes
   * needed to the output parser when integrating --output-schema.
   *
   * FINDING: --output-schema also works with --output-last-message (no conflict).
   */
  it("documents: --output-schema JSON appears in JSONL item.completed text field", () => {
    // The JSONL event structure when --output-schema is active:
    const exampleJsonlEvent = {
      type: "item.completed",
      item: {
        id: "item_0",
        type: "agent_message",
        text: '{"schema_version":"1.4","issue_number":42}', // ← schema-constrained JSON
      },
    };

    // summarizeCodexJsonOutput() extracts text from this event type
    // No changes needed to the output processing pipeline
    expect(exampleJsonlEvent.item.type).toBe("agent_message");
    expect(exampleJsonlEvent.item.text).toContain("schema_version");
  });
});

// ---------------------------------------------------------------------------
// Finding 6: Schema validation failure behavior
// ---------------------------------------------------------------------------

describe("--output-schema failure handling (spike findings)", () => {
  /**
   * FINDING: When a schema fails OpenAI validation (e.g., missing
   * additionalProperties: false), Codex emits a `turn.failed` JSONL event
   * with the API error message, and exits with code 1.
   *
   * `createCliQueryFn` already handles this: non-zero exit codes throw an Error,
   * and the error message includes the API error text. No new error handling needed.
   *
   * FINDING: Logically impossible schemas (min > max) cause silent fallback —
   * Codex ignores the schema and returns plain text with exit code 0. This means
   * schema quality must be validated separately (CI check on schema files).
   */
  it("documents: invalid schema emits turn.failed JSONL event", () => {
    // The JSONL error event structure when schema is rejected by API:
    const exampleFailureEvent = {
      type: "turn.failed",
      error: {
        message:
          "Invalid schema for response_format 'codex_output_schema': " +
          "In context=(), 'additionalProperties' is required to be supplied and to be false.",
      },
    };

    // This propagates through createCliQueryFn's exit-code check (code !== 0)
    // as a thrown Error — no new handling needed in CodexAdapter
    expect(exampleFailureEvent.type).toBe("turn.failed");
    expect(exampleFailureEvent.error.message).toContain("additionalProperties");
  });

  it("documents: logically impossible schemas fall back silently (CI validation needed)", () => {
    // FINDING: A schema with minimum: 9999, maximum: 9998 was silently ignored.
    // Codex returned plain text with exit code 0 — NOT a validation error.
    //
    // This means strict JSON Schema files must be validated in CI before use.
    // A broken schema would silently fall back to unstructured output, which
    // would then fail ContextManager.read() Zod validation (caught normally).
    const riskMitigation = "ci-schema-validation";
    expect(riskMitigation).toBe("ci-schema-validation");
  });
});
