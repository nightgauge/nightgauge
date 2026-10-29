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
 * Top-level numbered list item (`1.` or `1)`), indented at most three spaces
 * so a nested sub-step is not a criterion of its own. Matched against the
 * untrimmed line; a criterion only inside an AC section.
 */
const NUMBERED_RE = /^ {0,3}\d{1,3}[.)][ \t]+(.{1,4096})$/;

/**
 * Code-fence open/close line. Lines inside a fence are example text: a `#`
 * comment is not a heading and a `1.` line is not a criterion.
 */
const FENCE_RE = /^[ \t]*(`{3,}|~{3,})/;

/** Optional `[ ]` / `[x]` at the start of a numbered item's text. */
const LEADING_BOX_RE = /^\[([ xX])\][ \t]+(.+)$/;

/**
 * ATX Markdown heading: level marks and text. The text capture is greedy and
 * unanchored at the end so no two quantifiers overlap; a closing `#` run is
 * irrelevant to the "acceptance criteria" substring test below.
 */
const HEADING_RE = /^ {0,3}(#{1,6})[ \t]+(.*)$/;

/**
 * Collapsed `<details>` block whose opening tag starts a line — where issue
 * refinement archives the original body. An inline `<details>` mention does
 * not start a block.
 */
const DETAILS_BLOCK_RE = /^[ \t]*<details\b[\s\S]*?<\/details>/gim;

/**
 * Extract acceptance criteria from an issue body.
 *
 * Mirrors the authoritative Go parser (`parseACs` in
 * `internal/preflight/ac_reconcile.go`): checkbox bullets anywhere, plus
 * numbered items inside an "Acceptance Criteria" heading section, in
 * document order. `<details>` blocks and fenced code blocks are ignored, and
 * repeated criteria
 * (whitespace-collapsed text) are dropped.
 *
 * State is informational — the reconciler verifies satisfaction against
 * `main`, not the box state. A numbered item without a box is unchecked.
 */
export function parseAcceptanceCriteria(body: string): AcceptanceCriterion[] {
  if (typeof body !== "string" || body.length === 0) {
    return [];
  }

  const out: AcceptanceCriterion[] = [];
  const seen = new Set<string>();
  let sectionLevel = 0;
  let inFence = false;
  for (const rawLine of body.replace(DETAILS_BLOCK_RE, "").split(/\r?\n/)) {
    if (FENCE_RE.test(rawLine)) {
      inFence = !inFence;
      continue;
    }
    if (inFence) continue;
    const heading = HEADING_RE.exec(rawLine);
    if (heading) {
      const level = heading[1].length;
      if (heading[2].toLowerCase().includes("acceptance criteria")) {
        sectionLevel = level;
      } else if (sectionLevel > 0 && level <= sectionLevel) {
        sectionLevel = 0;
      }
      continue;
    }
    const line = rawLine.trim();
    let box: string;
    let text: string;
    const checkbox = CHECKBOX_RE.exec(line);
    if (checkbox) {
      box = checkbox[1];
      text = checkbox[2];
    } else if (sectionLevel > 0) {
      const numbered = NUMBERED_RE.exec(rawLine.trimEnd());
      if (!numbered) continue;
      box = " ";
      text = numbered[1];
      const lead = LEADING_BOX_RE.exec(text);
      if (lead) {
        box = lead[1];
        text = lead[2];
      }
    } else {
      continue;
    }
    text = text.trim();
    if (text.length === 0) continue;
    const key = text.split(/\s+/).join(" ");
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({
      index: out.length,
      text,
      checkbox_state: box === " " ? "unchecked" : "checked",
    });
  }
  return out;
}
