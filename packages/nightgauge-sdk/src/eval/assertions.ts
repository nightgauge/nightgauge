/**
 * Cross-Model Skill Evaluation Harness — assertion engine.
 *
 * Pure functions that evaluate a model's output against a scenario's
 * assertions. No I/O, no side effects — fully deterministic and unit-testable.
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 * @see docs/SKILL_EVALUATION.md - assertion reference
 */

import type { AssertionFailure, EvalAssertion } from "./schemas.js";

/** The raw output a runner produces for one matrix cell. */
export interface ModelOutput {
  /** The model's textual output (stdout). */
  text: string;
  /** Process exit code, when known (live mode / mock-supplied). */
  exit_code?: number;
}

/** Aggregate result of evaluating all assertions for one cell. */
export interface AssertionEvaluation {
  passed: boolean;
  failures: AssertionFailure[];
}

/**
 * Resolve a dot/bracket JSON path against a parsed object.
 * Supports `a.b.c` and `a.b[0].c`. Returns `undefined` if any segment is
 * missing. A resolved value of `null` is considered present (path exists).
 */
function resolveJsonPath(root: unknown, path: string): unknown {
  // Normalize bracket indices to dot segments: a.b[0].c -> a.b.0.c
  const segments = path
    .replace(/\[(\d+)\]/g, ".$1")
    .split(".")
    .filter((s) => s.length > 0);

  let current: unknown = root;
  for (const segment of segments) {
    if (current === null || current === undefined) return undefined;
    if (typeof current !== "object") return undefined;
    current = (current as Record<string, unknown>)[segment];
  }
  return current;
}

/**
 * Extract the first balanced JSON object or array from a text blob. Models
 * often wrap JSON in prose or fences, so we scan for the first `{`/`[` and
 * walk to its matching close, respecting string literals and escapes.
 * Returns the parsed value, or `undefined` if no valid JSON is found.
 */
function extractJson(text: string): unknown {
  const start = text.search(/[{[]/);
  if (start < 0) return undefined;

  const open = text[start];
  const close = open === "{" ? "}" : "]";
  let depth = 0;
  let inString = false;
  let escaped = false;

  for (let i = start; i < text.length; i++) {
    const ch = text[i];
    if (inString) {
      if (escaped) {
        escaped = false;
      } else if (ch === "\\") {
        escaped = true;
      } else if (ch === '"') {
        inString = false;
      }
      continue;
    }
    if (ch === '"') {
      inString = true;
    } else if (ch === open) {
      depth++;
    } else if (ch === close) {
      depth--;
      if (depth === 0) {
        const candidate = text.slice(start, i + 1);
        try {
          return JSON.parse(candidate);
        } catch {
          return undefined;
        }
      }
    }
  }
  return undefined;
}

/** Truncate evidence strings so JSONL records stay compact. */
function clip(value: string, max = 120): string {
  return value.length <= max ? value : `${value.slice(0, max)}…`;
}

/** Evaluate a single assertion. Returns a failure, or `null` on pass. */
function evaluateAssertion(output: ModelOutput, assertion: EvalAssertion): AssertionFailure | null {
  switch (assertion.type) {
    case "contains": {
      const haystack = assertion.ignore_case ? output.text.toLowerCase() : output.text;
      const needle = assertion.ignore_case ? assertion.value.toLowerCase() : assertion.value;
      if (haystack.includes(needle)) return null;
      return {
        type: assertion.type,
        reason: `output does not contain "${clip(assertion.value)}"`,
        expected: clip(assertion.value),
      };
    }

    case "not_contains": {
      const haystack = assertion.ignore_case ? output.text.toLowerCase() : output.text;
      const needle = assertion.ignore_case ? assertion.value.toLowerCase() : assertion.value;
      if (!haystack.includes(needle)) return null;
      return {
        type: assertion.type,
        reason: `output contains forbidden "${clip(assertion.value)}"`,
        expected: `absence of "${clip(assertion.value)}"`,
      };
    }

    case "matches_regex": {
      let re: RegExp;
      try {
        re = new RegExp(assertion.pattern, assertion.flags);
      } catch (err) {
        return {
          type: assertion.type,
          reason: `invalid regex /${assertion.pattern}/${assertion.flags ?? ""}: ${
            (err as Error).message
          }`,
          expected: assertion.pattern,
        };
      }
      if (re.test(output.text)) return null;
      return {
        type: assertion.type,
        reason: `output does not match /${clip(assertion.pattern)}/${assertion.flags ?? ""}`,
        expected: assertion.pattern,
      };
    }

    case "json_path_exists": {
      const parsed = extractJson(output.text);
      if (parsed === undefined) {
        return {
          type: assertion.type,
          reason: `no parseable JSON found in output (path "${assertion.path}")`,
          expected: assertion.path,
        };
      }
      const resolved = resolveJsonPath(parsed, assertion.path);
      if (resolved !== undefined) return null;
      return {
        type: assertion.type,
        reason: `JSON path "${assertion.path}" not present in output`,
        expected: assertion.path,
      };
    }

    case "exit_code": {
      if (output.exit_code === assertion.value) return null;
      return {
        type: assertion.type,
        reason: `exit code ${output.exit_code ?? "unknown"} != expected ${assertion.value}`,
        expected: String(assertion.value),
      };
    }
  }
}

/**
 * Evaluate all assertions for one cell. ALL assertions must pass for the cell
 * to pass; every failing assertion is captured as evidence.
 */
export function evaluateAssertions(
  output: ModelOutput,
  assertions: EvalAssertion[]
): AssertionEvaluation {
  const failures: AssertionFailure[] = [];
  for (const assertion of assertions) {
    const failure = evaluateAssertion(output, assertion);
    if (failure) failures.push(failure);
  }
  return { passed: failures.length === 0, failures };
}

// Internal helpers exported for focused unit tests.
export const __testing = { resolveJsonPath, extractJson };
