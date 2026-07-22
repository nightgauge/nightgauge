/**
 * PipelineTreeProvider - Phase event subscription and phase syncing tests
 *
 * Tests the phase-aware behaviour introduced in Issue #1028:
 * - setStateService() subscribes to onPhaseStart and onPhaseComplete
 * - Phase start / complete events update the corresponding StageTreeItem children
 * - syncFromState() populates PhaseTreeItem children from persisted state.json
 * - syncFromState() clears phase children when state no longer carries phases
 * - Phase children are visible via getChildren() on an expanded running stage
 * - getParent() returns the correct StageTreeItem for a PhaseTreeItem
 * - Phase children survive a stage status transition from running to complete
 *
 * @see Issue #1028 - Render phase progress as children in pipeline tree view
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { PipelineTreeProvider } from "../../src/views/PipelineTreeProvider";
import { StageTreeItem } from "../../src/views/items/StageTreeItem";
import { PhaseTreeItem } from "../../src/views/items/PhaseTreeItem";

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

/**
 * Build a minimal mock of PipelineStateService that records the callbacks
 * registered via onPhaseStart / onPhaseComplete / onStateChanged and exposes
 * helpers to fire those events from the tests.
 *
 * The mock satisfies the subset of the interface used by PipelineTreeProvider
 * without importing (and therefore executing) any real service code.
 */
