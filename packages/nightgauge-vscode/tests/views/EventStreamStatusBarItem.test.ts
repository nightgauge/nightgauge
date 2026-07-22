import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  window: {
    createStatusBarItem: vi.fn(() => ({
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: undefined,
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  StatusBarAlignment: { Left: 1, Right: 2 },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  EventEmitter: vi.fn(() => {
    const listeners: Array<(val: unknown) => void> = [];
    return {
      event: (cb: (val: unknown) => void) => {
        listeners.push(cb);
        return { dispose: () => listeners.splice(listeners.indexOf(cb), 1) };
      },
      fire: (val: unknown) => listeners.forEach((cb) => cb(val)),
      dispose: vi.fn(),
    };
  }),
}));

import { EventStreamStatusBarItem } from "../../src/views/EventStreamStatusBarItem";

function makeEmitter<T>() {
  const listeners: Array<(val: T) => void> = [];
  const event = (cb: (val: T) => void) => {
    listeners.push(cb);
    return { dispose: () => listeners.splice(listeners.indexOf(cb), 1) };
  };
  const fire = (val: T) => listeners.forEach((cb) => cb(val));
  return { event, fire };
}

function makeStreams() {
  const accountEmitter = makeEmitter<{ status: string; label: string }>();
  const projectEmitter = makeEmitter<{ status: string; label: string }>();

  const accountStream = { onStreamStatusChanged: accountEmitter.event } as Parameters<
    EventStreamStatusBarItem["attachStreams"]
  >[0];
  const projectStream = { onSseStatusChanged: projectEmitter.event } as Parameters<
    EventStreamStatusBarItem["attachStreams"]
  >[1];

  return { accountStream, projectStream, accountEmitter, projectEmitter };
}

describe("EventStreamStatusBarItem", () => {
  let bar: EventStreamStatusBarItem;

  beforeEach(() => {
    vi.clearAllMocks();
    bar = new EventStreamStatusBarItem("nightgauge.reconnectEventStreams");
  });

  it("starts in idle state with radio-tower icon", () => {
    expect(bar.item.text).toBe("$(radio-tower)");
    expect(bar.getAggregatedStatus()).toBe("idle");
  });

  it("shows connected icon when both streams connect", () => {
    const { accountStream, projectStream, accountEmitter, projectEmitter } = makeStreams();
    bar.attachStreams(accountStream, projectStream);

    accountEmitter.fire({ status: "connected", label: "● live" });
    projectEmitter.fire({ status: "connected", label: "● live" });

    expect(bar.item.text).toBe("$(radio-tower)");
    expect(bar.getAggregatedStatus()).toBe("connected");
  });

  it("shows reconnecting icon when one stream is reconnecting", () => {
    const { accountStream, projectStream, accountEmitter, projectEmitter } = makeStreams();
    bar.attachStreams(accountStream, projectStream);

    accountEmitter.fire({ status: "connected", label: "● live" });
    projectEmitter.fire({ status: "reconnecting", label: "↻ reconnecting" });

    expect(bar.item.text).toBe("$(sync~spin)");
    expect(bar.getAggregatedStatus()).toBe("reconnecting");
  });

  it("shows disconnected icon (worst-of) when one stream disconnects and other is connected", () => {
    const { accountStream, projectStream, accountEmitter, projectEmitter } = makeStreams();
    bar.attachStreams(accountStream, projectStream);

    accountEmitter.fire({ status: "connected", label: "● live" });
    projectEmitter.fire({ status: "disconnected", label: "✕ auth error" });

    expect(bar.item.text).toBe("$(plug)");
    expect(bar.getAggregatedStatus()).toBe("disconnected");
  });

  it("disconnected beats reconnecting in worst-of aggregation", () => {
    const { accountStream, projectStream, accountEmitter, projectEmitter } = makeStreams();
    bar.attachStreams(accountStream, projectStream);

    accountEmitter.fire({ status: "reconnecting", label: "↻ reconnecting" });
    projectEmitter.fire({ status: "disconnected", label: "✕ auth error" });

    expect(bar.getAggregatedStatus()).toBe("disconnected");
  });

  it("captures last error label from non-connected events", () => {
    const { accountStream, projectStream, accountEmitter } = makeStreams();
    bar.attachStreams(accountStream, projectStream);

    accountEmitter.fire({ status: "reconnecting", label: "↻ reconnecting" });

    expect(bar.getLastError()).toBe("↻ reconnecting");
  });

  it("does not overwrite last error on connected event", () => {
    const { accountStream, projectStream, accountEmitter, projectEmitter } = makeStreams();
    bar.attachStreams(accountStream, projectStream);

    accountEmitter.fire({ status: "disconnected", label: "✕ auth error" });
    accountEmitter.fire({ status: "connected", label: "● live" });
    projectEmitter.fire({ status: "connected", label: "● live" });

    expect(bar.getLastError()).toBe("✕ auth error");
    expect(bar.getAggregatedStatus()).toBe("connected");
  });

  it("includes last error in tooltip when set", () => {
    const { accountStream, projectStream, accountEmitter } = makeStreams();
    bar.attachStreams(accountStream, projectStream);

    accountEmitter.fire({ status: "disconnected", label: "✕ timeout" });

    expect(bar.item.tooltip).toContain("Last error: ✕ timeout");
  });

  it("disposes stream subscriptions on dispose", () => {
    const { accountStream, projectStream, accountEmitter } = makeStreams();
    bar.attachStreams(accountStream, projectStream);
    bar.dispose();

    // After dispose, firing events should not update state
    const statusBefore = bar.getAggregatedStatus();
    accountEmitter.fire({ status: "disconnected", label: "✕ error" });
    expect(bar.getAggregatedStatus()).toBe(statusBefore);
  });

  it("disposes status bar item on dispose", () => {
    bar.dispose();
    expect(bar.item.dispose).toHaveBeenCalled();
  });
});
