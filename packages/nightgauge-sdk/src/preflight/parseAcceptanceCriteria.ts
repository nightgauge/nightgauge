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

/** Numbered list item (`1.` or `1)`); a criterion only inside an AC section. */
const NUMBERED_RE = /^\d{1,3}[.)][ \t]+(.{1,4096})$/;

/** Optional `[ ]` / `[x]` at the start of a numbered item's text. */
const LEADING_BOX_RE = /^\[([ xX])\][ \t]+(.+)$/;

/**
 * ATX Markdown heading: level marks and text. The text capture is greedy and
 * unanchored at the end so no two quantifiers overlap; a closing `#` run is
 * irrelevant to the "acceptance criteria" substring test below.
 */
const HEADING_RE = /^ {0,3}(#{1,6})[ \t]+(.*)$/;

/** Line opening a collapsed `<details>` block; an inline mention does not. */
const DETAILS_OPEN_RE = /^[ \t]*<details\b/i;

/** Closing tag of a `<details>` block. */
const DETAILS_CLOSE_RE = /<\/details>/i;

/**
 * Drop `<details>` blocks — where issue refinement archives the original
 * body — that open outside a fenced code block (``` or ~~~), from the
 * opening line through the first `</details>`; text after the closing tag
 * is kept. A block with no closing tag is kept whole. Mirrors Go
 * `stripDetailsBlocks` (#2679).
 */
function stripDetailsBlocks(lines: string[]): string[] {
  const out: string[] = [];
  let fence = "";
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();
    if (fence !== "") {
      if (trimmed.startsWith(fence)) fence = "";
      out.push(line);
      continue;
    }
    if (trimmed.startsWith("```")) fence = "```";
    else if (trimmed.startsWith("~~~")) fence = "~~~";
    if (fence !== "" || !DETAILS_OPEN_RE.test(line)) {
      out.push(line);
      continue;
    }
    let end = -1;
    for (let j = i; j < lines.length; j++) {
      if (DETAILS_CLOSE_RE.test(lines[j])) {
        end = j;
        break;
      }
    }
    if (end < 0) {
      out.push(line);
      continue;
    }
    const m = DETAILS_CLOSE_RE.exec(lines[end]);
    const rest = m ? lines[end].slice(m.index + m[0].length) : "";
    if (rest.trim() !== "") out.push(rest);
    i = end;
  }
  return out;
}

/**
 * Extract acceptance criteria from an issue body.
 *
 * Mirrors the authoritative Go parser (`parseACs` in
 * `internal/preflight/ac_reconcile.go`): checkbox bullets anywhere, plus
 * numbered items inside an "Acceptance Criteria" heading section, in
 * document order. `<details>` blocks are ignored and repeated criteria
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
  for (const rawLine of stripDetailsBlocks(body.split(/\r?\n/))) {
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
      const numbered = NUMBERED_RE.exec(line);
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
