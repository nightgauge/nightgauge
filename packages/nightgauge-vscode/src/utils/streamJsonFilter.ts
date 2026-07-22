/**
 * Stream-JSON Envelope Filter
 *
 * Detects and filters raw stream-json message envelopes from pipeline output.
 * These are internal Claude API payloads (tool result acknowledgements, system
 * prompts, block boundary markers) that should never be visible to the user.
 *
 * Uses a fast prefix-check + JSON.parse confirmation strategy to avoid
 * expensive parsing on every line while preventing false positives on
 * partial buffer fragments.
 *
 * @see docs/ARCHITECTURE.md - Pipeline output flow
 * @see Issue #792 - Filter raw JSON tool result envelopes
 */

/**
 * Stream-json envelope type prefixes that should be filtered from output.
 *
 * These are internal protocol types that carry no user-visible information:
 * - user: tool result acknowledgements
 * - system: system prompt injections
 * - content_block_stop: block boundary markers
 * - message_start: message start markers
 * - message_stop: message end markers
 * - message_delta: message-level metadata (stop_reason, usage)
 */
const FILTERED_TYPE_PREFIXES = [
  '{"type":"user"',
  '{"type":"system"',
  '{"type":"content_block_stop"',
  '{"type":"message_start"',
  '{"type":"message_stop"',
  '{"type":"message_delta"',
] as const;

/**
 * Patterns that indicate a line is a fragment of a filtered envelope.
 *
 * When Node.js buffer boundaries split a large JSON envelope across
 * multiple `data` events, the resulting fragments are not valid JSON.
 * These patterns catch the most common fragment signatures.
 *
 * Issue #873: Added tool_result metadata field patterns (interrupted,
 * isImage, noOutputExpected) to catch trailing fragments that leak
 * into the output window as raw text.
 */
const FRAGMENT_PATTERNS = [
  '"tool_use_id"',
  '"tool_result"',
  '"type":"tool_result"',
  '"interrupted"',
  '"isImage"',
  '"noOutputExpected"',
] as const;

/**
 * Detect whether a line is a raw stream-json envelope that should be
 * filtered from user-visible output.
 *
 * Uses a two-step approach:
 * 1. Fast prefix check against known filtered types
 * 2. JSON.parse confirmation to avoid false positives on partial chunks
 *
 * @param line - A single line from CLI stdout/stderr
 * @returns true if the line is a stream-json envelope that should be hidden
 */
export function isStreamJsonEnvelope(line: string): boolean {
  const trimmed = line.trim();
  if (!trimmed) {
    return false;
  }

  // Fast prefix check — avoids JSON.parse on most lines
  const matchesPrefix = FILTERED_TYPE_PREFIXES.some((prefix) => trimmed.startsWith(prefix));

  if (!matchesPrefix) {
    return false;
  }

  // Confirm it's valid JSON to avoid filtering partial buffer chunks
  // that happen to start with a filtered prefix
  try {
    JSON.parse(trimmed);
    return true;
  } catch {
    return false;
  }
}

/**
 * Detect whether a line is a fragment of a stream-json envelope.
 *
 * When a large envelope spans multiple buffer chunks, the resulting
 * fragments fail JSON.parse but still contain recognizable patterns.
 * This function catches those fragments to prevent them from being
 * displayed as raw text.
 *
 * @param line - A single line from CLI stdout/stderr that is not valid JSON
 * @returns true if the line appears to be a fragment of a filtered envelope
 */
export function isEnvelopeFragment(line: string): boolean {
  const trimmed = line.trim();
  if (!trimmed) {
    return false;
  }

  return FRAGMENT_PATTERNS.some((pattern) => trimmed.includes(pattern));
}
