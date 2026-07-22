import { afterEach, beforeEach, describe, expect, it } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import { ContextManager } from "../../context/ContextManager.js";
import { EpicContextSchema } from "../../context/schemas/epic-context.js";

describe("ContextManager — epic context methods", () => {
  let tmpDir: string;
  let ctx: ContextManager;

  beforeEach(async () => {
    tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "epic-ctx-"));
    ctx = new ContextManager(tmpDir);
  });

  afterEach(async () => {
    await fs.rm(tmpDir, { recursive: true, force: true });
  });

  describe("readEpicContext", () => {
    it("returns undefined when no file exists", async () => {
      const result = await ctx.readEpicContext(999);
      expect(result).toBeUndefined();
    });

    it("parses a valid epic context file", async () => {
      const data = {
        schema_version: "1.0",
        epic_number: 100,
        last_updated: "2026-03-24T00:00:00Z",
        sub_issue_findings: {},
        shared_research: {
          codebase_notes: [],
          architecture_notes: [],
          relevant_files: [],
        },
      };
      await fs.writeFile(path.join(tmpDir, "epic-context-100.json"), JSON.stringify(data), "utf-8");

      const result = await ctx.readEpicContext(100);
      expect(result).toBeDefined();
      expect(result!.epic_number).toBe(100);
      expect(result!.schema_version).toBe("1.0");
    });

    it("returns undefined for invalid JSON", async () => {
      await fs.writeFile(path.join(tmpDir, "epic-context-100.json"), "not-json", "utf-8");

      const result = await ctx.readEpicContext(100);
      expect(result).toBeUndefined();
    });

    it("returns undefined for invalid schema", async () => {
      await fs.writeFile(
        path.join(tmpDir, "epic-context-100.json"),
        JSON.stringify({ schema_version: "9.9" }),
        "utf-8"
      );

      const result = await ctx.readEpicContext(100);
      expect(result).toBeUndefined();
    });
  });

  describe("appendEpicContext", () => {
    it("creates a new file for the first sub-issue", async () => {
      await ctx.appendEpicContext(100, 42, {
        files_touched: ["src/foo.ts"],
        decisions: ["Use Zod"],
        discoveries: ["Atomic writes"],
        patterns: ["Temp + rename"],
        recorded_at: "2026-03-24T01:00:00Z",
      });

      const filePath = path.join(tmpDir, "epic-context-100.json");
      const content = await fs.readFile(filePath, "utf-8");
      const parsed = EpicContextSchema.parse(JSON.parse(content));

      expect(parsed.epic_number).toBe(100);
      expect(parsed.schema_version).toBe("1.0");
      expect(parsed.sub_issue_findings["42"]).toBeDefined();
      expect(parsed.sub_issue_findings["42"].files_touched).toEqual(["src/foo.ts"]);
      expect(parsed.shared_research.relevant_files).toEqual(["src/foo.ts"]);
    });

    it("merges with existing findings without overwriting", async () => {
      // First sub-issue
      await ctx.appendEpicContext(100, 42, {
        files_touched: ["src/foo.ts"],
        decisions: ["Decision A"],
        discoveries: [],
        patterns: [],
        recorded_at: "2026-03-24T01:00:00Z",
      });

      // Second sub-issue
      await ctx.appendEpicContext(100, 43, {
        files_touched: ["src/bar.ts"],
        decisions: ["Decision B"],
        discoveries: [],
        patterns: [],
        recorded_at: "2026-03-24T02:00:00Z",
      });

      const result = await ctx.readEpicContext(100);
      expect(result).toBeDefined();
      expect(Object.keys(result!.sub_issue_findings)).toHaveLength(2);
      expect(result!.sub_issue_findings["42"].decisions).toEqual(["Decision A"]);
      expect(result!.sub_issue_findings["43"].decisions).toEqual(["Decision B"]);
    });

    it("deduplicates relevant_files across sub-issues", async () => {
      await ctx.appendEpicContext(100, 42, {
        files_touched: ["src/shared.ts", "src/foo.ts"],
        decisions: [],
        discoveries: [],
        patterns: [],
        recorded_at: "2026-03-24T01:00:00Z",
      });

      await ctx.appendEpicContext(100, 43, {
        files_touched: ["src/shared.ts", "src/bar.ts"],
        decisions: [],
        discoveries: [],
        patterns: [],
        recorded_at: "2026-03-24T02:00:00Z",
      });

      const result = await ctx.readEpicContext(100);
      expect(result).toBeDefined();
      // src/shared.ts should appear only once
      expect(result!.shared_research.relevant_files).toEqual([
        "src/shared.ts",
        "src/foo.ts",
        "src/bar.ts",
      ]);
    });

    it("does not add to relevant_files when files_touched is empty", async () => {
      await ctx.appendEpicContext(100, 42, {
        files_touched: ["src/foo.ts"],
        decisions: [],
        discoveries: [],
        patterns: [],
        recorded_at: "2026-03-24T01:00:00Z",
      });

      await ctx.appendEpicContext(100, 43, {
        files_touched: [],
        decisions: ["No files changed"],
        discoveries: [],
        patterns: [],
        recorded_at: "2026-03-24T02:00:00Z",
      });

      const result = await ctx.readEpicContext(100);
      expect(result).toBeDefined();
      // Only src/foo.ts from issue 42 — empty touch from 43 should not affect
      expect(result!.shared_research.relevant_files).toEqual(["src/foo.ts"]);
    });

    it("updates last_updated timestamp", async () => {
      await ctx.appendEpicContext(100, 42, {
        files_touched: [],
        decisions: [],
        discoveries: [],
        patterns: [],
        recorded_at: "2026-03-24T01:00:00Z",
      });

      const result1 = await ctx.readEpicContext(100);
      const firstUpdated = result1!.last_updated;

      // Small delay to ensure different timestamp
      await new Promise((r) => setTimeout(r, 10));

      await ctx.appendEpicContext(100, 43, {
        files_touched: [],
        decisions: [],
        discoveries: [],
        patterns: [],
        recorded_at: "2026-03-24T02:00:00Z",
      });

      const result2 = await ctx.readEpicContext(100);
      expect(result2!.last_updated).not.toBe(firstUpdated);
    });

    it("validates findings before writing", async () => {
      await expect(
        ctx.appendEpicContext(100, 42, {
          files_touched: "not-an-array" as unknown as string[],
          decisions: [],
          discoveries: [],
          patterns: [],
          recorded_at: "2026-03-24T01:00:00Z",
        })
      ).rejects.toThrow();
    });
  });
});
