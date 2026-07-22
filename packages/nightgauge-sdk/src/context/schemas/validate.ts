import { z } from "zod";
import { PipelineFeedbackSchema } from "./feedback.js";
import { flexEnum, optionalString } from "./helpers.js";

/**
 * Schema for validate-{N}.json context files
 *
 * Created by: /nightgauge-feature-validate
 * Read by: /nightgauge-pr-create (optional)
 *
 * Schema versions:
 * - 1.0: Initial schema
 * - 1.1: Added build object
 * - 1.2: Added dead_code_warnings array, unit_tests object
 * - 1.3: Added preexisting_failures array (issue #836)
 * - 1.4: Added skipped_phases array (issue #861)
 * - 1.5: Added ac_completion_check object (type:docs AC gate)
 * - 1.6: Added optional feedback field for backward pipeline signals (issue #1341)
 * - 1.7: Added terminal_output and orphaned_producer dead code warning types (issue #1405)
 * - 1.8: Added gate_metrics array for per-gate telemetry handoff (issue #1412)
 * - 1.9: Added commit_sha — commit now happens in feature-validate after validation passes (issue #1608)
 * - 2.0: Added selective_test_metrics optional field for selective testing handoff (issue #1975)
 * - 2.1: Added minimum_duration_check, build.duration_ms, build.exit_code, errorCategory (issue #3041)
 * - 2.2: Added coverage_map_path for PRD AC coverage check results (issue #3595)
 * - 2.3: Added mobile_mcp object for agent-driven mobile-mcp E2E results (issue #24)
 * - 2.4: Added verify_ui object for the browser-driven web UI verification gate (issue #4193)
 *
 * @see docs/CONTEXT_ARCHITECTURE.md for field documentation
 */

// Coerce number → boolean: agents sometimes write 0/1 or test counts instead of booleans
const coerceBool = z.preprocess((val) => {
  if (typeof val === "number") return val !== 0;
  return val;
}, z.boolean().nullish());

const TestResultSchema = z.object({
  ran: coerceBool,
  passed: coerceBool,
  framework: z.string().nullish(),
  tests_run: z.number().int().min(0).nullish(),
  tests_passed: z.number().int().min(0).nullish(),
});

/**
 * Schema for dead code and integration warnings.
 *
 * Valid `type` values:
 * - "unused-export" — exported symbol not imported anywhere
 * - "unregistered-command" — VSCode command in package.json without registerCommand
 * - "missing-arg-validation" — command handler lacks argument type checks
 * - "terminal_output" — function return value called but result discarded/never consumed
 * - "orphaned_producer" — event emitter/data writer with no subscribers/readers
 */
const DeadCodeWarningSchema = z.object({
  type: z.string().min(1),
  name: z.string().min(1),
  location: z.string().min(1),
  severity: z.enum(["error", "warning", "info"]),
});

/**
 * A test file that failed both on the feature branch and on main.
 * Written by feature-validate Phase 1.7.2 when baseline comparison confirms
 * a failure is pre-existing (not introduced by the current change).
 *
 * Field contract (mirrors SKILL.md Phase 1.7.3):
 *   test_file        — relative path to the failing test file (non-empty)
 *   failure_count    — number of failing test cases in this file (≥ 1)
 *   baseline_verified — true = also fails on main branch (pre-existing);
 *                       agents sometimes write 0/1, so coerceBool is used
 */
const PreexistingFailureSchema = z.object({
  test_file: z.string().min(1),
  failure_count: z.number().int().min(1),
  baseline_verified: z.preprocess((val) => {
    if (typeof val === "number") return val !== 0;
    return val;
  }, z.boolean()),
});

const SkippedPhaseSchema = z.object({
  phase: z.string().min(1),
  reason: z.string().min(1),
});

/**
 * Per-gate telemetry entry for skill→TS handoff (v1.8, Issue #1412).
 * The canonical long-term store is gate-metrics.jsonl; this is ephemeral per-run.
 */
