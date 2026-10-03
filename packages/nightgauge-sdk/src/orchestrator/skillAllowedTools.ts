/**
 * skillAllowedTools — a stage SKILL.md's `allowed-tools`, read exactly as the
 * Go side reads them (internal/skillrender/render.go: splitFrontmatter and
 * extractToolList find the field, splitTools splits it, FilterHeadlessTools
 * drops what a non-interactive run cannot use).
 *
 * The SDK stage path used to hand its query no tools at all, so a stage run
 * through `nightgauge-sdk stage` was never granted what its skill declares
 * (#2358). Both test suites read
 * internal/skillrender/testdata/allowed_tools_expected.json, so the grammar
 * cannot change on one side only. An `allowed-tools` field that is there but
 * lists no tool is refused on both sides (errNoAllowedTools in Go), and so is
 * one written only in a form neither reader can read (#2385).
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

function isQuote(ch: string): boolean {
  return ch === '"' || ch === "'";
}

/** Leading whitespace characters of a line (Go's indentOf counts runes). */
function indentOf(line: string): number {
  let n = 0;
  while (n < line.length && isSpace(line[n])) n += 1;
  return n;
}

/**
 * The text of a trimmed line that is a YAML block list item, a dash alone or a
 * dash and whitespace (`-Read` is not one), or undefined for any other line.
 */
function listItem(item: string): string | undefined {
  if (!item.startsWith("-") || (item.length > 1 && !isSpace(item[1]))) return undefined;
  return trimWhere(item.slice(1), isSpace);
}

/**
 * A trimmed value without its YAML comment: a `#` at its start or after
 * whitespace, outside parentheses as {@link splitAllowedTools} counts them, and
 * everything after it (Go's stripComment). `Read Grep # not Bash` used to
 * grant Bash.
 */
function stripComment(value: string): string {
  let depth = 0;
  let afterSpace = true;
  for (let i = 0; i < value.length; i += 1) {
    const ch = value[i];
    if (ch === "#" && depth === 0 && afterSpace) return trimWhere(value.slice(0, i), isSpace);
    if (ch === "(") depth += 1;
    else if (ch === ")" && depth > 0) depth -= 1;
    afterSpace = isSpace(ch);
  }
  return value;
}

/**
 * One value of a tool field, trimmed and without its comment (Go's
 * toolValue). A YAML flow sequence, from `[` to `]`, reads as its items: what
 * is between the brackets is split, and each item loses the quotes around it,
 * so `[Read, "Bash(gh *)"]` lists Read and Bash(gh *). Any other value loses
 * the quotes around it as a whole and is split.
 *
 * A value that starts with `|` or `>` is a block scalar this reader takes no
 * text for (one on the key's line is read by {@link fieldValue}), and a flow
 * sequence with no `]` is never closed: neither lists a tool. Each used to
 * read as a junk entry such as `|` or `[Read`, which grants nothing (#2385).
 */
function toolValue(value: string): string[] {
  if (value.startsWith("|") || value.startsWith(">")) return [];
  if (value.startsWith("[") && !value.includes("]")) return [];
  if (value.length >= 2 && value.startsWith("[") && value.endsWith("]")) {
    return splitAllowedTools(value.slice(1, -1))
      .map((entry) => trimWhere(entry, isQuote))
      .filter((entry) => entry !== "");
  }
  return splitAllowedTools(trimWhere(value, isQuote));
}

/**
 * The text after the colon when a trimmed line is `key`'s entry, the key
 * written as YAML writes an implicit one: bare, double-quoted or
 * single-quoted, then any spaces or tabs, then the colon (Go's keyEntry).
 * Undefined for any other line. `"allowed-tools": Read` and
 * `allowed-tools : Read` used to read as no field, which every runner grants
 * its default tools (#2385).
 */
function keyEntry(trimmed: string, key: string): string | undefined {
  let rest: string;
  if (trimmed.startsWith(key)) {
    rest = trimmed.slice(key.length);
  } else if (isQuote(trimmed.charAt(0)) && trimmed.startsWith(key + trimmed.charAt(0), 1)) {
    rest = trimmed.slice(key.length + 2);
  } else {
    return undefined;
  }
  let i = 0;
  while (i < rest.length && (rest[i] === " " || rest[i] === "\t")) i += 1;
  return rest[i] === ":" ? rest.slice(i + 1) : undefined;
}

