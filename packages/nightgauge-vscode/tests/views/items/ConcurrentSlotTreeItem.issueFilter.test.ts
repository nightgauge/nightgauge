/**
 * ConcurrentSlotTreeItem — issue-number filtering tests (#3486)
 *
 * Verifies that phase and token events for a different issue number are
 * ignored, and events for the slot's own issue number update the stage item.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  TreeItem: class {
    label: string;
    collapsibleState: number;
    description?: string;
    tooltip?: string;
    iconPath?: unknown;
    contextValue?: string;
    id?: string;
    constructor(label: string, collapsibleState: number) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
  },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  ThemeIcon: class {
    constructor(
      public id: string,
      public color?: unknown
    ) {}
  },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

vi.mock("../../../src/views/items/StageTreeItem", () => ({
  StageTreeItem: class {
    label = "";
    private _status = "pending";
    setStatus = vi.fn((s: string) => {
      this._status = s;
    });
    getStatus = vi.fn(() => this._status);
    setDuration = vi.fn();
    setError = vi.fn();
    setExecutionMode = vi.fn();
    setPhases = vi.fn();
    clearPhases = vi.fn();
    getPhaseCount = vi.fn().mockReturnValue(0);
    setTokenUsage = vi.fn();
    getTokenInfo = vi.fn().mockReturnValue(null);
    getChildren = vi.fn().mockReturnValue([]);
    constructor(public stage: string) {
      this.label = stage;
    }
  },
}));

vi.mock("../../../src/views/items/BaseTreeItem", () => ({
  BaseTreeItem: class {
    label: string;
    collapsibleState: number;
    description?: string;
    tooltip?: string;
    iconPath?: unknown;
    contextValue?: string;
    id?: string;
    private _children: unknown[] = [];
    constructor(label: string, collapsibleState: number) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
    addChild(child: unknown) {
      this._children.push(child);
    }
    clearChildren() {
      this._children = [];
    }
    getChildren() {
      return this._children;
    }
  },
}));

vi.mock("@nightgauge/sdk", () => ({
  PHASE_REGISTRY: {},
}));

function makeStateService() {
  const phaseStartListeners: Array<(event: unknown) => void> = [];
  const phaseCompleteListeners: Array<(event: unknown) => void> = [];
  const tokenListeners: Array<(event: unknown) => void> = [];

  return {
    onStateChanged: () => ({ dispose: vi.fn() }),
    onPhaseStart: (cb: (event: unknown) => void) => {
      phaseStartListeners.push(cb);
      return { dispose: vi.fn() };
    },
    onPhaseComplete: (cb: (event: unknown) => void) => {
      phaseCompleteListeners.push(cb);
      return { dispose: vi.fn() };
    },
    onTokenUsageUpdated: (cb: (event: unknown) => void) => {
      tokenListeners.push(cb);
      return { dispose: vi.fn() };
    },
    getState: vi.fn().mockResolvedValue(null),
    firePhaseStart: (event: unknown) => {
      for (const l of phaseStartListeners) l(event);
    },
    firePhaseComplete: (event: unknown) => {
      for (const l of phaseCompleteListeners) l(event);
    },
    fireTokenUsageUpdated: (event: unknown) => {
      for (const l of tokenListeners) l(event);
    },
  };
}

describe("ConcurrentSlotTreeItem — issue-number filtering (#3486)", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it("ignores onPhaseStart events for a different issue number", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService();
    const onChange = vi.fn();
    const item = new ConcurrentSlotTreeItem(0, 500, "Issue 500", svc as any, undefined, onChange);

    // Fire event for a different issue (501)
    svc.firePhaseStart({
      stage: "feature-dev",
      phase: "implementation",
      index: 0,
      total: 8,
      totalPhases: 8,
      issueNumber: 501,
    });

    // onChange should NOT have been called — wrong issue
    expect(onChange).not.toHaveBeenCalled();

    // Get the stage item and verify setPhases was not called
    const stageItem = (item as any).stages.get("feature-dev");
    if (stageItem) {
      expect(stageItem.setPhases).not.toHaveBeenCalled();
    }
  });

  it("processes onPhaseStart events for the slot's own issue number", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService();
    const onChange = vi.fn();
    const item = new ConcurrentSlotTreeItem(0, 500, "Issue 500", svc as any, undefined, onChange);

    svc.firePhaseStart({
      stage: "feature-dev",
      phase: "implementation",
      index: 2,
      total: 8,
      totalPhases: 8,
      issueNumber: 500,
    });

    expect(onChange).toHaveBeenCalled();
  });

  it("processes onPhaseStart events when issueNumber is undefined (legacy)", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService();
    const onChange = vi.fn();
    new ConcurrentSlotTreeItem(0, 500, "Issue 500", svc as any, undefined, onChange);

    // No issueNumber field — should pass through (backwards compat)
    svc.firePhaseStart({
      stage: "feature-dev",
      phase: "implementation",
      index: 0,
      total: 8,
      totalPhases: 8,
    });

    expect(onChange).toHaveBeenCalled();
  });

  it("ignores onPhaseComplete events for a different issue number", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService();
    const onChange = vi.fn();
    new ConcurrentSlotTreeItem(0, 500, "Issue 500", svc as any, undefined, onChange);

    svc.firePhaseComplete({
      stage: "feature-dev",
      phase: "implementation",
      index: 2,
      total: 8,
      totalPhases: 8,
      issueNumber: 999,
    });

    expect(onChange).not.toHaveBeenCalled();
  });

  it("ignores onTokenUsageUpdated events for a different issue number", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService();
    const onChange = vi.fn();
    new ConcurrentSlotTreeItem(0, 500, "Issue 500", svc as any, undefined, onChange);

    svc.fireTokenUsageUpdated({
      stage: "feature-dev",
      inputTokens: 100,
      outputTokens: 50,
      costUsd: 0.001,
      issueNumber: 999,
    });

    expect(onChange).not.toHaveBeenCalled();
  });

  it("processes onTokenUsageUpdated events for the slot's own issue number", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService();
    const onChange = vi.fn();
    new ConcurrentSlotTreeItem(0, 500, "Issue 500", svc as any, undefined, onChange);

    svc.fireTokenUsageUpdated({
      stage: "feature-dev",
      inputTokens: 100,
      outputTokens: 50,
      costUsd: 0.001,
      issueNumber: 500,
    });

    expect(onChange).toHaveBeenCalled();
  });
});
