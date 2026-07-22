/**
 * Tool call argument sanitizer for JSONL persistence and in-memory retention
 * (Issue #1004 — original; memory-cap additions for preventing ext-host RSS
 * growth on long pipelines).
 *
 * The raw `input` object passed to a tool_use block can be arbitrarily large:
 *   - `Edit({file_path, old_string, new_string})` — strings can be entire files
 *   - `Write({file_path, content})` — `content` can be hundreds of KB
 *   - Nested structures from custom tools that pass `{items: [...]}` etc.
 *
 * Since `HeadlessOrchestrator.accumulatedToolCalls` retains every record for
 * the duration of a pipeline run, unbounded args directly pin the extension
 * host's heap — at 1k+ tool calls in a long `feature-dev`, the aggregate can
 * cross hundreds of MB. This module enforces three bounds:
 *   1. Individual string values are truncated at `MAX_ARG_VALUE_LENGTH`.
 *   2. Arrays keep only the first 10 items (plus a tail summary).
 *   3. After recursive sanitization, the serialized result is checked against
 *      `MAX_SERIALIZED_BYTES`; if still too big, args collapse to a size-only
 *      summary that preserves key names for debuggability.
 */

const SENSITIVE_KEYS_PATTERN = /token|secret|key|password|auth|credential/i;

/** Per-string value truncation cap. */
export const MAX_ARG_VALUE_LENGTH = 200;

/**
 * Total serialized-size budget for a single tool call's args after per-value
 * truncation. At 1,000 tool calls × 2 KB/record = ~2 MB total in-memory
 * retention — a useful debug surface without the multi-GB risk.
 */
export const MAX_SERIALIZED_BYTES = 2048;

/** Recursion depth limit for sanitizeValue — caps pathological nesting. */
const MAX_DEPTH = 6;

function sanitizeValue(v: unknown, depth = 0): unknown {
  if (depth > MAX_DEPTH) return "[DEPTH_LIMIT]";
  if (typeof v === "string") {
    return v.length > MAX_ARG_VALUE_LENGTH ? v.substring(0, MAX_ARG_VALUE_LENGTH) + "…" : v;
  }
  if (Array.isArray(v)) {
    const slice = v.slice(0, 10).map((item) => sanitizeValue(item, depth + 1));
    if (v.length > 10) slice.push(`…+${v.length - 10} more`);
    return slice;
  }
  if (v && typeof v === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
      out[k] = SENSITIVE_KEYS_PATTERN.test(k) ? "[REDACTED]" : sanitizeValue(val, depth + 1);
    }
    return out;
  }
  return v;
}

/**
 * Sanitize tool call args for in-memory retention and JSONL persistence.
 *
 * Returns either the sanitized args (shape-preserving, with strings and
 * arrays truncated) or a small size summary when the sanitized form still
 * exceeds `MAX_SERIALIZED_BYTES`.
 */
export function sanitizeToolCallArgs(
  args: Record<string, unknown> | undefined
): Record<string, unknown> | undefined {
  if (!args) return undefined;
  const sanitized: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(args)) {
    sanitized[k] = SENSITIVE_KEYS_PATTERN.test(k) ? "[REDACTED]" : sanitizeValue(v);
  }
  try {
    const serialized = JSON.stringify(sanitized);
    if (serialized.length > MAX_SERIALIZED_BYTES) {
      return {
        _truncated: true,
        keys: Object.keys(sanitized),
        approx_bytes: serialized.length,
      };
    }
  } catch {
    return { _truncated: true, reason: "serialization_failed" };
  }
  return sanitized;
}
