/**
 * WorkspaceThrottle.test.ts
 *
 * The platform's workspace throttle as this agent reads and keeps it (#2337):
 * the `throttle` command's payload, the registration response's value, and
 * the state that survives a reload.
 */

import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({}));

import {
  applyRegistrationThrottle,
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

describe("WorkspaceThrottleState", () => {
  it("applies a throttle and keeps it; a clear drops both", async () => {
    const { state, target, memento } = makeState();

    await state.apply({ maxConcurrent: 1, resumeAt: LATER });
    expect(target.setWorkspaceThrottle).toHaveBeenLastCalledWith({
      maxConcurrent: 1,
      resumeAt: LATER,
    });
    expect(memento.values.get(WORKSPACE_THROTTLE_STATE_KEY)).toEqual({
      maxConcurrent: 1,
      resumeAt: LATER,
    });
    expect(state.hasPersisted()).toBe(true);

    await state.apply(null);
    expect(target.setWorkspaceThrottle).toHaveBeenLastCalledWith(null);
    expect(memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);
    expect(state.hasPersisted()).toBe(false);
  });

  it("does not keep a throttle that has already lifted", async () => {
    const { state, target, memento } = makeState();
    await state.apply({ maxConcurrent: 1, resumeAt: EARLIER });
    expect(target.setWorkspaceThrottle).toHaveBeenCalledWith({
      maxConcurrent: 1,
      resumeAt: EARLIER,
    });
    expect(memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);
  });

  it("restores a kept throttle that is still in force", () => {
    const { state, target } = makeState({
      [WORKSPACE_THROTTLE_STATE_KEY]: { maxConcurrent: 2, resumeAt: null },
    });
    state.restore();
    expect(target.setWorkspaceThrottle).toHaveBeenCalledWith({ maxConcurrent: 2, resumeAt: null });
  });

  it.each([
    ["lifted", { maxConcurrent: 2, resumeAt: EARLIER }],
    ["malformed", { maxConcurrent: "two" }],
  ])("drops a %s kept throttle instead of restoring it", async (_kind, kept) => {
    const { state, target, memento } = makeState({ [WORKSPACE_THROTTLE_STATE_KEY]: kept });
    state.restore();
    await vi.waitFor(() => expect(memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false));
    expect(target.setWorkspaceThrottle).not.toHaveBeenCalled();
  });

  it("restores nothing when nothing was kept", () => {
    const { state, target } = makeState();
    state.restore();
    expect(target.setWorkspaceThrottle).not.toHaveBeenCalled();
    expect(state.hasPersisted()).toBe(false);
  });

  it("applies the throttle even when keeping it fails", async () => {
    const { state, target, memento } = makeState();
    memento.update.mockRejectedValueOnce(new Error("storage unavailable"));
    await expect(state.apply({ maxConcurrent: 1, resumeAt: LATER })).resolves.toBeUndefined();
    expect(target.setWorkspaceThrottle).toHaveBeenCalledTimes(1);
  });
});

describe("applyRegistrationThrottle", () => {
  it("applies what the registration reported, including no throttle", async () => {
    const state = { apply: vi.fn().mockResolvedValue(undefined) };

    await applyRegistrationThrottle(
      { getLastThrottle: () => ({ maxConcurrent: 1, resumeAt: null }) },
      state
    );
    expect(state.apply).toHaveBeenLastCalledWith({ maxConcurrent: 1, resumeAt: null });

    await applyRegistrationThrottle({ getLastThrottle: () => null }, state);
    expect(state.apply).toHaveBeenLastCalledWith(null);
  });

  it("changes nothing when the registration reported no valid throttle", async () => {
    const state = { apply: vi.fn().mockResolvedValue(undefined) };
    await applyRegistrationThrottle({ getLastThrottle: () => undefined }, state);
    await applyRegistrationThrottle(null, state);
    expect(state.apply).not.toHaveBeenCalled();
  });
});
