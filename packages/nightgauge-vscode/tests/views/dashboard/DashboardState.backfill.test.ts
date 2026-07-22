/**
 * DashboardState.backfill.test.ts
 *
 * Tests for historical data backfill from pipeline run artifacts.
 * Validates that existing pipeline state files on disk are imported
 * into dashboard history on initial load.
 *
 * @see Issue #614 - Backfill dashboard from existing pipeline history
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { DashboardState } from "../../../src/views/dashboard/DashboardState";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";
import * as fs from "node:fs/promises";
import * as path from "node:path";

// Mock fs module
vi.mock("node:fs/promises");

// Mock vscode
vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn().mockReturnValue({
      get: vi.fn().mockImplementation((_key: string, defaultValue: unknown) => defaultValue),
    }),
  },
}));

/**
 * Create a mock pipeline state.json content for testing
 */
function createMockStateJson(overrides: Record<string, unknown> = {}): string {
  const state = {
    schema_version: "1.0",
    issue_number: 42,
    title: "Add dark mode toggle",
    branch: "feat/42-dark-mode",
    base_branch: "main",
    started_at: "2026-02-10T14:00:00.000Z",
    updated_at: "2026-02-10T15:30:00.000Z",
    execution_mode: "automatic",
    paused: false,
    stages: {
      "pipeline-start": {
        status: "complete",
        started_at: "2026-02-10T14:00:00.000Z",
        completed_at: "2026-02-10T14:00:01.000Z",
        duration_ms: 1000,
      },
      "issue-pickup": {
        status: "complete",
        started_at: "2026-02-10T14:00:01.000Z",
        completed_at: "2026-02-10T14:02:00.000Z",
        duration_ms: 119000,
      },
      "feature-planning": {
        status: "complete",
        started_at: "2026-02-10T14:02:00.000Z",
        completed_at: "2026-02-10T14:10:00.000Z",
        duration_ms: 480000,
      },
      "feature-dev": {
        status: "complete",
        started_at: "2026-02-10T14:10:00.000Z",
        completed_at: "2026-02-10T14:45:00.000Z",
        duration_ms: 2100000,
      },
      "feature-validate": {
        status: "complete",
        started_at: "2026-02-10T14:45:00.000Z",
        completed_at: "2026-02-10T14:55:00.000Z",
        duration_ms: 600000,
      },
      "pr-create": {
        status: "complete",
        started_at: "2026-02-10T14:55:00.000Z",
        completed_at: "2026-02-10T15:00:00.000Z",
        duration_ms: 300000,
      },
      "pr-merge": {
        status: "complete",
        started_at: "2026-02-10T15:00:00.000Z",
        completed_at: "2026-02-10T15:05:00.000Z",
        duration_ms: 300000,
      },
      "pipeline-finish": {
        status: "complete",
        started_at: "2026-02-10T15:05:00.000Z",
        completed_at: "2026-02-10T15:05:01.000Z",
        duration_ms: 1000,
      },
    },
    tokens: {
      total_input: 50000,
      total_output: 15000,
      total_cache_read: 10000,
      total_cache_creation: 5000,
      estimated_cost_usd: 0.25,
      per_stage: {
        "issue-pickup": {
          input: 5000,
          output: 1500,
          cache_read: 1000,
          cache_creation: 500,
          cost_usd: 0.025,
        },
        "feature-planning": {
          input: 10000,
          output: 3000,
          cache_read: 2000,
          cache_creation: 1000,
          cost_usd: 0.05,
        },
        "feature-dev": {
          input: 25000,
          output: 7500,
          cache_read: 5000,
          cache_creation: 2500,
          cost_usd: 0.125,
        },
        "feature-validate": {
          input: 5000,
          output: 1500,
          cache_read: 1000,
          cache_creation: 500,
          cost_usd: 0.025,
        },
        "pr-create": {
          input: 3000,
          output: 1000,
          cache_read: 500,
          cache_creation: 300,
          cost_usd: 0.015,
        },
        "pr-merge": {
          input: 2000,
          output: 500,
          cache_read: 500,
          cache_creation: 200,
          cost_usd: 0.01,
        },
      },
    },
    ...overrides,
  };
  return JSON.stringify(state);
}

