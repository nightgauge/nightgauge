/**
 * PEM-block redaction tests.
 *
 * Covers the `indexOf` scanner that replaced the quadratic PEM regex flagged by
 * CodeQL `js/polynomial-redos` (alert #45). The redactor runs over untrusted
 * stage stdout, so both its behaviour and its linear scaling are part of the
 * contract.
 */

import { describe, it, expect } from "vitest";
import { redactSecrets } from "../../src/utils/redaction";

const PRIVATE_KEY = [
  "-----BEGIN RSA PRIVATE KEY-----",
  "MIIEowIBAAKCAQEAxGZ0dGVyIHRlc3Qga2V5IG1hdGVyaWFsIGZvciByZWRhY3Rpb24=",
  "-----END RSA PRIVATE KEY-----",
].join("\n");

describe("redactSecrets — PEM blocks", () => {
  it("redacts a multi-line PEM block", () => {
    const out = redactSecrets(`before\n${PRIVATE_KEY}\nafter`);
    expect(out).toBe("before\n[REDACTED:PEM_BLOCK]\nafter");
    expect(out).not.toContain("MIIEow");
  });

  it("redacts a PEM block carrying literal \\n escapes", () => {
    const escaped = PRIVATE_KEY.split("\n").join("\\n");
    const out = redactSecrets(`key=${escaped}`);
    expect(out).toBe("key=[REDACTED:PEM_BLOCK]");
  });

  it("redacts every block when several appear in one string", () => {
    const out = redactSecrets(`${PRIVATE_KEY}\nmiddle\n${PRIVATE_KEY}`);
    expect(out).toBe("[REDACTED:PEM_BLOCK]\nmiddle\n[REDACTED:PEM_BLOCK]");
  });

  it("redacts labels the old character class did not cover", () => {
    const out = redactSecrets("-----BEGIN OpenSSH Private Key-----abc-----END OpenSSH-----");
    expect(out).toBe("[REDACTED:PEM_BLOCK]");
  });

  it("leaves an unterminated header untouched", () => {
    const text = "-----BEGIN RSA PRIVATE KEY-----no footer here";
    expect(redactSecrets(text)).toBe(text);
  });

  it("leaves text with no PEM header untouched", () => {
    expect(redactSecrets("plain log line")).toBe("plain log line");
  });

  it("scans unterminated headers in linear time", () => {
    // The old regex re-scanned the remainder from every failed header, so this
    // input grew quadratically in cost. Both sizes must cost about the same
    // per byte.
    const build = (n: number) => "-----BEGIN  -----".repeat(n);
    const time = (text: string) => {
      const started = performance.now();
      redactSecrets(text);
      return performance.now() - started;
    };

    time(build(1_000)); // warm up the JIT before measuring
    const small = time(build(2_000));
    const large = time(build(8_000));

    // 4x the input must not cost anywhere near 16x. Generous bound so the
    // assertion fails only on a genuine return to quadratic scaling.
    expect(large).toBeLessThan(Math.max(small, 1) * 8);
  });
});
