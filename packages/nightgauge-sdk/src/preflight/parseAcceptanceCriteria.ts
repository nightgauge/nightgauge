import type { AcceptanceCriterion } from "./types.js";

/**
 * Markdown checkbox pattern.
 *
 * Accepts both `-` and `*` bullet markers, any leading indentation
 * (allows nested checkboxes), and `[ ]`, `[x]`, or `[X]` states.
 *
 * Criterion text is capped at MAX_AC_TEXT code points (see capText), the
 * same rune cap the Go parser applies.
 */
/** Cap on criterion text, in code points; Go caps the same count of runes. */
const MAX_AC_TEXT = 4096;

/** Truncates to MAX_AC_TEXT code points without splitting a surrogate pair. */
function capText(s: string): string {
  const cps = Array.from(s);
  return cps.length > MAX_AC_TEXT ? cps.slice(0, MAX_AC_TEXT).join("") : s;
}

const CHECKBOX_RE = /^[-*][ \t]+\[([ xX])\][ \t]+(.+)$/;

/**
 * Numbered list item (`1.` or `1)`) indented at most three spaces. Group 1 is
 * the marker prefix, whose length is the item's content column; group 2 is
 * the text. Matched against the untrimmed line; a criterion only inside an AC
 * section, and one at or past the previous item's content column is a nested
 * sub-step, not a criterion (#2684).
 */
const NUMBERED_RE = /^( {0,3}\d{1,3}[.)][ \t]+)(.+)$/;

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
 * Plain bullet item (`-`, `*` or `+`) indented at most three spaces. Group 1
 * is the marker prefix (the item's content column), group 2 the text. Counts
 * only inside a criteria section (#2906); checkboxes are matched first.
 */
const BULLET_RE = /^( {0,3}[-*+][ \t]+)(.+)$/;

/** Bullet text that is only a checkbox (`- [ ]`): not a plain bullet item. */
const EMPTY_BOX_RE = /^\[[ xX]\][ \t]*$/;

/** Any list-item line; such a line is never a wrapped continuation. */
const LIST_MARKER_RE = /^[ \t]*(\d{1,3}[.)]|[-*+])[ \t]+\S/;

/**
 * Headings, matched whole, that open a criteria section besides any heading
 * containing "acceptance criteria" (#2906 ADR-001). Mirrors Go
 * `acSectionNames`.
 */
const AC_SECTION_NAMES = new Set(["acceptance", "verification", "done when", "definition of done"]);

/** Whether a heading's text opens a criteria section. Mirrors Go `isACSectionHeading`. */
function isACSectionHeading(text: string): boolean {
  const t = text.trim().toLowerCase();
  if (t.includes("acceptance criteria")) return true;
  return AC_SECTION_NAMES.has(t.replace(/[:#\s]+$/, "").trim());
}

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
 * numbered and plain bulleted items inside a criteria section (see
 * `isACSectionHeading`), in document order. `<details>` blocks and fenced
 * code blocks are ignored, and repeated criteria (whitespace-collapsed text)
 * are dropped. A list line indented to the content column of the section
 * item above it is a sub-step of that item; the column ends at a heading, or
 * at a non-blank line indented less than it that is not a section item. A
 * non-list line indented at or past the column, directly under the item's
 * text, is a wrapped continuation and joins it (#2906).
 *
 * State is informational — the reconciler verifies satisfaction against
 * `main`, not the box state. An item without a box is unchecked.
 */
export function parseAcceptanceCriteria(body: string): AcceptanceCriterion[] {
  if (typeof body !== "string" || body.length === 0) {
    return [];
  }

  const out: AcceptanceCriterion[] = [];
  const seen = new Set<string>();
  const normalize = (text: string): string => text.split(/\s+/).join(" ");
  let sectionLevel = 0;
  let inFence = false;
  let itemCol = 0; // content column of the open section item; 0 = none
  let joinIdx = -1; // index in out of the item a wrapped line joins; -1 = none
  for (const rawLine of stripDetailsBlocks(body.split(/\r?\n/))) {
    if (FENCE_RE.test(rawLine)) {
      inFence = !inFence;
      joinIdx = -1;
      continue;
    }
    if (inFence) continue;
    const heading = HEADING_RE.exec(rawLine);
    if (heading) {
      const level = heading[1].length;
      if (isACSectionHeading(heading[2])) {
        sectionLevel = level;
      } else if (sectionLevel > 0 && level <= sectionLevel) {
        sectionLevel = 0;
      }
      itemCol = 0;
      joinIdx = -1;
      continue;
    }
    const line = rawLine.trim();
    const checkbox = CHECKBOX_RE.exec(line);
    let item: RegExpExecArray | null = null;
    if (sectionLevel > 0 && !checkbox) {
      const right = rawLine.trimEnd();
      item = NUMBERED_RE.exec(right);
      if (!item) {
        item = BULLET_RE.exec(right);
        if (item && EMPTY_BOX_RE.test(item[2])) item = null;
      }
    }
    const indent = rawLine.length - rawLine.replace(/^[ \t]+/, "").length;
    if (item) {
      if (itemCol > 0 && indent >= itemCol) {
        joinIdx = -1;
        continue; // a sub-step of the criterion above
      }
      itemCol = item[1].length;
    } else if (line === "") {
      joinIdx = -1;
    } else if (indent < itemCol) {
      itemCol = 0;
      joinIdx = -1;
    } else if (!checkbox && joinIdx >= 0 && itemCol > 0 && !LIST_MARKER_RE.test(rawLine)) {
      const joined = capText(`${out[joinIdx].text} ${line}`);
      seen.delete(normalize(out[joinIdx].text));
      out[joinIdx].text = joined;
      seen.add(normalize(joined));
      continue;
    } else if (LIST_MARKER_RE.test(rawLine)) {
      joinIdx = -1;
    }
    let box: string;
    let text: string;
    if (checkbox) {
      box = checkbox[1];
      text = checkbox[2];
    } else if (item) {
      box = " ";
      text = item[2];
      const lead = LEADING_BOX_RE.exec(text);
      if (lead) {
        box = lead[1];
        text = lead[2];
      }
    } else {
      continue;
    }
    joinIdx = -1;
    text = capText(text.trim());
    if (text.length === 0) continue;
    const key = normalize(text);
    if (seen.has(key)) continue;
    seen.add(key);
    if (item) joinIdx = out.length;
    out.push({
      index: out.length,
      text,
      checkbox_state: box === " " ? "unchecked" : "checked",
    });
  }
  return out;
}
