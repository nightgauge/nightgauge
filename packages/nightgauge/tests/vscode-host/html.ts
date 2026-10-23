/**
 * The one place test code strips `<script>` and `<style>` blocks from
 * rendered webview HTML before reading its text.
 *
 * CodeQL's `js/bad-tag-filter` flagged a hand-rolled script stripper in these
 * suites three times, each fixed one copy at a time (#2286). Browsers accept
 * `<SCRIPT>`, attributes on the opening tag, and `</script foo="bar">`, so a
 * pattern that is case-sensitive or matches only a bare `</script>` leaves
 * script *source* in the extracted text, and a text assertion can then match
 * code instead of rendered output — passing for the wrong reason.
 *
 * Every suite that needs this imports it from here; none keeps its own regex.
 * Dependency-free, so both the host tier and the vitest arrival harness can
 * use it.
 */

const SCRIPT_BLOCK = /<script\b[^>]*>[\s\S]*?<\/script[^>]*>/gi;
const STYLE_BLOCK = /<style\b[^>]*>[\s\S]*?<\/style[^>]*>/gi;

/** Replace every `<script>…</script>` block in `html` with `replacement`. */
export function stripScriptBlocks(html: string, replacement = " "): string {
  return html.replace(SCRIPT_BLOCK, replacement);
}

/** Replace every `<style>…</style>` block in `html` with `replacement`. */
export function stripStyleBlocks(html: string, replacement = " "): string {
  return html.replace(STYLE_BLOCK, replacement);
}
