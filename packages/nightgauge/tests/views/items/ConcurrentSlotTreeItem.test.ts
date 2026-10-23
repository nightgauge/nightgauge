/**
 * ConcurrentSlotTreeItem — issue-level cumulative metrics display tests.
 *
 * Verifies the description field shows cumulative cost/tokens and current
 * stage context, and that values persist while a stage is running.
 *
 * @see Issue #2911 — Rework autonomous dashboard row to show issue-level cumulative metrics
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

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
    description = "";
    collapsibleState = 0;
    setStatus = vi.fn();
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

import type { PipelineState } from "../../../src/services/PipelineStateService";

function makeStateService(issueNumber: number) {
  const listeners: Array<(state: PipelineState | null) => void> = [];
  const phaseStartListeners: Array<(event: unknown) => void> = [];
  const phaseCompleteListeners: Array<(event: unknown) => void> = [];
  const tokenListeners: Array<() => void> = [];

  return {
    onStateChanged: (cb: (state: PipelineState | null) => void) => {
      listeners.push(cb);
      return { dispose: vi.fn() };
    },
    onPhaseStart: (cb: (event: unknown) => void) => {
      phaseStartListeners.push(cb);
      return { dispose: vi.fn() };
    },
    onPhaseComplete: (cb: (event: unknown) => void) => {
      phaseCompleteListeners.push(cb);
      return { dispose: vi.fn() };
    },
    onTokenUsageUpdated: (cb: () => void) => {
      tokenListeners.push(cb);
      return { dispose: vi.fn() };
    },
    getState: vi.fn().mockResolvedValue(null),
    // Test helpers
    fireStateChanged: (state: PipelineState | null) => {
      for (const l of listeners) l(state);
    },
  };
}

function makeState(issueNumber: number, overrides: Partial<PipelineState> = {}): PipelineState {
  return {
    issue_number: issueNumber,
    title: `Issue #${issueNumber}`,
    branch: `feat/${issueNumber}-test`,
    stages: {},
    started_at: new Date().toISOString(),
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ConcurrentSlotTreeItem — issue-level description", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it("shows cumulative cost and tokens when per_issue data is available", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService(400);
    const onChange = vi.fn();
    const item = new ConcurrentSlotTreeItem(0, 400, "My Issue", svc as any, undefined, onChange);

    svc.fireStateChanged(
      makeState(400, {
        tokens: {
          input: 0,
          output: 0,
          per_issue: {
            input: 10000,
            output: 1000,
            cache_read: 5000,
            cache_creation: 0,
            cost_usd: 0.1234,
          },
        },
      })
    );

    expect(item.description).toContain("$0.1234");
    expect(item.description).toContain("tokens");
  });

  it("shows stage context when current_stage is set", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService(401);
    const item = new ConcurrentSlotTreeItem(0, 401, "My Issue", svc as any);

    svc.fireStateChanged(
      makeState(401, {
        current_stage: "feature-dev",
        tokens: { input: 0, output: 0 },
      })
    );

    expect(item.description).toContain("Feature Development");
    expect(item.description).toMatch(/Stage \d+ of \d+/);
  });

  it("persists previously accumulated totals while new stage is running (no reset)", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService(402);
    const item = new ConcurrentSlotTreeItem(0, 402, "My Issue", svc as any);

    // Simulate: stage 1 completed with tokens, stage 2 just started
    svc.fireStateChanged(
      makeState(402, {
        current_stage: "feature-dev",
        tokens: {
          input: 0,
          output: 0,
          per_issue: { input: 8000, output: 800, cache_read: 0, cache_creation: 0, cost_usd: 0.08 },
        },
      })
    );

    // Description should show previous totals, not "0" or empty
    expect(item.description).toContain("$0.0800");
    expect(item.description).not.toContain("Slot 1");
  });

  it("falls back to slot label when no metrics or stage context available", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService(403);
    const item = new ConcurrentSlotTreeItem(0, 403, "My Issue", svc as any);

    svc.fireStateChanged(
      makeState(403, {
        tokens: { input: 0, output: 0 },
      })
    );

    expect(item.description).toBe("Slot 1");
  });

  it("tooltip includes cumulative metrics clarification when cost > 0", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService(404);
    const item = new ConcurrentSlotTreeItem(0, 404, "Real Title", svc as any);

    svc.fireStateChanged(
      makeState(404, {
        title: "Real Title",
        tokens: {
          input: 0,
          output: 0,
          per_issue: { input: 5000, output: 500, cache_read: 0, cache_creation: 0, cost_usd: 0.05 },
        },
      })
    );

    expect(String(item.tooltip)).toContain("Cumulative cost");
  });

  it("formats tokens >= 1000 as K notation", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService(405);
    const item = new ConcurrentSlotTreeItem(0, 405, "My Issue", svc as any);

    svc.fireStateChanged(
      makeState(405, {
        tokens: {
          input: 0,
          output: 0,
          per_issue: {
            input: 50000,
            output: 5000,
            cache_read: 10000,
            cache_creation: 0,
            cost_usd: 0.5,
          },
        },
      })
    );

    expect(item.description).toContain("K tokens");
  });

  it("includes epic number in fallback when epicNumber is set", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const svc = makeStateService(406);
    const item = new ConcurrentSlotTreeItem(1, 406, "My Issue", svc as any, 100);

    svc.fireStateChanged(
      makeState(406, {
        tokens: { input: 0, output: 0 },
      })
    );

    expect(item.description).toContain("Epic #100");
  });
});

// Phase downgrade tests for #3255 live in
// `ConcurrentSlotTreeItem.phaseDowngrade.test.ts` so they can run without the
// stubbed StageTreeItem mock used in the description-only tests above.
