/**
 * #2770: pr-merge's wall-clock budget leaves out the merge-queue wait the
 * binary recorded. The arithmetic is the binary's; these pin how the editor's
 * enforcer uses the answer.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  StageBudgetEnforcer,
  queueWaitExcludedMs,
  type StageBudgetBreach,
  type StageBudgets,
} from "../../src/utils/stageBudget";

const LIMIT = 1_000;
const budgets: StageBudgets = {
  maxTurns: -1,
  maxWallClockMs: LIMIT,
  maxTokens: -1,
  zeroCost: false,
};

function enforcer(excludedMs?: (sinceMs: number, nowMs: number) => Promise<number>) {
  const breaches: StageBudgetBreach[] = [];
  const e = new StageBudgetEnforcer({
    format: "claude",
    onBreach: (b) => breaches.push(b),
    now: () => Date.now(),
    ...(excludedMs ? { excludedMs } : {}),
  });
  e.start();
  e.arm(budgets);
  return { e, breaches };
}

describe("StageBudgetEnforcer queue-wait exclusion (#2770)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("does not fire at the limit while the queue wait is left out, and fires at limit + queue time", async () => {
    // Enqueued 200 ms in, merged 1 400 ms in: 1 200 ms of queue wait.
    const queue = (sinceMs: number, nowMs: number) =>
      Promise.resolve(Math.max(0, Math.min(nowMs, 1_400) - Math.max(sinceMs, 200)));
    const { breaches } = enforcer(queue);
    await vi.advanceTimersByTimeAsync(LIMIT);
    expect(breaches).toEqual([]);
    await vi.advanceTimersByTimeAsync(1_199);
    expect(breaches).toEqual([]);
    await vi.advanceTimersByTimeAsync(1);
    expect(breaches).toEqual([{ dimension: "wall_clock", observed: LIMIT, ceiling: LIMIT }]);
  });

  it("counts every millisecond with no recorded queue wait (review fixes before enqueue)", async () => {
    const { breaches } = enforcer(() => Promise.resolve(0));
    await vi.advanceTimersByTimeAsync(LIMIT);
    expect(breaches).toEqual([{ dimension: "wall_clock", observed: LIMIT, ceiling: LIMIT }]);
  });

  it("never leaves out more than elapsed time", async () => {
    // An answer larger than elapsed (10 000 ms of 1 000) is clamped to the
    // 1 000 elapsed: counted 0, so the timer re-arms for one limit, not for
    // the 10 000 the raw answer would buy.
    const answers = [10 * LIMIT, 0];
    const { breaches } = enforcer(() => Promise.resolve(answers.shift() ?? 0));
    await vi.advanceTimersByTimeAsync(LIMIT);
    expect(breaches).toEqual([]);
    await vi.advanceTimersByTimeAsync(LIMIT);
    expect(breaches).toEqual([{ dimension: "wall_clock", observed: 2 * LIMIT, ceiling: LIMIT }]);
  });

  it("a failed lookup leaves nothing out", async () => {
    const { breaches } = enforcer(() => Promise.reject(new Error("binary missing")));
    await vi.advanceTimersByTimeAsync(LIMIT);
    expect(breaches).toEqual([{ dimension: "wall_clock", observed: LIMIT, ceiling: LIMIT }]);
  });

  it("does not fire after disarm while a lookup is in flight", async () => {
    let answer: (n: number) => void = () => undefined;
    const { e, breaches } = enforcer(() => new Promise<number>((r) => (answer = r)));
    await vi.advanceTimersByTimeAsync(LIMIT);
    e.disarm();
    answer(0);
    await vi.advanceTimersByTimeAsync(10 * LIMIT);
    expect(breaches).toEqual([]);
  });
});

describe("queueWaitExcludedMs", () => {
  it("asks `pr-stage queue-wait` and reads excluded_ms", async () => {
    const calls: string[][] = [];
    const got = await queueWaitExcludedMs("/bin/ng", "/repo", 2770, 1234.7, (bin, args) => {
      calls.push([bin, ...args]);
      return Promise.resolve('{"excluded_ms": 5000}\n');
    });
    expect(got).toBe(5000);
    expect(calls).toEqual([
      [
        "/bin/ng",
        "pr-stage",
        "queue-wait",
        "2770",
        "--since-ms",
        "1234",
        "--workdir",
        "/repo",
        "--json",
      ],
    ]);
  });

  it("reads a malformed answer as nothing excluded", async () => {
    expect(
      await queueWaitExcludedMs("b", "/r", 1, 1, () => Promise.resolve('{"excluded_ms": -3}'))
    ).toBe(0);
  });
});
