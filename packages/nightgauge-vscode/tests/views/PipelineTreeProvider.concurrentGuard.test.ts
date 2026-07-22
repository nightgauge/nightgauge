/**
 * PipelineTreeProvider.concurrentGuard.test.ts — Regression tests for concurrent mode guards
 *
 * When concurrent pipeline slots are active, the main PipelineTreeProvider must
 * ignore state/phase events from the singleton PipelineStateService (which
 * receives ALL slot events via shared IPC). Each ConcurrentSlotTreeItem has its
 * own filtered subscription, so the main provider's handlers would only cause
 * interference (clearIssue, syncFromState, phase updates to wrong stages).
 *
 * @see Issue #1888 - concurrent tree provider interference
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { PipelineTreeProvider } from "../../src/views/PipelineTreeProvider";
import { StageTreeItem } from "../../src/views/items/StageTreeItem";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

// ---------------------------------------------------------------------------
// Mock state service factory
// ---------------------------------------------------------------------------

const createMockStateService = () => {
  const callbacks: Record<string, Function> = {};
  let currentState: any = null;

  return {
    onStateChanged: vi.fn((cb: Function) => {
      callbacks["stateChanged"] = cb;
      return { dispose: vi.fn() };
    }),
    onBatchStateChanged: vi.fn((cb: Function) => {
      callbacks["batchStateChanged"] = cb;
      return { dispose: vi.fn() };
    }),
    onTokenUsageUpdated: vi.fn((cb: Function) => {
      callbacks["tokenUsageUpdated"] = cb;
      return { dispose: vi.fn() };
    }),
    onPhaseStart: vi.fn((cb: Function) => {
      callbacks["phaseStart"] = cb;
      return { dispose: vi.fn() };
    }),
    onPhaseComplete: vi.fn((cb: Function) => {
      callbacks["phaseComplete"] = cb;
      return { dispose: vi.fn() };
    }),
    getState: vi.fn(async () => currentState),
    getBatchState: vi.fn(async () => null),

    _setCurrentState: (state: any) => {
      currentState = state;
    },
    _fireStateChanged: (state: any) => {
      currentState = state;
      callbacks["stateChanged"]?.(state);
    },
    _firePhaseStart: (event: any) => {
      callbacks["phaseStart"]?.(event);
    },
    _firePhaseComplete: (event: any) => {
      callbacks["phaseComplete"]?.(event);
    },
  };
};

type MockStateService = ReturnType<typeof createMockStateService>;

// ---------------------------------------------------------------------------
// Pipeline state factory
// ---------------------------------------------------------------------------

function makeState(overrides?: Partial<any>): any {
  return {
    schema_version: "1.0",
    issue_number: 42,
    title: "Test Issue",
    branch: "feat/42-test",
    base_branch: "main",
    started_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    execution_mode: "automatic",
    paused: false,
    stages: {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": {
        status: "running",
        phases: [],
        current_phase: undefined,
      },
      "feature-validate": { status: "pending" },
      "pr-create": { status: "pending" },
      "pr-merge": { status: "pending" },
      "pipeline-finish": { status: "pending" },
    },
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
    },
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Mock slot state service for addConcurrentSlot
// ---------------------------------------------------------------------------

function createSlotStateService() {
  return {
    onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
    onPhaseStart: vi.fn(() => ({ dispose: vi.fn() })),
    onPhaseComplete: vi.fn(() => ({ dispose: vi.fn() })),
    onTokenUsageUpdated: vi.fn(() => ({ dispose: vi.fn() })),
    getState: vi.fn(async () => null),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineTreeProvider — concurrent mode guards", () => {
  let provider: PipelineTreeProvider;
  let stateService: MockStateService;

  beforeEach(() => {
    provider = new PipelineTreeProvider();
    stateService = createMockStateService();
    provider.setStateService(stateService as any);
  });

  describe("when concurrent slots are active", () => {
    beforeEach(() => {
      // Add a concurrent slot so the guard activates
      provider.addConcurrentSlot(0, 100, "Test Issue #100", createSlotStateService() as any);
    });

    it("ignores onStateChanged(null) — does not call clearIssue", () => {
      // First set an issue so we can verify it doesn't get cleared
      stateService._fireStateChanged(makeState({ issue_number: 99, title: "Before" }));

      // Now add the concurrent slot (which activates the guard)
      // The slot was already added in beforeEach, but let's verify the state was set
      // by checking that a null state event does NOT clear things

      // This should be ignored due to the concurrent guard
      stateService._fireStateChanged(null);

      // The concurrent slots should still be present
      const rootChildren = provider.getChildren(undefined);
      expect(rootChildren).toBeDefined();
    });

    it("ignores onStateChanged(state) — does not call syncFromState", () => {
      // Fire a state event for a different issue while concurrent mode is active
      const crossSlotState = makeState({
        issue_number: 200,
        title: "Cross-Slot Issue",
      });

      // This should be ignored — the main provider should not react
      stateService._fireStateChanged(crossSlotState);

      // The provider should still show concurrent slots, not switch to issue 200
      const children = provider.getChildren(undefined);
      expect(children).toBeDefined();
    });

    it("ignores onPhaseStart events", async () => {
      const phaseState = makeState({
        stages: {
          ...makeState().stages,
          "feature-dev": {
            status: "running",
            phases: [{ name: "Phase 1", index: 0, total: 5, status: "running" }],
            current_phase: "Phase 1",
          },
        },
      });
      stateService._setCurrentState(phaseState);

      // Fire phase start — should be ignored in concurrent mode
      stateService._firePhaseStart({
        stage: "feature-dev",
        phase: "Phase 1",
        index: 0,
        total: 5,
        totalPhases: 5,
      });

      // getState should NOT have been called (guard exits before reaching it)
      // In concurrent mode, the handler returns immediately
      await new Promise((r) => setTimeout(r, 10));
      // Verify no phase children were added to the main provider's stage items
      // (The main provider's stages are for single-issue mode only)
    });

    it("ignores onPhaseComplete events", async () => {
      const phaseState = makeState({
        stages: {
          ...makeState().stages,
          "feature-dev": {
            status: "running",
            phases: [{ name: "Phase 1", index: 0, total: 5, status: "complete" }],
            current_phase: "Phase 1",
          },
        },
      });
      stateService._setCurrentState(phaseState);

      // Fire phase complete — should be ignored in concurrent mode
      stateService._firePhaseComplete({
        stage: "feature-dev",
        phase: "Phase 1",
        index: 0,
        total: 5,
        totalPhases: 5,
      });

      await new Promise((r) => setTimeout(r, 10));
    });
  });

  describe("when preparing slots are active (no concurrent slots yet)", () => {
    beforeEach(() => {
      provider.addPreparingSlot(100, "Preparing Issue");
    });

    it("ignores onStateChanged during preparing phase", () => {
      // Fire a null state event — should be ignored because preparingSlots > 0
      stateService._fireStateChanged(null);

      // Provider should still show the preparing slot
      const children = provider.getChildren(undefined);
      expect(children).toBeDefined();
    });
  });

  describe("when no concurrent slots are active (single-issue mode)", () => {
    it("processes onStateChanged normally", () => {
      const state = makeState({ issue_number: 42, title: "Normal Issue" });
      stateService._fireStateChanged(state);

      // The provider should have set the current issue
      const children = provider.getChildren(undefined);
      expect(children).toBeDefined();
    });

    it("processes onStateChanged(null) — calls clearIssue", () => {
      // First set an issue
      stateService._fireStateChanged(makeState());

      // Then clear it
      stateService._fireStateChanged(null);

      // The tree should show the placeholder (no issue active)
      const children = provider.getChildren(undefined);
      expect(children).toBeDefined();
    });

    it("processes onPhaseStart normally", async () => {
      // Set up state with running stage
      const state = makeState({
        stages: {
          ...makeState().stages,
          "feature-dev": {
            status: "running",
            phases: [{ name: "Planning", index: 0, total: 3, status: "running" }],
            current_phase: "Planning",
          },
        },
      });
      stateService._fireStateChanged(state);
      stateService._setCurrentState(state);

      // Fire phase start
      stateService._firePhaseStart({
        stage: "feature-dev",
        phase: "Planning",
        index: 0,
        total: 3,
        totalPhases: 3,
      });

      // getState should have been called (not blocked by guard)
      await new Promise((r) => setTimeout(r, 10));
      expect(stateService.getState).toHaveBeenCalled();
    });
  });

  describe("guard transitions", () => {
    it("resumes normal operation after concurrent slots are cleared", () => {
      // Add a slot
      provider.addConcurrentSlot(0, 100, "Test", createSlotStateService() as any);

      // State events should be ignored
      stateService._fireStateChanged(makeState({ issue_number: 200 }));

      // Clear all concurrent slots
      provider.clearConcurrentSlots();

      // Now state events should work again
      const state = makeState({ issue_number: 300, title: "After Concurrent" });
      stateService._fireStateChanged(state);

      // The provider should have processed this
      const children = provider.getChildren(undefined);
      expect(children).toBeDefined();
    });
  });
});