describe("DashboardState - Backfill from Pipeline Artifacts", () => {
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 1, 12, 10, 0, 0));
    workspaceState = createMockMemento();
    vi.mocked(fs.readdir).mockReset();
    vi.mocked(fs.readFile).mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should import a completed pipeline run from state.json", async () => {
    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);

    // History should be empty before backfill
    expect(state.getHistory()).toHaveLength(0);

    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(1);
    expect(state.getHistory()).toHaveLength(1);

    const run = state.getHistory()[0];
    expect(run.issueNumber).toBe(42);
    expect(run.title).toBe("Add dark mode toggle");
    expect(run.branch).toBe("feat/42-dark-mode");
    expect(run.status).toBe("complete");
    expect(run.usage.inputTokens).toBe(50000);
    expect(run.usage.outputTokens).toBe(15000);
    expect(run.usage.costUsd).toBe(0.25);
  });

  it("should not re-import runs already in history", async () => {
    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);

    // First backfill
    const first = await state.backfillFromPipelineArtifacts();
    expect(first).toBe(1);

    // Second backfill should skip the same issue
    const second = await state.backfillFromPipelineArtifacts();
    expect(second).toBe(0);
    expect(state.getHistory()).toHaveLength(1);
  });

  it("should skip state files with no completed stages", async () => {
    const pendingState = createMockStateJson({
      stages: {
        "pipeline-start": { status: "pending" },
        "issue-pickup": { status: "pending" },
        "feature-planning": { status: "pending" },
        "feature-dev": { status: "pending" },
        "feature-validate": { status: "pending" },
        "pr-create": { status: "pending" },
        "pr-merge": { status: "pending" },
        "pipeline-finish": { status: "pending" },
      },
    });

    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(pendingState);

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(0);
    expect(state.getHistory()).toHaveLength(0);
  });

  it("should detect failed runs from stage states", async () => {
    const failedState = createMockStateJson({
      stages: {
        "pipeline-start": {
          status: "complete",
          started_at: "2026-02-10T14:00:00.000Z",
          completed_at: "2026-02-10T14:00:01.000Z",
        },
        "issue-pickup": {
          status: "complete",
          started_at: "2026-02-10T14:00:01.000Z",
          completed_at: "2026-02-10T14:02:00.000Z",
        },
        "feature-planning": {
          status: "failed",
          started_at: "2026-02-10T14:02:00.000Z",
          completed_at: "2026-02-10T14:05:00.000Z",
          error: "Planning failed",
        },
        "feature-dev": { status: "skipped" },
        "feature-validate": { status: "skipped" },
        "pr-create": { status: "skipped" },
        "pr-merge": { status: "skipped" },
        "pipeline-finish": { status: "skipped" },
      },
    });

    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(failedState);

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(1);
    const run = state.getHistory()[0];
    expect(run.status).toBe("failed");
  });

  it("should calculate ROI metrics for imported runs", async () => {
    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);
    await state.backfillFromPipelineArtifacts();

    const run = state.getHistory()[0];
    expect(run.manualEstimateMs).toBeGreaterThan(0);
    expect(run.timeSavedMs).toBeDefined();
    expect(run.efficiency).toBeDefined();
    expect(run.efficiency!.tokensPerMinute).toBeGreaterThan(0);
  });

  it("should handle missing pipeline directory gracefully", async () => {
    vi.mocked(fs.readdir).mockRejectedValue(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(0);
    expect(state.getHistory()).toHaveLength(0);
  });

  it("should skip corrupted state files without failing", async () => {
    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue("{ invalid json !!!");

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(0);
    expect(state.getHistory()).toHaveLength(0);
  });

  it("should skip .corrupt backup files", async () => {
    vi.mocked(fs.readdir).mockResolvedValue(["state.json", "state.json.corrupt-2026-02-10"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    // Should only process state.json, not the corrupt backup
    expect(imported).toBe(1);
  });

  it("should return 0 when no workspace root is set", async () => {
    const state = new DashboardState(workspaceState);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(0);
  });

  it("should extract per-stage token usage from pipeline state", async () => {
    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);
    await state.backfillFromPipelineArtifacts();

    const run = state.getHistory()[0];
    const planningStage = run.stages.find((s) => s.stage === "feature-planning");
    expect(planningStage?.tokenUsage).toBeDefined();
    expect(planningStage?.tokenUsage?.inputTokens).toBe(10000);
    expect(planningStage?.tokenUsage?.outputTokens).toBe(3000);
    expect(planningStage?.tokenUsage?.costUsd).toBe(0.05);
  });

  it("should persist imported history to workspace storage", async () => {
    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);
    await state.backfillFromPipelineArtifacts();

    // Verify history was persisted to Memento
    const storedHistory = workspaceState.get<unknown[]>("nightgauge.dashboard.history", []);
    expect(storedHistory).toHaveLength(1);
  });

  // =========================================================================
  // In-progress run import tests (Issue #639)
  // =========================================================================

  it("should import an in-progress state.json with running stages but no complete stages", async () => {
    const inProgressState = createMockStateJson({
      issue_number: 99,
      title: "In-progress feature",
      branch: "feat/99-in-progress",
      stages: {
        "issue-pickup": {
          status: "running",
          started_at: "2026-02-10T14:00:00.000Z",
        },
        "feature-planning": { status: "pending" },
        "feature-dev": { status: "pending" },
        "feature-validate": { status: "pending" },
        "pr-create": { status: "pending" },
        "pr-merge": { status: "pending" },
      },
      tokens: {
        total_input: 1000,
        total_output: 200,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0.01,
        per_stage: {},
      },
    });

    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(inProgressState);

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(1);
    expect(state.getHistory()).toHaveLength(1);

    const run = state.getHistory()[0];
    expect(run.issueNumber).toBe(99);
    expect(run.title).toBe("In-progress feature");
    expect(run.status).toBe("running");
  });

  it("should import a state.json with one complete and one running stage", async () => {
    const mixedState = createMockStateJson({
      issue_number: 100,
      title: "Partially complete feature",
      branch: "feat/100-partial",
      stages: {
        "issue-pickup": {
          status: "complete",
          started_at: "2026-02-10T14:00:01.000Z",
          completed_at: "2026-02-10T14:02:00.000Z",
          duration_ms: 119000,
        },
        "feature-planning": {
          status: "running",
          started_at: "2026-02-10T14:02:00.000Z",
        },
        "feature-dev": { status: "pending" },
        "feature-validate": { status: "pending" },
        "pr-create": { status: "pending" },
        "pr-merge": { status: "pending" },
      },
      tokens: {
        total_input: 15000,
        total_output: 4500,
        total_cache_read: 3000,
        total_cache_creation: 1500,
        estimated_cost_usd: 0.075,
        per_stage: {
          "issue-pickup": {
            input: 5000,
            output: 1500,
            cache_read: 1000,
            cache_creation: 500,
            cost_usd: 0.025,
          },
          "feature-planning": {
            input: 10000,
            output: 3000,
            cache_read: 2000,
            cache_creation: 1000,
            cost_usd: 0.05,
          },
        },
      },
    });

    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(mixedState);

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(1);
    expect(state.getHistory()).toHaveLength(1);

    const run = state.getHistory()[0];
    expect(run.issueNumber).toBe(100);
    expect(run.title).toBe("Partially complete feature");
    // Not all stages are resolved, so status should be 'running'
    expect(run.status).toBe("running");
    expect(run.usage.inputTokens).toBe(15000);
    expect(run.usage.outputTokens).toBe(4500);
    expect(run.usage.costUsd).toBe(0.075);

    // Verify the complete stage has token usage
    const pickupStage = run.stages.find((s) => s.stage === "issue-pickup");
    expect(pickupStage?.status).toBe("complete");
    expect(pickupStage?.tokenUsage?.inputTokens).toBe(5000);

    // Verify the running stage is present
    const planningStage = run.stages.find((s) => s.stage === "feature-planning");
    expect(planningStage?.status).toBe("running");
  });

  it("should not import a state.json matching the current active run issue number and startedAt", async () => {
    // Use the same timestamp as the fake system time (what startRun will use)
    const activeIssueState = createMockStateJson({
      issue_number: 55,
      title: "Active run feature",
      branch: "feat/55-active",
      started_at: new Date(2026, 1, 12, 10, 0, 0).toISOString(),
      stages: {
        "issue-pickup": {
          status: "running",
          started_at: new Date(2026, 1, 12, 10, 0, 0).toISOString(),
        },
        "feature-planning": { status: "pending" },
        "feature-dev": { status: "pending" },
        "feature-validate": { status: "pending" },
        "pr-create": { status: "pending" },
        "pr-merge": { status: "pending" },
      },
    });

    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(activeIssueState);

    const state = new DashboardState(workspaceState, workspaceRoot);

    // Start a live run for issue #55 — same issue AND same startedAt as the state file
    state.startRun(55, "Active run feature", "feat/55-active");

    const imported = await state.backfillFromPipelineArtifacts();

    // Should skip because the issue and startedAt match the currentRun
    expect(imported).toBe(0);
    expect(state.getHistory()).toHaveLength(0);
  });

  it("should import a past run for the same issue as the current active run (#990)", async () => {
    // A completed past run for issue #55 with a different started_at
    const pastRunState = createMockStateJson({
      issue_number: 55,
      title: "Previous attempt",
      branch: "feat/55-active",
      started_at: "2026-02-09T10:00:00.000Z",
    });

    vi.mocked(fs.readdir).mockResolvedValue(["state.json"] as any);
    vi.mocked(fs.readFile).mockResolvedValue(pastRunState);

    const state = new DashboardState(workspaceState, workspaceRoot);

    // Start a NEW live run for issue #55 (different time than the past run)
    state.startRun(55, "Active run feature", "feat/55-active");

    const imported = await state.backfillFromPipelineArtifacts();

    // Should import because the past run has a different startedAt
    expect(imported).toBe(1);
    expect(state.getHistory()[0].issueNumber).toBe(55);
    expect(state.getHistory()[0].title).toBe("Previous attempt");
  });
});

