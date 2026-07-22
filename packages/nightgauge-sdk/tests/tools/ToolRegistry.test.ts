import { describe, it, expect } from "vitest";
import { ZodError } from "zod";
import { ToolRegistry } from "../../src/tools/ToolRegistry.js";
import type { CustomToolDefinition } from "../../src/tools/ToolDefinition.js";

const sampleDefinition: CustomToolDefinition = {
  name: "query_database",
  description: "Run a SQL query against the database",
  input_schema: {
    type: "object",
    properties: { sql: { type: "string" } },
    required: ["sql"],
  },
  allowed_callers: ["code_execution_20250825"],
};

const anotherDefinition: CustomToolDefinition = {
  name: "analyze_data",
  description: "Analyze a dataset",
  input_schema: {
    type: "object",
    properties: { dataset: { type: "string" } },
  },
};

describe("ToolRegistry", () => {
  describe("registerBuiltinTool / get / has", () => {
    it("registers and retrieves a built-in tool", () => {
      const registry = new ToolRegistry();
      registry.registerBuiltinTool("Bash");

      expect(registry.has("Bash")).toBe(true);
      const entry = registry.get("Bash");
      expect(entry?.type).toBe("builtin");
      expect(entry?.name).toBe("Bash");
      expect(entry?.definition).toBeUndefined();
    });

    it("returns undefined for unregistered tool", () => {
      const registry = new ToolRegistry();
      expect(registry.get("NonExistent")).toBeUndefined();
      expect(registry.has("NonExistent")).toBe(false);
    });
  });

  describe("registerCustomTool", () => {
    it("registers and retrieves a custom tool", () => {
      const registry = new ToolRegistry();
      registry.registerCustomTool(sampleDefinition);

      expect(registry.has("query_database")).toBe(true);
      const entry = registry.get("query_database");
      expect(entry?.type).toBe("custom");
      expect(entry?.definition?.description).toBe("Run a SQL query against the database");
      expect(entry?.definition?.allowed_callers).toEqual(["code_execution_20250825"]);
    });

    it("validates definition via Zod and rejects invalid", () => {
      const registry = new ToolRegistry();
      const invalid = { name: "", description: "", input_schema: {} };
      expect(() => registry.registerCustomTool(invalid as CustomToolDefinition)).toThrow(ZodError);
    });

    it("overwrites existing entry on duplicate registration", () => {
      const registry = new ToolRegistry();
      registry.registerCustomTool(sampleDefinition);

      const updated: CustomToolDefinition = {
        ...sampleDefinition,
        description: "Updated description",
      };
      registry.registerCustomTool(updated);

      expect(registry.get("query_database")?.definition?.description).toBe("Updated description");
      expect(registry.size).toBe(1);
    });
  });

  describe("getBuiltinToolNames", () => {
    it("returns only built-in tool names", () => {
      const registry = new ToolRegistry();
      registry.registerBuiltinTool("Read");
      registry.registerBuiltinTool("Write");
      registry.registerBuiltinTool("Bash");
      registry.registerCustomTool(sampleDefinition);

      const names = registry.getBuiltinToolNames();
      expect(names).toEqual(["Read", "Write", "Bash"]);
      expect(names).not.toContain("query_database");
    });

    it("returns empty array for empty registry", () => {
      const registry = new ToolRegistry();
      expect(registry.getBuiltinToolNames()).toEqual([]);
    });
  });

  describe("getCustomToolDefinitions", () => {
    it("returns only custom tool definitions", () => {
      const registry = new ToolRegistry();
      registry.registerBuiltinTool("Bash");
      registry.registerCustomTool(sampleDefinition);
      registry.registerCustomTool(anotherDefinition);

      const defs = registry.getCustomToolDefinitions();
      expect(defs).toHaveLength(2);
      expect(defs.map((d) => d.name)).toEqual(["query_database", "analyze_data"]);
    });

    it("returns empty array for registry with only built-in tools", () => {
      const registry = new ToolRegistry();
      registry.registerBuiltinTool("Bash");
      registry.registerBuiltinTool("Read");
      expect(registry.getCustomToolDefinitions()).toEqual([]);
    });

    it("excludes custom entries without definitions (placeholder entries)", () => {
      const registry = ToolRegistry.fromSkillFrontmatter(["Bash"], ["unresolved_tool"]);
      // 'unresolved_tool' is registered as custom but has no definition
      expect(registry.has("unresolved_tool")).toBe(true);
      expect(registry.getCustomToolDefinitions()).toEqual([]);
    });
  });

  describe("getAll", () => {
    it("returns all entries", () => {
      const registry = new ToolRegistry();
      registry.registerBuiltinTool("Bash");
      registry.registerCustomTool(sampleDefinition);

      const all = registry.getAll();
      expect(all).toHaveLength(2);
      expect(all.map((e) => e.name).sort()).toEqual(["Bash", "query_database"]);
    });
  });

  describe("size", () => {
    it("reflects registered count", () => {
      const registry = new ToolRegistry();
      expect(registry.size).toBe(0);

      registry.registerBuiltinTool("Bash");
      expect(registry.size).toBe(1);

      registry.registerCustomTool(sampleDefinition);
      expect(registry.size).toBe(2);
    });
  });

  describe("fromSkillFrontmatter", () => {
    it("creates registry with only built-in tools", () => {
      const registry = ToolRegistry.fromSkillFrontmatter(["Read", "Write", "Bash"]);

      expect(registry.size).toBe(3);
      expect(registry.getBuiltinToolNames()).toEqual(["Read", "Write", "Bash"]);
      expect(registry.getCustomToolDefinitions()).toEqual([]);
    });

    it("creates registry with built-in and programmatic tool names", () => {
      const registry = ToolRegistry.fromSkillFrontmatter(
        ["Read", "Bash"],
        ["query_database", "analyze_data"]
      );

      expect(registry.size).toBe(4);
      expect(registry.getBuiltinToolNames()).toEqual(["Read", "Bash"]);
      expect(registry.has("query_database")).toBe(true);
      expect(registry.has("analyze_data")).toBe(true);
    });

    it("creates registry with built-in and custom definitions", () => {
      const registry = ToolRegistry.fromSkillFrontmatter(
        ["Read"],
        ["query_database"],
        [sampleDefinition]
      );

      expect(registry.size).toBe(2);
      expect(registry.getBuiltinToolNames()).toEqual(["Read"]);
      const defs = registry.getCustomToolDefinitions();
      expect(defs).toHaveLength(1);
      expect(defs[0].name).toBe("query_database");
    });

    it("does not duplicate entries when name appears in both programmatic and definitions", () => {
      const registry = ToolRegistry.fromSkillFrontmatter(
        ["Bash"],
        ["query_database"],
        [sampleDefinition]
      );

      // sampleDefinition is registered first via customDefinitions,
      // then programmaticTools sees it already exists and skips
      expect(registry.size).toBe(2);
      expect(registry.get("query_database")?.definition).toBeDefined();
    });

    it("creates empty registry from empty inputs", () => {
      const registry = ToolRegistry.fromSkillFrontmatter([]);
      expect(registry.size).toBe(0);
      expect(registry.getBuiltinToolNames()).toEqual([]);
      expect(registry.getCustomToolDefinitions()).toEqual([]);
    });
  });

  describe("backward compatibility", () => {
    it("getBuiltinToolNames returns equivalent of string[] allowedTools", () => {
      const originalAllowedTools = ["Read", "Write", "Edit", "Glob", "Grep", "Bash", "Task"];
      const registry = ToolRegistry.fromSkillFrontmatter(originalAllowedTools);
      expect(registry.getBuiltinToolNames()).toEqual(originalAllowedTools);
    });
  });
});
