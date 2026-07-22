/**
 * Unit tests for IPC phase handler state mutation in PipelineStateService.
 *
 * Covers:
 * - phase.start: _lastState.stages[stage].phases gets new entry with status: 'running'
 * - phase.start: current_phase and total_phases updated
 * - phase.start: _onStateChanged fired
 * - phase.complete: matching phase entry set to status: 'complete' with completed_at
 * - phase.complete: _onStateChanged fired
 * - Both handlers: no-op when _lastState is null
 * - Both handlers: issue number filter (events for other issues ignored)
 * - Duplicate phase guard: second phase.start for same name doesn't push duplicate
 *
 * @see Issue #1966 — IPC phase handlers don't update _lastState
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// IPC event handler capture
// ---------------------------------------------------------------------------

type EventHandler = (data: unknown) => void;
const ipcHandlers: Map<string, EventHandler> = new Map();

function fireIpcEvent(event: string, data: unknown): void {
  ipcHandlers.get(event)?.(data);
}

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn((event: string, handler: EventHandler) => {
        ipcHandlers.set(event, handler);
        return { dispose: vi.fn() };
      }),
      call: vi.fn().mockRejectedValue(new Error("IPC not connected")),
    }),
  },
}));

vi.mock("vscode", () => ({
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function makeService(issueNumber: number | null = null) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  ipcHandlers.clear();

  const svc =
    issueNumber !== null
      ? PipelineStateService.createForWorktree("/tmp/repo", issueNumber)
      : PipelineStateService.getInstance("/tmp/repo");

  return svc;
}

function makeState(issueNumber = 42, stage = "feature-dev") {
  return {
    issue_number: issueNumber,
    title: `Issue #${issueNumber}`,
    branch: "feat/test",
    stages: {
      [stage]: {
        status: "running" as const,
        startTime: Date.now(),
      },
    },
    started_at: new Date().toISOString(),
    tokens: { input: 0, output: 0 },
    execution_mode: "headless" as const,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineStateService — IPC phase handlers", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  // ── phase.start ───────────────────────────────────────────────────────────

  describe("phase.start handler", () => {
    it("adds a new phase entry with status running to _lastState", async () => {
      const svc = await makeService(42);
      // Inject state directly via initializePipeline fallback (IPC throws)
      await svc.initializePipeline(42, "Test", "feat/test");
      // Also ensure the stage exists
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
      };

      fireIpcEvent("phase.start", {
        issueNumber: 42,
        stage: "feature-dev",
        name: "load-context",
        index: 0,
        total: 10,
      });

      const state = await svc.getState();
      const phases = state!.stages["feature-dev"].phases;
      expect(phases).toBeDefined();
      expect(phases).toHaveLength(1);
      expect(phases![0].name).toBe("load-context");
      expect(phases![0].status).toBe("running");
      expect(phases![0].index).toBe(0);
      expect(phases![0].total).toBe(10);
      expect(phases![0].started_at).toBeDefined();
    });

    it("updates current_phase and total_phases on the stage", async () => {
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
      };

      fireIpcEvent("phase.start", {
        issueNumber: 42,
        stage: "feature-dev",
        name: "implementation",
        index: 3,
        total: 15,
      });

      const state = await svc.getState();
      const stageState = state!.stages["feature-dev"];
      expect(stageState.current_phase).toBe("implementation");
      expect(stageState.total_phases).toBe(15);
    });

    it("fires _onStateChanged after mutation", async () => {
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
      };

      const stateChangedValues: unknown[] = [];
      svc.onStateChanged((s) => stateChangedValues.push(s));

      fireIpcEvent("phase.start", {
        issueNumber: 42,
        stage: "feature-dev",
        name: "testing",
        index: 7,
        total: 15,
      });

      expect(stateChangedValues).toHaveLength(1);
    });

    it("does not mutate state when _lastState is null", async () => {
      const svc = await makeService(42);
      // Do NOT initialize — _lastState stays null

      const stateChangedValues: unknown[] = [];
      svc.onStateChanged((s) => stateChangedValues.push(s));

      fireIpcEvent("phase.start", {
        issueNumber: 42,
        stage: "feature-dev",
        name: "load-context",
        index: 0,
        total: 10,
      });

      expect(await svc.getState()).toBeNull();
      expect(stateChangedValues).toHaveLength(0);
    });

    it("ignores events for a different issue number", async () => {
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
      };

      fireIpcEvent("phase.start", {
        issueNumber: 999, // different issue
        stage: "feature-dev",
        name: "load-context",
        index: 0,
        total: 10,
      });

      const state = await svc.getState();
      expect(state!.stages["feature-dev"].phases).toBeUndefined();
    });

    it("does not push a duplicate entry for the same phase name", async () => {
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
      };

      const phaseEvent = {
        issueNumber: 42,
        stage: "feature-dev",
        name: "load-context",
        index: 0,
        total: 10,
      };

      fireIpcEvent("phase.start", phaseEvent);
      fireIpcEvent("phase.start", phaseEvent); // second identical event

      const state = await svc.getState();
      const phases = state!.stages["feature-dev"].phases;
      expect(phases).toHaveLength(1); // still only one entry
    });
  });

  // ── phase.complete ────────────────────────────────────────────────────────

  describe("phase.complete handler", () => {
    it("marks the matching running phase as complete with completed_at", async () => {
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
        phases: [
          {
            name: "load-context",
            index: 0,
            total: 10,
            status: "running",
            started_at: new Date().toISOString(),
          },
        ],
      };

      fireIpcEvent("phase.complete", {
        issueNumber: 42,
        stage: "feature-dev",
        name: "load-context",
        index: 0,
        total: 10,
      });

      const state = await svc.getState();
      const phase = state!.stages["feature-dev"].phases![0];
      expect(phase.status).toBe("complete");
      expect(phase.completed_at).toBeDefined();
    });

    it("fires _onStateChanged after mutation", async () => {
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
        phases: [
          {
            name: "load-context",
            index: 0,
            total: 10,
            status: "running",
          },
        ],
      };

      const stateChangedValues: unknown[] = [];
      svc.onStateChanged((s) => stateChangedValues.push(s));

      fireIpcEvent("phase.complete", {
        issueNumber: 42,
        stage: "feature-dev",
        name: "load-context",
        index: 0,
        total: 10,
      });

      expect(stateChangedValues).toHaveLength(1);
    });

    it("does not mutate state when _lastState is null", async () => {
      const svc = await makeService(42);
      // Do NOT initialize

      const stateChangedValues: unknown[] = [];
      svc.onStateChanged((s) => stateChangedValues.push(s));

      fireIpcEvent("phase.complete", {
        issueNumber: 42,
        stage: "feature-dev",
        name: "load-context",
        index: 0,
        total: 10,
      });

      expect(await svc.getState()).toBeNull();
      expect(stateChangedValues).toHaveLength(0);
    });

    it("ignores events for a different issue number", async () => {
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
        phases: [
          {
            name: "load-context",
            index: 0,
            total: 10,
            status: "running",
          },
        ],
      };

      fireIpcEvent("phase.complete", {
        issueNumber: 999, // different issue
        stage: "feature-dev",
        name: "load-context",
        index: 0,
        total: 10,
      });

      const state = await svc.getState();
      // Phase should remain 'running' since event was ignored
      expect(state!.stages["feature-dev"].phases![0].status).toBe("running");
    });
  });

  // ── pipeline.stateChanged phase preservation ──────────────────────────────

  describe("pipeline.stateChanged phase preservation", () => {
    it("preserves phases for a stage that transitions from running to complete", async () => {
      // Regression test: Go's stateChanged sends completedStages with only
      // timing data — no phase data. Without carry-forward, phases are lost
      // when a stage finishes, making the "16/16 phases" display disappear.
      // Issue #3419 also requires that any trailing `running` phase be
      // sealed to the terminal status so the orphan spinner clears.
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");

      // Seed _lastState: feature-dev running with 3 completed phases
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
        phases: [
          { name: "load-context", index: 0, status: "complete" },
          { name: "analyze", index: 1, status: "complete" },
          { name: "implement", index: 2, status: "running" },
        ],
        current_phase: "implement",
        total_phases: 16,
      };

      // Go sends stateChanged with feature-dev now in completedStages,
      // but NO phase data (this is what triggered the bug)
      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 42,
        repo: "nightgauge/nightgauge",
        state: {
          issueNumber: 42,
          stage: null,
          completedStages: [
            {
              stage: "feature-dev",
              startedAt: new Date().toISOString(),
              duration: 1000000000,
            },
          ],
          skippedStages: [],
          stageErrors: null,
          paused: false,
        },
      });

      const state = await svc.getState();
      const devStage = state!.stages["feature-dev"];

      // Stage is now complete
      expect(devStage.status).toBe("complete");
      // Phases preserved (not wiped) and the trailing `running` phase sealed.
      expect(devStage.phases).toBeDefined();
      expect(devStage.phases!.length).toBe(3);
      expect(devStage.total_phases).toBe(16);
      // Issue #3419: terminal stage has no current phase, and the trailing
      // `implement` phase is sealed to `complete` with completed_at set.
      expect(devStage.current_phase).toBeUndefined();
      expect(devStage.phases![2].status).toBe("complete");
      expect(devStage.phases![2].completed_at).toBeDefined();
    });

    it("still shows no phases for a completed stage that never had any", async () => {
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");

      // Stage had no phase data before completing
      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
      };

      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 42,
        repo: "nightgauge/nightgauge",
        state: {
          issueNumber: 42,
          stage: null,
          completedStages: [
            {
              stage: "feature-dev",
              startedAt: new Date().toISOString(),
              duration: 1000000000,
            },
          ],
          skippedStages: [],
          stageErrors: null,
          paused: false,
        },
      });

      const state = await svc.getState();
      const devStage = state!.stages["feature-dev"];

      expect(devStage.status).toBe("complete");
      expect(devStage.phases).toBeUndefined();
    });

    it("rehydrates per-stage phases from goState.phaseHistory when _lastState is empty (Issue #3415)", async () => {
      // Reproduces the extension-reload scenario: by the time we receive a
      // stateChanged event, feature-dev has already finished and we never saw
      // its phase events. Without phaseHistory rehydration, the stage's
      // "16/16 phases" count would be permanently lost.
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");
      // Important: do NOT seed any per-stage phases on _lastState.

      const startedAt = new Date().toISOString();
      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 42,
        repo: "nightgauge/nightgauge",
        state: {
          issueNumber: 42,
          stage: null,
          completedStages: [{ stage: "feature-dev", startedAt, duration: 1000000000 }],
          skippedStages: [],
          stageErrors: null,
          paused: false,
          phaseHistory: [
            {
              stage: "feature-dev",
              name: "load-context",
              index: 0,
              total: 18,
              status: "complete",
              startedAt,
              completedAt: startedAt,
            },
            {
              stage: "feature-dev",
              name: "analyze",
              index: 1,
              total: 18,
              status: "complete",
              startedAt,
              completedAt: startedAt,
            },
            {
              stage: "feature-dev",
              name: "implement",
              index: 2,
              total: 18,
              status: "complete",
              startedAt,
              completedAt: startedAt,
            },
          ],
        },
      });

      const state = await svc.getState();
      const devStage = state!.stages["feature-dev"];

      expect(devStage.status).toBe("complete");
      expect(devStage.phases).toBeDefined();
      expect(devStage.phases!.length).toBe(3);
      expect(devStage.total_phases).toBe(18);
      expect(devStage.phases!.map((p) => p.name)).toEqual(["load-context", "analyze", "implement"]);
    });

    it("seals trailing running phase when a terminal stage transitions via completedStages (Issue #3419)", async () => {
      // Reproduces the issue 888 scenario: pr-merge runs through self-assessment
      // (the last phase of the terminal stage). Skills never emit a phase:complete
      // marker, and Go has no next stage to trigger priorStageReconcile, so without
      // sealPhases the spinner outlives the merged PR.
      const svc = await makeService(888);
      await svc.initializePipeline(888, "Issue 888", "feat/test");

      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "pr-merge"
      ] = {
        status: "running",
        startTime: Date.now(),
        phases: [
          { name: "validate-environment", index: 0, status: "complete" },
          { name: "self-assessment", index: 13, status: "running" },
        ],
        current_phase: "self-assessment",
        total_phases: 14,
      };

      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 888,
        repo: "acme/platform",
        state: {
          issueNumber: 888,
          stage: null,
          completedStages: [
            {
              stage: "pr-merge",
              startedAt: new Date().toISOString(),
              duration: 1_000_000_000,
            },
          ],
          skippedStages: [],
          stageErrors: null,
          paused: false,
        },
      });

      const state = await svc.getState();
      const prMerge = state!.stages["pr-merge"];

      expect(prMerge.status).toBe("complete");
      expect(prMerge.phases).toHaveLength(2);
      // The orphan spinner — self-assessment — must be sealed.
      const selfAssess = prMerge.phases!.find((p) => p.name === "self-assessment")!;
      expect(selfAssess.status).toBe("complete");
      expect(selfAssess.completed_at).toBeDefined();
      // No current phase once the stage has ended.
      expect(prMerge.current_phase).toBeUndefined();
    });

    it("seals trailing running phase from phaseHistory when stage is in completedStages (Issue #3419)", async () => {
      // Same orphan-spinner scenario but the source of phases is Go's
      // phaseHistory (not in-memory accumulation) — exercises the sealing
      // path through phaseCarryForward → fromGo branch.
      const svc = await makeService(888);
      await svc.initializePipeline(888, "Issue 888", "feat/test");

      const startedAt = new Date().toISOString();
      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 888,
        repo: "acme/platform",
        state: {
          issueNumber: 888,
          stage: null,
          completedStages: [{ stage: "pr-merge", startedAt, duration: 1_000_000_000 }],
          skippedStages: [],
          stageErrors: null,
          paused: false,
          phaseHistory: [
            {
              stage: "pr-merge",
              name: "validate-environment",
              index: 0,
              total: 14,
              status: "complete",
              startedAt,
              completedAt: startedAt,
            },
            {
              stage: "pr-merge",
              name: "self-assessment",
              index: 13,
              total: 14,
              status: "running",
              startedAt,
            },
          ],
        },
      });

      const state = await svc.getState();
      const prMerge = state!.stages["pr-merge"];

      expect(prMerge.status).toBe("complete");
      const selfAssess = prMerge.phases!.find((p) => p.name === "self-assessment")!;
      expect(selfAssess.status).toBe("complete");
      expect(selfAssess.completed_at).toBeDefined();
      expect(prMerge.current_phase).toBeUndefined();
    });

    it("seals trailing running phase when a stage fails mid-phase (Issue #3419)", async () => {
      // A stage that fails while a phase is still running must not leave
      // the spinner alive. The phase is sealed to "failed" with completed_at.
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");

      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
        phases: [
          { name: "load-context", index: 0, status: "complete" },
          { name: "implementation", index: 7, status: "running" },
        ],
        current_phase: "implementation",
        total_phases: 17,
      };

      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 42,
        repo: "nightgauge/nightgauge",
        state: {
          issueNumber: 42,
          stage: null,
          completedStages: [],
          skippedStages: [],
          stageErrors: { "feature-dev": "tests failed" },
          paused: false,
        },
      });

      const state = await svc.getState();
      const dev = state!.stages["feature-dev"];

      expect(dev.status).toBe("failed");
      expect(dev.error).toBe("tests failed");
      const impl = dev.phases!.find((p) => p.name === "implementation")!;
      expect(impl.status).toBe("failed");
      expect(impl.completed_at).toBeDefined();
      expect(dev.current_phase).toBeUndefined();
    });

    it("seals trailing running phase when a stage is skipped (Issue #3419)", async () => {
      // Stages can be skipped by routing/decision logic mid-pipeline. Any
      // trailing `running` phase must be sealed to "skipped".
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");

      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-validate"
      ] = {
        status: "running",
        startTime: Date.now(),
        phases: [{ name: "tests", index: 0, status: "running" }],
        current_phase: "tests",
        total_phases: 1,
      };

      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 42,
        repo: "nightgauge/nightgauge",
        state: {
          issueNumber: 42,
          stage: null,
          completedStages: [],
          skippedStages: ["feature-validate"],
          stageErrors: null,
          paused: false,
        },
      });

      const state = await svc.getState();
      const validate = state!.stages["feature-validate"];

      expect(validate.status).toBe("skipped");
      expect(validate.phases).toHaveLength(1);
      expect(validate.phases![0].status).toBe("skipped");
      expect(validate.phases![0].completed_at).toBeDefined();
      expect(validate.current_phase).toBeUndefined();
    });

    it("preserves running phases when the stage itself is still running (Issue #3419)", async () => {
      // Sanity check: sealing only fires for terminal stages. A still-running
      // stage must keep its current_phase and `running` status untouched so
      // the live spinner reflects actual progress.
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");

      (svc as unknown as { _lastState: ReturnType<typeof makeState> })._lastState!.stages[
        "feature-dev"
      ] = {
        status: "running",
        startTime: Date.now(),
        phases: [{ name: "implementation", index: 7, status: "running" }],
        current_phase: "implementation",
        total_phases: 17,
      };

      const startedAt = new Date().toISOString();
      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 42,
        repo: "nightgauge/nightgauge",
        state: {
          issueNumber: 42,
          stage: "feature-dev",
          stageStart: startedAt,
          completedStages: [],
          skippedStages: [],
          stageErrors: null,
          paused: false,
        },
      });

      const state = await svc.getState();
      const dev = state!.stages["feature-dev"];

      expect(dev.status).toBe("running");
      expect(dev.phases![0].status).toBe("running");
      expect(dev.current_phase).toBe("implementation");
    });

    it("rehydrates running-stage phases from phaseHistory after a fresh subscription (Issue #3415)", async () => {
      // After extension reload mid-stage: _lastState is null, but Go's snapshot
      // already carries every phase that fired so far. The running-stage
      // branch should pick them up rather than starting fresh at zero.
      const svc = await makeService(42);
      await svc.initializePipeline(42, "Test", "feat/test");

      const startedAt = new Date().toISOString();
      fireIpcEvent("pipeline.stateChanged", {
        issueNumber: 42,
        repo: "nightgauge/nightgauge",
        state: {
          issueNumber: 42,
          stage: "feature-dev",
          stageStart: startedAt,
          completedStages: [],
          skippedStages: [],
          stageErrors: null,
          paused: false,
          phaseHistory: [
            {
              stage: "feature-dev",
              name: "load-context",
              index: 0,
              total: 18,
              status: "complete",
              startedAt,
              completedAt: startedAt,
            },
            {
              stage: "feature-dev",
              name: "implement",
              index: 1,
              total: 18,
              status: "running",
              startedAt,
            },
          ],
        },
      });

      const state = await svc.getState();
      const devStage = state!.stages["feature-dev"];

      expect(devStage.status).toBe("running");
      expect(devStage.phases).toHaveLength(2);
      expect(devStage.current_phase).toBe("implement");
      expect(devStage.total_phases).toBe(18);
    });
  });
});
