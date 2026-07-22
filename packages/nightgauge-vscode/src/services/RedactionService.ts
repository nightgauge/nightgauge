/**
 * RedactionService — PII / sensitive-data scrubbing for telemetry uploads.
 *
 * Runs over every {@link AnalyticsEvent} payload before
 * `IpcClient.platformSubmitAnalytics()` transmits it to the platform
 * analytics API. Pipeline records are designed to be metadata-only (see
 * `utils/telemetryEventBuilder.ts`), but that rule was enforced only by
 * reviewer discipline. This service makes it enforceable in code: any
 * future change that accidentally leaks user content fails the
 * hard-guarantee unit test and is stripped at runtime as
 * defense-in-depth.
 *
 * Semantics differ intentionally from the in-process
 * `utils/toolCallSanitizer.ts`:
 *
 * | Surface         | Sanitizer                 | Match style         | Action on match                  |
 * | --------------- | ------------------------- | ------------------- | -------------------------------- |
 * | In-process      | `toolCallSanitizer`       | substring (token in name) | replace value with `[REDACTED]` |
 * | Network upload  | `RedactionService` (this) | anchored (canonical names only) | drop the field entirely |
 *
 * The drop-field semantics (vs redact-value) is the stricter rule
 * appropriate for the network surface — if the field is present in the
 * upload, it is not a known sensitive name. See ADR-001 in
 * `.nightgauge/knowledge/features/3326-build-pii-redaction-layer-for-telemetry-uploads/decisions.md`.
 *
 * @see Issue #3326 - Build PII redaction layer for telemetry uploads
 * @see utils/toolCallSanitizer.ts — sister sanitizer for the in-process retention surface
 */

import type { AnalyticsEvent } from "../platform/types.js";

/**
 * Anchored regex that matches the canonical sensitive key names. Anchored
 * (not substring) to avoid silently dropping legitimate metric fields like
 * `auth_attempts` or `api_key_count`. See ADR-003.
 */
const SECRET_KEY_PATTERN = /^(api_?key|token|password|ssh_?key|secret|credential|auth)$/i;

/** Prefix that marks a debug-only field for unconditional removal. */
const DEBUG_FIELD_PREFIX = "_debug_";

/** Default per-string truncation cap. Mirrors `toolCallSanitizer.MAX_ARG_VALUE_LENGTH`. */
const DEFAULT_MAX_STRING_LENGTH = 200;

/**
 * Recursion depth limit measured in nested-container levels. Slightly higher
 * than `toolCallSanitizer` to fit realistic per-stage telemetry payload shapes
 * (which can include nested stage→model→token structures) while still bounding
 * pathological nesting.
 */
const MAX_DEPTH = 8;

interface Counters {
  fieldsRemoved: number;
  fieldsRedacted: number;
}

/** Result of a single `redact()` call. */
export interface RedactionResult {
  /** The sanitized event. `eventType` and `timestamp` are pass-through. */
  event: AnalyticsEvent;
  /** Number of fields dropped (debug prefix or secret-key match) at any depth. */
  fieldsRemoved: number;
  /** Number of string values truncated at any depth. */
  fieldsRedacted: number;
}

export interface RedactionServiceOptions {
  /** Maximum length for string values before truncation. Default 200. */
  maxStringLength?: number;
}

/**
 * Stateless redactor for outbound `AnalyticsEvent` payloads.
 *
 * Constructor accepts options but holds no mutable state across `redact()`
 * calls. Instances are safe to share. Counters are per-call and returned
 * in the `RedactionResult`; the service does not aggregate.
 */
export class RedactionService {
  private readonly maxStringLength: number;

  constructor(opts?: RedactionServiceOptions) {
    this.maxStringLength = opts?.maxStringLength ?? DEFAULT_MAX_STRING_LENGTH;
  }

  /**
   * Scrub `event.payload` recursively. `eventType` and `timestamp` are
   * pass-through verbatim.
   */
  redact(event: AnalyticsEvent): RedactionResult {
    const counters: Counters = { fieldsRemoved: 0, fieldsRedacted: 0 };
    const sanitizedPayload =
      event.payload === undefined
        ? undefined
        : (this.scrubValue(event.payload, 0, counters) as Record<string, unknown>);

    return {
      event: {
        eventType: event.eventType,
        payload: sanitizedPayload,
        timestamp: event.timestamp,
      },
      fieldsRemoved: counters.fieldsRemoved,
      fieldsRedacted: counters.fieldsRedacted,
    };
  }

  /**
   * Recursively scrub a value. `depth` counts nested-container levels:
   * the top-level payload is depth 0, its immediate children are depth 1,
   * and so on. At `depth > MAX_DEPTH` the value is replaced with the
   * `[DEPTH_LIMIT]` sentinel to bound pathological nesting.
   */
  private scrubValue(value: unknown, depth: number, counters: Counters): unknown {
    if (depth > MAX_DEPTH) {
      return "[DEPTH_LIMIT]";
    }
    if (typeof value === "string") {
      if (value.length > this.maxStringLength) {
        counters.fieldsRedacted++;
        return value.substring(0, this.maxStringLength) + "…";
      }
      return value;
    }
    if (Array.isArray(value)) {
      return value.map((item) => this.scrubValue(item, depth + 1, counters));
    }
    if (value && typeof value === "object") {
      const obj = value as Record<string, unknown>;
      const out: Record<string, unknown> = {};
      for (const [key, val] of Object.entries(obj)) {
        if (key.startsWith(DEBUG_FIELD_PREFIX) || SECRET_KEY_PATTERN.test(key)) {
          counters.fieldsRemoved++;
          continue;
        }
        out[key] = this.scrubValue(val, depth + 1, counters);
      }
      return out;
    }
    return value;
  }
}
