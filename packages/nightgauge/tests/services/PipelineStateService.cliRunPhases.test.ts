/**
 * PipelineStateService.cliRunPhases.test.ts
 *
 * #586 — phase progress for a run the extension did not spawn.
 *
 * A `nightgauge run` started in a terminal reaches the tree only through its
 * on-disk runtime snapshot. The scheduler now persists that snapshot on every
 * phase event (internal/orchestrator/clone_layout.go recordPhaseProgress), so a
 * mid-stage snapshot carries the running stage's phaseHistory. This pins the
 * consumer half: such a snapshot renders `[7/17]` on the run's stage row, not
 * the `0/17` that read as a stall. (The issue quotes 18; feature-dev declares
 * 17 phases in this tree's skill and PHASE_REGISTRY alike.)
 */

import { describe, it, expect, vi } from "vitest";
import { PHASE_REGISTRY } from "@nightgauge/sdk";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn(() => Promise.resolve({ status: "ok" })),
    }),
  },
}));

vi.mock("vscode", () => ({
  TreeItem: class {
    label: string;
    collapsibleState: number;
    description?: string;
    tooltip?: unknown;
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
  MarkdownString: class {
    value = "";
    isTrusted = false;
    supportThemeIcons = false;
    appendMarkdown(s: string) {
      this.value += s;
      return this;
    }
    appendText(s: string) {
      this.value += s;
      return this;
    }
    appendCodeblock(s: string) {
      this.value += s;
      return this;
    }
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
  Disposable: class {
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

const ISSUE = 586;

describe("a CLI-started run's mid-stage snapshot", () => {
  it("renders seven completed phases of the running stage as 7/17", async () => {
    const { PipelineStateService } = await import("../../src/services/PipelineStateService");
    const { ConcurrentSlotTreeItem } = await import("../../src/views/items/ConcurrentSlotTreeItem");
    PipelineStateService.resetInstance();
    const svc = PipelineStateService.createForWorktree("/tmp/repo", ISSUE);

    const registry = PHASE_REGISTRY["feature-dev"];
    expect(registry).toHaveLength(17);
    const phaseHistory = registry.slice(0, 8).map((p, i) => ({
      stage: "feature-dev",
      name: p.name,
      index: i,
      total: 17,
      status: i === 7 ? "running" : "complete",
      startedAt: "2026-10-26T21:40:00.000Z",
    }));

    svc.applyRuntimeSnapshot({
      issueNumber: ISSUE,
      title: "surface CLI-started runs",
      stage: "feature-dev",
      stageStart: "2026-10-26T21:39:00.000Z",
      completedStages: [{ stage: "issue-pickup", startedAt: "2026-10-26T21:30:00.000Z" }],
      phaseHistory,
    });

    const state = await svc.getState();
    expect(state?.stages["feature-dev"]).toMatchObject({ status: "running", total_phases: 17 });
    expect(state?.stages["feature-dev"]?.phases).toHaveLength(8);
    expect(state?.stages["feature-dev"]?.current_phase).toBe("implementation");

    const slot = new ConcurrentSlotTreeItem(0, ISSUE, "surface CLI-started runs", svc);
    await vi.waitFor(() =>
      expect(String(slot.getStage("feature-dev")?.description ?? "")).toBe("Implementation [7/17]")
    );
  });
});
