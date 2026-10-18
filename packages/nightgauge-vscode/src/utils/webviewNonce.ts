/**
 * webviewNonce - the one nonce source for every webview Content-Security-Policy
 *
 * A CSP nonce only protects a webview if it cannot be predicted, so it comes
 * from the operating system's CSPRNG (`crypto.randomBytes`), never
 * `Math.random`. 16 bytes give 128 bits; hex keeps the value alphanumeric so
 * it is safe in a `nonce-...` source, an HTML attribute and a RegExp alike.
 */

import { randomBytes } from "node:crypto";

/** Number of random bytes behind each nonce (128 bits). */
export const NONCE_BYTES = 16;

/** Returns a fresh, unpredictable nonce for one webview render. */
export function getNonce(): string {
  return randomBytes(NONCE_BYTES).toString("hex");
}
