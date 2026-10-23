/**
 * WorkspaceThrottle.test.ts
 *
 * The platform's workspace throttle as this window reads and keeps it
 * (#2337): the `throttle` command's payload, a reported throttle value, and
 * the per-workspace state that survives a reload.
 */

import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({}));

import {
  describeWorkspaceThrottle,
  parseThrottleCommand,
  parseThrottleValue,
  throttleInForce,
  WORKSPACE_THROTTLE_STATE_KEY,
  WorkspaceThrottleState,
} from "../../src/services/WorkspaceThrottle";

const NOW = Date.parse("2026-10-02T12:00:00.000Z");
const LATER = "2026-10-02T13:00:00.000Z";
const EARLIER = "2026-10-02T11:00:00.000Z";

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

/** A Memento over a plain map. */
function makeMemento(initial: Record<string, unknown> = {}) {
  const values = new Map(Object.entries(initial));
  return {
    values,
    get: vi.fn(<T>(key: string) => values.get(key) as T | undefined),
    update: vi.fn(async (key: string, value: unknown) => {
      if (value === undefined) values.delete(key);
      else values.set(key, value);
    }),
  };
}

function makeState(initial: Record<string, unknown> = {}) {
  const target = { setWorkspaceThrottle: vi.fn() };
  const memento = makeMemento(initial);
  const state = new WorkspaceThrottleState(
    target,
    memento as never,
    makeLogger() as never,
    () => NOW
  );
  return { state, target, memento };
}

describe("parseThrottleCommand", () => {
  it("reads a set, with and without an end", () => {
    expect(parseThrottleCommand({ action: "set", maxConcurrent: 1, resumeAt: LATER })).toEqual({
      throttle: { maxConcurrent: 1, resumeAt: LATER },
    });
    expect(parseThrottleCommand({ action: "set", maxConcurrent: 0, resumeAt: null })).toEqual({
      throttle: { maxConcurrent: 0, resumeAt: null },
    });
    expect(parseThrottleCommand({ action: "set", maxConcurrent: 2 })).toEqual({
      throttle: { maxConcurrent: 2, resumeAt: null },
    });
  });

  it("reads a clear as no throttle", () => {
    expect(
      parseThrottleCommand({ action: "cleared", maxConcurrent: null, resumeAt: null })
    ).toEqual({ throttle: null });
  });

  it.each([
    [null],
    ["set"],
    [{ action: "pause" }],
    [{ action: "set" }],
    [{ action: "set", maxConcurrent: -1 }],
    [{ action: "set", maxConcurrent: 1.5 }],
    [{ action: "set", maxConcurrent: "2" }],
    [{ action: "set", maxConcurrent: 1, resumeAt: "not a time" }],
    [{ action: "set", maxConcurrent: 1, resumeAt: 12 }],
  ])("refuses %j", (payload) => {
    expect(parseThrottleCommand(payload)).toHaveProperty("invalid");
  });
});

describe("parseThrottleValue", () => {
  it("tells no throttle from a missing or malformed one", () => {
    expect(parseThrottleValue(null)).toBeNull();
    expect(parseThrottleValue(undefined)).toBeUndefined();
    expect(parseThrottleValue({ maxConcurrent: "1" })).toBeUndefined();
    expect(
      parseThrottleValue({ maxConcurrent: 1, resumeAt: LATER, setBy: "u", setAt: EARLIER })
    ).toEqual({ maxConcurrent: 1, resumeAt: LATER });
  });
});

describe("throttleInForce", () => {
  it("holds until resumeAt, and for ever with no end", () => {
    expect(throttleInForce({ maxConcurrent: 1, resumeAt: LATER }, NOW)).toBe(true);
    expect(throttleInForce({ maxConcurrent: 1, resumeAt: EARLIER }, NOW)).toBe(false);
    expect(throttleInForce({ maxConcurrent: 1, resumeAt: null }, NOW)).toBe(true);
  });
});

