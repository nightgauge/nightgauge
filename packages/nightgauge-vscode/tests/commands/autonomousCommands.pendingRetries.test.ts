/**
 * Tests for pending-retry visibility (Issue #195).
 *
 * A run waiting out a backoff after a transient failure used to be invisible:
 * it left `running`, its board item flipped back to Ready, and the badge read
 * "0 running, N remaining" — exactly what an idle queue looks like. An operator
 * watching a fleet recover from a provider outage saw a stalled one.
 *
 * @see src/commands/autonomousCommands.ts
 * @see src/utils/statusBar.ts
 */

import { describe, it, expect } from "vitest";
import { pendingRetriesInFuture } from "../../src/commands/autonomousCommands";

const at = (msFromNow: number) => new Date(Date.now() + msFromNow).toISOString();

describe("pendingRetriesInFuture", () => {
  it("returns nothing for absent or empty input", () => {
    expect(pendingRetriesInFuture(undefined)).toEqual([]);
    expect(pendingRetriesInFuture([])).toEqual([]);
  });

  it("drops deadlines that have already passed", () => {
    // The Go side filters at projection time, but the snapshot ages between
    // pushes — announcing a retry that is already due would be its own lie.
    const got = pendingRetriesInFuture([
      { repo: "o/r", number: 1, retryAfter: at(-60_000), attempts: 1 },
    ]);
    expect(got).toEqual([]);
  });

  it("orders surviving entries soonest-first", () => {
    const got = pendingRetriesInFuture([
      { repo: "o/r", number: 2, retryAfter: at(600_000), attempts: 3 },
      { repo: "o/r", number: 3, retryAfter: at(120_000), attempts: 1 },
      { repo: "o/r", number: 1, retryAfter: at(-1_000), attempts: 9 },
    ]);
    expect(got.map((p) => p.retry.number)).toEqual([3, 2]);
    expect(got[0].until.getTime()).toBeLessThan(got[1].until.getTime());
  });

  it("carries the reason and attempt count through to the caller", () => {
    const got = pendingRetriesInFuture([
      {
        repo: "nightgauge/nightgauge",
        number: 135,
        retryAfter: at(300_000),
        kind: "api_overloaded",
        reason: "provider overloaded",
        attempts: 2,
      },
    ]);
    expect(got).toHaveLength(1);
    expect(got[0].retry).toMatchObject({
      repo: "nightgauge/nightgauge",
      number: 135,
      kind: "api_overloaded",
      reason: "provider overloaded",
      attempts: 2,
    });
  });

  it("ignores unparseable timestamps rather than rendering Invalid Date", () => {
    const got = pendingRetriesInFuture([
      { repo: "o/r", number: 1, retryAfter: "not-a-date", attempts: 1 },
      { repo: "o/r", number: 2, retryAfter: at(60_000), attempts: 1 },
    ]);
    expect(got.map((p) => p.retry.number)).toEqual([2]);
  });
});
