/**
 * ConcurrentSlotTreeItem — the private badge (#2400).
 *
 * A run started private shows the badge only once the hosted service
 * confirms it; a service answer without private leaves the slot unbadged.
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

import { ConcurrentSlotTreeItem } from "../../../src/views/items/ConcurrentSlotTreeItem";
import type { PipelineStateService } from "../../../src/services/PipelineStateService";
import type { PrivateConfirmation } from "../../../src/services/RunVisibility";

function makeStateService() {
  const confirmationListeners: Array<(outcome: PrivateConfirmation) => void> = [];
  let confirmation: PrivateConfirmation | undefined;
  const subscribe = () => ({ dispose: vi.fn() });
  return {
    onStateChanged: subscribe,
    onPhaseStart: subscribe,
    onPhaseComplete: subscribe,
    onTokenUsageUpdated: subscribe,
    getState: vi.fn().mockResolvedValue(null),
    onPrivateConfirmation: (cb: (outcome: PrivateConfirmation) => void) => {
      confirmationListeners.push(cb);
      return { dispose: vi.fn() };
    },
    isPrivateConfirmed: () => confirmation === "confirmed",
    setPrivateConfirmation: (outcome: PrivateConfirmation) => {
      confirmation = outcome;
      for (const l of confirmationListeners) l(outcome);
    },
  };
}

describe("ConcurrentSlotTreeItem private badge (#2400)", () => {
  let stateService: ReturnType<typeof makeStateService>;
  let item: ConcurrentSlotTreeItem;

  beforeEach(() => {
    stateService = makeStateService();
    item = new ConcurrentSlotTreeItem(
      0,
      912,
      "Private work",
      stateService as unknown as PipelineStateService
    );
  });

  it("shows no badge before the service answers", () => {
    expect(String(item.description)).not.toContain(ConcurrentSlotTreeItem.PRIVATE_BADGE);
  });

  it("shows the badge once the service confirms the run private", () => {
    stateService.setPrivateConfirmation("confirmed");
    expect(String(item.description)).toMatch(/^Private · Slot 1$/);
  });

  it("shows no badge when the service's answer is not private", () => {
    stateService.setPrivateConfirmation("unconfirmed");
    expect(String(item.description)).not.toContain(ConcurrentSlotTreeItem.PRIVATE_BADGE);
    expect(String(item.description)).toBe("Slot 1");
  });
});