/**
 * Create a mock JSONL history record (one line per run)
 */
function createMockJsonlRecord(overrides: Record<string, unknown> = {}): string {
  const record = {
    schema_version: "1",
    record_type: "run",
    issue_number: 100,
    title: "feat: Add dark mode toggle",
    branch: "feat/100-dark-mode",
    base_branch: "main",
    execution_mode: "automatic",
    started_at: "2026-02-13T10:00:00.000Z",
    completed_at: "2026-02-13T10:20:00.000Z",
    total_duration_ms: 1200000,
    outcome: "complete",
    stages: {
      "pipeline-start": {
        status: "complete",
        started_at: "2026-02-13T10:00:00.000Z",
        completed_at: "2026-02-13T10:00:01.000Z",
        duration_ms: 1000,
      },
      "issue-pickup": {
        status: "complete",
        started_at: "2026-02-13T10:00:01.000Z",
        completed_at: "2026-02-13T10:02:00.000Z",
        duration_ms: 119000,
      },
      "feature-planning": {
        status: "complete",
        started_at: "2026-02-13T10:02:00.000Z",
        completed_at: "2026-02-13T10:07:00.000Z",
        duration_ms: 300000,
      },
      "feature-dev": {
        status: "complete",
        started_at: "2026-02-13T10:07:00.000Z",
        completed_at: "2026-02-13T10:14:00.000Z",
        duration_ms: 420000,
      },
      "feature-validate": {
        status: "complete",
        started_at: "2026-02-13T10:14:00.000Z",
        completed_at: "2026-02-13T10:17:00.000Z",
        duration_ms: 180000,
      },
      "pr-create": {
        status: "complete",
        started_at: "2026-02-13T10:17:00.000Z",
        completed_at: "2026-02-13T10:18:00.000Z",
        duration_ms: 60000,
      },
      "pr-merge": {
        status: "complete",
        started_at: "2026-02-13T10:18:00.000Z",
        completed_at: "2026-02-13T10:20:00.000Z",
        duration_ms: 120000,
      },
      "pipeline-finish": {
        status: "complete",
        started_at: "2026-02-13T10:20:00.000Z",
      },
    },
    tokens: {
      total_input: 3000,
      total_output: 25000,
      total_cache_read: 3000000,
      total_cache_creation: 150000,
      estimated_cost_usd: 4.5,
      per_stage: {
        "issue-pickup": {
          input: 50,
          output: 3000,
          cache_read: 350000,
          cache_creation: 20000,
          cost_usd: 0.5,
        },
        "feature-planning": {
          input: 500,
          output: 7000,
          cache_read: 1000000,
          cache_creation: 60000,
          cost_usd: 1.5,
        },
        "feature-dev": {
          input: 1000,
          output: 8000,
          cache_read: 800000,
          cache_creation: 40000,
          cost_usd: 1.2,
        },
        "feature-validate": {
          input: 800,
          output: 4000,
          cache_read: 500000,
          cache_creation: 20000,
          cost_usd: 0.8,
        },
        "pr-create": {
          input: 300,
          output: 1500,
          cache_read: 200000,
          cache_creation: 5000,
          cost_usd: 0.3,
        },
        "pr-merge": {
          input: 350,
          output: 1500,
          cache_read: 150000,
          cache_creation: 5000,
          cost_usd: 0.2,
        },
      },
    },
    ...overrides,
  };
  return JSON.stringify(record);
}

