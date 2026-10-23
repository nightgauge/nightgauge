import { describe, it, expect } from "vitest";
import {
  parseToolType,
  extractTarget,
  getToolConfig,
  createToolCallData,
  formatToolSummary,
  formatToolIndicator,
  generateToolCallId,
  type ToolType,
  type ToolCallSummary,
} from "../../../src/views/outputWindow/ToolCallIndicator";

describe("ToolCallIndicator", () => {
  describe("parseToolType", () => {
    it("should parse exact tool names", () => {
      expect(parseToolType("Edit")).toBe("Edit");
      expect(parseToolType("Read")).toBe("Read");
      expect(parseToolType("Write")).toBe("Write");
      expect(parseToolType("Bash")).toBe("Bash");
      expect(parseToolType("Glob")).toBe("Glob");
      expect(parseToolType("Grep")).toBe("Grep");
      expect(parseToolType("Task")).toBe("Task");
    });

    it("should handle case-insensitive matching", () => {
      expect(parseToolType("edit")).toBe("Edit");
      expect(parseToolType("READ")).toBe("Read");
      expect(parseToolType("bAsH")).toBe("Bash");
    });

    it("should handle common variations", () => {
      expect(parseToolType("command")).toBe("Bash");
      expect(parseToolType("shell")).toBe("Bash");
      expect(parseToolType("exec")).toBe("Bash");
      expect(parseToolType("find")).toBe("Glob");
      expect(parseToolType("search")).toBe("Grep");
      expect(parseToolType("rg")).toBe("Grep");
      expect(parseToolType("ripgrep")).toBe("Grep");
      expect(parseToolType("subagent")).toBe("Task");
      expect(parseToolType("agent")).toBe("Task");
    });

    it("should return Unknown for unrecognized tools", () => {
      expect(parseToolType("SomeRandomTool")).toBe("Unknown");
      expect(parseToolType("FooBar")).toBe("Unknown");
    });
  });

  describe("extractTarget", () => {
    it("should extract file paths from common argument names", () => {
      expect(extractTarget("Edit", { file_path: "/src/index.ts" })).toBe("index.ts");
      expect(extractTarget("Read", { path: "/src/utils/helper.ts" })).toBe("helper.ts");
      expect(extractTarget("Write", { filename: "output.json" })).toBe("output.json");
    });

    it("should truncate commands for Bash tool", () => {
      expect(extractTarget("Bash", { command: "npm test" })).toBe("npm test");
      expect(
        extractTarget("Bash", {
          command: "npm run build && npm run test && npm run lint && npm run deploy",
        })
      ).toBe("npm ...");
    });

    it("should extract patterns for Glob/Grep", () => {
      expect(extractTarget("Glob", { pattern: "**/*.ts" })).toBe("**/*.ts");
      expect(extractTarget("Grep", { pattern: "function\\s+\\w+" })).toBe("function\\s+\\w+");
    });

    it("should extract domain for WebFetch", () => {
      expect(extractTarget("WebFetch", { url: "https://api.example.com/data" })).toBe(
        "api.example.com"
      );
    });

    it("should truncate long descriptions", () => {
      const longDescription = "This is a very long task description that should be truncated";
      const result = extractTarget("Task", { description: longDescription });
      // Should truncate to maxLength (30) - 3 (for "...") = 27 chars + "..."
      expect(result).toBe("This is a very long task de...");
      expect(result.length).toBe(30);
    });

    it("should handle missing arguments", () => {
      expect(extractTarget("Edit", undefined)).toBe("");
      expect(extractTarget("Read", {})).toBe("");
    });

    it("should format TodoWrite with item count", () => {
      expect(
        extractTarget("TodoWrite", {
          todos: [{ content: "item1" }, { content: "item2" }],
        })
      ).toBe("2 items");
    });

    it("should extract header from AskUserQuestion questions array", () => {
      expect(
        extractTarget("AskUserQuestion", {
          questions: [
            {
              question: "Which option do you prefer?",
              header: "Preference",
              options: [{ label: "A" }, { label: "B" }],
              multiSelect: false,
            },
          ],
        })
      ).toBe("Preference");
    });

    it("should extract question text if no header in AskUserQuestion", () => {
      expect(
        extractTarget("AskUserQuestion", {
          questions: [
            {
              question: "A very long question that should be truncated",
              options: [{ label: "A" }, { label: "B" }],
              multiSelect: false,
            },
          ],
        })
      ).toBe("A very long question that s...");
    });

    it("should handle legacy single question format for AskUserQuestion", () => {
      expect(
        extractTarget("AskUserQuestion", {
          question: "What is your choice?",
        })
      ).toBe("What is your choice?");
    });
  });

  describe("getToolConfig", () => {
    it("should return config for known tools", () => {
      const editConfig = getToolConfig("Edit");
      expect(editConfig.label).toBe("Edit");
      expect(editConfig.colorClass).toBe("tool-edit");
      expect(editConfig.animation).toBe("pulse");

      const bashConfig = getToolConfig("Bash");
      expect(bashConfig.label).toBe("Bash");
      expect(bashConfig.colorClass).toBe("tool-bash");
      expect(bashConfig.animation).toBe("spin");
    });

    it("should return Unknown config for unrecognized tools", () => {
      const config = getToolConfig("Unknown");
      expect(config.label).toBe("Tool");
      expect(config.colorClass).toBe("tool-unknown");
    });
  });

  describe("createToolCallData", () => {
    it("should create tool call data with all fields", () => {
      const data = createToolCallData("Edit", {
        file_path: "/src/index.ts",
      });

      expect(data.tool).toBe("Edit");
      expect(data.target).toBe("index.ts");
      expect(data.isActive).toBe(true);
      expect(data.id).toBeTruthy();
      expect(data.startedAt).toBeInstanceOf(Date);
      expect(data.args).toEqual({ file_path: "/src/index.ts" });
    });

    it("should handle tools without arguments", () => {
      const data = createToolCallData("Bash");

      expect(data.tool).toBe("Bash");
      expect(data.target).toBe("");
      expect(data.isActive).toBe(true);
    });

    it("should allow setting isActive to false", () => {
      const data = createToolCallData("Read", { path: "/test.ts" }, false);

      expect(data.isActive).toBe(false);
    });
  });

  describe("generateToolCallId", () => {
    it("should generate unique IDs", () => {
      const id1 = generateToolCallId();
      const id2 = generateToolCallId();

      expect(id1).not.toBe(id2);
      expect(id1).toMatch(/^tool-\d+-[a-z0-9]+$/);
      expect(id2).toMatch(/^tool-\d+-[a-z0-9]+$/);
    });
  });

  describe("formatToolSummary", () => {
    it("should format empty summary", () => {
      const summary: ToolCallSummary = {
        total: 0,
        byTool: new Map(),
        startedAt: new Date(),
        endedAt: new Date(),
      };

      expect(formatToolSummary(summary)).toBe("No tools used");
    });

    it("should format single tool type", () => {
      const summary: ToolCallSummary = {
        total: 5,
        byTool: new Map([["Edit" as ToolType, 5]]),
        startedAt: new Date(),
        endedAt: new Date(),
      };

      expect(formatToolSummary(summary)).toBe("Used 5 tools: 5 Edit");
    });

    it("should format multiple tool types sorted by count", () => {
      const summary: ToolCallSummary = {
        total: 12,
        byTool: new Map([
          ["Edit" as ToolType, 5],
          ["Read" as ToolType, 4],
          ["Bash" as ToolType, 3],
        ]),
        startedAt: new Date(),
        endedAt: new Date(),
      };

      expect(formatToolSummary(summary)).toBe("Used 12 tools: 5 Edit, 4 Read, 3 Bash");
    });

    it("should exclude zero-count tools", () => {
      const summary: ToolCallSummary = {
        total: 5,
        byTool: new Map([
          ["Edit" as ToolType, 5],
          ["Read" as ToolType, 0],
        ]),
        startedAt: new Date(),
        endedAt: new Date(),
      };

      expect(formatToolSummary(summary)).toBe("Used 5 tools: 5 Edit");
    });
  });

  describe("formatToolIndicator", () => {
    it("should format indicator with target", () => {
      const data = createToolCallData("Edit", { file_path: "/src/index.ts" });
      expect(formatToolIndicator(data)).toBe("Edit index.ts");
    });

    it("should format indicator without target", () => {
      const data = createToolCallData("Bash");
      expect(formatToolIndicator(data)).toBe("Bash");
    });
  });
});
