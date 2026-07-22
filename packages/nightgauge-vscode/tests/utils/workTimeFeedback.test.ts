/**
 * Unit tests for WorkTimeFeedback
 *
 * @see workTimeFeedback.ts
 * @see Issue #310 - Add Actual Work Time Feedback Loop
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import {
  calculateWorkTime,
  getCompletedStages,
  createObservation,
  appendObservationToYAML,
  pruneOldObservations,
  calculateSizeAverages,
  readWorkTimeFeedback,
  type WorkTimeObservation,
} from "../../src/utils/workTimeFeedback";
import type { PipelineState } from "../../src/services/PipelineStateService";
import type { PipelineStage } from "@nightgauge/sdk";

describe("calculateWorkTime", () => {
  it("should sum duration_ms for all completed stages", () => {
    const state: PipelineState = {
      schema_version: "1.0",
      issue_number: 310,
      title: "Test Issue",
      branch: "feat/310-test",
      base_branch: "main",
      started_at: "2026-02-06T10:00:00Z",
      updated_at: "2026-02-06T10:30:00Z",
      execution_mode: "automatic",
      paused: false,
      stages: {
        "pipeline-start": {
          status: "complete",
          started_at: "2026-02-06T10:00:00Z",
          completed_at: "2026-02-06T10:00:10Z",
          duration_ms: 10000,
        },
        "issue-pickup": {
          status: "complete",
          started_at: "2026-02-06T10:00:10Z",
          completed_at: "2026-02-06T10:05:00Z",
          duration_ms: 290000, // ~5 min
        },
        "feature-planning": {
          status: "complete",
          started_at: "2026-02-06T10:05:00Z",
          completed_at: "2026-02-06T10:15:00Z",
          duration_ms: 600000, // 10 min
        },
        "feature-dev": {
          status: "complete",
          started_at: "2026-02-06T10:15:00Z",
          completed_at: "2026-02-06T10:35:00Z",
          duration_ms: 1200000, // 20 min
        },
        "feature-validate": {
          status: "complete",
          started_at: "2026-02-06T10:35:00Z",
          completed_at: "2026-02-06T10:40:00Z",
          duration_ms: 300000, // 5 min
        },
        "pr-create": {
          status: "complete",
          started_at: "2026-02-06T10:40:00Z",
          completed_at: "2026-02-06T10:45:00Z",
          duration_ms: 300000, // 5 min
        },
        "pr-merge": {
          status: "pending",
        },
        "pipeline-finish": {
          status: "pending",
        },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.05,
      },
    };

    const workTime = calculateWorkTime(state);

    // 10s + 290s + 600s + 1200s + 300s + 300s = 2700s = 45 min
    expect(workTime).toBe(45);
  });

  it("should return 0 when no stages are complete", () => {
    const state: PipelineState = {
      schema_version: "1.0",
      issue_number: 310,
      title: "Test Issue",
      branch: "feat/310-test",
      base_branch: "main",
      started_at: "2026-02-06T10:00:00Z",
      updated_at: "2026-02-06T10:00:00Z",
      execution_mode: "automatic",
      paused: false,
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
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
    };

    expect(calculateWorkTime(state)).toBe(0);
  });

  it("should exclude failed and skipped stages", () => {
    const state: PipelineState = {
      schema_version: "1.0",
      issue_number: 310,
      title: "Test Issue",
      branch: "feat/310-test",
      base_branch: "main",
      started_at: "2026-02-06T10:00:00Z",
      updated_at: "2026-02-06T10:30:00Z",
      execution_mode: "automatic",
      paused: false,
      stages: {
        "pipeline-start": {
          status: "complete",
          duration_ms: 10000,
        },
        "issue-pickup": {
          status: "complete",
          duration_ms: 300000, // 5 min
        },
        "feature-planning": {
          status: "skipped", // Should NOT count
        },
        "feature-dev": {
          status: "complete",
          duration_ms: 600000, // 10 min
        },
        "feature-validate": {
          status: "failed", // Should NOT count
          duration_ms: 100000,
        },
        "pr-create": {
          status: "complete",
          duration_ms: 300000, // 5 min
        },
        "pr-merge": { status: "pending" },
        "pipeline-finish": { status: "pending" },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.05,
      },
    };

    const workTime = calculateWorkTime(state);

    // Only: 10s + 300s + 600s + 300s = 1210s = ~20 min
    expect(workTime).toBe(20);
  });

  it("should skip stages with missing duration_ms", () => {
    const state: PipelineState = {
      schema_version: "1.0",
      issue_number: 310,
      title: "Test Issue",
      branch: "feat/310-test",
      base_branch: "main",
      started_at: "2026-02-06T10:00:00Z",
      updated_at: "2026-02-06T10:30:00Z",
      execution_mode: "automatic",
      paused: false,
      stages: {
        "pipeline-start": {
          status: "complete",
          // Missing duration_ms - should skip
        },
        "issue-pickup": {
          status: "complete",
          duration_ms: 300000, // 5 min
        },
        "feature-planning": {
          status: "complete",
          duration_ms: 0, // Zero duration - should skip
        },
        "feature-dev": {
          status: "complete",
          duration_ms: 600000, // 10 min
        },
        "feature-validate": { status: "pending" },
        "pr-create": { status: "pending" },
        "pr-merge": { status: "pending" },
        "pipeline-finish": { status: "pending" },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.05,
      },
    };

    const workTime = calculateWorkTime(state);

    // Only: 300s + 600s = 900s = 15 min
    expect(workTime).toBe(15);
  });
});

describe("getCompletedStages", () => {
  it("should return only completed stages", () => {
    const state: PipelineState = {
      schema_version: "1.0",
      issue_number: 310,
      title: "Test Issue",
      branch: "feat/310-test",
      base_branch: "main",
      started_at: "2026-02-06T10:00:00Z",
      updated_at: "2026-02-06T10:30:00Z",
      execution_mode: "automatic",
      paused: false,
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "skipped" },
        "feature-dev": { status: "complete" },
        "feature-validate": { status: "failed" },
        "pr-create": { status: "running" },
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
    };

    const completed = getCompletedStages(state);

    expect(completed).toEqual(["pipeline-start", "issue-pickup", "feature-dev"]);
  });

  it("should return empty array when no stages complete", () => {
    const state: PipelineState = {
      schema_version: "1.0",
      issue_number: 310,
      title: "Test Issue",
      branch: "feat/310-test",
      base_branch: "main",
      started_at: "2026-02-06T10:00:00Z",
      updated_at: "2026-02-06T10:00:00Z",
      execution_mode: "automatic",
      paused: false,
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
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
    };

    expect(getCompletedStages(state)).toEqual([]);
  });
});

describe("createObservation", () => {
  it("should create observation with all fields", () => {
    const state: PipelineState = {
      schema_version: "1.0",
      issue_number: 310,
      title: "Test Issue",
      branch: "feat/310-test",
      base_branch: "main",
      started_at: "2026-02-06T10:00:00Z",
      updated_at: "2026-02-06T10:45:00Z",
      execution_mode: "automatic",
      paused: false,
      stages: {
        "pipeline-start": { status: "complete", duration_ms: 10000 },
        "issue-pickup": { status: "complete", duration_ms: 300000 },
        "feature-planning": { status: "complete", duration_ms: 600000 },
        "feature-dev": { status: "complete", duration_ms: 1200000 },
        "feature-validate": { status: "complete", duration_ms: 300000 },
        "pr-create": { status: "complete", duration_ms: 300000 },
        "pr-merge": { status: "complete", duration_ms: 90000 },
        "pipeline-finish": { status: "complete", duration_ms: 10000 },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.05,
      },
    };

    const observation = createObservation(state, {
      size: "M",
      priority: "high",
      task_type: "feature",
      estimated_minutes: 30,
      routing: "standard",
    });

    expect(observation.issue_number).toBe(310);
    expect(observation.size).toBe("M");
    expect(observation.priority).toBe("high");
    expect(observation.task_type).toBe("feature");
    expect(observation.actual_work_minutes).toBe(47); // Sum of all durations (2810s = 46.8 min, rounds to 47)
    expect(observation.estimated_minutes).toBe(30);
    expect(observation.routing).toBe("standard");
    expect(observation.stages_completed).toHaveLength(8);
    expect(observation.timestamp).toMatch(/^\d{4}-\d{2}-\d{2}T/);
  });

  it("should handle null/undefined optional fields", () => {
    const state: PipelineState = {
      schema_version: "1.0",
      issue_number: 310,
      title: "Test Issue",
      branch: "feat/310-test",
      base_branch: "main",
      started_at: "2026-02-06T10:00:00Z",
      updated_at: "2026-02-06T10:45:00Z",
      execution_mode: "automatic",
      paused: false,
      stages: {
        "pipeline-start": { status: "complete", duration_ms: 300000 },
        "issue-pickup": { status: "pending" },
        "feature-planning": { status: "pending" },
        "feature-dev": { status: "pending" },
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
    };

    const observation = createObservation(state, {
      size: null,
      // priority, task_type, estimated_minutes, routing omitted
    });

    expect(observation.size).toBeNull();
    expect(observation.priority).toBeNull();
    expect(observation.task_type).toBeNull();
    expect(observation.estimated_minutes).toBe(0);
    expect(observation.routing).toBe("unknown");
  });
});

describe("pruneOldObservations", () => {
  it("should keep last N observations", () => {
    const observations: WorkTimeObservation[] = [];

    // Create 60 observations
    for (let i = 1; i <= 60; i++) {
      observations.push({
        issue_number: i,
        size: "M",
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 30,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup", "feature-dev"] as PipelineStage[],
        timestamp: `2026-02-06T10:${String(i).padStart(2, "0")}:00Z`,
      });
    }

    const pruned = pruneOldObservations(observations, 50);

    expect(pruned).toHaveLength(50);
    expect(pruned[0].issue_number).toBe(11); // First kept observation
    expect(pruned[49].issue_number).toBe(60); // Last observation
  });

  it("should not modify array when under limit", () => {
    const observations: WorkTimeObservation[] = [
      {
        issue_number: 1,
        size: "M",
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 30,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      },
    ];

    const pruned = pruneOldObservations(observations, 50);

    expect(pruned).toHaveLength(1);
    expect(pruned).toBe(observations); // Same reference
  });

  it("should handle exactly at limit", () => {
    const observations: WorkTimeObservation[] = Array.from({ length: 50 }, (_, i) => ({
      issue_number: i + 1,
      size: "M",
      priority: "medium",
      task_type: "feature",
      actual_work_minutes: 30,
      estimated_minutes: 30,
      routing: "standard",
      stages_completed: ["issue-pickup"] as PipelineStage[],
      timestamp: "2026-02-06T10:00:00Z",
    }));

    const pruned = pruneOldObservations(observations, 50);

    expect(pruned).toHaveLength(50);
    expect(pruned).toBe(observations); // Same reference (no pruning needed)
  });
});

describe("calculateSizeAverages", () => {
  it("should calculate averages per size", () => {
    const observations: WorkTimeObservation[] = [
      {
        issue_number: 1,
        size: "S",
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 10,
        estimated_minutes: 15,
        routing: "trivial",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      },
      {
        issue_number: 2,
        size: "S",
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 20,
        estimated_minutes: 15,
        routing: "trivial",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      },
      {
        issue_number: 3,
        size: "M",
        priority: "high",
        task_type: "feature",
        actual_work_minutes: 40,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      },
    ];

    const averages = calculateSizeAverages(observations);

    expect(averages["S"]).toEqual({
      estimated: 15,
      actual_average: 15, // (10 + 20) / 2
      observation_count: 2,
    });

    expect(averages["M"]).toEqual({
      estimated: 30,
      actual_average: 40,
      observation_count: 1,
    });
  });

  it("should exclude observations with zero work time", () => {
    const observations: WorkTimeObservation[] = [
      {
        issue_number: 1,
        size: "M",
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 0, // Should be excluded
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: [] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      },
      {
        issue_number: 2,
        size: "M",
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 40,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      },
    ];

    const averages = calculateSizeAverages(observations);

    // Should only count the second observation
    expect(averages["M"]).toEqual({
      estimated: 30,
      actual_average: 40,
      observation_count: 1,
    });
  });

  it("should skip observations with null size", () => {
    const observations: WorkTimeObservation[] = [
      {
        issue_number: 1,
        size: null,
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 30,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      },
    ];

    const averages = calculateSizeAverages(observations);

    expect(Object.keys(averages)).toHaveLength(0);
  });

  it("should return empty object for no observations", () => {
    const averages = calculateSizeAverages([]);

    expect(averages).toEqual({});
  });
});

describe("YAML persistence", () => {
  let tmpDir: string;
  let yamlPath: string;

  beforeEach(async () => {
    tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "nightgauge-test-"));
    yamlPath = path.join(tmpDir, "complexity-model.yaml");
  });

  afterEach(async () => {
    await fs.rm(tmpDir, { recursive: true, force: true });
  });

  describe("appendObservationToYAML", () => {
    it("should create file if it does not exist", async () => {
      const observation: WorkTimeObservation = {
        issue_number: 310,
        size: "M",
        priority: "high",
        task_type: "feature",
        actual_work_minutes: 45,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup", "feature-dev"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      };

      await appendObservationToYAML(observation, yamlPath);

      const content = await fs.readFile(yamlPath, "utf-8");
      expect(content).toContain("work_time_feedback");
      expect(content).toContain("issue_number: 310");
      expect(content).toContain("size: M");
    });

    it("should append to existing file", async () => {
      // Create initial file with one observation
      const obs1: WorkTimeObservation = {
        issue_number: 1,
        size: "S",
        priority: "low",
        task_type: "bugfix",
        actual_work_minutes: 10,
        estimated_minutes: 15,
        routing: "trivial",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T09:00:00Z",
      };

      await appendObservationToYAML(obs1, yamlPath);

      // Append second observation
      const obs2: WorkTimeObservation = {
        issue_number: 2,
        size: "M",
        priority: "high",
        task_type: "feature",
        actual_work_minutes: 45,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup", "feature-dev"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      };

      await appendObservationToYAML(obs2, yamlPath);

      const feedback = await readWorkTimeFeedback(yamlPath);
      expect(feedback?.observations).toHaveLength(2);
      expect(feedback?.observations[0].issue_number).toBe(1);
      expect(feedback?.observations[1].issue_number).toBe(2);
    });

    it("should recalculate size averages after append", async () => {
      const obs1: WorkTimeObservation = {
        issue_number: 1,
        size: "M",
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 40,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T09:00:00Z",
      };

      const obs2: WorkTimeObservation = {
        issue_number: 2,
        size: "M",
        priority: "medium",
        task_type: "feature",
        actual_work_minutes: 50,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      };

      await appendObservationToYAML(obs1, yamlPath);
      await appendObservationToYAML(obs2, yamlPath);

      const feedback = await readWorkTimeFeedback(yamlPath);

      expect(feedback?.size_averages["M"]).toEqual({
        estimated: 30,
        actual_average: 45, // (40 + 50) / 2
        observation_count: 2,
      });
    });

    it("should prune to 50 observations", async () => {
      // Append 52 observations
      for (let i = 1; i <= 52; i++) {
        const obs: WorkTimeObservation = {
          issue_number: i,
          size: "M",
          priority: "medium",
          task_type: "feature",
          actual_work_minutes: 30,
          estimated_minutes: 30,
          routing: "standard",
          stages_completed: ["issue-pickup"] as PipelineStage[],
          timestamp: `2026-02-06T10:${String(i).padStart(2, "0")}:00Z`,
        };

        await appendObservationToYAML(obs, yamlPath);
      }

      const feedback = await readWorkTimeFeedback(yamlPath);

      expect(feedback?.observations).toHaveLength(50);
      expect(feedback?.observations[0].issue_number).toBe(3); // First 2 pruned
      expect(feedback?.observations[49].issue_number).toBe(52); // Last kept
    });
  });

  describe("readWorkTimeFeedback", () => {
    it("should return null if file does not exist", async () => {
      const feedback = await readWorkTimeFeedback(path.join(tmpDir, "nonexistent.yaml"));

      expect(feedback).toBeNull();
    });

    it("should return null if work_time_feedback section missing", async () => {
      await fs.writeFile(yamlPath, "some_other_key: value\n", "utf-8");

      const feedback = await readWorkTimeFeedback(yamlPath);

      expect(feedback).toBeNull();
    });

    it("should read valid feedback data", async () => {
      const obs: WorkTimeObservation = {
        issue_number: 310,
        size: "M",
        priority: "high",
        task_type: "feature",
        actual_work_minutes: 45,
        estimated_minutes: 30,
        routing: "standard",
        stages_completed: ["issue-pickup", "feature-dev"] as PipelineStage[],
        timestamp: "2026-02-06T10:00:00Z",
      };

      await appendObservationToYAML(obs, yamlPath);

      const feedback = await readWorkTimeFeedback(yamlPath);

      expect(feedback?.enabled).toBe(true);
      expect(feedback?.observations).toHaveLength(1);
      expect(feedback?.observations[0].issue_number).toBe(310);
    });
  });
});
