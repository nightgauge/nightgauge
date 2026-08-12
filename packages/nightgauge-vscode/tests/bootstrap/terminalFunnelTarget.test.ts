/**
 * terminalFunnelTarget.test.ts
 *
 * Every terminal outcome of a concurrent slot — completed, failed, deferred —
 * reaches the Go autonomous scheduler through one `autonomousComplete` call,
 * and every one of those calls hangs off a single split of the slot's
 * `repoSlug`. When that slug is absent or unsplittable the whole funnel is
 * skipped, and pre-#302 all three were skipped in total silence: the
 * neighbouring refresh skip logs, these did not.
 *
 * What is lost differs per outcome, so the helper takes the consequence from
 * the caller and names it:
 *
 * - completed — the scheduler never learns the run ended, so `NotifyComplete`
 *   → `onPipelineComplete` never runs, the run is never removed from
 *   `state.Running`, and its concurrency slot is held forever. The fleet stops
 *   dispatching with nothing anywhere saying why. This one is the worst,
 *   because it fires on the SUCCESS path.
 * - failed — the cascade breaker, the lifetime failure cap, and the board
 *   revert never hear about the failure; a fleet could fail indefinitely
 *   without ever tripping.
 * - deferred — the board is not returned to Ready and the blocker-close
 *   requeue never fires.
 *
 * This test imports the real `resolveTerminalFunnelTarget` from
 * bootstrap/services.ts — the whole point of the guard is what the shipped
 * code does when the slug is unusable, and a reimplementation cannot witness
 * that. #404 made this the directory's rule: no test here reimplements shipped
 * logic; tests either import the real symbol or, when the fix is a deletion,
 * assert against the source (the *Removed.test.ts pins). See docs/TESTING.md
 * § Testing Anti-Patterns, "Mirror Tests".
 *
 * @see Issue #302 — four small silent-no-op guards
 */

import { describe, it, expect, vi } from "vitest";

