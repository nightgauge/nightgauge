import type { AcceptanceCriterion } from "./types.js";

/**
 * Markdown checkbox pattern.
 *
 * Accepts both `-` and `*` bullet markers, any leading indentation
 * (allows nested checkboxes), and `[ ]`, `[x]`, or `[X]` states.
 *
 * The text capture is bounded to 4096 chars as a defensive measure
 * against pathological input.
 */
const CHECKBOX_RE = /^[-*][ \t]+\[([ xX])\][ \t]+(.{1,4096})$/;

/** An opening or closing `<details>` tag (twin of Go's detailsTagRE). */
const DETAILS_TAG_RE = /<(\/?)details\b[^>]*>/gi;

/**
 * Remove every `<details>` block, where issue refinement archives the
 * original body; its checkboxes are not live criteria (#2655). Nesting is
 * tracked so an archive holding an archive is removed whole, and an
 * unclosed `<details>` drops everything after it.
 */
function stripDetails(body: string): string {
  let out = "";
  let depth = 0;
  let last = 0;
  for (const m of body.matchAll(DETAILS_TAG_RE)) {
    const start = m.index ?? 0;
    if (m[1] === "") {
      if (depth === 0) out += body.slice(last, start);
      depth += 1;
    } else if (depth > 0) {
      depth -= 1;
      if (depth === 0) last = start + m[0].length;
    }
  }
  return depth === 0 ? out + body.slice(last) : out;
}

/**
 * Extract Markdown acceptance-criterion checkboxes from an issue body.
 *
 * Returns an ordered array preserving the position of each checkbox in
 * the body, skipping `<details>` blocks and criteria whose
 * whitespace-normalised text repeats an earlier one (#2655), so indices
 * match the Go parser's. The text is trimmed; the state is normalized to
 * `"checked"` (any of `x`, `X`) or `"unchecked"` (a single space).
 *
 * Checkbox state is informational — the reconciler verifies satisfaction
 * against `main`, not the box state.
 */
export function parseAcceptanceCriteria(body: string): AcceptanceCriterion[] {
  if (typeof body !== "string" || body.length === 0) {
    return [];
  }

  const out: AcceptanceCriterion[] = [];
  const seen = new Set<string>();
  let index = 0;
  for (const rawLine of stripDetails(body).split(/\r?\n/)) {
    const match = CHECKBOX_RE.exec(rawLine.trim());
    if (!match) continue;
    const stateChar = match[1];
    const text = match[2].trim();
    if (text.length === 0) continue;
    const key = text.split(/\s+/).join(" ");
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({
      index,
      text,
      checkbox_state: stateChar === " " ? "unchecked" : "checked",
    });
    index += 1;
  }
  return out;
}