const GateMetricEntrySchema = z.object({
  gate_name: z.string().min(1),
  result: z.enum(["pass", "catch"]),
  duration_ms: z.number().int().min(0).nullish(),
});

/**
 * Result of a single mobile-mcp spec run (v2.3, Issue #24).
 *
 * Mirrors the per-spec result format published by the acmesvc-tracker repo
 * (test/mobile_mcp/README.md#result-format): each assertion carries a stable
 * `id`, a `status` of pass|fail, and the observed `actual` value. `notes`
 * captures spec-level context. Object is `.passthrough()` so the contract can
 * add fields without breaking validation here.
 */
const MobileMcpAssertionSchema = z
  .object({
    id: z.string().min(1),
    status: z.enum(["pass", "fail"]),
    actual: optionalString(),
  })
  .passthrough();

const MobileMcpSpecResultSchema = z
  .object({
    spec: z.string().min(1),
    platform: optionalString(),
    device: optionalString(),
    status: z.enum(["pass", "fail", "error"]),
    assertions: z.array(MobileMcpAssertionSchema).nullish(),
    screenshots: z.array(z.string()).nullish(),
    notes: optionalString(),
    error: optionalString(),
  })
  .passthrough();

/**
 * Mobile-mcp E2E validation block (v2.3, Issue #24).
 *
 * Written by feature-validate Phase 2.4 when test/mobile_mcp/specs/ contains
 * runnable specs. Agent-driven: Claude reads each spec and drives the debug APK
 * on the Pixel_9_Pro emulator via the mobile-mcp MCP server. Consumed by
 * pr-create to attach screenshot evidence to the PR body.
 *
 * `null` when the phase never produced a block (e.g. SKIP_TO_PHASE bypassed it).
 * When the phase runs but does not execute specs, `ran` is false and
 * `skipped_reason` explains why (config skip, no specs, build/emulator failure).
 */
const MobileMcpSchema = z.object({
  ran: coerceBool,
  passed: coerceBool,
  specs_run: z.number().int().min(0).nullish(),
  specs_passed: z.number().int().min(0).nullish(),
  specs_failed: z.number().int().min(0).nullish(),
  results: z.array(MobileMcpSpecResultSchema).nullish(),
  evidence_dir: optionalString(),
  skipped_reason: optionalString(),
});

/**
 * A single verify-ui flow step result (v2.4, Issue #4193). Mirrors
 * skills/nightgauge-verify-ui/SKILL.md's report.json step shape.
 * `.passthrough()` so the flow can add fields without breaking validation.
 */
const VerifyUiStepResultSchema = z
  .object({
    n: z.number().int().min(1).nullish(),
    name: z.string().min(1).nullish(),
    status: z.enum(["passed", "failed"]).nullish(),
    screenshot: optionalString(),
    new_console_errors: z.array(z.string()).nullish(),
  })
  .passthrough();

/**
 * Core Web Vitals measured for the flow's primary page load (v2.4, Issue
 * #4193). `budget` is the optional configured budget (null when unset);
 * `budget_exceeded` is only meaningful when a budget was configured.
 */
const VerifyUiWebVitalsSchema = z
  .object({
    lcp_ms: z.number().nullish(),
    cls: z.number().nullish(),
    ttfb_ms: z.number().nullish(),
    budget: z.record(z.string(), z.number()).nullish(),
    budget_exceeded: z.boolean().nullish(),
  })
  .passthrough();

/**
 * verify-ui's report.json, as written by that skill and read back by
 * feature-validate Phase 2.45 (v2.4, Issue #4193).
 */
const VerifyUiReportSchema = z
  .object({
    flow: z.string().nullish(),
    base_url: optionalString(),
    status: z.enum(["passed", "failed", "error"]).nullish(),
    steps: z.array(VerifyUiStepResultSchema).nullish(),
    web_vitals: VerifyUiWebVitalsSchema.nullish(),
    artifacts_dir: optionalString(),
    error: optionalString(),
  })
  .passthrough();

