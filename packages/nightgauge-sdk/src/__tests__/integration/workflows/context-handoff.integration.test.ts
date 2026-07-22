/**
 * Integration tests — Stage Execution & Context Handoff
 *
 * Workflow 2: ContextManager.write() → read() with real fs/promises I/O
 *
 * Tests atomic writes, Zod schema validation, error classes, and
 * sequential stage handoff (issue → planning). Uses temp workspace
 * to isolate file I/O from the project directory.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  ContextManager,
  ContextNotFoundError,
  ContextValidationError,
} from "../../../context/ContextManager.js";
import { IssueContextSchema, PlanningContextSchema } from "../../../context/schemas/index.js";
import { createTestWorkspace, type TestWorkspace } from "../helpers/workspace.js";
import * as path from "node:path";
import * as fs from "node:fs/promises";

describe("Context Handoff Workflow", () => {
  let workspace: TestWorkspace;
  let ctx: ContextManager;

  beforeEach(async () => {
    workspace = await createTestWorkspace();
    ctx = new ContextManager(workspace.pipelineDir);
  });

  afterEach(async () => {
    await workspace.cleanup();
  });

  describe("write and read round-trip", () => {
    it("writes and reads back a valid issue context", async () => {
      const issueData = {
        schema_version: "1.3",
        issue_number: 42,
        title: "Add token tracking",
        type: "feature" as const,
        branch: "feat/42-add-token-tracking",
        base_branch: "main",
        requirements: {
          summary: "Track token usage across pipeline stages",
          acceptance_criteria: ["Token usage per stage", "Cost calculated"],
        },
        labels: [],
        routing: null,
        created_at: new Date().toISOString(),
      };

      await ctx.write(IssueContextSchema, "issue-42.json", issueData);
      const loaded = await ctx.read(IssueContextSchema, "issue-42.json");

      expect(loaded.issue_number).toBe(42);
      expect(loaded.title).toBe("Add token tracking");
      expect(loaded.branch).toBe("feat/42-add-token-tracking");
    });

    it("writes and reads back a valid planning context", async () => {
      const planningData = {
        schema_version: "1.5",
        issue_number: 42,
        plan_file: ".nightgauge/plans/42-plan.md",
        approach: "Add TokenTracker to orchestrator",
        files_to_create: ["src/tracking/TokenTracker.ts"],
        files_to_modify: ["src/orchestrator/PipelineOrchestrator.ts"],
        decisions: ["Use immutable records for thread safety"],
        revision_count: 0,
        revision_reasons: [],
        created_at: new Date().toISOString(),
      };

      await ctx.write(PlanningContextSchema, "planning-42.json", planningData);
      const loaded = await ctx.read(PlanningContextSchema, "planning-42.json");

      expect(loaded.issue_number).toBe(42);
      expect(loaded.plan_file).toBe(".nightgauge/plans/42-plan.md");
      expect(loaded.files_to_create).toContain("src/tracking/TokenTracker.ts");
    });
  });

  describe("error handling", () => {
    it("throws ContextNotFoundError when file does not exist", async () => {
      await expect(ctx.read(IssueContextSchema, "issue-999.json")).rejects.toThrow(
        ContextNotFoundError
      );
    });

    it("ContextNotFoundError includes the filename", async () => {
      try {
        await ctx.read(IssueContextSchema, "issue-999.json");
        expect.fail("Should have thrown");
      } catch (err) {
        expect(err).toBeInstanceOf(ContextNotFoundError);
        expect((err as ContextNotFoundError).filename).toContain("issue-999.json");
      }
    });

    it("throws ContextValidationError when JSON is invalid for the schema", async () => {
      // Write a JSON file that violates PlanningContextSchema (empty plan_file, negative issue)
      const badData = {
        schema_version: "1.5",
        issue_number: -1,
        plan_file: "",
        approach: "",
      };
      const filePath = path.join(workspace.pipelineDir, "planning-bad.json");
      await fs.writeFile(filePath, JSON.stringify(badData), "utf-8");

      await expect(ctx.read(PlanningContextSchema, "planning-bad.json")).rejects.toThrow(
        ContextValidationError
      );
    });

    it("throws SyntaxError-like error when file contains malformed JSON", async () => {
      const filePath = path.join(workspace.pipelineDir, "issue-corrupt.json");
      await fs.writeFile(filePath, "{ not valid json }", "utf-8");

      await expect(ctx.read(IssueContextSchema, "issue-corrupt.json")).rejects.toThrow();
    });
  });

  describe("sequential stage handoff", () => {
    it("issue context written by issue-pickup is readable by feature-planning", async () => {
      // Simulate issue-pickup writing context
      const issuePickupOutput = {
        schema_version: "1.3",
        issue_number: 100,
        title: "Integration test issue",
        type: "feature" as const,
        branch: "feat/100-integration-test",
        base_branch: "main",
        requirements: {
          summary: "Test that context handoff works end-to-end",
        },
        labels: [],
        routing: null,
      };
      await ctx.write(IssueContextSchema, "issue-100.json", issuePickupOutput);

      // Simulate feature-planning reading it
      const loaded = await ctx.read(IssueContextSchema, "issue-100.json");
      expect(loaded.issue_number).toBe(100);
      expect(loaded.title).toBe("Integration test issue");

      // Then feature-planning writes its own context
      const planningOutput = {
        schema_version: "1.5",
        issue_number: 100,
        plan_file: ".nightgauge/plans/100-plan.md",
        approach: "Implement context handoff tests",
        files_to_create: ["src/__tests__/integration/workflows/context-handoff.test.ts"],
        files_to_modify: [],
        decisions: ["Use real filesystem for I/O accuracy"],
        revision_count: 0,
        revision_reasons: [],
        created_at: new Date().toISOString(),
      };
      await ctx.write(PlanningContextSchema, "planning-100.json", planningOutput);

      // Simulate feature-dev reading planning context
      const planningLoaded = await ctx.read(PlanningContextSchema, "planning-100.json");
      expect(planningLoaded.issue_number).toBe(100);
      expect(planningLoaded.files_to_create).toHaveLength(1);
    });
  });

  describe("atomic write behavior", () => {
    it("concurrent writes both complete without data corruption", async () => {
      const data1 = {
        schema_version: "1.5",
        issue_number: 1,
        plan_file: ".nightgauge/plans/1.md",
        approach: "First write",
        decisions: ["decision A"],
        revision_count: 0,
        revision_reasons: [],
        created_at: new Date().toISOString(),
      };
      const data2 = {
        schema_version: "1.5",
        issue_number: 2,
        plan_file: ".nightgauge/plans/2.md",
        approach: "Second write",
        decisions: ["decision B"],
        revision_count: 0,
        revision_reasons: [],
        created_at: new Date().toISOString(),
      };

      // Write to different files concurrently
      await Promise.all([
        ctx.write(PlanningContextSchema, "planning-1.json", data1),
        ctx.write(PlanningContextSchema, "planning-2.json", data2),
      ]);

      const loaded1 = await ctx.read(PlanningContextSchema, "planning-1.json");
      const loaded2 = await ctx.read(PlanningContextSchema, "planning-2.json");

      expect(loaded1.approach).toBe("First write");
      expect(loaded2.approach).toBe("Second write");
    });
  });

  describe("exists() and delete()", () => {
    it("exists() returns false before write and true after", async () => {
      expect(await ctx.exists("issue-77.json")).toBe(false);

      const data = {
        schema_version: "1.3",
        issue_number: 77,
        title: "Test",
        type: "feature" as const,
        branch: "feat/77",
        base_branch: "main",
        requirements: {},
        labels: [],
        routing: null,
      };
      await ctx.write(IssueContextSchema, "issue-77.json", data);

      expect(await ctx.exists("issue-77.json")).toBe(true);
    });

    it("delete() removes the file", async () => {
      const data = {
        schema_version: "1.3",
        issue_number: 88,
        title: "Delete me",
        type: "feature" as const,
        branch: "feat/88",
        base_branch: "main",
        requirements: {},
        labels: [],
        routing: null,
      };
      await ctx.write(IssueContextSchema, "issue-88.json", data);
      expect(await ctx.exists("issue-88.json")).toBe(true);

      await ctx.delete("issue-88.json");
      expect(await ctx.exists("issue-88.json")).toBe(false);
    });
  });
});
