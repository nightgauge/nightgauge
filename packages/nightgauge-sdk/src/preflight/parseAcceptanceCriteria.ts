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

/**
 * Extract Markdown acceptance-criterion checkboxes from an issue body.
 *
 * Returns an ordered array preserving the position of each checkbox in
 * the body. The text is trimmed; the state is normalized to
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
  let index = 0;
  for (const rawLine of body.split(/\r?\n/)) {
    const match = CHECKBOX_RE.exec(rawLine.trim());
    if (!match) continue;
    const stateChar = match[1];
    const text = match[2].trim();
    if (text.length === 0) continue;
    out.push({
      index,
      text,
      checkbox_state: stateChar === " " ? "unchecked" : "checked",
    });
    index += 1;
  }
  return out;
}
