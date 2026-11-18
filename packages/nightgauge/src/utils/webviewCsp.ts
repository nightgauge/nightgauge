/**
 * webviewCsp - the one Content-Security-Policy builder for every webview
 *
 * Every webview panel renders its policy through `buildWebviewCsp`, so each
 * one starts from the same baseline:
 *
 * - `default-src 'none'`: nothing loads unless a directive below allows it.
 * - `script-src 'nonce-...'`: only the panel's own nonce-tagged scripts run.
 *   No origin is ever listed here, not even the webview's own resource origin.
 * - `form-action 'none'` and `base-uri 'none'`: no form submission and no
 *   `<base>` rewrite of relative URLs.
 *
 * Options only widen style, font and image loading, never script. Pair the
 * nonce with `getNonce()` from `webviewNonce.ts`.
 */

/** Panel-specific widening of the baseline policy. Script is never widened. */
export interface WebviewCspOptions {
  /**
   * Allow inline `style="..."` attributes and untagged `<style>` blocks
   * (`'unsafe-inline'`). Without it, `<style>` blocks must carry the nonce.
   * Browsers ignore `'unsafe-inline'` when a nonce is present in the same
   * directive, so the two are mutually exclusive.
   */
  inlineStyles?: boolean;
  /** Allow stylesheets from the webview's resource origin (`cspSource`). */
  styleSource?: boolean;
  /** Allow fonts from the webview's resource origin. */
  fontSource?: boolean;
}

/** The directives every policy carries, in emission order. */
export const REQUIRED_CSP_DIRECTIVES = [
  "default-src 'none'",
  "form-action 'none'",
  "base-uri 'none'",
] as const;

const NONCE_PATTERN = /^[A-Za-z0-9+/_=-]+$/;

function assertCspToken(name: string, value: string, pattern: RegExp): void {
  if (!pattern.test(value)) {
    throw new Error(`buildWebviewCsp: invalid ${name}`);
  }
}

/**
 * Build the Content-Security-Policy value for a webview's
 * `<meta http-equiv="Content-Security-Policy">` tag.
 *
 * @param nonce     The per-render nonce from `getNonce()`.
 * @param cspSource `webview.cspSource`, the webview's resource origin.
 * @param opts      Style and font widening for this panel.
 */
export function buildWebviewCsp(
  nonce: string,
  cspSource: string | undefined,
  opts: WebviewCspOptions = {}
): string {
  assertCspToken("nonce", nonce, NONCE_PATTERN);
  // A host that reports no resource origin gets a policy that names none.
  cspSource = cspSource ?? "";
  // A source expression never contains a directive separator or a quote that
  // could close the surrounding attribute.
  // VS Code's value is a source list such as "'self' https://*.vscode-cdn.net",
  // so quoted keywords are allowed; a directive separator, an attribute quote
  // or markup is not.
  assertCspToken("cspSource", cspSource, /^[^;"<>\r\n]*$/);
  const origin = cspSource.trim();

  const styleSources: string[] = [];
  if ((opts.styleSource || opts.inlineStyles) && origin) styleSources.push(origin);
  styleSources.push(opts.inlineStyles ? "'unsafe-inline'" : `'nonce-${nonce}'`);

  const directives = [
    "default-src 'none'",
    `script-src 'nonce-${nonce}'`,
    `style-src ${styleSources.join(" ")}`,
  ];
  if (opts.fontSource && origin) directives.push(`font-src ${origin}`);
  directives.push("form-action 'none'", "base-uri 'none'");
  return directives.join("; ");
}

/** The per-render inputs a pure webview HTML builder needs for its CSP. */
export interface WebviewHtmlSecurity {
  /** Per-render nonce from `getNonce()`. */
  nonce: string;
  /** `webview.cspSource`. */
  cspSource: string;
}