const createMockStateService = () => {
  const callbacks: Record<string, Function> = {};
  let currentState: any = null;

  return {
    onStateChanged: vi.fn((cb: Function) => {
      callbacks["stateChanged"] = cb;
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

    // ---- helpers used by the tests ----

    /** Replace the state returned by getState() without firing any event. */
    _setCurrentState: (state: any) => {
      currentState = state;
    },
    /** Fire stateChanged AND update the internal state. */
    _fireStateChanged: (state: any) => {
      currentState = state;
      callbacks["stateChanged"]?.(state);
    },
    /** Fire phaseStart. The provider will call getState() in response, so
     *  ensure _setCurrentState() has been called first if fresh phase data is needed. */
    _firePhaseStart: (event: any) => {
      callbacks["phaseStart"]?.(event);
    },
    /** Fire phaseComplete. Same getState() note applies. */
    _firePhaseComplete: (event: any) => {
      callbacks["phaseComplete"]?.(event);
    },
  };
};

type MockStateService = ReturnType<typeof createMockStateService>;

// ---------------------------------------------------------------------------
// Pipeline state factory
// ---------------------------------------------------------------------------

/**
 * Build a minimal valid PipelineState-shaped object.
 * Overrides are shallowly merged into the top-level fields.
 * To override stage data, pass `stages` in overrides.
 */
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

/** Build a StagePhase object. */
function makePhase(
  name: string,
  status: "pending" | "running" | "complete" | "skipped" = "pending"
) {
  return { name, status };
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Create a provider, wire up the mock service, and flush the microtask queue
 * so that the async initial-sync in setStateService() has finished.
 */
async function createProvider(mockService: MockStateService): Promise<PipelineTreeProvider> {
  const provider = new PipelineTreeProvider();
  provider.setStateService(mockService as any);
  // Flush the promises queued by setStateService's async initial sync.
  await Promise.resolve();
  return provider;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineTreeProvider — phase event subscription and syncing (Issue #1028)", () => {
  let mockService: MockStateService;
  let provider: PipelineTreeProvider;

  beforeEach(() => {
    vi.clearAllMocks();
    mockService = createMockStateService();
  });

  // -------------------------------------------------------------------------
  // 1. Subscription registration
  // -------------------------------------------------------------------------

  describe("setStateService() — event subscriptions", () => {
    it("subscribes to onPhaseStart when setStateService is called", async () => {
      provider = await createProvider(mockService);
      expect(mockService.onPhaseStart).toHaveBeenCalledOnce();
    });

    it("subscribes to onPhaseComplete when setStateService is called", async () => {
      provider = await createProvider(mockService);
      expect(mockService.onPhaseComplete).toHaveBeenCalledOnce();
    });

    it("also subscribes to onStateChanged and onTokenUsageUpdated", async () => {
      provider = await createProvider(mockService);
      expect(mockService.onStateChanged).toHaveBeenCalledOnce();
      expect(mockService.onTokenUsageUpdated).toHaveBeenCalledOnce();
    });
  });

  // -------------------------------------------------------------------------
  // 2. phaseStart event updates stage children
  // -------------------------------------------------------------------------

  describe("onPhaseStart event", () => {
    it("populates phase children on the running stage when phaseStart fires", async () => {
      provider = await createProvider(mockService);

      // Fire the phaseStart event — handler now uses event payload directly (Issue #3486).
      // index=1 → 1 synthetic "complete" preceding phase + 1 "running" current phase = 2 children.
      mockService._firePhaseStart({
        stage: "feature-dev",
        phase: "implementation",
        index: 1,
        total: 8,
        totalPhases: 8,
      });

      const stageItem = provider.getStage("feature-dev");
      expect(stageItem).toBeDefined();
      const children = stageItem!.getChildren();
      expect(children).toHaveLength(2);
      expect(children.every((c) => c instanceof PhaseTreeItem)).toBe(true);
    });

    it("assigns correct statuses to phase children after phaseStart", async () => {
      provider = await createProvider(mockService);

      mockService._firePhaseStart({
        stage: "feature-dev",
        phase: "implementation",
        index: 1,
        total: 8,
        totalPhases: 8,
      });

      const stageItem = provider.getStage("feature-dev")!;
      const children = stageItem.getChildren() as PhaseTreeItem[];
      // Preceding phase (registry index 0) → "complete"; current phase → "running".
      expect(children[0].getStatus()).toBe("complete");
      expect(children[1].getStatus()).toBe("running");
    });

    it("does not crash when phaseStart fires for an unknown stage", async () => {
      provider = await createProvider(mockService);

      // Fire for a stage name not in STAGE_ORDER — should be silently ignored.
      expect(() =>
        mockService._firePhaseStart({
          stage: "nonexistent-stage",
          phase: "some-phase",
          index: 0,
          total: 3,
          totalPhases: 3,
        })
      ).not.toThrow();
    });

    it("updates stage children from event data (no getState() round-trip required)", async () => {
      // getState returns null throughout — but phases should still be populated
      // because the handler now reads directly from the event payload (Issue #3486).
      mockService._setCurrentState(null);
      provider = await createProvider(mockService);

      mockService._firePhaseStart({
        stage: "feature-dev",
        phase: "load-context",
        index: 0,
        total: 5,
        totalPhases: 5,
      });

      const stageItem = provider.getStage("feature-dev")!;
      // index=0 → no preceding phases, just the running phase itself.
      expect(stageItem.getChildren()).toHaveLength(1);
      expect((stageItem.getChildren()[0] as PhaseTreeItem).getStatus()).toBe("running");
    });

    it("shows live phase count in description after phaseStart (Issue #3486)", async () => {
      provider = await createProvider(mockService);
      // Stage must be "running" for formatDescription to show the count.
      mockService._fireStateChanged(
        makeState({
          stages: {
            "pipeline-start": { status: "complete" },
            "issue-pickup": { status: "complete" },
            "feature-planning": { status: "complete" },
            "feature-dev": { status: "running", phases: [], current_phase: undefined },
            "feature-validate": { status: "pending" },
            "pr-create": { status: "pending" },
            "pr-merge": { status: "pending" },
            "pipeline-finish": { status: "pending" },
          },
        })
      );

      mockService._firePhaseStart({
        stage: "feature-dev",
        phase: "implementation",
        index: 8,
        total: 18,
        totalPhases: 18,
      });

      const stageItem = provider.getStage("feature-dev")!;
      // Description should include "[8/18]" — 8 completed phases before "implementation" (index 8 in 18-phase registry).
      expect(stageItem.description).toContain("[8/18]");
    });
  });

  // -------------------------------------------------------------------------
  // 3. phaseComplete event updates stage children
  // -------------------------------------------------------------------------

  describe("onPhaseComplete event", () => {
    it("updates phase children when phaseComplete fires", async () => {
      provider = await createProvider(mockService);

      // index=0 → no preceding phases; "load-context" itself becomes "complete".
      mockService._firePhaseComplete({
        stage: "feature-dev",
        phase: "load-context",
        index: 0,
        total: 8,
        totalPhases: 8,
      });

      const stageItem = provider.getStage("feature-dev")!;
      const children = stageItem.getChildren() as PhaseTreeItem[];
      expect(children).toHaveLength(1);
      expect(children[0].getStatus()).toBe("complete");
    });

    it("does not crash when phaseComplete fires for an unknown stage", async () => {
      provider = await createProvider(mockService);

      expect(() =>
        mockService._firePhaseComplete({
          stage: "nonexistent-stage",
          phase: "some-phase",
          index: 0,
          total: 3,
          totalPhases: 3,
        })
      ).not.toThrow();
    });
  });

  // -------------------------------------------------------------------------
  // 3b. onTokenUsageUpdated — real-time token accumulation (Issue #3486)
  // -------------------------------------------------------------------------

  describe("onTokenUsageUpdated event — real-time token accumulation", () => {
    /** Helper: fire a token event via the registered callback. */
    function fireTokenUpdate(event: {
      stage: string;
      inputTokens: number;
      outputTokens: number;
      costUsd?: number;
    }) {
      // Access the registered callback through the mock service internals.
      // The provider registers via onTokenUsageUpdated which records the cb.
      (mockService as any)._callbacks?.["tokenUsageUpdated"]?.(event);
    }

    it("sets token info on the stage item immediately when onTokenUsageUpdated fires", async () => {
      // Rebuild the mock so we can capture the token callback.
      const callbacks: Record<string, Function> = {};
      const localService = {
        onStateChanged: vi.fn((cb: Function) => {
          callbacks["stateChanged"] = cb;
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
        getState: vi.fn(async () => null),
      };

      const p = new PipelineTreeProvider();
      p.setStateService(localService as any);
      await Promise.resolve();

      // Fire a token update for the running stage.
      callbacks["tokenUsageUpdated"]?.({
        stage: "feature-dev",
        inputTokens: 1000,
        outputTokens: 500,
        costUsd: 0.003,
      });

      const stageItem = p.getStage("feature-dev")!;
      const tokenInfo = stageItem.getTokenInfo();
      expect(tokenInfo).not.toBeNull();
      expect(tokenInfo!.inputTokens).toBe(1000);
      expect(tokenInfo!.outputTokens).toBe(500);
      expect(tokenInfo!.costUsd).toBeCloseTo(0.003);
    });

    it("accumulates successive token deltas on the same stage", async () => {
      const callbacks: Record<string, Function> = {};
      const localService = {
        onStateChanged: vi.fn((cb: Function) => {
          callbacks["stateChanged"] = cb;
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
        getState: vi.fn(async () => null),
      };

      const p = new PipelineTreeProvider();
      p.setStateService(localService as any);
      await Promise.resolve();

      callbacks["tokenUsageUpdated"]?.({
        stage: "feature-dev",
        inputTokens: 500,
        outputTokens: 200,
        costUsd: 0.001,
      });
      callbacks["tokenUsageUpdated"]?.({
        stage: "feature-dev",
        inputTokens: 300,
        outputTokens: 100,
        costUsd: 0.0005,
      });

      const stageItem = p.getStage("feature-dev")!;
      const tokenInfo = stageItem.getTokenInfo();
      expect(tokenInfo!.inputTokens).toBe(800);
      expect(tokenInfo!.outputTokens).toBe(300);
      expect(tokenInfo!.costUsd).toBeCloseTo(0.0015);
    });

    it("ignores token updates with no stage field", async () => {
      const callbacks: Record<string, Function> = {};
      const localService = {
        onStateChanged: vi.fn((cb: Function) => {
          callbacks["stateChanged"] = cb;
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
        getState: vi.fn(async () => null),
      };

      const p = new PipelineTreeProvider();
      p.setStateService(localService as any);
      await Promise.resolve();

      // No stage field — should not throw and should leave token info null.
      expect(() =>
        callbacks["tokenUsageUpdated"]?.({ inputTokens: 100, outputTokens: 50 })
      ).not.toThrow();

      const stageItem = p.getStage("feature-dev")!;
      expect(stageItem.getTokenInfo()).toBeNull();
    });

    it("skips provider-level token accumulation when concurrent slots are active (#3486)", async () => {
      // When concurrentSlots.size > 0, the provider's token handler must NOT
      // accumulate tokens on its own stage items — slots own their own tokens.
      const callbacks: Record<string, Function> = {};
      const localService = {
        onStateChanged: vi.fn((cb: Function) => {
          callbacks["stateChanged"] = cb;
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
        getState: vi.fn(async () => null),
      };

      const p = new PipelineTreeProvider();
      p.setStateService(localService as any);
      await Promise.resolve();

      // Inject a fake concurrent slot so the guard activates
      (p as any).concurrentSlots.set(42, {});

      callbacks["tokenUsageUpdated"]?.({
        stage: "feature-dev",
        inputTokens: 1000,
        outputTokens: 500,
        costUsd: 0.003,
        issueNumber: 42,
      });

      // Provider-level stage item should NOT have accumulated any tokens
      const stageItem = p.getStage("feature-dev")!;
      expect(stageItem.getTokenInfo()).toBeNull();
    });
  });

  // -------------------------------------------------------------------------
  // 4. syncFromState — populated phases
  // -------------------------------------------------------------------------

  describe("syncFromState() — phase population from persisted state", () => {
    it("populates phase children when state contains phases for a stage", async () => {
      const phases = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "running"),
        makePhase("write-tests", "pending"),
      ];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases,
            current_phase: "implementation",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);

      // Fire stateChanged to trigger syncFromState.
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-dev")!;
      // getPhaseCount() returns the registry total (authoritative denominator),
      // not the number of phases recorded so far in state.json. Issue #1608: 14, #1684: 15 (knowledge-base-read added).
      expect(stageItem.getPhaseCount()).toBe(18);
      const children = stageItem.getChildren();
      expect(children.every((c) => c instanceof PhaseTreeItem)).toBe(true);
    });

    it("creates PhaseTreeItem children with correct phaseName values", async () => {
      const phases = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "running"),
      ];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases,
            current_phase: "implementation",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-dev")!;
      const children = stageItem.getChildren() as PhaseTreeItem[];
      expect(children[0].phaseName).toBe("load-context");
      expect(children[1].phaseName).toBe("implementation");
    });

    it("syncs phases for multiple stages simultaneously", async () => {
      const featureDevPhases = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "complete"),
      ];
      const featureValidatePhases = [makePhase("run-tests", "running")];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "complete",
            phases: featureDevPhases,
            current_phase: undefined,
          },
          "feature-validate": {
            status: "running",
            phases: featureValidatePhases,
            current_phase: "run-tests",
          },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      // feature-dev is complete: fill-in adds all 18 registry phases (e2e-testing added in #9).
      expect(provider.getStage("feature-dev")!.getPhaseCount()).toBe(18);
      // feature-validate is running: total is still registry count (23,
      // verify-ui-gate added in #4193), not the number of phases recorded so far.
      expect(provider.getStage("feature-validate")!.getPhaseCount()).toBe(23);
    });
  });

  // -------------------------------------------------------------------------
  // 5. syncFromState — no phases keeps stage without children
  // -------------------------------------------------------------------------

  describe("syncFromState() — stage with no phases stays leaf node", () => {
    it("leaves stage item with zero children when state has no phases", async () => {
      const state = makeState(); // feature-dev has phases: []

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-dev")!;
      expect(stageItem.getChildren()).toHaveLength(0);
      expect(stageItem.getPhaseCount()).toBe(0);
    });

    it("stage collapsibleState is None when no phases are present", async () => {
      const state = makeState();

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-dev")!;
      expect(stageItem.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });
  });

  // -------------------------------------------------------------------------
  // 6. syncFromState — clears phases when state no longer has them
  // -------------------------------------------------------------------------

  describe("syncFromState() — phase clearing", () => {
    it("clears phase children when a subsequent state update removes the phases array", async () => {
      const stateWithPhases = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases: [makePhase("load-context", "complete"), makePhase("implementation", "running")],
            current_phase: "implementation",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);

      // First sync: populate phases. Total is always the registry count (14, Issue #1608),
      // not the 2 phases recorded so far.
      mockService._fireStateChanged(stateWithPhases);

      const stageItem = provider.getStage("feature-dev")!;
      expect(stageItem.getPhaseCount()).toBe(18);

      // Second sync: state no longer carries phases (e.g. reset).
      const stateWithoutPhases = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": { status: "pending" },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });
      mockService._fireStateChanged(stateWithoutPhases);

      expect(stageItem.getPhaseCount()).toBe(0);
      expect(stageItem.getChildren()).toHaveLength(0);
    });

    it("preserves phases for running stages when stateChanged has no phase data", async () => {
      // Go's pipeline.stateChanged events omit phase data for the running stage.
      // Phases should NOT be cleared mid-run — they survive until the stage
      // transitions out of 'running'.
      const stateWithPhases = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases: [makePhase("load-context", "running")],
            current_phase: "load-context",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(stateWithPhases);

      const stageItem = provider.getStage("feature-dev")!;
      // Running + phases → Expanded.
      expect(stageItem.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);

      // Fire stateChanged with same stage still running but no phases (as Go sends it)
      const stateStillRunningNoPhases = makeState();
      mockService._fireStateChanged(stateStillRunningNoPhases);

      // Phases should be PRESERVED — stage is still running, don't wipe mid-run
      expect(stageItem.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);
      expect(stageItem.getPhaseCount()).toBeGreaterThan(0);
    });

    it("resets collapsibleState to None when a running stage completes without phases", async () => {
      const stateWithPhases = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases: [makePhase("load-context", "running")],
            current_phase: "load-context",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(stateWithPhases);

      const stageItem = provider.getStage("feature-dev")!;
      expect(stageItem.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);

      // Stage transitions to complete with no phases in the new state
      const stateCompleteNoPhases = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": { status: "complete" },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });
      mockService._fireStateChanged(stateCompleteNoPhases);

      expect(stageItem.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });
  });

  // -------------------------------------------------------------------------
  // 7. Phase children visible via getChildren() on the provider
  // -------------------------------------------------------------------------

  describe("getChildren() — phase items visible in tree", () => {
    it("phase children are returned when calling getChildren() on a running stage item", async () => {
      const phases = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "running"),
        makePhase("write-tests", "pending"),
      ];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases,
            current_phase: "implementation",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-dev")!;

      // Ask the provider for children of the running stage item.
      const phaseItems = await provider.getChildren(stageItem);

      expect(phaseItems).toHaveLength(3);
      expect(phaseItems.every((p) => p instanceof PhaseTreeItem)).toBe(true);
    });

    it("phase children have correct labels (kebab → Title Case)", async () => {
      const phases = [
        makePhase("load-context", "complete"),
        makePhase("read-planning-context", "running"),
      ];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases,
            current_phase: "read-planning-context",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-dev")!;
      const phaseItems = await provider.getChildren(stageItem);

      expect((phaseItems[0] as PhaseTreeItem).label).toBe("Load Context");
      expect((phaseItems[1] as PhaseTreeItem).label).toBe("Read Planning Context");
    });
  });

  // -------------------------------------------------------------------------
  // 8. getParent() returns StageTreeItem for a PhaseTreeItem
  // -------------------------------------------------------------------------

  describe("getParent() — PhaseTreeItem parent resolution", () => {
    it("returns the parent StageTreeItem for a PhaseTreeItem child", async () => {
      const phases = [makePhase("load-context", "complete")];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases,
            current_phase: undefined,
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-dev")!;
      const phaseItems = stageItem.getChildren() as PhaseTreeItem[];
      expect(phaseItems).toHaveLength(1);

      const parent = provider.getParent(phaseItems[0]);
      expect(parent).toBe(stageItem);
    });

    it("returns undefined for a PhaseTreeItem that is not a child of any stage", async () => {
      provider = await createProvider(mockService);

      // Create an orphan PhaseTreeItem not registered in any stage.
      const orphan = new PhaseTreeItem("orphan-phase", "pending");
      const parent = provider.getParent(orphan);

      expect(parent).toBeUndefined();
    });

    it("resolves parent correctly when multiple stages have phase children", async () => {
      const featureDevPhases = [makePhase("load-context", "complete")];
      const featureValidatePhases = [makePhase("run-tests", "running")];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "complete",
            phases: featureDevPhases,
            current_phase: undefined,
          },
          "feature-validate": {
            status: "running",
            phases: featureValidatePhases,
            current_phase: "run-tests",
          },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const devStage = provider.getStage("feature-dev")!;
      const validateStage = provider.getStage("feature-validate")!;

      const devPhases = devStage.getChildren() as PhaseTreeItem[];
      const validatePhases = validateStage.getChildren() as PhaseTreeItem[];

      expect(provider.getParent(devPhases[0])).toBe(devStage);
      expect(provider.getParent(validatePhases[0])).toBe(validateStage);
    });
  });

  // -------------------------------------------------------------------------
  // 9. Phase children persist after stage completes
  // -------------------------------------------------------------------------

  describe("phase children after stage status transitions", () => {
    it("phase children are preserved when stage transitions from running to complete", async () => {
      const phases = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "complete"),
        makePhase("write-tests", "complete"),
      ];

      // First state: stage is running with phases.
      const runningState = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases,
            current_phase: "write-tests",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(runningState);

      const stageItem = provider.getStage("feature-dev")!;
      // Registry total (18) is always the authoritative count.
      // Issue #1608: was 16, now 18 (commit + push-commits phases removed, recall phase added). #1684: 15 (knowledge-base-read added).
      expect(stageItem.getPhaseCount()).toBe(18);

      // Second state: stage is now complete, phases still present in state.
      const completedState = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "complete",
            phases,
            current_phase: undefined,
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      mockService._fireStateChanged(completedState);

      // Completed stage: fill-in adds all missing registry phases.
      // feature-dev has 18 registry phases; 'implementation' is the only one
      // in the original 3 that matches a registry name, so 18 are added as
      // skipped. getPhaseCount() = registry total (18).
      // Children = 3 original + 18 fill-in = 20.
      expect(stageItem.getPhaseCount()).toBe(18);
      expect(stageItem.getChildren()).toHaveLength(20);
    });

    it("stage collapsibleState becomes Collapsed (not None) after completing with phases", async () => {
      const phases = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "complete"),
      ];

      const runningState = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases,
            current_phase: "implementation",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(runningState);

      const stageItem = provider.getStage("feature-dev")!;
      expect(stageItem.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);

      const completedState = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "complete",
            phases,
            current_phase: undefined,
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      mockService._fireStateChanged(completedState);

      // Completed + has phases → Collapsed (not None, not Expanded).
      expect(stageItem.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("phase children persist after stage fails with phases present", async () => {
      const phases = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "running"),
      ];

      const runningState = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "running",
            phases,
            current_phase: "implementation",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(runningState);

      const failedState = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "failed",
            phases,
            current_phase: undefined,
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      mockService._fireStateChanged(failedState);

      const stageItem = provider.getStage("feature-dev")!;
      // Failed stage: fill-in adds all missing registry phases.
      // feature-dev has 18 registry phases; 'implementation' matches one,
      // so 18 are added as skipped. getPhaseCount() = registry total (18).
      expect(stageItem.getPhaseCount()).toBe(18);
      expect(stageItem.getStatus()).toBe("failed");
    });

    // -----------------------------------------------------------------------
    // Issue #3240: stale "running" phase on a completed stage
    // -----------------------------------------------------------------------

    it("downgrades a phase stuck at 'running' when parent stage is complete (#3240)", async () => {
      // state.json carries a phase still marked "running" because its
      // phase.complete write was lost before stage.complete fired.
      const stalePhases = [
        makePhase("load-context", "complete"),
        makePhase("knowledge-base-read", "running"),
      ];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": {
            status: "complete",
            phases: stalePhases,
            current_phase: "knowledge-base-read",
          },
          "feature-dev": { status: "running", phases: [], current_phase: undefined },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-planning")!;
      const children = stageItem.getChildren() as PhaseTreeItem[];
      const stuck = children.find((c) => c.label === "Knowledge Base Read");
      expect(stuck, "knowledge-base-read child should exist").toBeDefined();
      expect(stuck!.getStatus()).toBe("complete");
      expect(children.some((c) => c.getStatus() === "running")).toBe(false);
    });

    it("downgrades a phase stuck at 'running' to 'failed' when parent stage failed (#3240)", async () => {
      const stalePhases = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "running"),
      ];

      const state = makeState({
        stages: {
          "pipeline-start": { status: "complete" },
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "complete" },
          "feature-dev": {
            status: "failed",
            phases: stalePhases,
            current_phase: "implementation",
          },
          "feature-validate": { status: "pending" },
          "pr-create": { status: "pending" },
          "pr-merge": { status: "pending" },
          "pipeline-finish": { status: "pending" },
        },
      });

      provider = await createProvider(mockService);
      mockService._fireStateChanged(state);

      const stageItem = provider.getStage("feature-dev")!;
      const children = stageItem.getChildren() as PhaseTreeItem[];
      const stuck = children.find((c) => c.label === "Implementation");
      expect(stuck, "implementation child should exist").toBeDefined();
      expect(stuck!.getStatus()).toBe("failed");
      expect(children.some((c) => c.getStatus() === "running")).toBe(false);
    });
  });
});
