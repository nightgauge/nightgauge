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
 * Numbered list item (`1.` or `1)`) indented at most three spaces. Group 1 is
 * the marker prefix, whose length is the item's content column; group 2 is
 * the text. Matched against the untrimmed line; a criterion only inside an AC
 * section, and one at or past the previous item's content column is a nested
 * sub-step, not a criterion (#2684).
 */
const NUMBERED_RE = /^( {0,3}\d{1,3}[.)][ \t]+)(.{1,4096})$/;

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

/** Line opening a collapsed `<details>` block; an inline mention does not. */
const DETAILS_OPEN_RE = /^[ \t]*<details\b/i;

/** Closing tag of a `<details>` block. */
const DETAILS_CLOSE_ALL_RE = /<\/details>/gi;

/**
 * Drop `<details>` blocks — where issue refinement archives the original
 * body — that open outside a fenced code block (``` or ~~~), from the
 * opening line through the `</details>` that closes it, counting nested
 * `<details>` so an archive holding an archive is dropped whole (#2655);
 * text after the closing tag is kept. A block with no closing tag is kept whole. Mirrors Go
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
    let depth = 0;
    for (let j = i; j < lines.length && end < 0; j++) {
      if (j > i && DETAILS_OPEN_RE.test(lines[j])) depth += 1;
      else if (j === i) depth = 1;
      depth -= (lines[j].match(DETAILS_CLOSE_ALL_RE) ?? []).length;
      if (depth <= 0) end = j;
    }
    if (end < 0) {
      out.push(line);
      continue;
    }
    const closeAt = lines[end].toLowerCase().lastIndexOf("</details>");
    const rest = lines[end].slice(closeAt + "</details>".length);
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
 * document order. `<details>` blocks and fenced code blocks are ignored, and
 * repeated criteria (whitespace-collapsed text) are dropped. A numbered line
 * indented to the content column of the numbered criterion above it is a
 * sub-step of that item; the column ends at a heading, or at a non-blank line
 * indented less than it that is not a numbered item.
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
  let itemCol = 0; // content column of the open numbered criterion; 0 = none
  for (const rawLine of stripDetailsBlocks(body.split(/\r?\n/))) {
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
      itemCol = 0;
      continue;
    }
    const line = rawLine.trim();
    const numbered = sectionLevel > 0 ? NUMBERED_RE.exec(rawLine.trimEnd()) : null;
    const indent = rawLine.length - rawLine.replace(/^[ \t]+/, "").length;
    if (numbered) {
      if (itemCol > 0 && indent >= itemCol) continue; // a sub-step of the criterion above
      itemCol = numbered[1].length;
    } else if (line !== "" && indent < itemCol) {
      itemCol = 0;
    }
    let box: string;
    let text: string;
    const checkbox = CHECKBOX_RE.exec(line);
    if (checkbox) {
      box = checkbox[1];
      text = checkbox[2];
    } else if (numbered) {
      box = " ";
      text = numbered[2];
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
