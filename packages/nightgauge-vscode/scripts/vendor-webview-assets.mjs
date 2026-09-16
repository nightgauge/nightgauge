/**
 * Vendor the browser builds that webviews load into dist/vendor/.
 *
 * Webviews must never fetch script from a remote origin: the VS Marketplace
 * and Open VSX both require that an extension only execute code contained in
 * the published package, and a webview `<script src="https://...">` breaks
 * that even when the CSP names the origin. Copying the browser build here lets
 * OutputWindowHtml load it through `webview.asWebviewUri`, so the CSP stays
 * `script-src 'nonce-...'` with no remote origin at all.
 *
 * `marked` is a pinned dependency of this package, so the vendored copy moves
 * only when the lockfile does.
 */
import { copyFileSync, mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const vendorDir = join(pkgDir, "dist", "vendor");

// marked's `exports` map does not expose ./lib/, so resolve via package.json.
const markedRoot = dirname(require.resolve("marked/package.json"));

const assets = [
  [join(markedRoot, "lib", "marked.umd.js"), "marked.umd.js"],
  [join(markedRoot, "LICENSE"), "marked.LICENSE"],
];

mkdirSync(vendorDir, { recursive: true });
for (const [from, name] of assets) {
  copyFileSync(from, join(vendorDir, name));
}

console.log(`Vendored ${assets.length} webview assets into dist/vendor/`);
