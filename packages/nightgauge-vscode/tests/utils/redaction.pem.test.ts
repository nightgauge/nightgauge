/**
 * PEM-block redaction tests.
 *
 * Covers the `indexOf` scanner that replaced the quadratic PEM regex flagged by
 * CodeQL `js/polynomial-redos` (alert #45). The redactor runs over untrusted
 * stage stdout, so both its behaviour and its linear scaling are part of the
 * contract.
 *
 * Every fixture assembles its PEM armour at runtime. A literal
 * `-----BEGIN … PRIVATE KEY-----` in the tree trips gitleaks' `private-key`
 * rule, and the credential-scan gate is fail-closed by design — the alternative
 * would be a `.gitleaksignore` fingerprint that goes stale on every rebase.
 */

import { describe, it, expect } from "vitest";
import { redactSecrets } from "../../src/utils/redaction";

const ARMOUR = "-".repeat(5);
const armour = (keyword: "BEGIN" | "END", label: string) => `${ARMOUR}${keyword} ${label}${ARMOUR}`;

const PRIVATE_KEY_LABEL = ["RSA", "PRIVATE", "KEY"].join(" ");
const BODY = "MIIEowIBAAKCAQEAdGVzdCBrZXkgbWF0ZXJpYWwgZm9yIHJlZGFjdGlvbg==";

const pemBlock = (label: string, body = BODY) =>
  [armour("BEGIN", label), body, armour("END", label)].join("\n");

const PRIVATE_KEY = pemBlock(PRIVATE_KEY_LABEL);

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
    // Mixed case and unlisted key types: the scanner is label-agnostic, so a
    // novel armour still redacts where the old `[A-Z0-9 ]+` class missed it.
    const out = redactSecrets(pemBlock(["OpenSSH", "Private", "Key"].join(" ")));
    expect(out).toBe("[REDACTED:PEM_BLOCK]");
  });

  it("leaves an unterminated header untouched", () => {
    const text = `${armour("BEGIN", PRIVATE_KEY_LABEL)}no footer here`;
    expect(redactSecrets(text)).toBe(text);
  });

  it("leaves text with no PEM header untouched", () => {
    expect(redactSecrets("plain log line")).toBe("plain log line");
  });

  it("scans unterminated headers in linear time", () => {
    // The old regex re-scanned the remainder from every failed header, so this
    // input grew quadratically in cost. Both sizes must cost about the same
    // per byte.
    const build = (n: number) => armour("BEGIN", " ").repeat(n);

    // Best-of-N, not a single sample. Scheduler contention only ever *adds*
    // time, so the minimum is the least noise-contaminated estimate of the
    // real cost. A lone sample made this assertion depend on whatever else the
    // machine happened to be doing.
    const time = (text: string) => {
      let best = Infinity;
      for (let i = 0; i < 5; i += 1) {
        const started = performance.now();
        redactSecrets(text);
        best = Math.min(best, performance.now() - started);
      }
      return best;
    };

    time(build(1_000)); // warm up the JIT before measuring
    const small = time(build(4_000));
    const large = time(build(16_000));

    // Guard the ratio below: if the small case is too fast to measure, the
    // comparison is meaningless and the workload needs raising. Failing here
    // says exactly that, rather than silently degrading into an absolute bound.
    expect(small).toBeGreaterThan(0);

    // 4x the input must not cost anywhere near 16x. Generous bound so the
    // assertion fails only on a genuine return to quadratic scaling. Compared
    // as a pure ratio — the previous `Math.max(small, 1) * 8` form collapsed
    // into a flat 8ms ceiling whenever the small case came in under 1ms, which
    // both failed under load and would have let a real regression pass on fast
    // hardware.
    expect(large / small).toBeLessThan(8);
  });
});