describe("DashboardState - Backfill from JSONL History", () => {
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 1, 14, 10, 0, 0));
    workspaceState = createMockMemento();
    vi.mocked(fs.readdir).mockReset();
    vi.mocked(fs.readFile).mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should import runs from history JSONL files", async () => {
    // Pipeline dir has no state files, but history dir has JSONL
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return [] as any; // No state files in pipeline dir
    });
    vi.mocked(fs.readFile).mockResolvedValue(createMockJsonlRecord());

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(1);
    const run = state.getHistory()[0];
    expect(run.issueNumber).toBe(100);
    expect(run.title).toBe("feat: Add dark mode toggle");
    expect(run.status).toBe("complete");
    expect(run.usage.inputTokens).toBe(3000);
    expect(run.usage.outputTokens).toBe(25000);
    expect(run.usage.costUsd).toBe(4.5);
    expect(run.usage.durationMs).toBe(1200000);
  });

  it("should import multiple runs from multi-line JSONL", async () => {
    const line1 = createMockJsonlRecord({ issue_number: 101, title: "First" });
    const line2 = createMockJsonlRecord({ issue_number: 102, title: "Second" });
    const content = `${line1}\n${line2}\n`;

    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return [] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(content);

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(2);
    expect(state.getHistory()).toHaveLength(2);
  });

  it("should deduplicate between state.json and JSONL records", async () => {
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return ["state.json"] as any;
    });
    // Both state.json and JSONL have issue #42 with matching started_at
    vi.mocked(fs.readFile).mockImplementation(async (filePath) => {
      const p = filePath.toString();
      if (p.endsWith(".jsonl")) {
        return createMockJsonlRecord({
          issue_number: 42,
          started_at: "2026-02-10T14:00:00.000Z",
        });
      }
      return createMockStateJson(); // Also issue #42
    });

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    // Should only import once (state.json is processed first)
    expect(imported).toBe(1);
    expect(state.getHistory()).toHaveLength(1);
  });

  it("should import multiple runs for the same issue with different timestamps (#990)", async () => {
    const run1 = createMockJsonlRecord({
      issue_number: 42,
      started_at: "2026-02-13T10:00:00.000Z",
      title: "First attempt",
    });
    const run2 = createMockJsonlRecord({
      issue_number: 42,
      started_at: "2026-02-13T14:00:00.000Z",
      title: "Retry after failure",
      outcome: "failed",
    });
    const content = `${run1}\n${run2}\n`;

    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return [] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(content);

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    // Both runs should be imported — same issue but different timestamps
    expect(imported).toBe(2);
    expect(state.getHistory()).toHaveLength(2);
    // Most recent first (sorted by startedAt descending)
    expect(state.getHistory()[0].title).toBe("Retry after failure");
    expect(state.getHistory()[1].title).toBe("First attempt");
  });

  it("should skip malformed JSONL lines gracefully", async () => {
    const content = `${createMockJsonlRecord()}\n{ broken json !!!\n${createMockJsonlRecord({ issue_number: 200, title: "Good" })}\n`;

    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return [] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(content);

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    // Should import 2 valid records, skip the malformed line
    expect(imported).toBe(2);
  });

  it("should use outcome field from JSONL for run status", async () => {
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return [] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(createMockJsonlRecord({ outcome: "failed" }));

    const state = new DashboardState(workspaceState, workspaceRoot);
    await state.backfillFromPipelineArtifacts();

    expect(state.getHistory()[0].status).toBe("failed");
  });

  it("should use total_duration_ms from JSONL records", async () => {
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return [] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(createMockJsonlRecord({ total_duration_ms: 999000 }));

    const state = new DashboardState(workspaceState, workspaceRoot);
    await state.backfillFromPipelineArtifacts();

    expect(state.getHistory()[0].usage.durationMs).toBe(999000);
  });

  it("should handle missing history directory gracefully", async () => {
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) {
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      }
      return ["state.json"] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    // Should still import from state.json
    expect(imported).toBe(1);
  });

  it("should read from multiple JSONL files across days", async () => {
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) {
        return ["2026-02-13.jsonl", "2026-02-14.jsonl"] as any;
      }
      return [] as any;
    });
    vi.mocked(fs.readFile).mockImplementation(async (filePath) => {
      const p = filePath.toString();
      if (p.includes("2026-02-13")) {
        return createMockJsonlRecord({ issue_number: 101 });
      }
      return createMockJsonlRecord({ issue_number: 102 });
    });

    const state = new DashboardState(workspaceState, workspaceRoot);
    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(2);
  });

  it("should calculate efficiency metrics for JSONL-imported runs", async () => {
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return [] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(createMockJsonlRecord());

    const state = new DashboardState(workspaceState, workspaceRoot);
    await state.backfillFromPipelineArtifacts();

    const run = state.getHistory()[0];
    expect(run.manualEstimateMs).toBeGreaterThan(0);
    expect(run.timeSavedMs).toBeDefined();
    expect(run.efficiency).toBeDefined();
    expect(run.efficiency!.cacheHitRate).toBeGreaterThan(0);
  });
});