/**
 * Whether a trimmed line is YAML's explicit form of `key`'s entry: `?`,
 * whitespace, the key bare or quoted, and nothing after it but a comment
 * (Go's explicitKey). Its value follows on a `:` line.
 */
function explicitKey(trimmed: string, key: string): boolean {
  if (!trimmed.startsWith("?") || trimmed.length < 2 || !isSpace(trimmed[1])) return false;
  const name = stripComment(trimWhere(trimmed.slice(1), isSpace));
  return name === key || name === `"${key}"` || name === `'${key}'`;
}

/**
 * Whether a value is a YAML block scalar header: `|` or `>`, then at most one
 * chomping indicator (`+` or `-`) and one indentation indicator (a digit 1 to
 * 9), in either order (Go's blockScalarHeader).
 */
function blockScalarHeader(value: string): boolean {
  if (!value.startsWith("|") && !value.startsWith(">")) return false;
  let chomping = false;
  let indentation = false;
  for (const ch of value.slice(1)) {
    if ((ch === "+" || ch === "-") && !chomping) chomping = true;
    else if (ch >= "1" && ch <= "9" && !indentation) indentation = true;
    else return false;
  }
  return true;
}

/**
 * The text of a block scalar whose header is on the key's line: the lines
 * below it up to the first line, not blank, that is no deeper than the key
 * (Go's blockScalar). A `#` in it is text, as YAML reads it, not a comment.
 */
function blockScalar(below: readonly string[], keyIndent: number): string {
  const text: string[] = [];
  for (const line of below) {
    if (trimWhere(line, isSpace) !== "" && indentOf(line) <= keyIndent) break;
    text.push(line);
  }
  return text.join("\n");
}

/**
 * Whether a value is a quoted value or a flow sequence that ends on its own
 * line (Go's closedOnItsLine). Any other value on the key's line continues
 * over the deeper lines below it, as YAML continues a plain value and a quoted
 * value or flow sequence that is not closed yet.
 */
function closedOnItsLine(value: string): boolean {
  if (value.startsWith("[")) return value.includes("]");
  if (!isQuote(value.charAt(0))) return false;
  return value.length >= 2 && value.endsWith(value.charAt(0));
}

/**
 * The entries of a tool field whose entry is on line `at`, `entry` being the
 * text after its colon (Go's fieldValue).
 *
 * A value there is read without its comment. A block scalar header (`|` or
 * `>`) reads as the block's text, split as one value. A quoted value or a
 * flow sequence closed on that line is read as one value. Any other value
 * there continues over the lines indented deeper than the key: they are
 * joined to it with spaces and read as one value, which is how YAML continues
 * a plain value, a quoted one or a flow sequence onto the next lines.
 *
 * With no value there, the value is on the lines after it: a YAML block list,
 * one `- entry` per line with each entry read as one value, or else the lines
 * indented deeper than the key, joined with spaces and read as one value.
 * Whichever the first of those lines is decides which it is. Blank and `#`
 * comment lines are skipped, and the first other line ends the value.
 */
function fieldValue(lines: readonly string[], at: number, entry: string): string[] {
  const keyIndent = indentOf(lines[at]);
  const below = lines.slice(at + 1);
  const value = stripComment(trimWhere(entry, isSpace));
  if (blockScalarHeader(value)) return splitAllowedTools(blockScalar(below, keyIndent));
  if (closedOnItsLine(value)) return toolValue(value);
  let inList = false;
  let inValue = value !== "";
  const entries: string[] = [];
  const continued: string[] = inValue ? [value] : [];
  for (const next of below) {
    const item = trimWhere(next, isSpace);
    if (item === "" || item.startsWith("#")) continue;
    const rest = listItem(item);
    if (rest !== undefined && !inValue) {
      inList = true;
      entries.push(...toolValue(stripComment(rest)));
      continue;
    }
    if (!inList && indentOf(next) > keyIndent) {
      inValue = true;
      continued.push(stripComment(item));
      continue;
    }
    break;
  }
  return inValue ? toolValue(continued.join(" ")) : entries;
}

