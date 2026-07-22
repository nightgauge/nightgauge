import { describe, it, expect } from "vitest";
import { CustomToolDefinitionSchema } from "../../src/tools/ToolDefinition.js";
import { ToolRegistry } from "../../src/tools/ToolRegistry.js";
import {
  // Individual tool definitions
  RUN_BUILD_TOOL,
  RUN_LINT_TOOL,
  RUN_TESTS_TOOL,
  RUN_TYPECHECK_TOOL,
  READ_CONTEXT_FILE_TOOL,
  WRITE_CONTEXT_FILE_TOOL,
  LIST_CONTEXT_FILES_TOOL,
  GIT_DIFF_SUMMARY_TOOL,
  GIT_LOG_STRUCTURED_TOOL,
  GIT_STATUS_STRUCTURED_TOOL,
  // Category arrays
  VALIDATION_TOOLS,
  CONTEXT_TOOLS,
  GIT_TOOLS,
  // Factory functions
  getAllPipelineToolDefinitions,
  registerPipelineTools,
} from "../../src/tools/definitions/index.js";

const ALL_TOOLS = [
  RUN_BUILD_TOOL,
  RUN_LINT_TOOL,
  RUN_TESTS_TOOL,
  RUN_TYPECHECK_TOOL,
  READ_CONTEXT_FILE_TOOL,
  WRITE_CONTEXT_FILE_TOOL,
  LIST_CONTEXT_FILES_TOOL,
  GIT_DIFF_SUMMARY_TOOL,
  GIT_LOG_STRUCTURED_TOOL,
  GIT_STATUS_STRUCTURED_TOOL,
];

describe("Pipeline Tool Definitions", () => {
  describe("schema validation", () => {
    ALL_TOOLS.forEach((tool) => {
      it(`${tool.name} passes CustomToolDefinitionSchema validation`, () => {
        const result = CustomToolDefinitionSchema.safeParse(tool);
        expect(result.success).toBe(true);
      });
    });
  });

  describe("naming conventions", () => {
    ALL_TOOLS.forEach((tool) => {
      it(`${tool.name} is a valid snake_case identifier`, () => {
        expect(tool.name).toMatch(/^[a-z][a-z0-9_]*$/);
      });
    });

    it("all tool names are unique", () => {
      const names = ALL_TOOLS.map((t) => t.name);
      expect(new Set(names).size).toBe(names.length);
    });
  });

  describe("allowed_callers", () => {
    ALL_TOOLS.forEach((tool) => {
      it(`${tool.name} uses code_execution_20250825 caller`, () => {
        expect(tool.allowed_callers).toEqual(["code_execution_20250825"]);
      });
    });
  });

  describe("description quality", () => {
    ALL_TOOLS.forEach((tool) => {
      it(`${tool.name} has a non-empty description`, () => {
        expect(tool.description.length).toBeGreaterThan(0);
      });

      it(`${tool.name} documents the JSON output format`, () => {
        expect(tool.description).toContain("Returns JSON:");
      });
    });
  });

  describe("input schemas", () => {
    ALL_TOOLS.forEach((tool) => {
      it(`${tool.name} has type: "object" input schema`, () => {
        expect(tool.input_schema).toHaveProperty("type", "object");
      });

      it(`${tool.name} has a properties object`, () => {
        expect(tool.input_schema).toHaveProperty("properties");
        expect(typeof tool.input_schema.properties).toBe("object");
      });
    });
  });

  describe("category arrays", () => {
    it("VALIDATION_TOOLS contains 4 tools", () => {
      expect(VALIDATION_TOOLS).toHaveLength(4);
    });

    it("VALIDATION_TOOLS contains the correct tools", () => {
      const names = VALIDATION_TOOLS.map((t) => t.name);
      expect(names).toEqual(["run_build", "run_lint", "run_tests", "run_typecheck"]);
    });

    it("CONTEXT_TOOLS contains 3 tools", () => {
      expect(CONTEXT_TOOLS).toHaveLength(3);
    });

    it("CONTEXT_TOOLS contains the correct tools", () => {
      const names = CONTEXT_TOOLS.map((t) => t.name);
      expect(names).toEqual(["read_context_file", "write_context_file", "list_context_files"]);
    });

    it("GIT_TOOLS contains 3 tools", () => {
      expect(GIT_TOOLS).toHaveLength(3);
    });

    it("GIT_TOOLS contains the correct tools", () => {
      const names = GIT_TOOLS.map((t) => t.name);
      expect(names).toEqual(["git_diff_summary", "git_log_structured", "git_status_structured"]);
    });
  });

  describe("getAllPipelineToolDefinitions()", () => {
    it("returns exactly 10 tools", () => {
      expect(getAllPipelineToolDefinitions()).toHaveLength(10);
    });

    it("returns all unique names", () => {
      const tools = getAllPipelineToolDefinitions();
      const names = tools.map((t) => t.name);
      expect(new Set(names).size).toBe(10);
    });

    it("returns a new array on each call", () => {
      const a = getAllPipelineToolDefinitions();
      const b = getAllPipelineToolDefinitions();
      expect(a).not.toBe(b);
      expect(a).toEqual(b);
    });
  });

  describe("registerPipelineTools()", () => {
    it("registers all 10 tools in a ToolRegistry", () => {
      const registry = new ToolRegistry();
      registerPipelineTools(registry);

      expect(registry.size).toBe(10);
    });

    it("all registered tools appear in getCustomToolDefinitions()", () => {
      const registry = new ToolRegistry();
      registerPipelineTools(registry);

      const defs = registry.getCustomToolDefinitions();
      expect(defs).toHaveLength(10);
    });

    it("each tool is retrievable by name", () => {
      const registry = new ToolRegistry();
      registerPipelineTools(registry);

      for (const tool of ALL_TOOLS) {
        expect(registry.has(tool.name)).toBe(true);
        const entry = registry.get(tool.name);
        expect(entry?.type).toBe("custom");
        expect(entry?.definition?.name).toBe(tool.name);
      }
    });
  });

  describe("backward compatibility", () => {
    it("built-in tools are unaffected by pipeline tool registration", () => {
      const builtinTools = ["Read", "Write", "Edit", "Bash", "Glob", "Grep"];
      const registry = ToolRegistry.fromSkillFrontmatter(builtinTools);

      registerPipelineTools(registry);

      // Built-in tools still present
      const builtinNames = registry.getBuiltinToolNames();
      expect(builtinNames).toEqual(builtinTools);

      // Custom tools also present
      const customDefs = registry.getCustomToolDefinitions();
      expect(customDefs).toHaveLength(10);

      // Total = 6 built-in + 10 custom
      expect(registry.size).toBe(16);
    });
  });
});
