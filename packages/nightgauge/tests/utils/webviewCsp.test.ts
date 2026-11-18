import { describe, it, expect } from "vitest";
import { buildWebviewCsp } from "../../src/utils/webviewCsp";

describe("buildWebviewCsp (#3405)", () => {
  it("emits the strict baseline with a nonce-only script-src", () => {
    expect(buildWebviewCsp("n0nce", "vscode-webview://x")).toBe(
      "default-src 'none'; script-src 'nonce-n0nce'; style-src 'nonce-n0nce'; " +
        "form-action 'none'; base-uri 'none'"
    );
  });

  it("never lists the resource origin in script-src, whatever the options", () => {
    const csp = buildWebviewCsp("n0nce", "vscode-webview://x", {
      inlineStyles: true,
      styleSource: true,
      fontSource: true,
    });
    expect(csp).toContain("script-src 'nonce-n0nce';");
    expect(csp).toContain("style-src vscode-webview://x 'unsafe-inline'");
    expect(csp).toContain("font-src vscode-webview://x");
    expect(csp).not.toMatch(/script-src[^;]*vscode-webview/);
  });

  it("adds the resource origin to style-src when asked, keeping the nonce", () => {
    expect(buildWebviewCsp("n0nce", "o", { styleSource: true })).toContain(
      "style-src o 'nonce-n0nce'"
    );
  });

  it("omits an empty resource origin", () => {
    expect(buildWebviewCsp("n0nce", "", { inlineStyles: true, fontSource: true })).toBe(
      "default-src 'none'; script-src 'nonce-n0nce'; style-src 'unsafe-inline'; " +
        "form-action 'none'; base-uri 'none'"
    );
  });

  it.each([
    ["nonce", "a'b", "o"],
    ["nonce", "", "o"],
    ["nonce", "a b", "o"],
    ["cspSource", "n0nce", "o; script-src *"],
    ["cspSource", "n0nce", "'unsafe-inline'"],
    ["cspSource", "n0nce", 'o"><script>'],
  ])("rejects an invalid %s", (_name, nonce, source) => {
    expect(() => buildWebviewCsp(nonce, source)).toThrow(/invalid/);
  });
});