describe("DashboardState - Rescrub (Clear and Rebuild)", () => {
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 1, 14, 10, 0, 0));
    workspaceState = createMockMemento();
    vi.mocked(fs.readdir).mockReset();
    vi.mocked(fs.readFile).mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should clear existing history when rescrub is true", async () => {
    // First, populate history with a state.json run
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return [] as any;
      return ["state.json"] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);
    await state.backfillFromPipelineArtifacts();
    expect(state.getHistory()).toHaveLength(1);

    // Now rescrub — should clear and rebuild with JSONL data
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return ["2026-02-13.jsonl"] as any;
      return [] as any; // No more state.json
    });
    vi.mocked(fs.readFile).mockResolvedValue(
      createMockJsonlRecord({ issue_number: 200, title: "New run" })
    );

    const imported = await state.backfillFromPipelineArtifacts({
      rescrub: true,
    });

    expect(imported).toBe(1);
    expect(state.getHistory()).toHaveLength(1);
    // Should be the new JSONL run, not the old state.json run
    expect(state.getHistory()[0].issueNumber).toBe(200);
  });

  it("should not clear history when rescrub is false/default", async () => {
    vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
      const dir = dirPath.toString();
      if (dir.endsWith("history")) return [] as any;
      return ["state.json"] as any;
    });
    vi.mocked(fs.readFile).mockResolvedValue(createMockStateJson());

    const state = new DashboardState(workspaceState, workspaceRoot);
    await state.backfillFromPipelineArtifacts();
    expect(state.getHistory()).toHaveLength(1);

    // Second call without rescrub should not reimport (dedup)
    const imported = await state.backfillFromPipelineArtifacts();
    expect(imported).toBe(0);
    expect(state.getHistory()).toHaveLength(1);
  });
});

describe("DashboardState - Last Refreshed Timestamp", () => {
  it("should initialize with current time", () => {
    vi.useFakeTimers();
    const now = new Date(2026, 1, 12, 14, 30, 0);
    vi.setSystemTime(now);

    const state = new DashboardState(createMockMemento());
    expect(state.getLastRefreshedAt().getTime()).toBe(now.getTime());

    vi.useRealTimers();
  });

  it("should update when markRefreshed is called", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 1, 12, 14, 0, 0));

    const state = new DashboardState(createMockMemento());
    const initial = state.getLastRefreshedAt().getTime();

    // Advance time
    vi.setSystemTime(new Date(2026, 1, 12, 14, 5, 0));
    state.markRefreshed();

    expect(state.getLastRefreshedAt().getTime()).toBeGreaterThan(initial);

    vi.useRealTimers();
  });
});
