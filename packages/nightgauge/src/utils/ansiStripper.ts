/**
 * ANSI Escape Code Stripper
 *
 * Removes ANSI escape sequences from text for clean display in WebView.
 * Used by interactive mode output processing to strip colors and formatting
 * that would otherwise appear as garbled text in HTML.
 *
 * @see docs/INTERACTIVE_MODE.md - Interactive mode output handling
 * @see Issue #496 - Dual-mode output window rendering
 */

/**
 * ANSI escape code regex pattern
 *
 * Matches:
 * - CSI sequences: ESC [ ... (control sequences like colors, cursor, DEC private modes)
 * - OSC sequences: ESC ] ... BEL/ST (terminal title, hyperlinks)
 * - Simple escapes: ESC ( ) * + - . / (character set selection)
 *
 * The alternation handles two cases:
 * 1. OSC sequences: ESC ] followed by any chars until BEL (\x07) or ST (ESC \)
 * 2. CSI sequences: ESC [ (or C1 CSI \x9b) with parameters and a final byte
 *
 * Based on https://en.wikipedia.org/wiki/ANSI_escape_code
 * Enhanced for Issue #873 to handle OSC/hyperlink sequences
 */
const ANSI_ESCAPE_REGEX =
  // eslint-disable-next-line no-control-regex
  /[\u001b\u009b](?:\][^\u0007\u001b]*(?:\u0007|\u001b\\)?|[[()#;?]*(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-Za-z=><~])/g;

/**
 * ANSI escape code regex pattern for detection (no global flag)
 *
 * Same pattern as above but without /g flag to avoid lastIndex issues with test()
 */
const ANSI_DETECT_REGEX =
  // eslint-disable-next-line no-control-regex
  /[\u001b\u009b](?:\][^\u0007\u001b]*(?:\u0007|\u001b\\)?|[[()#;?]*(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-Za-z=><~])/;

/**
 * Strip ANSI escape codes from text
 *
 * Removes all ANSI escape sequences including:
 * - Color codes (foreground, background, 256-color, RGB)
 * - Text formatting (bold, italic, underline, etc.)
 * - Cursor movement and positioning
 * - Screen clearing and scrolling
 *
 * @param text - Input text potentially containing ANSI codes
 * @returns Clean text with all ANSI codes removed
 *
 * @example
 * ```typescript
 * stripAnsi('\u001b[31mRed text\u001b[0m') // 'Red text'
 * stripAnsi('Normal text') // 'Normal text'
 * stripAnsi('\u001b[1;4;32mBold underline green\u001b[0m') // 'Bold underline green'
 * ```
 */
export function stripAnsi(text: string): string {
  if (!text) {
    return "";
  }
  return text.replace(ANSI_ESCAPE_REGEX, "");
}

/**
 * Check if text contains ANSI escape codes
 *
 * Useful for detecting whether stripping is necessary.
 *
 * @param text - Input text to check
 * @returns true if text contains ANSI codes
 *
 * @example
 * ```typescript
 * hasAnsi('\u001b[31mRed\u001b[0m') // true
 * hasAnsi('Plain text') // false
 * ```
 */
export function hasAnsi(text: string): boolean {
  if (!text) {
    return false;
  }
  return ANSI_DETECT_REGEX.test(text);
}
