/**
 * webviewNonce.test.ts
 *
 * Every webview CSP nonce comes from one crypto-random helper. Before this,
 * eight views each carried a private `getNonce()` built on `Math.random`,
 * whose output is predictable, while the conversation panel alone used
 * `crypto.randomBytes`.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as crypto from "node:crypto";
import { getNonce, NONCE_BYTES } from "../../src/utils/webviewNonce";

vi.mock("node:crypto", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:crypto")>();
  return { ...actual, randomBytes: vi.fn(actual.randomBytes) };
});

const SRC = path.resolve(__dirname, "../../src");

function sourceFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) return sourceFiles(p);
    return /\.tsx?$/.test(e.name) ? [p] : [];
  });
}

describe("getNonce", () => {
  afterEach(() => vi.restoreAllMocks());

  it("is 128 bits of hex, safe in a CSP source, an attribute and a RegExp", () => {
    const nonce = getNonce();
    expect(nonce).toMatch(new RegExp(`^[0-9a-f]{${NONCE_BYTES * 2}}$`));
  });

  it("draws from crypto.randomBytes, not Math.random", () => {
    const mathRandom = vi.spyOn(Math, "random");
    getNonce();
    expect(crypto.randomBytes).toHaveBeenCalledWith(NONCE_BYTES);
    expect(mathRandom).not.toHaveBeenCalled();
  });

  it("does not repeat", () => {
    const seen = new Set(Array.from({ length: 1000 }, () => getNonce()));
    expect(seen.size).toBe(1000);
  });

  it("is the only nonce source in the extension", () => {
    const helper = path.join(SRC, "utils", "webviewNonce.ts");
    const offenders = sourceFiles(SRC)
      .filter((f) => f !== helper)
      .filter((f) => {
        const text = fs.readFileSync(f, "utf8");
        return (
          /function\s+getNonce\b|getNonce\s*=/.test(text) ||
          /nonce[^\n]*Math\.random|Math\.random[^\n]*nonce/i.test(text) ||
          /nonce:\s*randomBytes/.test(text)
        );
      })
      .map((f) => path.relative(SRC, f));
    expect(offenders).toEqual([]);
  });
});
