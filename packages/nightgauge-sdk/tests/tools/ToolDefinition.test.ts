import { describe, it, expect } from "vitest";
import { ZodError } from "zod";
import {
  AllowedCallerSchema,
  ToolTypeSchema,
  CustomToolDefinitionSchema,
  ToolEntrySchema,
} from "../../src/tools/ToolDefinition.js";

describe("AllowedCallerSchema", () => {
  it("accepts valid caller types", () => {
    expect(AllowedCallerSchema.parse("direct")).toBe("direct");
    expect(AllowedCallerSchema.parse("code_execution_20250825")).toBe("code_execution_20250825");
    expect(AllowedCallerSchema.parse("code_execution_20260120")).toBe("code_execution_20260120");
  });

  it("rejects invalid caller types", () => {
    expect(() => AllowedCallerSchema.parse("invalid")).toThrow(ZodError);
    expect(() => AllowedCallerSchema.parse("")).toThrow(ZodError);
    expect(() => AllowedCallerSchema.parse(42)).toThrow(ZodError);
  });
});

describe("ToolTypeSchema", () => {
  it("accepts valid tool types", () => {
    expect(ToolTypeSchema.parse("builtin")).toBe("builtin");
    expect(ToolTypeSchema.parse("custom")).toBe("custom");
    expect(ToolTypeSchema.parse("code_execution")).toBe("code_execution");
  });

  it("rejects invalid tool types", () => {
    expect(() => ToolTypeSchema.parse("mcp")).toThrow(ZodError);
  });
});

describe("CustomToolDefinitionSchema", () => {
  const validDefinition = {
    name: "query_database",
    description: "Run a SQL query against the database",
    input_schema: {
      type: "object",
      properties: {
        sql: { type: "string" },
      },
      required: ["sql"],
    },
  };

  it("accepts a valid definition without allowed_callers", () => {
    const result = CustomToolDefinitionSchema.parse(validDefinition);
    expect(result.name).toBe("query_database");
    expect(result.description).toBe("Run a SQL query against the database");
    expect(result.input_schema).toEqual(validDefinition.input_schema);
    expect(result.allowed_callers).toBeUndefined();
  });

  it("accepts a valid definition with allowed_callers", () => {
    const def = {
      ...validDefinition,
      allowed_callers: ["code_execution_20250825"],
    };
    const result = CustomToolDefinitionSchema.parse(def);
    expect(result.allowed_callers).toEqual(["code_execution_20250825"]);
  });

  it("accepts allowed_callers with multiple values", () => {
    const def = {
      ...validDefinition,
      allowed_callers: ["direct", "code_execution_20260120"],
    };
    const result = CustomToolDefinitionSchema.parse(def);
    expect(result.allowed_callers).toHaveLength(2);
  });

  it("rejects missing name", () => {
    const { name, ...noName } = validDefinition;
    expect(() => CustomToolDefinitionSchema.parse(noName)).toThrow(ZodError);
  });

  it("rejects empty name", () => {
    expect(() => CustomToolDefinitionSchema.parse({ ...validDefinition, name: "" })).toThrow(
      ZodError
    );
  });

  it("rejects name starting with a digit", () => {
    expect(() =>
      CustomToolDefinitionSchema.parse({
        ...validDefinition,
        name: "1invalid",
      })
    ).toThrow(ZodError);
  });

  it("rejects name with special characters", () => {
    expect(() =>
      CustomToolDefinitionSchema.parse({
        ...validDefinition,
        name: "my-tool",
      })
    ).toThrow(ZodError);
  });

  it("accepts name with underscores", () => {
    const result = CustomToolDefinitionSchema.parse({
      ...validDefinition,
      name: "my_tool_v2",
    });
    expect(result.name).toBe("my_tool_v2");
  });

  it("rejects missing description", () => {
    const { description, ...noDef } = validDefinition;
    expect(() => CustomToolDefinitionSchema.parse(noDef)).toThrow(ZodError);
  });

  it("rejects empty description", () => {
    expect(() =>
      CustomToolDefinitionSchema.parse({
        ...validDefinition,
        description: "",
      })
    ).toThrow(ZodError);
  });

  it("rejects missing input_schema", () => {
    const { input_schema, ...noSchema } = validDefinition;
    expect(() => CustomToolDefinitionSchema.parse(noSchema)).toThrow(ZodError);
  });

  it("rejects invalid allowed_callers values", () => {
    expect(() =>
      CustomToolDefinitionSchema.parse({
        ...validDefinition,
        allowed_callers: ["invalid_caller"],
      })
    ).toThrow(ZodError);
  });
});

describe("ToolEntrySchema", () => {
  it("accepts a builtin tool entry", () => {
    const entry = { type: "builtin", name: "Bash" };
    const result = ToolEntrySchema.parse(entry);
    expect(result.type).toBe("builtin");
    expect(result.name).toBe("Bash");
    expect(result.definition).toBeUndefined();
  });

  it("accepts a custom tool entry with definition", () => {
    const entry = {
      type: "custom",
      name: "query_database",
      definition: {
        name: "query_database",
        description: "Run SQL",
        input_schema: { type: "object", properties: {} },
      },
    };
    const result = ToolEntrySchema.parse(entry);
    expect(result.type).toBe("custom");
    expect(result.definition?.name).toBe("query_database");
  });

  it("accepts a custom tool entry without definition", () => {
    const entry = { type: "custom", name: "placeholder_tool" };
    const result = ToolEntrySchema.parse(entry);
    expect(result.type).toBe("custom");
    expect(result.definition).toBeUndefined();
  });

  it("rejects empty name", () => {
    expect(() => ToolEntrySchema.parse({ type: "builtin", name: "" })).toThrow(ZodError);
  });
});