/**
 * The entries of the first frontmatter field that is `key` (Go's
 * extractToolList), or undefined when there is none. The field is the first
 * line that is the key's entry ({@link keyEntry}), or YAML's explicit form of
 * it ({@link explicitKey}), whose value is on the next line that is not blank
 * or a comment when that line is a `:` at the key's indent. An explicit key
 * with no such line has no value. {@link fieldValue} reads the value.
 */
function frontmatterTools(head: string, key: string): string[] | undefined {
  const lines = head.split("\n");
  for (let i = 0; i < lines.length; i += 1) {
    const trimmed = trimWhere(lines[i], isSpace);
    const entry = keyEntry(trimmed, key);
    if (entry !== undefined) return fieldValue(lines, i, entry);
    if (!explicitKey(trimmed, key)) continue;
    for (let j = i + 1; j < lines.length; j += 1) {
      const next = trimWhere(lines[j], isSpace);
      if (next === "" || next.startsWith("#")) continue;
      if (next.startsWith(":") && indentOf(lines[j]) === indentOf(lines[i])) {
        return fieldValue(lines, j, next.slice(1));
      }
      break;
    }
    return [];
  }
  return undefined;
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
 * Why a skill is refused: its `allowed-tools` field is there but lists no tool.
 * A skill without the field gets each runner's default, the extension's
 * default set (Bash, Write and Edit among it) and full access under Codex, and
 * a field that read as empty used to get it too, so a list written in a form
 * the reader did not know granted more than it named (#2358). A field written
 * only in a form the reader cannot read, such as a flow sequence that never
 * closes, lists no tool and is refused too (#2385). The Go render refuses the
 * same skill with the same words.
 */
export const NO_ALLOWED_TOOLS =
  "allowed-tools is present but lists no tool: " +
  "list the tools the skill needs (allowed-tools: Read Grep), or remove the field";

/**
 * The tools a SKILL.md's `field` frontmatter declares, verbatim. Empty when
 * the document has no frontmatter or the frontmatter has no such field.
 *
 * @throws Error ({@link NO_ALLOWED_TOOLS}) when `field` is `allowed-tools` and
 * the field is there but lists no tool.
 */
export function skillFrontmatterTools(skillContent: string, field: SkillToolField): string[] {
  if (!skillContent.startsWith("---\n")) return [];
  const end = skillContent.indexOf("\n---", 4);
  if (end < 0) return [];
  const tools = frontmatterTools(skillContent.slice(4, end), field);
  if (field === "allowed-tools" && tools?.length === 0) throw new Error(NO_ALLOWED_TOOLS);
  return tools ?? [];
}

/**
 * The tools a SKILL.md's `allowed-tools` frontmatter declares, verbatim. A
 * headless caller passes the result through {@link filterHeadlessTools}.
 *
 * @throws Error ({@link NO_ALLOWED_TOOLS}) when the field is there but lists
 * no tool.
 */
export function skillAllowedTools(skillContent: string): string[] {
  return skillFrontmatterTools(skillContent, "allowed-tools");
}

/**
 * {@link skillAllowedTools} of the SKILL.md at `skillPath`. A refusal names the
 * file, as the Go render's does.
 */
export function skillFileAllowedTools(skillContent: string, skillPath: string): string[] {
  try {
    return skillAllowedTools(skillContent);
  } catch (error) {
    throw new Error(`${skillPath}: ${(error as Error).message}`, { cause: error });
  }
}

/**
 * The tools a non-interactive run is granted (Go's FilterHeadlessTools):
 * everything but AskUserQuestion, which the Claude CLI treats as a permission
 * denial under `-p`, so the agent retries it in a loop (#118, #171, #205).
 */
export function filterHeadlessTools(tools: readonly string[]): string[] {
  return tools.filter((tool) => tool !== "AskUserQuestion");
}
