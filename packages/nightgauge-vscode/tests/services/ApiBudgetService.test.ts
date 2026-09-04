/**
 * The measured API spend meter (Issue #1347).
 *
 * The behaviour worth pinning is not the happy path — it is that every
 * "cannot tell" outcome renders as ABSENT rather than as zero. A meter that
 * reports zero spend when it cannot read the ledger draws a quiet workspace
 * over a runaway one, which is worse than showing nothing at all.
 */

import { describe, expect, it } from "vitest";
import { readApiBudget } from "../../src/services/apiBudget/ApiBudgetService";

const deps = (run: (b: string, a: string[], c: string) => Promise<{ stdout: string }>) => ({
  binaryPath: "/usr/local/bin/nightgauge",
  cwd: "/w",
  run,
});

describe("readApiBudget", () => {
  it("reads points, calls and the top caller from the binary's aggregation", async () => {
    const reading = await readApiBudget(
      deps(async () => ({
        stdout: JSON.stringify({
          records: 12,
          points: 3400,
          by: "caller",
          groups: [
            { key: "sweep.Producers", points: 3000, calls: 9 },
            { key: "depgraph.Rebuild", points: 400, calls: 3 },
          ],
        }),
      }))
    );
    expect(reading).toEqual({
      points: 3400,
      calls: 12,
      topCaller: "sweep.Producers",
      topCallerPoints: 3000,
    });
  });

  it("asks the binary for the one-hour GraphQL window grouped by caller", async () => {
    let seen: string[] = [];
    await readApiBudget(
      deps(async (_b, args) => {
        seen = args;
        return { stdout: JSON.stringify({ points: 0, groups: [] }) };
      })
    );
    // The --resource filter is load-bearing, not cosmetic: without it the
    // reading sums two separate quotas and renders ~30x too large against the
    // GraphQL limit the status bar compares it to.
    expect(seen).toEqual([
      "api-usage",
      "--since",
      "1h",
      "--by",
      "caller",
      "--resource",
      "graphql",
      "--json",
    ]);
  });

  it("reports a genuinely empty window as zero spend, not as unmeasurable", async () => {
    const reading = await readApiBudget(
      deps(async () => ({
        stdout:
          "no ledger records in the last 1h0m0s — the workspace made no GitHub requests in that window\n",
      }))
    );
    expect(reading).toEqual({ points: 0, calls: 0, topCaller: null, topCallerPoints: 0 });
  });

  it("returns null when the binary cannot be run", async () => {
    const reading = await readApiBudget(
      deps(async () => {
        throw new Error("ENOENT");
      })
    );
    expect(reading).toBeNull();
  });

  it("returns null on unparseable output rather than substituting zero", async () => {
    expect(
      await readApiBudget(deps(async () => ({ stdout: "<html>proxy error</html>" })))
    ).toBeNull();
  });

  it("returns null when the payload carries no point total", async () => {
    expect(
      await readApiBudget(deps(async () => ({ stdout: JSON.stringify({ groups: [] }) })))
    ).toBeNull();
  });

  it("skips zero-point callers — a caller that cost nothing cannot be the answer", async () => {
    const reading = await readApiBudget(
      deps(async () => ({
        stdout: JSON.stringify({
          points: 17,
          groups: [
            { key: "chatty.But.Free", points: 0, calls: 400 },
            { key: "board.Read", points: 17, calls: 1 },
          ],
        }),
      }))
    );
    expect(reading?.topCaller).toBe("board.Read");
  });

  it("reports no attribution rather than inventing one when nothing was priced", async () => {
    const reading = await readApiBudget(
      deps(async () => ({
        stdout: JSON.stringify({ points: 0, groups: [{ key: "free.Loop", points: 0, calls: 5 }] }),
      }))
    );
    expect(reading).toEqual({ points: 0, calls: 5, topCaller: null, topCallerPoints: 0 });
  });
});
