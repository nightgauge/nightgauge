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

/** One fenced code block: its info-string language (lower-cased) and body. */
interface FencedBlock {
  lang: string;
  body: string;
}

/**
 * Extract every CLOSED fenced code block (``` or ~~~, CommonMark-style: up to
 * three spaces of indent, a closing fence of the same character at least as
 * long as the opener). An unclosed block is not a block, so an answer cut off
 * mid-fence fails a scoped assertion instead of being judged on a fragment.
 */
function fencedBlocks(text: string): FencedBlock[] {
  const blocks: FencedBlock[] = [];
  let open: { ch: string; len: number; lang: string; body: string[] } | null = null;
  for (const line of text.split(/\r?\n/)) {
    if (!open) {
      const m = /^ {0,3}(`{3,}|~{3,})[ \t]*([^\s`]*)[^`]*$/.exec(line);
      if (m) open = { ch: m[1][0], len: m[1].length, lang: m[2].toLowerCase(), body: [] };
      continue;
    }
    const close = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(line);
    if (close && close[1][0] === open.ch && close[1].length >= open.len) {
      blocks.push({ lang: open.lang, body: open.body.join("\n") });
      open = null;
    } else {
      open.body.push(line);
    }
  }
  return blocks;
}

/**
 * Remove unquoted shell `#` comments (a `#` at the start of a word, outside
 * single/double quotes). Quote state carries across lines, so a multi-line
 * `--body "…# heading…"` argument is kept intact.
 */
function stripShellComments(src: string): string {
  let out = "";
  let quote: string | null = null;
  for (let i = 0; i < src.length; i++) {
    const ch = src[i];
    if (quote) {
      out += ch;
      if (ch === "\\" && quote === '"' && i + 1 < src.length) {
        out += src[++i];
      } else if (ch === quote) {
        quote = null;
      }
      continue;
    }
    if (ch === "\\" && i + 1 < src.length) {
      out += ch + src[++i];
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      out += ch;
      continue;
    }
    if (ch === "#" && (i === 0 || /\s/.test(src[i - 1]))) {
      while (i + 1 < src.length && src[i + 1] !== "\n") i++;
      continue;
    }
    out += ch;
  }
  return out;
}

interface ScopeOptions {
  scope?: "last_fenced_block";
  lang?: string | string[];
  strip_comments?: boolean;
}

function describeScope(a: ScopeOptions): string {
  if (!a.scope) return "output";
  const langs = a.lang === undefined ? [] : Array.isArray(a.lang) ? a.lang : [a.lang];
  return langs.length ? `last \`\`\`${langs.join("|")} block` : "last fenced block";
}

/**
 * Resolve the text an assertion reads. Unscoped → the whole output. Scoped →
 * the last matching fenced block, or a failure reason when there is none
 * (fail closed: a missing block never lets an assertion pass).
 */
function scopedText(output: ModelOutput, a: ScopeOptions): { text: string } | { missing: string } {
  if (!a.scope) return { text: output.text };
  const langs =
    a.lang === undefined
      ? undefined
      : (Array.isArray(a.lang) ? a.lang : [a.lang]).map((l) => l.toLowerCase());
  const blocks = fencedBlocks(output.text).filter((b) => !langs || langs.includes(b.lang));
  const last = blocks[blocks.length - 1];
  if (!last) return { missing: `no ${describeScope(a)} found in output` };
  return { text: a.strip_comments ? stripShellComments(last.body) : last.body };
}

/** Parse the JSON an assertion reads: scoped → whole block body; unscoped → first balanced JSON. */
function scopedJson(
  output: ModelOutput,
  a: ScopeOptions
): { value: unknown } | { missing: string } {
  const scoped = scopedText(output, a);
  if ("missing" in scoped) return scoped;
  if (!a.scope) {
    const parsed = extractJson(scoped.text);
    return parsed === undefined
      ? { missing: "no parseable JSON found in output" }
      : { value: parsed };
  }
  try {
    return { value: JSON.parse(scoped.text) };
  } catch (err) {
    return { missing: `${describeScope(a)} is not valid JSON: ${(err as Error).message}` };
  }
}

/** Evaluate a single assertion. Returns a failure, or `null` on pass. */
function evaluateAssertion(output: ModelOutput, assertion: EvalAssertion): AssertionFailure | null {
  if (assertion.type === "exit_code") {
    if (output.exit_code === assertion.value) return null;
    return {
      type: assertion.type,
      reason: `exit code ${output.exit_code ?? "unknown"} != expected ${assertion.value}`,
      expected: String(assertion.value),
    };
  }

  const where = describeScope(assertion);

  switch (assertion.type) {
    case "contains":
    case "not_contains": {
      const scoped = scopedText(output, assertion);
      if ("missing" in scoped) {
        return { type: assertion.type, reason: scoped.missing, expected: clip(assertion.value) };
      }
      const haystack = assertion.ignore_case ? scoped.text.toLowerCase() : scoped.text;
      const needle = assertion.ignore_case ? assertion.value.toLowerCase() : assertion.value;
      const found = haystack.includes(needle);
      if (assertion.type === "contains") {
        if (found) return null;
        return {
          type: assertion.type,
          reason: `${where} does not contain "${clip(assertion.value)}"`,
          expected: clip(assertion.value),
        };
      }
      if (!found) return null;
      return {
        type: assertion.type,
        reason: `${where} contains forbidden "${clip(assertion.value)}"`,
        expected: `absence of "${clip(assertion.value)}"`,
      };
    }

    case "matches_regex":
    case "not_matches_regex": {
      const flags = assertion.flags ?? "";
      let re: RegExp;
      try {
        re = new RegExp(assertion.pattern, assertion.flags);
      } catch (err) {
        return {
          type: assertion.type,
          reason: `invalid regex /${assertion.pattern}/${flags}: ${(err as Error).message}`,
          expected: assertion.pattern,
        };
      }
      const scoped = scopedText(output, assertion);
      if ("missing" in scoped) {
        return { type: assertion.type, reason: scoped.missing, expected: assertion.pattern };
      }
      const match = re.exec(scoped.text);
      if (assertion.type === "matches_regex") {
        if (match) return null;
        return {
          type: assertion.type,
          reason: `${where} does not match /${clip(assertion.pattern)}/${flags}`,
          expected: assertion.pattern,
        };
      }
      if (!match) return null;
      return {
        type: assertion.type,
        reason: `${where} matches forbidden /${clip(assertion.pattern)}/${flags} at "${clip(match[0])}"`,
        expected: `no match for /${clip(assertion.pattern)}/${flags}`,
      };
    }

    case "json_path_exists":
    case "json_path_equals": {
      const parsed = scopedJson(output, assertion);
      if ("missing" in parsed) {
        return {
          type: assertion.type,
          reason: `${parsed.missing} (path "${assertion.path}")`,
          expected: assertion.path,
        };
      }
      const resolved = resolveJsonPath(parsed.value, assertion.path);
      if (assertion.type === "json_path_exists") {
        if (resolved !== undefined) return null;
        return {
          type: assertion.type,
          reason: `JSON path "${assertion.path}" not present in ${where}`,
          expected: assertion.path,
        };
      }
      if (resolved === assertion.value) return null;
      return {
        type: assertion.type,
        reason: `JSON path "${assertion.path}" in ${where} is ${clip(
          JSON.stringify(resolved) ?? "undefined"
        )}, expected ${JSON.stringify(assertion.value)}`,
        expected: `${assertion.path} == ${JSON.stringify(assertion.value)}`,
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
export const __testing = { resolveJsonPath, extractJson, fencedBlocks, stripShellComments };
