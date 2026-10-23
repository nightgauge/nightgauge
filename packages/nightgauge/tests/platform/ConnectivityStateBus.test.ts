/**
 * ConnectivityStateBus.test.ts — singleton bus that broadcasts network state.
 *
 * @see Issue #3203 - Pipeline pause-on-offline
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ConnectionState } from "../../src/platform/types";

vi.mock("vscode", () => {
  class MockEventEmitter<T> {
    private listeners: ((e: T) => void)[] = [];
    event = (listener: (e: T) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          const idx = this.listeners.indexOf(listener);
          if (idx >= 0) this.listeners.splice(idx, 1);
        },
      };
    };
    fire(data: T) {
      for (const l of this.listeners) l(data);
    }
    dispose() {
      this.listeners = [];
    }
  }
  return { EventEmitter: MockEventEmitter };
});

import { ConnectivityStateBus } from "../../src/platform/ConnectivityStateBus";

describe("ConnectivityStateBus", () => {
  beforeEach(() => {
    ConnectivityStateBus.resetForTests();
  });

  it("defaults to online state", () => {
    expect(ConnectivityStateBus.state).toBe<ConnectionState>("online");
  });

  it("emits a change event with previous and current state on transition", () => {
    const observed: Array<{ previous: ConnectionState; current: ConnectionState }> = [];
    const sub = ConnectivityStateBus.onChanged((evt) =>
      observed.push({ previous: evt.previous, current: evt.current })
    );

    ConnectivityStateBus.set("degraded");
    ConnectivityStateBus.set("offline");
    ConnectivityStateBus.set("online");

    sub.dispose();
    expect(observed).toEqual([
      { previous: "online", current: "degraded" },
      { previous: "degraded", current: "offline" },
      { previous: "offline", current: "online" },
    ]);
  });

  it("does not emit when set to the same state", () => {
    const fired: ConnectionState[] = [];
    const sub = ConnectivityStateBus.onChanged((evt) => fired.push(evt.current));

    ConnectivityStateBus.set("online"); // same as current
    expect(fired).toEqual([]);

    ConnectivityStateBus.set("offline");
    ConnectivityStateBus.set("offline"); // duplicate
    expect(fired).toEqual(["offline"]);

    sub.dispose();
  });

  it("includes an ISO timestamp on every event", () => {
    const at: string[] = [];
    const sub = ConnectivityStateBus.onChanged((evt) => at.push(evt.at));

    ConnectivityStateBus.set("offline");
    sub.dispose();

    expect(at).toHaveLength(1);
    expect(() => new Date(at[0]).toISOString()).not.toThrow();
  });
});
