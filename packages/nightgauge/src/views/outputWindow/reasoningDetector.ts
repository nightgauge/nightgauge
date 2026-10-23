/**
 * Reasoning Detector - Classifies output lines as reasoning vs substantive
 *
 * Agent reasoning steps appear as individual INFO lines in the pipeline output
 * window, adding noise without value. This module detects those lines so they
 * can be buffered and collapsed into expandable sections.
 *
 * Classification strategy (conservative — default to NOT reasoning):
 * 1. NEVER classify as reasoning if the line contains substantive markers
 * 2. Only classify as reasoning if the line matches known reasoning patterns
 *
 * @see Issue #796 - Collapse agent reasoning lines into expandable sections
 */

/**
 * Substantive line patterns that should NEVER be classified as reasoning.
 * These are checked first — if any match, the line is substantive regardless
 * of reasoning pattern matches.
 */
const SUBSTANTIVE_PATTERNS: RegExp[] = [
  // Markdown headers
  /^#{1,6}\s/,
  // Markdown tables
  /\|/,
  // Markdown checkboxes
  /^-\s\[[ x]\]/,
  // Code fences
  /^```/,
  // Error/warning prefixes
  /^(Error|Warning|ERROR|WARN):/i,
  // Status icons
  /[✗✓⊘]/,
  // Stage status messages
  /\b(Starting|completed|failed|skipped)\b/,
  // Separator lines (Issue #794)
  /═/,
  // skillRunner metadata (already compacted by #795)
  /\[skillRunner\]/,
  // File paths (with slash or standalone filename with code extension)
  /\/\S+\.\w{1,5}\b/,
  /\b\w[\w.-]*\.(ts|js|tsx|jsx|json|md|yaml|yml|py|rs|go|java|sh|css|html|xml|toml|lock|cfg|env|sql|rb|kt|swift)\b/,
  // URLs
  /https?:\/\//,
  // JSON/object notation
  /^\s*[{[\]]/,
  // Numbered lists with content (e.g., "1. Something")
  /^\s*\d+\.\s+\S/,
  // Bullet points with content
  /^\s*[-*]\s+\S/,
  // Acceptance criteria keywords
  /\b(acceptance criteria|requirement|must|shall)\b/i,
  // Issue references
  /#\d{2,}/,
];

/**
 * Reasoning line patterns — lines matching these are collapsed when no
 * substantive markers are present.
 */
const REASONING_PATTERNS: RegExp[] = [
  // "Let me...", "Now let me...", "I'll...", "I will...", "Now I'll..."
  /^(Now\s+)?Let me\b/i,
  /^(Now\s+)?I'll\b/,
  /^I will\b/,
  // Acknowledgments
  /^(Good|Perfect|Excellent|Great|OK|Alright|Right|Understood|Done)\.\s*$/,
  // Observation starters
  /^(Looking at|Checking|Reading|Examining)\b/,
  // Sequence words
  /^(First|Next|Now|Then|Finally),\s/,
  // "This/That/These" followed by short continuation
  /^(This|That|These)\s/,
  // Self-referential patterns
  /^I (can |need to |should |want to |see |notice |found )/,
  // Short transitional lines ending with colon
  /^.{1,79}:\s*$/,
  // Single word acknowledgments (already covered above, but explicit)
  /^(Done|Right|Understood|OK)\.\s*$/,
];

/**
 * Maximum length for a reasoning line. Lines longer than this are assumed
 * to be substantive content.
 */
const MAX_REASONING_LENGTH = 120;

/**
 * Check if a line is an agent reasoning/transitional message.
 *
 * Reasoning lines are short, transitional messages the agent emits while
 * thinking. They add no user value by default but are useful for debugging.
 *
 * @param text - The line text (after ANSI stripping)
 * @returns true if the line is a reasoning line that should be collapsed
 */
export function isReasoningLine(text: string): boolean {
  const trimmed = text.trim();

  // Empty/whitespace-only lines are not reasoning — they're blank lines
  // handled separately by the blank line normalization
  if (trimmed.length === 0) {
    return false;
  }

  // Long lines are substantive
  if (trimmed.length > MAX_REASONING_LENGTH) {
    return false;
  }

  // Check substantive markers FIRST — never collapse these
  for (const pattern of SUBSTANTIVE_PATTERNS) {
    if (pattern.test(trimmed)) {
      return false;
    }
  }

  // Check reasoning patterns — only collapse on positive match
  for (const pattern of REASONING_PATTERNS) {
    if (pattern.test(trimmed)) {
      return true;
    }
  }

  // Unknown lines default to substantive (conservative)
  return false;
}