describe("describeWorkspaceThrottle", () => {
  it("names the cap and when it ends", () => {
    expect(describeWorkspaceThrottle({ maxConcurrent: 1, resumeAt: null })).toBe(
      "1 run at once until it is cleared"
    );
    expect(describeWorkspaceThrottle({ maxConcurrent: 0, resumeAt: LATER })).toBe(
      `0 runs at once until ${new Date(LATER).toLocaleString()}`
    );
  });
});

describe("WorkspaceThrottleState", () => {
  it("applies a throttle and keeps it with its workspace; a clear drops both", async () => {
    const { state, target, memento } = makeState();

    await state.apply("alpha", { maxConcurrent: 1, resumeAt: LATER });
    expect(target.setWorkspaceThrottle).toHaveBeenLastCalledWith({
      maxConcurrent: 1,
      resumeAt: LATER,
    });
    expect(memento.values.get(WORKSPACE_THROTTLE_STATE_KEY)).toEqual({
      slug: "alpha",
      maxConcurrent: 1,
      resumeAt: LATER,
    });

    await state.apply("alpha", null);
    expect(target.setWorkspaceThrottle).toHaveBeenLastCalledWith(null);
    expect(memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);
  });

  it("does not keep a throttle that has already lifted, or one for no workspace", async () => {
    const { state, target, memento } = makeState();
    await state.apply("alpha", { maxConcurrent: 1, resumeAt: EARLIER });
    expect(target.setWorkspaceThrottle).toHaveBeenCalledWith({
      maxConcurrent: 1,
      resumeAt: EARLIER,
    });
    expect(memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);

    await state.apply(null, { maxConcurrent: 1, resumeAt: null });
    expect(memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);
  });

  it("restores a kept throttle that is still in force, for its own workspace", () => {
    const { state, target } = makeState({
      [WORKSPACE_THROTTLE_STATE_KEY]: { slug: "alpha", maxConcurrent: 2, resumeAt: null },
    });
    state.restore("alpha");
    expect(target.setWorkspaceThrottle).toHaveBeenCalledWith({ maxConcurrent: 2, resumeAt: null });
  });

  it.each([
    ["lifted", { slug: "alpha", maxConcurrent: 2, resumeAt: EARLIER }, "alpha"],
    ["malformed", { slug: "alpha", maxConcurrent: "two" }, "alpha"],
    ["another workspace's", { slug: "beta", maxConcurrent: 2, resumeAt: null }, "alpha"],
    ["unattributed", { maxConcurrent: 2, resumeAt: null }, "alpha"],
    ["no-workspace", { slug: "alpha", maxConcurrent: 2, resumeAt: null }, null],
  ])("drops a %s kept throttle instead of restoring it", async (_kind, kept, slug) => {
    const { state, target, memento } = makeState({ [WORKSPACE_THROTTLE_STATE_KEY]: kept });
    state.restore(slug);
    await vi.waitFor(() => expect(memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false));
    expect(target.setWorkspaceThrottle).not.toHaveBeenCalled();
  });

  it("restores nothing when nothing was kept", () => {
    const { state, target } = makeState();
    state.restore("alpha");
    expect(target.setWorkspaceThrottle).not.toHaveBeenCalled();
  });

  it("lifts the cap and forgets the kept throttle on clear", async () => {
    const { state, target, memento } = makeState({
      [WORKSPACE_THROTTLE_STATE_KEY]: { slug: "alpha", maxConcurrent: 0, resumeAt: null },
    });
    await state.clear();
    expect(target.setWorkspaceThrottle).toHaveBeenCalledWith(null);
    expect(memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);
  });

  it("applies the throttle even when keeping it fails", async () => {
    const { state, target, memento } = makeState();
    memento.update.mockRejectedValueOnce(new Error("storage unavailable"));
    await expect(
      state.apply("alpha", { maxConcurrent: 1, resumeAt: LATER })
    ).resolves.toBeUndefined();
    expect(target.setWorkspaceThrottle).toHaveBeenCalledTimes(1);
  });
});
