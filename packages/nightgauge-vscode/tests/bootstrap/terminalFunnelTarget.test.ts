/**
 * terminalFunnelTarget.test.ts
 *
 * The autonomous terminal funnel — conflict-restart check, terminal-kind
 * signal, and `IpcClient.autonomousComplete(false, …)`, which feeds the
 * cascade circuit breaker, the lifetime failure cap, and the board revert —
 * hangs off one split of the failed slot's `repoSlug`. When that slug is
 * absent or unsplittable the whole funnel is skipped, and pre-#302 it was
 * skipped in total silence: the neighbouring refresh skip logs, this one did
 * not. A fleet whose runs cannot name their repo would fail indefinitely
 * without the breaker ever hearing about a single failure.
 *
 * Unlike the mirror-style tests in this directory, this one imports the real
 * `resolveTerminalFunnelTarget` from bootstrap/services.ts — the whole point
 * of the guard is what the shipped code does when the slug is unusable, and a
 * reimplementation cannot witness that.
 *
 * @see Issue #302 — four small silent-no-op guards
 */

import { describe, it, expect, vi } from "vitest";

import {
  resolveTerminalFunnelTarget,
  type TerminalFunnelLogger,
} from "../../src/bootstrap/services";

function spyLogger(): TerminalFunnelLogger & { warn: ReturnType<typeof vi.fn> } {
  return { warn: vi.fn() };
}

/** Every warn() call flattened to one searchable string. */
function warnText(log: { warn: ReturnType<typeof vi.fn> }): string {
  return log.warn.mock.calls.map((args) => JSON.stringify(args)).join("\n");
}

describe("resolveTerminalFunnelTarget (#302)", () => {
  it("splits a well-formed slug and says nothing — the funnel runs as before", () => {
    const log = spyLogger();

    expect(resolveTerminalFunnelTarget("octocat/acme", 302, log)).toEqual({
      owner: "octocat",
      repo: "acme",
    });
    expect(log.warn).not.toHaveBeenCalled();
  });

  // Each of these reaches the same dead end: no owner/repo, so no
  // autonomousComplete, so no cascade-breaker input for this failure.
  const unusable: Array<[string, string | undefined]> = [
    ["undefined (the slot never resolved a repo)", undefined],
    ["empty string", ""],
    ["bare repo name with no owner", "acme"],
    ["leading slash — empty owner", "/acme"],
    ["trailing slash — empty repo", "octocat/"],
    ["a lone separator", "/"],
  ];

  for (const [label, slug] of unusable) {
    it(`refuses to resolve, and SAYS SO, for ${label}`, () => {
      const log = spyLogger();

      expect(resolveTerminalFunnelTarget(slug, 4242, log)).toBeUndefined();

      // The load-bearing assertion: skipping the funnel must never be silent.
      expect(log.warn).toHaveBeenCalled();
      const text = warnText(log);
      // Name the run, or the operator cannot tell which failure went unreported.
      expect(text).toContain("4242");
      // Name what was lost, not just that something was skipped.
      expect(text.toLowerCase()).toContain("cascade");
    });
  }

  it("reports the unusable slug value itself, so the cause is diagnosable", () => {
    const log = spyLogger();
    resolveTerminalFunnelTarget("acme", 77, log);

    expect(warnText(log)).toContain("acme");
  });

  it("does not invent a repo — visibility only, never a guessed attribution", () => {
    // Guessing would mis-attribute the breaker window and the lifetime failure
    // cap to a repo that did not fail, which is worse than a loud skip.
    const log = spyLogger();
    expect(resolveTerminalFunnelTarget("acme", 77, log)).toBeUndefined();
  });
});