/**
 * Web UI verification gate block (v2.4, Issue #4193).
 *
 * Written by feature-validate Phase 2.45 when the diff touches UI-bearing
 * frontend surface (deterministic classifier, not LLM-judged) AND a
 * verify-ui flow is registered for the repo. `null` when the phase never
 * produced a block (e.g. SKIP_TO_PHASE bypassed it). When the phase runs but
 * does not execute a flow (surface not UI-relevant, or UI-relevant but no
 * flow registered), `ran` is false and `skipped_reason` explains why — never
 * a silent pass.
 */
const VerifyUiSchema = z.object({
  ran: coerceBool,
  passed: coerceBool,
  repo: optionalString(),
  flow: optionalString(),
  report: VerifyUiReportSchema.nullish(),
  artifacts_dir: optionalString(),
  skipped_reason: optionalString(),
});

export const ValidateContextSchema = z
  .object({
    schema_version: z.string().regex(/^\d+\.\d+$/),
    issue_number: z.number().int().positive(),
    validation_status: flexEnum(["passed", "failed", "partial", "skipped"] as const).nullish(),
    /**
     * Explicit failure category for hard-gate failures (v2.1, Issue #3041).
     *
     * MUST stay a superset of every `ERROR_CATEGORY="..."` the feature-validate
     * skill emits (`skills/nightgauge-feature-validate/_includes/`:
     * build-and-tests.md, verify-ui-gate.md). When the skill gains a new
     * category, add it here in the SAME change — a value the skill emits but
     * this enum omits fails the parse and the whole validation-failure signal
     * is silently dropped as a "non-fatal schema mismatch", which is how a
     * failed validation once slipped through to pr-create. Kept as a strict
     * `z.enum` (NOT flexEnum) on purpose: flexEnum normalizes hyphens to
     * underscores, which would corrupt these hyphenated values on the wire for
     * the Go consumers that match them literally (e.g. failure taxonomy).
     */
    errorCategory: z
      .enum([
        "build-failed",
        "tests-failed",
        "integration-failed",
        "dead-code-blocked",
        "mobile-apk-build-failed",
        "mobile-mcp-tests-failed",
        "verify-ui-gate-failed",
      ])
      .nullish(),
    build: z
      .object({
        ran: z.boolean().nullish(),
        passed: z.boolean().nullish(),
        command: z.string().nullish(),
        /** Build execution time in milliseconds (v2.1, Issue #3041) */
        duration_ms: z.number().int().min(0).nullish(),
        /** Build process exit code (v2.1, Issue #3041) */
        exit_code: z.number().int().nullish(),
      })
      .nullish(),
    /**
     * Minimum duration check result for detecting premature/skipped build/test (v2.1, Issue #3041).
     * Flagged when actual build time is below the p10 baseline, indicating the build
     * may not have actually run (LLM rubber stamp without deterministic execution).
     */
    minimum_duration_check: z
      .object({
        flagged: z.boolean(),
        actual_build_time_ms: z.number().int().min(0),
        p10_baseline_ms: z.number().int().min(0),
        warning: z.string().nullish(),
      })
      .nullish(),
    integration_tests: TestResultSchema.nullish(),
    unit_tests: TestResultSchema.nullish(),
    e2e_tests: z
      .object({
        ran: coerceBool,
        passed: coerceBool,
        framework: z.string().nullish(),
        reason: optionalString(),
      })
      .nullish(),
    dead_code_warnings: z.array(DeadCodeWarningSchema).nullish(),
    preexisting_failures: z.array(PreexistingFailureSchema).nullish(),
    skipped_phases: z.array(SkippedPhaseSchema).nullish(),
    /** AC completion gate result for type:docs issues (v1.5+) */
    ac_completion_check: z
      .object({
        status: flexEnum(["passed", "failed", "skipped", "not_applicable"] as const),
        checked_count: z.number().int().min(0).nullish(),
        unchecked_count: z.number().int().min(0).nullish(),
        applicable: z.boolean().nullish(),
      })
      .nullish(),
    // Agents write manual_checklist in many forms:
    //   - [{item: "...", verified: true}]  (canonical)
    //   - {"item1": true, "item2": false}  (record form)
    //   - ["item1", "item2"]               (plain strings)
    //   - [{description: "...", done: true}]  (alt keys)
    // Preprocess normalizes all to canonical form.
    manual_checklist: z.preprocess(
      (val) => {
        if (val === undefined || val === null) return val;
        // Plain string array → structured
        if (Array.isArray(val)) {
          return val.map((entry) => {
            if (typeof entry === "string") {
              return { item: entry, verified: false };
            }
            if (entry && typeof entry === "object") {
              const obj = entry as Record<string, unknown>;
              // Normalize alt keys: description→item, done/status/checked→verified
              const item = (obj.item ?? obj.description ?? obj.text ?? obj.name ?? "") as string;
              const verified = Boolean(
                obj.verified ?? obj.done ?? obj.status ?? obj.checked ?? false
              );
              return { item: String(item), verified };
            }
            return entry;
          });
        }
        // Record form: {"check X": true, "check Y": false}
        if (typeof val === "object") {
          return Object.entries(val as Record<string, unknown>).map(([key, value]) => ({
            item: key,
            verified: Boolean(value),
          }));
        }
        return val;
      },
      z
        .array(
          z.object({
            item: z.string(),
            verified: z.boolean(),
          })
        )
        .nullish()
    ),
    project_type: optionalString(),
    notes: z.preprocess(
      (v) => (typeof v === "string" && v.trim() === "" ? null : v),
      z.union([z.string().min(1), z.array(z.string())]).nullish()
    ),
    created_at: z.string().datetime().nullish(),
    /** Backward pipeline signals emitted during feature-validate (v1.6+) */
    feedback: PipelineFeedbackSchema.nullish(),
    /** Gate-level validation metrics for skill→TS handoff (v1.8, Issue #1412) */
    gate_metrics: z.array(GateMetricEntrySchema).nullish(),
    /** Commit SHA created during validation commit+push phase (v1.9, Issue #1608) */
    commit_sha: optionalString(),
    /**
     * Selective test metrics captured during validation (v2.0, Issue #1975).
     * Written by feature-validate when selective testing was used.
     * Consumed by EscapedDefectDetector for post-merge gap analysis.
     */
    selective_test_metrics: z
      .object({
        mode: z.enum(["selective", "full", "skipped"]),
        selected_tests: z.number().int().min(0),
        skipped_tests: z.number().int().min(0).nullish(),
        total_tests: z.number().int().min(0).nullish(),
        selected_test_files: z.array(z.string()),
        estimated_tokens_saved: z.number().min(0),
        estimated_time_saved_ms: z.number().min(0),
      })
      .nullish(),
    /** Path to coverage-map-{N}.json produced by Phase 2.6 knowledge coverage check (v2.2, Issue #3595) */
    coverage_map_path: z.string().nullish(),
    /** Agent-driven mobile-mcp E2E results (v2.3, Issue #24). null when the phase produced no block. */
    mobile_mcp: MobileMcpSchema.nullable().default(null),
    /** Browser-driven web UI verification gate result (v2.4, Issue #4193). null when the phase produced no block. */
    verify_ui: VerifyUiSchema.nullable().default(null),
  })
  // AI agents may include extra fields. passthrough() prevents
  // unknown properties from causing validation failures.
  .passthrough();

export type ValidateContext = z.infer<typeof ValidateContextSchema>;
export type DeadCodeWarning = z.infer<typeof DeadCodeWarningSchema>;
export type PreexistingFailure = z.infer<typeof PreexistingFailureSchema>;
export type SkippedPhase = z.infer<typeof SkippedPhaseSchema>;
export type MobileMcpResult = z.infer<typeof MobileMcpSchema>;
export type MobileMcpSpecResult = z.infer<typeof MobileMcpSpecResultSchema>;
export type VerifyUiResult = z.infer<typeof VerifyUiSchema>;
export type VerifyUiReport = z.infer<typeof VerifyUiReportSchema>;
