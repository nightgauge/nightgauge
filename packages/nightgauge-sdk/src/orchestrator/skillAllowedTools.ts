/**
 * skillAllowedTools — a stage SKILL.md's `allowed-tools`, read exactly as the
 * Go side reads them (internal/skillrender/render.go: splitFrontmatter and
 * extractYAMLField find the field, splitTools splits it, FilterHeadlessTools
 * drops what a non-interactive run cannot use).
 *
 * The SDK stage path used to hand its query no tools at all, so a stage run
 * through `nightgauge-sdk stage` was never granted what its skill declares
 * (#2358). Both test suites read
 * internal/skillrender/testdata/allowed_tools_expected.json, so the grammar
 * cannot change on one side only.
 *
 * Written as scans, not regular expressions: an end-anchored quantifier is
 * quadratic on a long run of the character it repeats (CodeQL
 * `js/polynomial-redos`), and a skill's frontmatter is not trusted input.
 */

/**
 * Go's `unicode.IsSpace`, which `strings.TrimSpace` and `splitTools` use.
 * JavaScript's `\s` differs from it at U+0085 and U+FEFF.
 */
const GO_SPACE = /[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]/;

function isSpace(ch: string): boolean {
  return GO_SPACE.test(ch);
}

/** Drop leading and trailing characters `drop` matches. */
function trimWhere(value: string, drop: (ch: string) => boolean): string {
  let start = 0;
  let end = value.length;
  while (start < end && drop(value[start])) start += 1;
  while (end > start && drop(value[end - 1])) end -= 1;
  return value.slice(start, end);
}

/**
 * The value of the first frontmatter line whose trimmed text starts with
 * `key:`, trimmed and stripped of surrounding quotes (Go's extractYAMLField).
 */
function frontmatterField(head: string, key: string): string {
  const prefix = `${key}:`;
  for (const line of head.split("\n")) {
    const trimmed = trimWhere(line, isSpace);
    if (trimmed.startsWith(prefix)) {
      const value = trimWhere(trimmed.slice(prefix.length), isSpace);
      return trimWhere(value, (ch) => ch === '"' || ch === "'");
    }
  }
  return "";
}

/**
 * Split a frontmatter tool list into its entries (Go's splitTools).
 *
 * Entries are separated by whitespace or commas. A separator inside
 * parentheses does not split, so a `Tool(pattern)` entry such as `Bash(gh *)`
 * stays whole; an unclosed parenthesis keeps the rest of the list in its entry.
 */
export function splitAllowedTools(value: string): string[] {
  const entries: string[] = [];
  let start = -1;
  let depth = 0;
  for (let i = 0; i < value.length; i += 1) {
    const ch = value[i];
    if (depth === 0 && (ch === "," || isSpace(ch))) {
      if (start >= 0) {
        entries.push(value.slice(start, i));
        start = -1;
      }
      continue;
    }
    if (ch === "(") depth += 1;
    else if (ch === ")" && depth > 0) depth -= 1;
    if (start < 0) start = i;
  }
  if (start >= 0) entries.push(value.slice(start));
  return entries;
}

/** The SKILL.md frontmatter fields that list tools, each read the same way. */
export type SkillToolField = "allowed-tools" | "mcp-tools" | "programmatic-tools";

/**
 * The tools a SKILL.md's `field` frontmatter declares, verbatim. Empty when
 * the document has no frontmatter or the frontmatter has no such field.
 */
export function skillFrontmatterTools(skillContent: string, field: SkillToolField): string[] {
  if (!skillContent.startsWith("---\n")) return [];
  const end = skillContent.indexOf("\n---", 4);
  if (end < 0) return [];
  return splitAllowedTools(frontmatterField(skillContent.slice(4, end), field));
}

/**
 * The tools a SKILL.md's `allowed-tools` frontmatter declares, verbatim. A
 * headless caller passes the result through {@link filterHeadlessTools}.
 */
export function skillAllowedTools(skillContent: string): string[] {
  return skillFrontmatterTools(skillContent, "allowed-tools");
}

/**
 * The tools a non-interactive run is granted (Go's FilterHeadlessTools):
 * everything but AskUserQuestion, which the Claude CLI treats as a permission
 * denial under `-p`, so the agent retries it in a loop (#118, #171, #205).
 */
export function filterHeadlessTools(tools: readonly string[]): string[] {
  return tools.filter((tool) => tool !== "AskUserQuestion");
}
