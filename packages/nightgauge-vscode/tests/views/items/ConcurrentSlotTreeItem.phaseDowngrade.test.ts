/**
 * #3255: parity for the #3242 stuck-running-phase fix on the
 * ConcurrentSlotTreeItem path (which is what renders in autonomous mode).
 *
 * Uses the REAL StageTreeItem and PhaseTreeItem (no class mocks) so we can
 * inspect the actual phase children produced by syncFromState — unlike the
 * description-only tests in `ConcurrentSlotTreeItem.test.ts` which stub
 * StageTreeItem to keep their assertions string-based.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { PipelineState } from "../../../src/services/PipelineStateService";

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
  MarkdownString: class {
    value = "";
    isTrusted = false;
    appendMarkdown(s: string) {
      this.value += s;
    }
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

function makeStateService(_issueNumber: number) {
  const listeners: Array<(state: PipelineState | null) => void> = [];
  return {
    onStateChanged: (cb: (state: PipelineState | null) => void) => {
      listeners.push(cb);
      return { dispose: vi.fn() };
    },
    onPhaseStart: () => ({ dispose: vi.fn() }),
    onPhaseComplete: () => ({ dispose: vi.fn() }),
    onTokenUsageUpdated: () => ({ dispose: vi.fn() }),
    getState: vi.fn().mockResolvedValue(null),
    fireStateChanged: (state: PipelineState | null) => {
      for (const l of listeners) l(state);
    },
  };
}

function makeState(issueNumber: number, overrides: Partial<PipelineState> = {}): PipelineState {
  return {
    issue_number: issueNumber,
    title: `Issue #${issueNumber}`,
    branch: `feat/${issueNumber}`,
    stages: {},
    started_at: new Date().toISOString(),
    ...overrides,
  };
}

describe("ConcurrentSlotTreeItem — phase downgrade on terminal stage (#3255)", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it("downgrades a phase stuck at 'running' when parent stage is complete", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const { PhaseTreeItem } = await import("../../../src/views/items/PhaseTreeItem");
    const svc = makeStateService(3214);
    const item = new ConcurrentSlotTreeItem(0, 3214, "Wire perf mode", svc as any);

    // feature-planning carries a phase the registry doesn't know about
    // ("completion-checklist" — the skill markdown section name leaked
    // into the phase event stream). phase.complete was missed before
    // stage transitioned. Stage is now complete; phase is stuck running.
    svc.fireStateChanged(
      makeState(3214, {
        current_stage: "feature-dev",
        stages: {
          "feature-planning": {
            status: "complete",
            phases: [
              { name: "load-context", index: 1, total: 13, status: "complete" },
              { name: "completion-checklist", index: 11, total: 13, status: "running" },
            ],
            current_phase: "completion-checklist",
            total_phases: 13,
          },
          "feature-dev": { status: "running" },
        } as any,
      })
    );

    const planningStage = item.getStage("feature-planning")!;
    const children = planningStage.getChildren() as InstanceType<typeof PhaseTreeItem>[];
    const stuck = children.find((c) => c.phaseName === "completion-checklist");
    expect(stuck, "completion-checklist child should still exist").toBeDefined();
    expect(stuck!.getStatus()).toBe("complete");
    expect(children.some((c) => c.getStatus() === "running")).toBe(false);
  });

  it("downgrades a phase stuck at 'running' to 'failed' when parent stage failed", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const { PhaseTreeItem } = await import("../../../src/views/items/PhaseTreeItem");
    const svc = makeStateService(3214);
    const item = new ConcurrentSlotTreeItem(0, 3214, "x", svc as any);

    svc.fireStateChanged(
      makeState(3214, {
        stages: {
          "feature-dev": {
            status: "failed",
            phases: [
              { name: "load-context", index: 1, total: 17, status: "complete" },
              { name: "implementation", index: 7, total: 17, status: "running" },
            ],
            current_phase: "implementation",
            total_phases: 17,
          },
        } as any,
      })
    );

    const devStage = item.getStage("feature-dev")!;
    const children = devStage.getChildren() as InstanceType<typeof PhaseTreeItem>[];
    const stuck = children.find((c) => c.phaseName === "implementation");
    expect(stuck, "implementation child should still exist").toBeDefined();
    expect(stuck!.getStatus()).toBe("failed");
    expect(children.some((c) => c.getStatus() === "running")).toBe(false);
  });

  it("does not downgrade phases when parent stage is still running", async () => {
    const { ConcurrentSlotTreeItem } =
      await import("../../../src/views/items/ConcurrentSlotTreeItem");
    const { PhaseTreeItem } = await import("../../../src/views/items/PhaseTreeItem");
    const svc = makeStateService(3214);
    const item = new ConcurrentSlotTreeItem(0, 3214, "x", svc as any);

    svc.fireStateChanged(
      makeState(3214, {
        stages: {
          "feature-planning": {
            status: "running",
            phases: [
              { name: "load-context", index: 1, total: 13, status: "complete" },
              { name: "produce-plan", index: 8, total: 13, status: "running" },
            ],
            current_phase: "produce-plan",
            total_phases: 13,
          },
        } as any,
      })
    );

    const planningStage = item.getStage("feature-planning")!;
    const children = planningStage.getChildren() as InstanceType<typeof PhaseTreeItem>[];
    const live = children.find((c) => c.phaseName === "produce-plan");
    expect(live, "produce-plan child should exist").toBeDefined();
    expect(live!.getStatus()).toBe("running");
  });
});