import {
  TERMINAL_FUNNEL_CONSEQUENCE,
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

/** The three shipped consequence texts, keyed by the callback that passes them. */
const consequences: Array<[keyof typeof TERMINAL_FUNNEL_CONSEQUENCE, string]> = [
  ["completed", TERMINAL_FUNNEL_CONSEQUENCE.completed],
  ["failed", TERMINAL_FUNNEL_CONSEQUENCE.failed],
  ["deferred", TERMINAL_FUNNEL_CONSEQUENCE.deferred],
];

describe("resolveTerminalFunnelTarget (#302)", () => {
  it("splits a well-formed slug and says nothing — the funnel runs as before", () => {
    const log = spyLogger();

    expect(
      resolveTerminalFunnelTarget("octocat/acme", 302, TERMINAL_FUNNEL_CONSEQUENCE.failed, log)
    ).toEqual({
      owner: "octocat",
      repo: "acme",
    });
    expect(log.warn).not.toHaveBeenCalled();
  });

  // Each of these reaches the same dead end: no owner/repo, so no
  // autonomousComplete, so no signal of any kind for this terminal outcome.
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

      expect(
        resolveTerminalFunnelTarget(slug, 4242, TERMINAL_FUNNEL_CONSEQUENCE.failed, log)
      ).toBeUndefined();

      // The load-bearing assertion: skipping the funnel must never be silent.
      expect(log.warn).toHaveBeenCalled();
      const text = warnText(log);
      // Name the run, or the operator cannot tell which outcome went unreported.
      expect(text).toContain("4242");
      // Name what was lost, not just that something was skipped.
      expect(text.toLowerCase()).toContain("cascade");
    });
  }

  it("reports the unusable slug value itself, so the cause is diagnosable", () => {
    const log = spyLogger();
    resolveTerminalFunnelTarget("acme", 77, TERMINAL_FUNNEL_CONSEQUENCE.failed, log);

    expect(warnText(log)).toContain("acme");
  });

  it("does not invent a repo — visibility only, never a guessed attribution", () => {
    // Guessing would mis-attribute the breaker window and the lifetime failure
    // cap to a repo that did not fail, which is worse than a loud skip.
    const log = spyLogger();
    expect(
      resolveTerminalFunnelTarget("acme", 77, TERMINAL_FUNNEL_CONSEQUENCE.failed, log)
    ).toBeUndefined();
  });

  /**
   * The helper serves three callbacks that lose three different things. A
   * message that named only the failure path would mis-describe two of them —
   * an operator reading "cascade breaker" after a *successful* run would look
   * in entirely the wrong place for a fleet that has stopped dispatching.
   */
  describe("threads the caller's consequence into the warning", () => {
    for (const [callback, consequence] of consequences) {
      it(`states what ${callback} loses`, () => {
        const log = spyLogger();

        expect(resolveTerminalFunnelTarget(undefined, 909, consequence, log)).toBeUndefined();

        const text = warnText(log);
        // JSON.stringify escapes nothing in these strings, so a raw substring
        // match is exact.
        expect(text).toContain(consequence);
        expect(text).toContain("909");
      });
    }

    it("keeps the three messages distinguishable from one another", () => {
      const texts = consequences.map(([, consequence]) => {
        const log = spyLogger();
        resolveTerminalFunnelTarget(undefined, 1, consequence, log);
        return warnText(log);
      });

      expect(new Set(texts).size).toBe(texts.length);
    });

    it("names the held concurrency slot on the completed path specifically", () => {
      // The success-path skip is the one with no other symptom: no failure, no
      // card, no error — the fleet simply stops. The log has to say so.
      const log = spyLogger();
      resolveTerminalFunnelTarget(undefined, 55, TERMINAL_FUNNEL_CONSEQUENCE.completed, log);

      const text = warnText(log).toLowerCase();
      expect(text).toContain("concurrency slot");
      expect(text).toContain("dispatch");
    });

    it("names the Ready revert and the requeue on the deferred path specifically", () => {
      const log = spyLogger();
      resolveTerminalFunnelTarget(undefined, 56, TERMINAL_FUNNEL_CONSEQUENCE.deferred, log);

      const text = warnText(log).toLowerCase();
      expect(text).toContain("ready");
      expect(text).toContain("requeue");
    });
  });

  /**
   * `repoSlug` is declared `string | undefined`, but it arrives across a
   * JSON/IPC boundary that enforces nothing. Pre-fixup the first statement was
   * `.split()` on `repoSlug ?? ""`, so a number or object THREW inside the
   * slot handler — a louder, later, harder-to-attribute failure than the
   * silent skip this function exists to replace, in the one function #302
   * rewrote to fail loud.
   */
  describe("is total — a non-string slug warns instead of throwing", () => {
    const nonStrings: Array<[string, unknown]> = [
      ["a number", 42],
      ["an object", {}],
      ["an owner/repo object from a mis-serialised envelope", { owner: "octocat", repo: "acme" }],
      ["null", null],
      ["a boolean", false],
      ["an array", ["octocat", "acme"]],
    ];

    for (const [label, slug] of nonStrings) {
      it(`survives ${label}`, () => {
        const log = spyLogger();

        let got: { owner: string; repo: string } | undefined;
        expect(() => {
          got = resolveTerminalFunnelTarget(
            slug as unknown as string | undefined,
            808,
            TERMINAL_FUNNEL_CONSEQUENCE.completed,
            log
          );
        }).not.toThrow();

        expect(got).toBeUndefined();
        expect(log.warn).toHaveBeenCalled();
        expect(warnText(log)).toContain("808");
      });
    }

    it("renders the offending value so the boundary bug is diagnosable", () => {
      const log = spyLogger();
      resolveTerminalFunnelTarget(
        42 as unknown as string,
        808,
        TERMINAL_FUNNEL_CONSEQUENCE.failed,
        log
      );

      expect(warnText(log)).toContain("42");
    });
  });
});
