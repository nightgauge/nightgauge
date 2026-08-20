import { describe, expect, it, vi } from "vitest";
import { ACQUIRE_ATTEMPTS, ACQUIRE_BACKOFF_MS, acquireVSCode } from "./acquireVSCode";
import type { AcquireDeps } from "./acquireVSCode";

/**
 * A retry that has never been exercised is a guess. #770 was a real, once-seen
 * network failure; these assertions pin the two properties that decide whether
 * absorbing it was the right call — that it recovers from a transient blip, and
 * that a sustained outage still goes red and still says so out loud.
 */

function harness(download: AcquireDeps["download"]) {
  const slept: number[] = [];
  const logged: string[] = [];
  const warned: string[] = [];
  const deps: AcquireDeps = {
    download,
    sleep: async (ms) => {
      slept.push(ms);
    },
    log: (m) => logged.push(m),
    warn: (m) => warned.push(m),
  };
  return { deps, slept, logged, warned };
}

/** The failure actually observed on `main` — a TCP connect timeout. */
function timeout(): Error {
  const err = new Error("connect ETIMEDOUT update.code.visualstudio.com");
  err.name = "AggregateError";
  return err;
}

describe("acquireVSCode retry", () => {
  it("returns the executable and never sleeps when the first attempt works", async () => {
    const download = vi.fn().mockResolvedValue("/tmp/vscode/code");
    const { deps, slept, logged, warned } = harness(download);

    expect(await acquireVSCode(deps)).toBe("/tmp/vscode/code");
    expect(download).toHaveBeenCalledTimes(1);
    // The healthy path must stay silent: a retry that narrates itself on every
    // green run trains readers to skim past the message that matters.
    expect(slept).toEqual([]);
    expect(logged).toEqual([]);
    expect(warned).toEqual([]);
  });

  it("recovers from a transient failure and reports which attempt succeeded", async () => {
    const download = vi.fn().mockRejectedValueOnce(timeout()).mockResolvedValue("/tmp/vscode/code");
    const { deps, slept, logged, warned } = harness(download);

    expect(await acquireVSCode(deps)).toBe("/tmp/vscode/code");
    expect(download).toHaveBeenCalledTimes(2);
    expect(slept).toEqual([ACQUIRE_BACKOFF_MS[0]]);
    expect(logged).toEqual(["VSCode acquired on attempt 2/3."]);
    // Absorbed, not hidden — the failed attempt is still on the record.
    expect(warned).toHaveLength(1);
    expect(warned[0]).toContain("ETIMEDOUT");
  });

  it("uses the whole budget before succeeding on the last attempt", async () => {
    const download = vi
      .fn()
      .mockRejectedValueOnce(timeout())
      .mockRejectedValueOnce(timeout())
      .mockResolvedValue("/tmp/vscode/code");
    const { deps, slept } = harness(download);

    expect(await acquireVSCode(deps)).toBe("/tmp/vscode/code");
    expect(download).toHaveBeenCalledTimes(ACQUIRE_ATTEMPTS);
    expect(slept).toEqual(ACQUIRE_BACKOFF_MS);
  });

  it("gives up after the bounded number of attempts and rethrows the last error", async () => {
    const last = new Error("connect ECONNREFUSED");
    const download = vi
      .fn()
      .mockRejectedValueOnce(timeout())
      .mockRejectedValueOnce(timeout())
      .mockRejectedValueOnce(last);
    const { deps, slept, warned } = harness(download);

    // A sustained outage must still fail the tier. The point of the retry is to
    // absorb a blip, not to make the tier unable to report that VSCode is
    // unreachable.
    await expect(acquireVSCode(deps)).rejects.toBe(last);
    expect(download).toHaveBeenCalledTimes(ACQUIRE_ATTEMPTS);
    // No sleep after the final failure — that delay would buy nothing.
    expect(slept).toEqual(ACQUIRE_BACKOFF_MS);
    // Every attempt is on the record, so an outage reads as a trend.
    expect(warned).toHaveLength(ACQUIRE_ATTEMPTS);
  });

  it("has one backoff fewer than it has attempts", () => {
    expect(ACQUIRE_BACKOFF_MS).toHaveLength(ACQUIRE_ATTEMPTS - 1);
  });
});
